//go:build integration

package uploader

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newTestClient 构造一个指向 httptest.Server 的 S3 客户端，
// 便于在集成测试中替换真实 AWS 后端。
func newTestClient(t *testing.T, handler http.Handler) *s3.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})
	return cl
}

// TestAwsPutterIntegration 验证 AwsPutter 经由 httptest.Server 完成一次 PUT 上传后，
// 能正确返回 ETag、BytesIn，并将请求体与 Key 透传至服务端。
func TestAwsPutterIntegration(t *testing.T) {
	var gotBody []byte
	var gotKey string
	var gotMD5 string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// UsePathStyle 为 true 时，路径形如 /{bucket}/{key}，
		// 需剥离前导斜杠与 bucket 段以还原出对象 Key。
		gotKey = strings.TrimPrefix(r.URL.Path, "/b/")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		sum := md5.Sum(body)
		gotMD5 = hex.EncodeToString(sum[:])
		w.Header().Set("ETag", `"`+gotMD5+`"`)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})

	p := &AwsPutter{client: cl}
	if p.Method() != "put" {
		t.Fatalf("Method() = %q", p.Method())
	}
	body := strings.NewReader("hello world")
	res, err := p.Upload(context.Background(), UploadSpec{
		Bucket: "b", Key: "k", Content: body, Size: 11,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.ETag != gotMD5 {
		t.Errorf("ETag mismatch: client=%q server=%q", res.ETag, gotMD5)
	}
	if gotKey != "k" {
		t.Errorf("key = %q", gotKey)
	}
	if string(gotBody) != "hello world" {
		t.Errorf("body = %q", gotBody)
	}
	if res.BytesIn != 11 {
		t.Errorf("BytesIn = %d", res.BytesIn)
	}
}

// TestAwsMultipartUploaderIntegration 验证 AwsMultipartUploader 经由 httptest.Server 完成一次
// Create→UploadPart×N→Complete 流程后，能正确返回 ETag（已去引号）、Location、BytesIn，
// 并在 Details["part_md5s"] 中按顺序记录每个分片的 MD5 hex，Details["part_count"] 为分片数。
//
// Mock 协议（参考 S3 REST API）：
//  1. POST /{bucket}/{key}?uploads       → CreateMultipartUpload，返回 InitiateMultipartUploadResult XML；
//  2. PUT  /{bucket}/{key}?partNumber=N&uploadId=... → UploadPart，读取 body 计算 MD5，ETag 头返回 "<hex>"；
//  3. POST /{bucket}/{key}?uploadId=...  → CompleteMultipartUpload，返回 CompleteMultipartUploadResult XML（含 Location、ETag）。
//
// Brief 原始 mock 存在两处缺陷（重复 POST case + complete 阶段无 XML body），此处已修正。
func TestAwsMultipartUploaderIntegration(t *testing.T) {
	var recordedPartMD5s []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// UsePathStyle 为 true 时，路径形如 /{bucket}/{key}。
		q := r.URL.Query()

		switch r.Method {
		case http.MethodPost:
			if _, ok := q["uploads"]; ok {
				// CreateMultipartUpload：返回 UploadId，Content-Type 必须为 application/xml，
				// 否则 SDK 的 XML 解析会失败。
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`))
				return
			}
			if _, ok := q["uploadId"]; ok {
				// CompleteMultipartUpload：返回 Location 与 ETag，Content-Type 必须为 application/xml。
				// SDK 会保留 ETag 周围的双引号，这里返回带引号的 ETag 以验证客户端 trim 行为。
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`<CompleteMultipartUploadResult><Location>http://example/b/k</Location><Bucket>b</Bucket><Key>k</Key><ETag>"combined-etag"</ETag></CompleteMultipartUploadResult>`))
				return
			}
			w.WriteHeader(400)
			return
		case http.MethodPut:
			// UploadPart：partNumber 与 uploadId 同时存在。
			body, _ := io.ReadAll(r.Body)
			sum := md5.Sum(body)
			hx := hex.EncodeToString(sum[:])
			recordedPartMD5s = append(recordedPartMD5s, hx)
			// S3 的 ETag 头通常带双引号包裹 hex。
			w.Header().Set("ETag", `"`+hx+`"`)
			w.WriteHeader(200)
			return
		default:
			w.WriteHeader(405)
			return
		}
	})
	cl := newTestClient(t, handler)

	// 12 字节内容、PartSize=5 → 期望 3 个分片（5+5+2）。
	content := bytes.Repeat([]byte{0x41}, 12)
	u := &AwsMultipartUploader{client: cl}
	if u.Method() != "multipart" {
		t.Fatalf("Method() = %q, want %q", u.Method(), "multipart")
	}
	res, err := u.Upload(context.Background(), UploadSpec{
		Bucket: "b", Key: "k", Content: bytes.NewReader(content), Size: 12,
		Multipart: &MultipartSpec{PartSize: 5},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	parts, _ := res.Details["part_md5s"].([]string)
	if len(parts) != 3 {
		t.Errorf("expected 3 parts, got %d", len(parts))
	}
	if pc, _ := res.Details["part_count"].(int); pc != 3 {
		t.Errorf("part_count = %d, want 3", pc)
	}
	if res.BytesIn != 12 {
		t.Errorf("BytesIn = %d, want 12", res.BytesIn)
	}
	if res.ETag == "" {
		t.Errorf("ETag empty, expected non-empty trimmed value")
	}
	if res.ETag != "combined-etag" {
		t.Errorf("ETag = %q, want %q (should be trimmed of surrounding quotes)", res.ETag, "combined-etag")
	}
	if res.Location != "http://example/b/k" {
		t.Errorf("Location = %q, want %q", res.Location, "http://example/b/k")
	}
	// 客户端记录的 part_md5s 必须与服务端记录的顺序完全一致。
	if len(parts) != len(recordedPartMD5s) {
		t.Errorf("parts len = %d, server recorded = %d", len(parts), len(recordedPartMD5s))
	} else {
		for i := range parts {
			if parts[i] != recordedPartMD5s[i] {
				t.Errorf("part[%d] MD5 = %q, server = %q", i, parts[i], recordedPartMD5s[i])
			}
		}
	}
}

// TestAwsPresignedUploaderPUTIntegration 验证 AwsPresignedUploader 经由 httptest.Server 完成一次
// PUT presign 上传后，能正确返回 ETag（已去引号）、BytesIn，并将请求体透传至服务端。
//
// 服务端按 S3 习惯返回 ETag: "<hex-md5>"（带双引号）；客户端必须 trim 双引号，
// 使 res.ETag 可与本地对相同内容计算的 hex MD5 直接比较。
func TestAwsPresignedUploaderPUTIntegration(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})

	u := &AwsPresignedUploader{client: cl, presigner: s3.NewPresignClient(cl)}
	if u.Method() != "presigned" {
		t.Fatalf("Method() = %q, want %q", u.Method(), "presigned")
	}
	res, err := u.Upload(context.Background(), UploadSpec{
		Bucket:    "b",
		Key:       "k",
		Content:   bytes.NewReader([]byte("presigned-body")),
		Size:      14,
		Presigned: &PresignedSpec{ExpiresIn: 60 * time.Second, Method: "put"},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if string(gotBody) != "presigned-body" {
		t.Errorf("body = %q, want %q", gotBody, "presigned-body")
	}
	if res.BytesIn != 14 {
		t.Errorf("BytesIn = %d, want 14", res.BytesIn)
	}
	// 预期 ETag 为本地对 "presigned-body" 计算的 hex MD5（无引号）。
	wantSum := md5.Sum([]byte("presigned-body"))
	wantETag := hex.EncodeToString(wantSum[:])
	if res.ETag != wantETag {
		t.Errorf("ETag = %q, want %q (raw hex MD5, quotes trimmed)", res.ETag, wantETag)
	}
	if res.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want > 0", res.Elapsed)
	}
}

