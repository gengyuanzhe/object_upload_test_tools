package uploader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// AwsPoster 是基于 S3 浏览器表单 POST（presigned POST policy）流程实现的 Uploader。
//
// 该实现构造一个 POST policy JSON（包含 expiration 与 conditions），base64 编码后
// 用 AWS SigV4 手动签名（AWS4-HMAC-SHA256），再以 multipart/form-data 形式提交，
// 适用于需要把上传凭证下发给浏览器或第三方执行的场景。
// Method 返回 "post"，便于工厂函数与日志按上传方式区分。
//
// 注意：AWS SDK V2 的 signer/v4 包主要为 HTTP 请求签名设计，POST policy 签名
// 仅需对 policy 字符串做 HMAC-SHA256，不涉及 canonical request 构造，因此这里
// 采用 AWS 文档中标准的"派生签名密钥 + HMAC"手动实现，不依赖 signer 包。
type AwsPoster struct {
	client *s3.Client
}

// NewAwsPoster 构造一个绑定到指定 s3.Client 的 AwsPoster。
// 调用方负责 client 的初始化（Region、Credentials、BaseEndpoint 等）。
func NewAwsPoster(client *s3.Client) *AwsPoster {
	return &AwsPoster{client: client}
}

// Method 返回该实现的上传方式标识 "post"。
func (p *AwsPoster) Method() string { return "post" }

// Upload 构造 POST policy 并以 multipart/form-data 提交一次上传。
//
// 输入侧：Bucket/Key 来自 spec；spec.Content 作为 multipart "file" 字段的请求体；
// spec.Size 写入 UploadResult.BytesIn；spec.Post.SuccessActionStatus 决定期望返回的
// HTTP 状态码（默认 201）；spec.Post.ExtraFields 作为额外的表单字段同时写入 policy
// conditions 与 multipart 表单（如 Content-Type、acl 等）。
//
// 签名侧：使用 AWS SigV4 派生签名密钥——
//
//	signingKey = HMAC(HMAC(HMAC(HMAC("AWS4"+secret, date), region), "s3"), "aws4_request")
//	signature  = hex(HMAC(signingKey, base64(policy)))
//
// 所有时间戳（expiration、amzDate、date）均从同一个 now 派生，避免多次调用
// time.Now() 导致的瞬时漂移。
//
// 输出侧：ETag 来自响应头并 trim 掉 S3 默认包裹的双引号，便于与本地内容哈希直接
// 比较；BytesIn 取 spec.Size；Elapsed 记录从函数入口到响应返回的耗时。
//
// 容错：spec.Post == nil 时按零值处理（status=201、无 ExtraFields），不拒绝请求，
// 因为 AwsPoster 是 POST 上传的专用实现，不应因 Post 子规格缺失而失败。
func (p *AwsPoster) Upload(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	start := time.Now()

	// success_action_status：默认 201；调用方提供非零值时采用调用方值。
	status := 201
	if spec.Post != nil && spec.Post.SuccessActionStatus != 0 {
		status = spec.Post.SuccessActionStatus
	}

	// 统一时间基准：expiration / amzDate / date 全部从 now 派生，避免多次
	// time.Now() 调用造成的瞬时漂移。
	now := time.Now().UTC()
	expiration := now.Add(15 * time.Minute)
	expiryStr := expiration.Format("2006-01-02T15:04:05.000Z")
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	// 构造 policy JSON：expiration + conditions 列表。
	conditions := []any{
		map[string]any{"bucket": spec.Bucket},
		map[string]any{"key": spec.Key},
		map[string]any{"success_action_status": strconv.Itoa(status)},
	}
	if spec.Post != nil {
		for k, v := range spec.Post.ExtraFields {
			conditions = append(conditions, map[string]any{k: v})
		}
	}
	policy := map[string]any{
		"expiration": expiryStr,
		"conditions": conditions,
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post upload: marshal policy: %w", err)
	}
	policyB64 := base64.StdEncoding.EncodeToString(policyJSON)

	// 凭证与签名：从 client.Options() 读取 Credentials 与 Region。
	creds, err := p.client.Options().Credentials.Retrieve(ctx)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post upload: retrieve credentials: %w", err)
	}
	region := p.client.Options().Region
	scope := fmt.Sprintf("%s/%s/s3/aws4_request", date, region)

	signingKey := hmacSHA256(
		hmacSHA256(
			hmacSHA256(
				hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), []byte(date)),
				[]byte(region),
			),
			[]byte("s3"),
		),
		[]byte("aws4_request"),
	)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(policyB64)))

	// 构造 multipart/form-data 表单。
	var formBuf bytes.Buffer
	mw := multipart.NewWriter(&formBuf)
	// 表单字段写入错误统一在最后检查：mime/multipart.Writer 在内存缓冲下不会失败，
	// 忽略单步错误可减少噪音；Close 时会失败则视为致命错误。
	_ = mw.WriteField("key", spec.Key)
	_ = mw.WriteField("x-amz-credential", fmt.Sprintf("%s/%s", creds.AccessKeyID, scope))
	_ = mw.WriteField("x-amz-algorithm", "AWS4-HMAC-SHA256")
	_ = mw.WriteField("x-amz-date", amzDate)
	if creds.SessionToken != "" {
		_ = mw.WriteField("x-amz-security-token", creds.SessionToken)
	}
	_ = mw.WriteField("x-amz-signature", signature)
	_ = mw.WriteField("policy", policyB64)
	_ = mw.WriteField("success_action_status", strconv.Itoa(status))
	if spec.Post != nil {
		for k, v := range spec.Post.ExtraFields {
			_ = mw.WriteField(k, v)
		}
	}
	fw, err := mw.CreateFormFile("file", spec.Key)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post upload: create file part: %w", err)
	}
	if _, err := io.Copy(fw, spec.Content); err != nil {
		return UploadResult{}, fmt.Errorf("post upload: copy content: %w", err)
	}
	if err := mw.Close(); err != nil {
		return UploadResult{}, fmt.Errorf("post upload: close multipart writer: %w", err)
	}

	// URL 构造：path-style，BaseEndpoint 为 *string，使用 aws.ToString 安全解引用；
	// 去掉 trailing slash 以避免 //，bucket 名直接拼接（测试用 "b"，安全）。
	endpoint := aws.ToString(p.client.Options().BaseEndpoint)
	endpoint = strings.TrimSuffix(endpoint, "/")
	url := endpoint + "/" + spec.Bucket

	req, err := http.NewRequestWithContext(ctx, "POST", url, &formBuf)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post upload: new request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post upload: do request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != status {
		return UploadResult{}, fmt.Errorf("post upload: %s: %s", resp.Status, body)
	}

	return UploadResult{
		ETag:    strings.Trim(resp.Header.Get("ETag"), `"`),
		BytesIn: spec.Size,
		Elapsed: time.Since(start),
	}, nil
}

// hmacSHA256 计算 HMAC-SHA256(key, data) 并返回原始摘要字节。
// 用于 AWS SigV4 派生签名密钥与 policy 签名。
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
