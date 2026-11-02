package uploader

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// AwsMultipartUploader 是基于 AWS SDK V2 的 s3.Client 分片上传实现的 Uploader。
//
// 该实现采用 CreateMultipartUpload → UploadPart×N → CompleteMultipartUpload
// 三阶段流程完成上传，适用于大对象（默认分片 5 MiB）。任意阶段失败或 ctx
// 取消都会触发 AbortMultipartUpload 以避免残留未完成的 upload。
// Method 返回 "multipart"，便于工厂函数与日志按上传方式区分。
type AwsMultipartUploader struct {
	client *s3.Client
}

// NewAwsMultipartUploader 构造一个绑定到指定 s3.Client 的 AwsMultipartUploader。
// 调用方负责 client 的初始化（Region、Credentials、BaseEndpoint 等）。
func NewAwsMultipartUploader(client *s3.Client) *AwsMultipartUploader {
	return &AwsMultipartUploader{client: client}
}

// Method 返回该实现的上传方式标识 "multipart"。
func (u *AwsMultipartUploader) Method() string { return "multipart" }

// Upload 执行一次分片上传。
//
// 默认分片大小 5 MiB（5242880 字节）；当 spec.Multipart 非空且 PartSize > 0
// 时采用 spec 提供的分片大小。每个分片在内存中缓冲后计算 MD5，作为
// UploadPartInput.Body（bytes.NewReader）与 ContentMD5（base64 编码的原始
// MD5 字节）一并提交，使服务端可校验数据完整性。
//
// 输出侧：CompleteMultipartUploadOutput.Location 写入 UploadResult.Location；
// ETag 去除 S3 默认包裹的双引号后写入 UploadResult.ETag；BytesIn 为各分片
// 字节数之和；Details["part_md5s"] 为每个分片的 hex MD5（按顺序），
// Details["part_count"] 为分片数。
//
// 错误处理：CreateMultipartUpload 失败直接返回；分片循环中任意失败或 ctx
// 取消都调用 AbortMultipartUpload 后返回错误；若首读即返回 (0, io.EOF)
// 表示内容为空，同样 Abort 并返回 io.EOF（S3 不允许零分片 Complete）。
func (u *AwsMultipartUploader) Upload(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	start := time.Now()
	partSize := int64(5242880)
	if spec.Multipart != nil && spec.Multipart.PartSize > 0 {
		partSize = spec.Multipart.PartSize
	}

	createInput := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(spec.Bucket),
		Key:    aws.String(spec.Key),
	}
	// 与 AwsPutter 保持一致：仅在调用方提供元数据时才初始化 map，
	// 避免向 nil map 写入触发 panic。
	if len(spec.Metadata) > 0 {
		createInput.Metadata = make(map[string]string, len(spec.Metadata))
		for k, v := range spec.Metadata {
			createInput.Metadata[k] = v
		}
	}

	initOut, err := u.client.CreateMultipartUpload(ctx, createInput)
	if err != nil {
		return UploadResult{}, err
	}
	uploadID := initOut.UploadId

	var completed []types.CompletedPart
	var partMD5s []string
	var totalIn int64
	partNum := int32(1)

	for {
		if ctx.Err() != nil {
			_ = u.abort(ctx, spec.Bucket, spec.Key, uploadID)
			return UploadResult{}, ctx.Err()
		}
		buf := make([]byte, partSize)
		n, readErr := io.ReadFull(spec.Content, buf)
		// 首读即 EOF：内容为空，Abort 后返回 io.EOF，避免零分片 Complete（S3 拒绝）。
		if n == 0 && (readErr == io.EOF || readErr == io.ErrUnexpectedEOF) {
			_ = u.abort(ctx, spec.Bucket, spec.Key, uploadID)
			return UploadResult{}, io.EOF
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			_ = u.abort(ctx, spec.Bucket, spec.Key, uploadID)
			return UploadResult{}, readErr
		}
		partBuf := buf[:n]
		sum := md5.Sum(partBuf)
		hx := hex.EncodeToString(sum[:])

		out, err := u.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(spec.Bucket),
			Key:        aws.String(spec.Key),
			UploadId:   uploadID,
			PartNumber: aws.Int32(partNum),
			Body:       bytes.NewReader(partBuf),
			ContentMD5: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
		})
		if err != nil {
			_ = u.abort(ctx, spec.Bucket, spec.Key, uploadID)
			return UploadResult{}, err
		}
		completed = append(completed, types.CompletedPart{
			ETag:       out.ETag,
			PartNumber: aws.Int32(partNum),
		})
		partMD5s = append(partMD5s, hx)
		totalIn += int64(n)
		partNum++
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	completeOut, err := u.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(spec.Bucket),
		Key:             aws.String(spec.Key),
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return UploadResult{}, err
	}

	res := UploadResult{
		BytesIn: totalIn,
		Elapsed: time.Since(start),
		Details: map[string]any{
			"part_md5s":  partMD5s,
			"part_count": len(completed),
		},
	}
	// CompleteMultipartUploadOutput.Location 字段确实存在（与 PutObjectOutput 不同），
	// 直接透传给调用方。
	if completeOut.Location != nil {
		res.Location = aws.ToString(completeOut.Location)
	}
	// SDK 保留 ETag 周围的双引号，trim 后写入以便与本地计算的内容哈希直接比较。
	if completeOut.ETag != nil {
		res.ETag = strings.Trim(*completeOut.ETag, `"`)
	}
	if completeOut.VersionId != nil {
		res.VersionID = *completeOut.VersionId
	}
	return res, nil
}

// abort 调用 AbortMultipartUpload 取消未完成的 upload。错误被忽略，因为调用方
// 已经持有一个更重要的错误需要返回；abort 失败通常意味着 upload 已超时或被服务端清理。
func (u *AwsMultipartUploader) abort(ctx context.Context, bucket, key string, uploadID *string) error {
	_, err := u.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: uploadID,
	})
	return err
}