// TestAwsPresignedUploaderPOSTDelegates 验证 AwsPresignedUploader 在
// PresignedSpec.Method == "post" 时委托至 AwsPoster 的 POST policy 流程，
// 并返回与服务端 mock 一致的成功结果。
//
// 该测试替代了原先的 TestAwsPresignedUploaderPOSTNotImplemented：Task 12
// 移除了 ErrPresignedPostNotImplemented 哨兵，POST 分支改为委托给 AwsPoster。
// Mock 不校验 SigV4 签名，仅用于验证请求体与响应处理。
//
// Mock 行为：模拟 S3 POST policy 的成功响应——解析 multipart 表单提取 "file"
// 字段内容，计算其 MD5 作为 ETag（带双引号）返回，状态码 201。这样 ETag 与
// 客户端对原始内容计算的 hex MD5 一致，可验证 trim 行为。
func TestAwsPresignedUploaderPOSTDelegates(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		gotContentType = r.Header.Get("Content-Type")
		// 从 multipart 表单中提取 file 字段内容，模拟 S3 按对象内容计算 ETag。
		fileContent := extractFileContent(t, gotContentType, body)
		sum := md5.Sum(fileContent)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.WriteHeader(201)
	}))
	t.Cleanup(srv.Close)
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})

	u := &AwsPresignedUploader{client: cl, presigner: s3.NewPresignClient(cl)}
	res, err := u.Upload(context.Background(), UploadSpec{
		Bucket:    "b",
		Key:       "k",
		Content:   bytes.NewReader([]byte("post-body")),
		Size:      9,
		Presigned: &PresignedSpec{Method: "post", ExpiresIn: 60 * time.Second},
		Post:      &PostSpec{SuccessActionStatus: 201},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.Contains(string(gotBody), "post-body") {
		t.Errorf("body did not contain upload: %q", gotBody)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want prefix %q", gotContentType, "multipart/form-data")
	}
	if res.BytesIn != 9 {
		t.Errorf("BytesIn = %d, want 9", res.BytesIn)
	}
	wantSum := md5.Sum([]byte("post-body"))
	wantETag := hex.EncodeToString(wantSum[:])
	if res.ETag != wantETag {
		t.Errorf("ETag = %q, want %q (raw hex MD5, quotes trimmed)", res.ETag, wantETag)
	}
	if res.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want > 0", res.Elapsed)
	}
}

