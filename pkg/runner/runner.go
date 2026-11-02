// Package runner 实现对象上传测试工具的运行层。
//
// 本文件 runner.go 是 runner 包的核心：定义 Config 与 Runner，
// 提供并发 worker pool 调度 Task、调用 Uploader 执行上传、
// 可选触发 Verifier 进行数据一致性校验、可选执行 DELETE 清理，
// 并将全部 Result 汇总成 Report 返回。
//
// Worker pool 模型：
//   - Producer goroutine 将 buildTasks 生成的全部 Task 写入 taskCh；
//   - Concurrency 个 worker goroutine 从 taskCh 取 Task、调用 executeTask、
//     将 Result 写入 resultCh；
//   - 主 goroutine 在 Run 中串行从 resultCh 收集全部 Result，并组装 Report。
//
// resultCh 以 r.cfg.Total 为容量做缓冲（pre-flight T17 ruling），
// 避免 worker 在主 goroutine 尚未开始消费时阻塞；同时为 Task 20
// 的 graceful-shutdown 测试提供更宽裕的运行空间。
//
// Report 相关类型（Report/ReportMeta/ReportSummary/MethodStat/
// SizeBucketStat/ErrorStat）定义在本文件而非 Task 18 的 reporter.go，
// 以打破 runner.go buildReport 与 Task 18 Reporter 之间的循环依赖：
// Task 18 仅新增 Reporter 类型及其方法，不再重复定义这些类型。
package runner

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"object_upload_test_tools/pkg/config"
	"object_upload_test_tools/pkg/content"
	"object_upload_test_tools/pkg/uploader"
)

// Config 描述 Runner 一次 Run 调用所需的全部静态输入。
//
// 各字段语义：
//   - EndpointName / EndpointURL / Bucket：仅用于 Report.Meta 展示，
//     不参与 worker 调度；
//   - Methods：上传方式循环列表，buildTasks 通过 i % len(Methods) 为每个
//     Task 分配 Method；空切片会导致 buildTasks panic，调用方需自行校验；
//   - Sizes / Content / ContentSeed：构造 Sampler 与 Generator，控制每次
//     上传的内容大小与字节流；ContentSeed 同时写入 Report.Meta.BaseSeed；
//   - Total / Concurrency：worker pool 规模与任务总数；
//   - KeyPrefix：keyFor 的前缀（通常以 "/" 结尾）；
//   - Verify / Cleanup：是否在 Upload 成功后调用 Verifier / 是否 DELETE 清理；
//   - Timeout / OutPrefix：保留字段，runner.go 当前不直接消费
//     （Timeout 由调用方通过 ctx 传入，OutPrefix 由 Task 18 Reporter 消费）；
//   - MultipartPartSize：multipart 上传的分片大小（pre-flight T17 ruling），
//     0 表示使用默认 5 MiB；Task 21 的 runReal 会从
//     cfg.MethodOptions.Multipart.PartSize 填入；
//   - S3Client / UploaderFactory：S3 客户端与上传器工厂映射。
type Config struct {
	EndpointName      string
	EndpointURL       string
	Bucket            string
	Methods           []string
	Sizes             []config.SizeBucket
	Content           content.Generator
	ContentSeed       int64
	Total             int
	Concurrency       int
	KeyPrefix         string
	Verify            bool
	Cleanup           bool
	Timeout           time.Duration
	OutPrefix         string
	MultipartPartSize int64
	S3Client          *s3.Client
	UploaderFactory   map[string]func(*s3.Client) uploader.Uploader
}

// Runner 是绑定了 Config 与 logger 的运行器实例。
//
// Runner 自身无可变状态（每次 Run 都重建所有 channel 与 goroutine），
// 因此理论上同一 Runner 可被多次调用 Run；但调用方应避免对同一 Runner
// 并发调用 Run —— Run 内部假设它独占 Config 与 logger。
type Runner struct {
	cfg    Config
	logger *slog.Logger
}

// New 构造一个 Runner 实例。
// logger 用于在 Cleanup 失败时打印 warning；nil logger 会引发后续调用 panic，
// 调用方应传入非 nil 的 *slog.Logger。
func New(cfg Config, logger *slog.Logger) *Runner {
	return &Runner{cfg: cfg, logger: logger}
}

