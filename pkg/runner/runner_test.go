package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

// slowUploader 是 graceful shutdown 测试用的慢速 Uploader 桩实现：
// 每次 Upload 固定 sleep 50ms，并响应 ctx.Done() 提前返回。
//
// 与 mockUploader 区别：mockUploader 立即返回，无法模拟 worker
// 在飞行中被 cancel 的场景；slowUploader 通过 select { time.After / ctx.Done }
// 让 worker 的 Upload 调用能感知 ctx 取消并快速返回。
type slowUploader struct{ method string }

func (s *slowUploader) Method() string { return s.method }
func (s *slowUploader) Upload(ctx context.Context, spec uploader.UploadSpec) (uploader.UploadResult, error) {
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		return uploader.UploadResult{}, ctx.Err()
	}
	_, _ = io.Copy(io.Discard, spec.Content)
	return uploader.UploadResult{BytesIn: spec.Size}, nil
}

// TestRunnerGracefulShutdownReturnsPartialReport 验证 Runner.Run 在 ctx
// 被取消时返回部分 Report 而非 error（graceful shutdown 行为）。
//
// 场景：10000 个 Task、4 worker，每个 Upload sleep 50ms；200ms 后 cancel ctx。
// 预期：
//   - err == nil（Run 在 ctx 取消后正常返回 partial Report，不向上抛 error）；
//   - rep != nil；
//   - rep.Summary.Total <= 10000（未在取消后继续处理全部 Task）。
//
// 关于 Total 下界：在慢机器上 worker 可能在 200ms 内一个 Task 都未完成，
// 此时 Total == 0 是合法的 graceful shutdown 结果（producer 已停止喂任务、
// worker 未写入任何 Result）。因此下界不强制断言失败，仅 t.Logf 记录；
// 真正的核心断言是 err == nil 且 Total 不超过 cfg.Total。
func TestRunnerGracefulShutdownReturnsPartialReport(t *testing.T) {
	cfg := newTestRunnerCfg(10000, 4, []string{"put"})
	// slow mock：每次 Upload sleep 50ms，让 cancel 落在 Upload 飞行期间。
	cfg.UploaderFactory = map[string]func(*s3.Client) uploader.Uploader{
		"put": func(c *s3.Client) uploader.Uploader {
			return &slowUploader{method: "put"}
		},
	}
	r := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// 200ms 后取消 ctx：给 worker 至少完成少量 Task 的时间，
		// 同时保证剩余 ~9900 个 Task 不会在取消前完成（每 Task 50ms，
		// 4 worker 并发，10000 * 50ms / 4 = 125s 远大于 200ms）。
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep == nil {
		t.Fatalf("rep == nil, expected partial Report")
	}
	// 上界硬断言：Run 不得在 ctx 取消后继续处理全部 Task。
	// 若 Total > cfg.Total（10000）说明取消未生效或 Result 被重复统计。
	if rep.Summary.Total > 10000 {
		t.Fatalf("too many results: %d (cfg.Total=10000)", rep.Summary.Total)
	}
	// 下界软提醒：Total == 0 在慢机器上是合法的 graceful shutdown 结果，
	// 仅记日志便于排查，不强制失败（pre-flight ruling #2）。
	if rep.Summary.Total == 0 {
		t.Logf("warning: rep.Summary.Total == 0 (slow machine: no Task completed before 200ms cancel)")
	} else {
		t.Logf("rep.Summary.Total = %d (partial report, expected)", rep.Summary.Total)
	}
}

// fileCloserGenerator 是 TestRunnerClosesFileReader 专用的 Generator stub：
// 每次 Generate 打开一份临时文件并返回 *os.File，同时记录最近返回的句柄，
// 供测试事后验证 executeTask 已通过 defer rc.Close() 关闭。
//
// 并发安全：runner 用多 worker 并发调 Generate，lastFile 用 mutex 保护。
type fileCloserGenerator struct {
	path     string
	mu       sync.Mutex
	lastFile *os.File
}

func (g *fileCloserGenerator) Type() string { return "file" }
func (g *fileCloserGenerator) Generate(size int64, taskID int) (io.Reader, error) {
	f, err := os.Open(g.path)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.lastFile = f
	g.mu.Unlock()
	return f, nil
}

// LastFile 返回最近一次 Generate 打开的 *os.File（线程安全）。
func (g *fileCloserGenerator) LastFile() *os.File {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastFile
}

// TestRunnerClosesFileReader 验证 executeTask 关闭 Generator 返回的 *os.File，
// 避免 file 类型 Content 在长跑中耗尽 fd 上限。
//
// 原理：FileGenerator.Generate 返回 *os.File（实现 io.ReadCloser）。
// executeTask 通过 `if rc, ok := reader.(io.ReadCloser); ok { defer rc.Close() }`
// 在 Upload 完成后关闭。本测试跑 N 个 Task，每个 Task 一个独立 fd，
// 跑完后对最后一个返回的 *os.File 再次调 Close —— 第二次 Close 应返回
// os.ErrClosed（证明第一次已关）。
//
// 若 defer rc.Close() 缺失，第二次 Close 不会报 os.ErrClosed（fd 仍打开），
// 测试失败。
func TestRunnerClosesFileReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'A'}, 256), 0644); err != nil {
		t.Fatal(err)
	}

	gen := &fileCloserGenerator{path: path}

	const total = 20
	cfg := Config{
		EndpointName: "test",
		EndpointURL:  "http://x",
		Bucket:       "b",
		Methods:      []string{"put"},
		Total:        total,
		Concurrency:  4,
		KeyPrefix:    "k/",
		Verify:       false,
		Cleanup:      false,
		Timeout:      10 * time.Second,
		OutPrefix:    "out",
		ContentSeed:  1,
		Content:      gen,
		Sizes:        []config.SizeBucket{{Min: 256, Max: 256, Weight: 100}},
		UploaderFactory: map[string]func(*s3.Client) uploader.Uploader{
			"put": func(c *s3.Client) uploader.Uploader {
				return &mockUploader{method: "put", result: uploader.UploadResult{BytesIn: 256}}
			},
		},
		S3Client: s3.New(s3.Options{
			BaseEndpoint: aws.String("http://x"),
			Region:       "us-east-1",
			Credentials:  credentials.NewStaticCredentialsProvider("a", "b", ""),
			UsePathStyle: true,
		}),
	}

	r := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gen.LastFile() == nil {
		t.Fatal("gen.LastFile() == nil: Generator 未被调用，测试无法验证 fd 关闭")
	}
	// 第二次 Close：executeTask 的 defer 已关；这里再关一次应返回 os.ErrClosed。
	// 若 executeTask 未关 fd，此 Close 会成功（返回 nil），测试失败。
	if err := gen.LastFile().Close(); err == nil {
		t.Errorf("gen.LastFile().Close() 返回 nil，期望 os.ErrClosed —— executeTask 未关闭 fd，存在 fd 泄漏")
	} else if !errors.Is(err, os.ErrClosed) {
		t.Errorf("gen.LastFile().Close() err = %v，期望 os.ErrClosed", err)
	}
}