// TestAwsPosterIntegration 验证 AwsPoster 经由 httptest.Server 完成一次 POST policy
// 上传后，能正确返回 ETag（已去引号）、BytesIn、Elapsed，并通过 multipart/form-data
// 请求体把内容透传至服务端。
//
// 服务端按 S3 习惯返回 ETag: "<hex-md5>"（带双引号）+ 状态码 201；客户端必须 trim
// 双引号，使 res.ETag 可与本地对相同内容计算的 hex MD5 直接比较。
// Mock 不校验 SigV4 签名，仅用于验证请求体、Content-Type 与响应处理。
func TestAwsPosterIntegration(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		gotContentType = r.Header.Get("Content-Type")
		fileContent := extractFileContent(t, gotContentType, body)
		sum := md5.Sum(fileContent)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.WriteHeader(201)
	}))
	t.Cleanup(srv.Close)
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})

	p := &AwsPoster{client: cl}
	if p.Method() != "post" {
		t.Fatalf("Method() = %q, want %q", p.Method(), "post")
	}
	res, err := p.Upload(context.Background(), UploadSpec{
		Bucket:  "b",
		Key:     "k",
		Content: bytes.NewReader([]byte("post-body")),
		Size:    9,
		Post:    &PostSpec{SuccessActionStatus: 201},
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.Contains(string(gotBody), "post-body") {
		t.Errorf("body did not contain upload: %q", gotBody)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want prefix %q", gotContentType, "multipart/form-data")
	}
	if res.BytesIn != 9 {
		t.Errorf("BytesIn = %d, want 9", res.BytesIn)
	}
	if res.Elapsed <= 0 {
		t.Errorf("Elapsed = %v, want > 0", res.Elapsed)
	}
	wantSum := md5.Sum([]byte("post-body"))
	wantETag := hex.EncodeToString(wantSum[:])
	if res.ETag != wantETag {
		t.Errorf("ETag = %q, want %q (raw hex MD5, quotes trimmed)", res.ETag, wantETag)
	}
}

// extractFileContent 从 multipart/form-data 请求体中提取 "file" 字段的内容字节。
// 模拟 S3 按对象内容（而非整段 multipart body）计算 ETag 的行为。
// 提取失败时返回 nil，调用方对 nil 计算 MD5 会得到一个固定值，测试断言会失败
// 并暴露问题。
func extractFileContent(t *testing.T, contentType string, body []byte) []byte {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil
		}
		if part.FormName() == "file" {
			content, _ := io.ReadAll(part)
			return content
		}
	}
}
