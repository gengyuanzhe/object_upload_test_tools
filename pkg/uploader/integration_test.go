//go:build integration

package uploader

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
