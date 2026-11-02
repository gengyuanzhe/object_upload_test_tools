package uploader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ErrPresignedPostNotImplemented 是 AwsPresignedUploader 在 PresignedSpec.Method == "post"
// 时返回的哨兵错误。
//
// 生命周期说明：该哨兵仅存在于 Task 11 与 Task 12 之间。Task 12 step 5 将移除此哨兵，
// 把 "post" 分支委托给 AwsPoster 的 POST policy 流程。在此之前，本实现仅支持
// PUT presign，POST 显式返回该错误以避免静默误用。
var ErrPresignedPostNotImplemented = errors.New("presigned POST not implemented in this task")

// AwsPresignedUploader 是基于 AWS SDK V2 的 s3.PresignClient.PresignPutObject
// 实现的 Uploader。
//
// 该实现先生成一次性的预签名 PUT URL，再通过 net/http 直接发起 PUT 请求，
// 适用于无法直接持有 AWS 凭证、或需要把 URL 交给第三方执行的场合。
// Method 返回 "presigned"，便于工厂函数与日志按上传方式区分。
//
// 当前仅支持 PUT presign；POST policy 流程由 Task 12 的 AwsPoster 提供，
// 在 Task 12 完成前，PresignedSpec.Method == "post" 返回 ErrPresignedPostNotImplemented。
type AwsPresignedUploader struct {
	client    *s3.Client
	presigner *s3.PresignClient
}

// NewAwsPresignedUploader 构造一个绑定到指定 s3.Client 的 AwsPresignedUploader。
// 调用方负责 client 的初始化（Region、Credentials、BaseEndpoint 等）；
// presigner 在内部由 s3.NewPresignClient(client) 创建。
func NewAwsPresignedUploader(client *s3.Client) *AwsPresignedUploader {
	return &AwsPresignedUploader{
		client:    client,
		presigner: s3.NewPresignClient(client),
	}
}

// Method 返回该实现的上传方式标识 "presigned"。
func (u *AwsPresignedUploader) Method() string { return "presigned" }

// Upload 依据 spec.Presigned.Method 选择 PUT presign 或 POST policy 流程。
//
// PUT presign 分支（Method == "" || Method == "put"）：
//  1. 调用 presigner.PresignPutObject 生成预签名 URL，opts 透传 spec.Presigned.ExpiresIn；
//  2. 以 req.URL 为目标构造 http.Request，Method 固定 PUT，Body 为 spec.Content，
//     ContentLength 显式设置为 spec.Size；
//  3. 把 presigner 返回的 SignedHeader（http.Header）整体复制到 httpReq.Header；
//     presigned PUT 的签名覆盖范围有限，禁止额外添加未签名头（如 spec.Metadata），
//     否则会破坏签名导致服务端 403。
//  4. 通过 http.DefaultClient 发起请求；非 2xx 时读取最多 4096 字节响应体并返回错误。
//
// 输出侧：ETag 来自响应头，trim 掉 S3 默认包裹的双引号以便与本地内容哈希直接比较；
// BytesIn 直接取 spec.Size（PUT 一次请求即写完）；Elapsed 记录从函数入口到响应返回的耗时。
//
// POST policy 分支（Method == "post"）当前返回 ErrPresignedPostNotImplemented，
// 由 Task 12 接管实现。
func (u *AwsPresignedUploader) Upload(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	start := time.Now()
	if spec.Presigned == nil || spec.Presigned.Method == "" || spec.Presigned.Method == "put" {
		expires := spec.Presigned.ExpiresIn
		req, err := u.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(spec.Bucket),
			Key:    aws.String(spec.Key),
		}, s3.WithPresignExpires(expires))
		if err != nil {
			return UploadResult{}, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, "PUT", req.URL, spec.Content)
		if err != nil {
			return UploadResult{}, err
		}
		httpReq.ContentLength = spec.Size
		// SignedHeader 为 http.Header（map[string][]string）；逐键复制即可。
		// presigned PUT 的签名只覆盖这些头，添加任何未签名头都会破坏请求。
		for k, v := range req.SignedHeader {
			httpReq.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return UploadResult{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return UploadResult{}, errors.New("presigned PUT failed: " + resp.Status + ": " + string(body))
		}
		return UploadResult{
			ETag:    strings.Trim(resp.Header.Get("ETag"), `"`),
			BytesIn: spec.Size,
			Elapsed: time.Since(start),
		}, nil
	}
	return UploadResult{}, ErrPresignedPostNotImplemented
}