// Run 启动 worker pool 执行全部 Task，并在结束后返回组装好的 Report。
//
// 流程：
//  1. 记录 startedAt，构造 Sampler 与 buildTasks 的全部 Task；
//  2. 创建 taskCh（无缓冲）与 resultCh（缓冲容量 = Total）；
//  3. 启动 Concurrency 个 worker goroutine 从 taskCh 消费 Task；
//  4. 启动一个 producer goroutine 将 Task 序列写入 taskCh，写完后关闭 taskCh；
//  5. 启动一个 closer goroutine 等所有 worker 退出后关闭 resultCh；
//  6. 主 goroutine 串行从 resultCh 收集全部 Result；
//  7. 计算 endedAt 与 duration，调用 buildReport 组装 Report 返回。
//
// ctx 取消时，producer 与 worker 会通过 select 响应 ctx.Done() 退出，
// 已收集的部分 Result 仍会被组装到 Report 中（graceful shutdown）。
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	startedAt := time.Now()
	sampler := NewSampler(r.cfg.Sizes, r.cfg.ContentSeed)
	tasks := r.buildTasks(startedAt, sampler)

	taskCh := make(chan Task)
	// 缓冲 resultCh 容纳全部 Task 的 Result：避免 worker 在主 goroutine
	// 尚未开始消费前阻塞，同时为 Task 20 的 graceful-shutdown 留出空间。
	resultCh := make(chan Result, r.cfg.Total)

	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r.worker(ctx, id, taskCh, resultCh)
		}(i)
	}

	// Producer goroutine：将 Task 序列写入 taskCh，写完或 ctx 取消时关闭 taskCh。
	go func() {
		defer close(taskCh)
		for _, t := range tasks {
			select {
			case taskCh <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Closer goroutine：等所有 worker 退出后再关闭 resultCh，
	// 确保主 goroutine 的 for-range 在所有 Result 落地后才退出。
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 主 goroutine 串行收集所有 Result。
	// for-range 在 resultCh 关闭且缓冲排空后自然退出。
	results := make([]Result, 0, len(tasks))
	for res := range resultCh {
		results = append(results, res)
	}

	endedAt := time.Now()
	report := buildReport(r.cfg, startedAt, endedAt, results)
	return &report, nil
}

// buildTasks 预先生成全部 Task 的静态结构。
//
// 对 i in [0, Total)：
//   - Method 按 Methods[i % len(Methods)] 轮转分配；
//   - Size 默认由 Sampler.Sample(i) 给出；若 Content.Type() == "file"，
//     则通过 os.Stat 读取文件真实大小覆盖采样值（确保 Spec.Size 与磁盘一致）；
//   - Key 由 keyFor(i, startedAt) 生成；
//   - Spec 预填 Bucket/Key/Size 与三种子规格（Multipart/Post/Presigned），
//     executeTask 在执行时只补填 Content 字段。
//
// 预生成 Task 列表便于 producer goroutine 顺序写入 taskCh，无需在循环中
// 动态构造，降低 worker 与 producer 之间的耦合。
func (r *Runner) buildTasks(startedAt time.Time, sampler *Sampler) []Task {
	tasks := make([]Task, r.cfg.Total)
	for i := 0; i < r.cfg.Total; i++ {
		method := r.cfg.Methods[i%len(r.cfg.Methods)]
		size := sampler.Sample(i)
		// file 类型必须以磁盘真实大小为准；其它类型保留 Sampler 采样值。
		if r.cfg.Content.Type() == "file" {
			if st, err := os.Stat(r.cfg.filePath()); err == nil {
				size = st.Size()
			}
		}
		tasks[i] = Task{
			ID:        i,
			Method:    method,
			Bucket:    r.cfg.Bucket,
			Key:       r.keyFor(i, startedAt),
			Size:      size,
			Generator: r.cfg.Content,
			Spec: uploader.UploadSpec{
				Bucket:    r.cfg.Bucket,
				Key:       r.keyFor(i, startedAt),
				Size:      size,
				Multipart: &uploader.MultipartSpec{PartSize: r.cfg.multipartPartSize()},
				Post:      &uploader.PostSpec{SuccessActionStatus: 201},
				Presigned: &uploader.PresignedSpec{ExpiresIn: 3600 * time.Second, Method: "put"},
			},
		}
	}
	return tasks
}

// keyFor 生成 Task 的对象 Key：KeyPrefix + startedAt 时间戳 + 4 位 ID 序号。
//
// 时间戳格式 "20060102-150405" 让同一次 Run 的所有对象在按前缀列出时
// 聚集在一起，便于后续清理与排查；4 位 ID 序号保证单次 Run 内 Key 唯一。
func (r *Runner) keyFor(id int, startedAt time.Time) string {
	return fmt.Sprintf("%s%s-%04d", r.cfg.KeyPrefix, startedAt.Format("20060102-150405"), id)
}

// worker 是 worker pool 中的单个 goroutine：循环从 taskCh 取 Task，
// 调用 executeTask 得到 Result，写入 resultCh。
//
// 退出条件：
//   - taskCh 关闭（for-range 自然退出）；
//   - ctx 取消（先 check ctx.Err()，再 select 写 resultCh 时也响应 ctx.Done()）。
//
// worker 不持有任何可变状态，多个 worker 共享 taskCh / resultCh；
// Sampler 在 buildTasks 阶段已被一次性消费完，worker 不接触 Sampler。
func (r *Runner) worker(ctx context.Context, id int, taskCh <-chan Task, resultCh chan<- Result) {
	for task := range taskCh {
		if ctx.Err() != nil {
			return
		}
		select {
		case resultCh <- r.executeTask(ctx, task):
		case <-ctx.Done():
			return
		}
	}
}

// executeTask 执行单个 Task 的完整生命周期：生成内容、上传、可选校验、可选清理。
//
// 各阶段失败均返回对应状态的 Result（不向上抛 error）：
//   - Generator.Generate 失败 → StatusFailedUpload + ErrMessage；
//   - UploaderFactory 无对应 method → StatusFailedUpload + ErrMessage；
//   - Uploader.Upload 失败 → StatusFailedUpload + ErrMessage；
//   - Verify 失败或 ETag 不一致 → StatusFailedVerify + ErrMessage（Upload 仍成功）；
//   - Cleanup DELETE 失败仅 logger.Warn，不影响 Result.Status。
//
// 内容 MD5 通过 io.TeeReader 在 Uploader 读取 Content 时同步计算，
// 无需二次流式扫描；本地 MD5 用 hex.EncodeToString 标准库编码，
// 不再使用 hexEncode 包装函数（pre-flight T17 ruling）。
//
// multipart 场景从 UploadResult.Details["part_md5s"] / ["part_count"]
// 提取分片 MD5 列表与分片数，封装为 multipartState 传给 Verifier，
// 用于复算 S3 multipart ETag。
func (r *Runner) executeTask(ctx context.Context, task Task) Result {
	start := time.Now()
	reader, err := task.Generator.Generate(task.Size, task.ID)
	if err != nil {
		return Result{
			TaskID: task.ID, Method: task.Method, Key: task.Key, Size: task.Size,
			Status: StatusFailedUpload, ErrMessage: err.Error(), Elapsed: time.Since(start),
		}
	}

	// TeeReader 在 Uploader 读取 Content 时同步累积 MD5，
	// 避免对内容做二次全量扫描。
	hash := md5.New()
	teed := io.TeeReader(reader, hash)
	spec := task.Spec
	spec.Content = teed

	factory, ok := r.cfg.UploaderFactory[task.Method]
	if !ok {
		return Result{
			TaskID: task.ID, Method: task.Method, Key: task.Key, Size: task.Size,
			Status: StatusFailedUpload, ErrMessage: "no factory for method " + task.Method,
			Elapsed: time.Since(start),
		}
	}
	up := factory(r.cfg.S3Client)
	uploadResult, err := up.Upload(ctx, spec)
	if err != nil {
		return Result{
			TaskID: task.ID, Method: task.Method, Key: task.Key, Size: task.Size,
			Status: StatusFailedUpload, ErrMessage: err.Error(), Elapsed: time.Since(start),
		}
	}

	localMD5 := hex.EncodeToString(hash.Sum(nil))

	// multipart 场景下从 UploadResult.Details 提取分片 MD5 列表与分片数，
	// 用于 Verifier 复算 S3 multipart ETag；非 multipart 时 ms 保持 nil。
	var ms *multipartState
	if task.Method == "multipart" {
		if md5s, ok := uploadResult.Details["part_md5s"].([]string); ok {
			if pc, ok := uploadResult.Details["part_count"].(int); ok {
				ms = &multipartState{PartMD5s: md5s, PartCount: pc}
			}
		}
	}

	res := Result{
		TaskID: task.ID, Method: task.Method, Key: task.Key, Size: task.Size,
		Status: StatusSuccess, Elapsed: time.Since(start),
		UploadResult: uploadResult, LocalMD5: localMD5,
	}

	// 可选校验：上传成功后调用 Verifier 进行远端 ETag 比对。
	// 校验失败将覆盖 Status 为 StatusFailedVerify，但不影响已写入的 UploadResult。
	if r.cfg.Verify {
		verifier := NewVerifier(r.cfg.S3Client)
		etag, matched, err := verifier.Verify(ctx, task.Bucket, task.Key, localMD5, ms)
		if err != nil {
			res.Status = StatusFailedVerify
			res.ErrMessage = err.Error()
		} else if !matched {
			res.Status = StatusFailedVerify
			res.ErrMessage = "etag mismatch"
		}
		res.RemoteETag = etag
		res.HashMatched = matched
	}

	// 可选清理：DELETE 上传对象，失败仅记 warning，不污染 Result 状态。
	// 这是测试工具而非数据存储服务，清理失败不影响上传成功的事实。
	if r.cfg.Cleanup {
		if _, err := r.cfg.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(task.Bucket),
			Key:    aws.String(task.Key),
		}); err != nil {
			r.logger.Warn("cleanup delete failed", "key", task.Key, "err", err)
		}
	}

	return res
}

