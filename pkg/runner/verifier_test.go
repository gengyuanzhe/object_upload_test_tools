package runner

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// newVerifierClient 启动一个返回固定 ETag 头的 httptest 服务器，
// 并基于该端点构造一个 path-style 的 *s3.Client，用于 Verifier 测试。
// 返回的 cleanup 函数用于关闭底层 httptest 服务器。
func newVerifierClient(t *testing.T, etag string) (*s3.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"`+etag+`"`)
		w.WriteHeader(200)
	}))
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})
	return cl, srv.Close
}

// TestVerifierNonMultipartMatch 验证 ms == nil 场景下，
// 远端 ETag 与本地 MD5 一致时 matched 返回 true 且 remoteETag 等于本地值。
func TestVerifierNonMultipartMatch(t *testing.T) {
	content := []byte("hello")
	sum := md5.Sum(content)
	local := hex.EncodeToString(sum[:])
	cl, closeFn := newVerifierClient(t, local)
	defer closeFn()

	v := NewVerifier(cl)
	etag, matched, err := v.Verify(context.Background(), "b", "k", local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if etag != local {
		t.Errorf("etag = %q, want %q", etag, local)
	}
	if !matched {
		t.Errorf("expected match")
	}
}

// TestVerifierNonMultipartMismatch 验证 ms == nil 场景下，
// 远端 ETag 与本地 MD5 不一致时 matched 返回 false。
func TestVerifierNonMultipartMismatch(t *testing.T) {
	cl, closeFn := newVerifierClient(t, "deadbeef")
	defer closeFn()
	v := NewVerifier(cl)
	_, matched, _ := v.Verify(context.Background(), "b", "k", "cafebabe", nil)
	if matched {
		t.Errorf("expected mismatch")
	}
}

// TestVerifierMultipartReconstructs 验证 ms != nil 场景下，
// Verifier 按 S3 multipart ETag 规范复算 md5hex(concat(md5(part_i))...) + "-" + N，
// 并与远端 ETag 比对成功。
func TestVerifierMultipartReconstructs(t *testing.T) {
	part1 := []byte("aaaaa")
	part2 := []byte("bbbbb")
	part3 := []byte("ccccc")
	parts := [][]byte{part1, part2, part3}

	var concatMD5s []byte
	var partMD5s []string
	for _, p := range parts {
		s := md5.Sum(p)
		concatMD5s = append(concatMD5s, s[:]...)
		partMD5s = append(partMD5s, hex.EncodeToString(s[:]))
	}
	final := md5.Sum(concatMD5s)
	expectedETag := hex.EncodeToString(final[:]) + "-3"

	cl, closeFn := newVerifierClient(t, expectedETag)
	defer closeFn()
	v := NewVerifier(cl)
	_, matched, _ := v.Verify(context.Background(), "b", "k", "ignored",
		&multipartState{PartMD5s: partMD5s, PartCount: 3})
	if !matched {
		t.Errorf("expected multipart match")
	}
}

// TestVerifierHeadFailure 验证 HeadObject 返回 404 时 Verify 将错误向上透传。
// SDK v2 会将 S3 404 响应映射为 *types.NotFound 错误，故 err 应为非 nil。
func TestVerifierHeadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer srv.Close()
	cl := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ak", "sk", ""),
		UsePathStyle: true,
	})
	v := NewVerifier(cl)
	_, _, err := v.Verify(context.Background(), "b", "k", "x", nil)
	if err == nil {
		t.Errorf("expected error from 404")
	}
}
