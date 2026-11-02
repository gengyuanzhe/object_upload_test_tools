package uploader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// AwsPresignedUploader 是基于 AWS SDK V2 的 s3.PresignClient.PresignPutObject
// 实现的 Uploader。
//
// 该实现先生成一次性的预签名 PUT URL，再通过 net/http 直接发起 PUT 请求，
// 适用于无法直接持有 AWS 凭证、或需要把 URL 交给第三方执行的场合。
// Method 返回 "presigned"，便于工厂函数与日志按上传方式区分。
//
// 当 PresignedSpec.Method == "post" 时，Upload 委托给 AwsPoster 的 POST policy
// 流程（浏览器表单 POST 上传）。AwsPoster 与 AwsPresignedUploader 共享同一个
// s3.Client，但走完全不同的代码路径——前者构造 multipart form + SigV4 签名 policy，
// 后者仅生成一个预签名 URL 并 PUT。两条路径之间不共享签名 helper。
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
// 调度规则（修复 Task 11 review M1 的 nil-deref 缺陷）：
//   - spec.Presigned == nil：直接返回错误，避免对 nil 指针解引用。
//   - Method == "" 或 "put"：走 PUT presign 路径。
//   - Method == "post"：委托给 AwsPoster.Upload。
//   - 其它取值：返回错误，拒绝静默误用。
//
// PUT presign 分支：
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
func (u *AwsPresignedUploader) Upload(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	// M1 修复：先校验 Presigned 非空，避免 nil 解引用。
	if spec.Presigned == nil {
		return UploadResult{}, errors.New("presigned: Presigned spec required")
	}
	switch spec.Presigned.Method {
	case "", "put":
		return u.presignedPUT(ctx, spec)
	case "post":
		// 委托给 AwsPoster：复用同一 client，走 POST policy 流程。
		poster := &AwsPoster{client: u.client}
		return poster.Upload(ctx, spec)
	default:
		return UploadResult{}, fmt.Errorf("presigned: unsupported Method %q", spec.Presigned.Method)
	}
}

// presignedPUT 执行 PUT presign 路径的实际上传逻辑。
//
// 抽取为独立方法以保持 Upload 的调度清晰；该方法不暴露给包外。
func (u *AwsPresignedUploader) presignedPUT(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	start := time.Now()
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