// filePath 返回 file 类型 Content 的本地路径，用于 buildTasks 通过 os.Stat
// 获取文件真实大小。非 file 类型返回空字符串。
//
// 类型断言到 *content.FileGenerator 后读取其 Path 字段（Task 7 已确认存在）；
// 不匹配时返回空字符串，buildTasks 的 os.Stat 会失败并保留 Sampler 采样值。
func (c Config) filePath() string {
	if fg, ok := c.Content.(*content.FileGenerator); ok {
		return fg.Path
	}
	return ""
}

// multipartPartSize 返回 multipart 上传的分片大小（pre-flight T17 ruling）。
//
// 优先返回 Config.MultipartPartSize；为零时回落到默认 5 MiB (5242880)。
// Task 21 的 runReal 会从 cfg.MethodOptions.Multipart.PartSize 填入此字段。
func (c Config) multipartPartSize() int64 {
	if c.MultipartPartSize > 0 {
		return c.MultipartPartSize
	}
	return 5242880
}

// Report 是 Runner.Run 返回的最终汇总结构，由 Task 18 的 Reporter 进一步
// 序列化为 JSON / CSV / Console 输出。本文件只定义类型，Reporter 方法在
// reporter.go 中追加（pre-flight T17 ruling，避免循环依赖）。
type Report struct {
	Meta    ReportMeta
	Summary ReportSummary
	Results []Result
}

