package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"object_upload_test_tools/pkg/config"
	"object_upload_test_tools/pkg/content"
	"object_upload_test_tools/pkg/uploader"
)

// mockUploader 是 Task 17 测试用的 Uploader 桩实现：可注入返回结果或错误。
// 它同时丢弃传入的 Content Reader，避免阻塞上游 Generator。
type mockUploader struct {
	method string
	err    error
	result uploader.UploadResult
}

func (m *mockUploader) Method() string { return m.method }
func (m *mockUploader) Upload(ctx context.Context, spec uploader.UploadSpec) (uploader.UploadResult, error) {
	if m.err != nil {
		return uploader.UploadResult{}, m.err
	}
	_, _ = io.Copy(io.Discard, spec.Content)
	return m.result, nil
}

// newTestRunnerCfg 构造一个 100% 走 mockUploader 的 runner.Config，
// 用于 TestRunnerRunAllSuccess / TestRunnerRecordsFailures / TestRunnerKeyFormat 三个用例。
// 该 helper 不设置 MultipartPartSize（保持 0），multipartPartSize() 会回落到默认 5MiB。
func newTestRunnerCfg(total, conc int, methods []string) Config {
	return Config{
		EndpointName: "test",
		EndpointURL:  "http://x",
		Bucket:       "b",
		Methods:      methods,
		Total:        total,
		Concurrency:  conc,
		KeyPrefix:    "k/",
		Verify:       false,
		Cleanup:      false,
		Timeout:      10 * time.Second,
		OutPrefix:    "out",
		ContentSeed:  1,
		Content:      content.NewFromConfig(config.ContentConfig{Type: "random", Seed: 1}),
		Sizes:        []config.SizeBucket{{Min: 10, Max: 100, Weight: 100}},
		UploaderFactory: map[string]func(*s3.Client) uploader.Uploader{
			"put": func(c *s3.Client) uploader.Uploader {
				return &mockUploader{method: "put", result: uploader.UploadResult{BytesIn: 50}}
			},
		},
		S3Client: s3.New(s3.Options{BaseEndpoint: aws.String("http://x"), Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("a", "b", ""), UsePathStyle: true}),
	}
}

// TestRunnerRunAllSuccess 验证 100 个任务、8 worker 全部成功时，
// Report.Summary 中 Total/Success/Failed 分别为 100/100/0。
// 额外断言 Results[0].Size 落在测试配置的 [10, 100] 区间内，
// 用于捕获 Sampler 接线缺陷（未调用 Sample 或返回常量）。
func TestRunnerRunAllSuccess(t *testing.T) {
	cfg := newTestRunnerCfg(100, 8, []string{"put"})
	r := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Summary.Total != 100 {
		t.Errorf("total = %d", rep.Summary.Total)
	}
	if rep.Summary.Success != 100 {
		t.Errorf("success = %d", rep.Summary.Success)
	}
	if rep.Summary.Failed != 0 {
		t.Errorf("failed = %d", rep.Summary.Failed)
	}
	// Sampler 接线自检：第一个任务的 Size 必须落在测试 bucket 的 [10, 100] 闭区间内。
	if len(rep.Results) == 0 {
		t.Fatalf("Results empty")
	}
	sz := rep.Results[0].Size
	if sz < 10 || sz > 100 {
		t.Errorf("Results[0].Size = %d, want in [10, 100]", sz)
	}
}

// TestRunnerRecordsFailures 验证 Uploader.Upload 返回 errors.New("boom") 时，
// 所有任务被分类为 failed，且 ByError[0].Error == "boom"。
func TestRunnerRecordsFailures(t *testing.T) {
	cfg := newTestRunnerCfg(50, 4, []string{"put"})
	cfg.UploaderFactory = map[string]func(*s3.Client) uploader.Uploader{
		"put": func(c *s3.Client) uploader.Uploader {
			return &mockUploader{method: "put", err: errors.New("boom")}
		},
	}
	r := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Summary.Success != 0 || rep.Summary.Failed != 50 {
		t.Errorf("success=%d failed=%d, want 0/50", rep.Summary.Success, rep.Summary.Failed)
	}
	if rep.Summary.ByError[0].Error != "boom" {
		t.Errorf("ByError = %+v", rep.Summary.ByError)
	}
}

// TestRunnerKeyFormat 验证 keyFor 生成的 Key 以配置的 KeyPrefix 为前缀。
func TestRunnerKeyFormat(t *testing.T) {
	cfg := newTestRunnerCfg(1, 1, []string{"put"})
	r := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rep, _ := r.Run(context.Background())
	if !bytes.HasPrefix([]byte(rep.Results[0].Key), []byte("k/")) {
		t.Errorf("Key = %q, want prefix k/", rep.Results[0].Key)
	}
}
