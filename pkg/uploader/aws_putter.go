package uploader

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// AwsPutter 是基于 AWS SDK V2 的 s3.Client.PutObject 实现的 Uploader。
//
// 该实现采用单一的 PUT Object 请求完成上传，适用于中小体量对象；
// 大对象应使用 multipart 实现（详见后续任务）。
// Method 返回 "put"，便于工厂函数与日志按上传方式区分。
type AwsPutter struct {
	client *s3.Client
}

// NewAwsPutter 构造一个绑定到指定 s3.Client 的 AwsPutter。
// 调用方负责 client 的初始化（Region、Credentials、BaseEndpoint 等）。
func NewAwsPutter(client *s3.Client) *AwsPutter {
	return &AwsPutter{client: client}
}

// Method 返回该实现的上传方式标识 "put"。
func (p *AwsPutter) Method() string { return "put" }

// Upload 调用 s3.Client.PutObject 完成一次 PUT 上传。
//
// 输入侧：Bucket/Key 来自 spec；spec.Content 作为请求体；spec.Size 写入
// ContentLength；spec.Metadata 作为 X-Amz-Meta-* 自定义元数据透传。
//
// 输出侧：将 out.ETag、out.VersionId 写入 UploadResult；ETag 会去除 S3
// 默认包裹的双引号以便与本地计算的内容哈希直接比较。BytesIn 直接取
// spec.Size（PUT Object 为单次请求，写入字节数即请求体大小）。
// Elapsed 记录从请求发起到 SDK 返回的耗时（含序列化与网络）。
//
// 注意：PutObjectOutput 不提供 Location 字段，故 UploadResult.Location
// 在该实现中保持空值；如需对象 URL，应由调用方按 Bucket/Key 自行构造。
func (p *AwsPutter) Upload(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	start := time.Now()
	input := &s3.PutObjectInput{
		Bucket:        aws.String(spec.Bucket),
		Key:           aws.String(spec.Key),
		Body:          spec.Content,
		ContentLength: aws.Int64(spec.Size),
	}
	if len(spec.Metadata) > 0 {
		input.Metadata = make(map[string]string, len(spec.Metadata))
		for k, v := range spec.Metadata {
			input.Metadata[k] = v
		}
	}
	out, err := p.client.PutObject(ctx, input)
	if err != nil {
		return UploadResult{}, err
	}
	elapsed := time.Since(start)
	res := UploadResult{
		Elapsed: elapsed,
		BytesIn: spec.Size,
	}
	if out.ETag != nil {
		res.ETag = strings.Trim(*out.ETag, `"`)
	}
	if out.VersionId != nil {
		res.VersionID = *out.VersionId
	}
	return res, nil
}