// ReportMeta 描述本次 Run 的运行环境元信息，对应 JSON 顶层 meta 字段。
type ReportMeta struct {
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	EndpointName    string    `json:"endpoint_name"`
	EndpointURL     string    `json:"endpoint_url"`
	Bucket          string    `json:"bucket"`
	Concurrency     int       `json:"concurrency"`
	Methods         []string  `json:"methods"`
	BaseSeed        int64     `json:"base_seed"`
}

// ReportSummary 描述本次 Run 的聚合统计结果，对应 JSON 顶层 summary 字段。
//
// ByMethod 与 ByError 在 buildReport 中按 map 迭代填充；BySizeBucket
// 当前留空（nil），由 Task 18 Reporter 或后续增强补全（pre-flight T17 ruling）。
type ReportSummary struct {
	Total          int              `json:"total"`
	Success        int              `json:"success"`
	Failed         int              `json:"failed"`
	TotalBytes     int64            `json:"total_bytes"`
	ThroughputMBps float64          `json:"throughput_mbps"`
	ByMethod       []MethodStat     `json:"by_method"`
	BySizeBucket   []SizeBucketStat `json:"by_size_bucket"`
	ByError        []ErrorStat      `json:"by_error"`
}

// MethodStat 描述按上传方式聚合的统计行（对应 CSV by_method 的每一行）。
type MethodStat struct {
	Method       string  `json:"method"`
	Count        int     `json:"count"`
	Success      int     `json:"success"`
	Failed       int     `json:"failed"`
	Bytes        int64   `json:"bytes"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

// SizeBucketStat 描述按对象大小区间聚合的统计行。
// buildReport 当前不填充该结构（BySizeBucket 留 nil）。
type SizeBucketStat struct {
	Range        string  `json:"range"`
	Count        int     `json:"count"`
	Success      int     `json:"success"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

// ErrorStat 描述按错误信息聚合的统计行（对应 CSV by_error 的每一行）。
type ErrorStat struct {
	Error string `json:"error"`
	Count int    `json:"count"`
}

// buildReport 基于本次 Run 的 Config、起止时间与全部 Result 组装 Report。
//
// 聚合规则：
//   - Total / Success / Failed：按 Result.Status 分类计数；
//   - TotalBytes：累加 Result.Size（含失败任务，因为 Size 是预期上传字节数）；
//   - ByMethod：按 Method 聚合 Count / Success / Failed / Bytes / AvgLatencyMS，
//     AvgLatencyMS 为该 Method 所有任务的算术平均毫秒耗时；
//   - ByError：仅对失败任务（Status != StatusSuccess）按 ErrMessage 聚合计数；
//   - BySizeBucket：留 nil（pre-flight T17 ruling，不在本任务填充）；
//   - ThroughputMBps：TotalBytes / (1024*1024) / duration_seconds；
//     duration 为零时保留零值，避免除零。
//
// 注意：ByMethod 与 ByError 的迭代顺序由 Go map 决定，非确定；
// Task 18 Reporter 在序列化前应自行排序以保证输出稳定。
func buildReport(cfg Config, startedAt, endedAt time.Time, results []Result) Report {
	rep := Report{
		Meta: ReportMeta{
			StartedAt:       startedAt,
			EndedAt:         endedAt,
			DurationSeconds: int64(endedAt.Sub(startedAt).Seconds()),
			EndpointName:    cfg.EndpointName,
			EndpointURL:     cfg.EndpointURL,
			Bucket:          cfg.Bucket,
			Concurrency:     cfg.Concurrency,
			Methods:         cfg.Methods,
			BaseSeed:        cfg.ContentSeed,
		},
		Results: results,
	}

	byMethod := map[string]*MethodStat{}
	byErrorCount := map[string]int{}
	for i := range results {
		r := &results[i]
		rep.Summary.Total++
		rep.Summary.TotalBytes += r.Size
		stat, ok := byMethod[r.Method]
		if !ok {
			stat = &MethodStat{Method: r.Method}
			byMethod[r.Method] = stat
		}
		stat.Count++
		stat.Bytes += r.Size
		stat.AvgLatencyMS += float64(r.Elapsed.Milliseconds())
		switch r.Status {
		case StatusSuccess:
			rep.Summary.Success++
			stat.Success++
		default:
			rep.Summary.Failed++
			stat.Failed++
			byErrorCount[r.ErrMessage]++
		}
	}
	for _, ms := range byMethod {
		if ms.Count > 0 {
			ms.AvgLatencyMS /= float64(ms.Count)
		}
		rep.Summary.ByMethod = append(rep.Summary.ByMethod, *ms)
	}
	for msg, n := range byErrorCount {
		rep.Summary.ByError = append(rep.Summary.ByError, ErrorStat{Error: msg, Count: n})
	}
	if dur := endedAt.Sub(startedAt).Seconds(); dur > 0 {
		rep.Summary.ThroughputMBps = float64(rep.Summary.TotalBytes) / (1024 * 1024) / dur
	}
	return rep
}
