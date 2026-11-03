// Package main 是 upload-tool CLI 的入口点。
//
// main.go 实现 Task 21：解析命令行 flag、加载 config、应用 CLI 覆盖、
// 在 dry-run 模式下打印配置与采样 task 预览，或在 real 模式下构建
// runner.Config、绑定 SIGINT/SIGTERM 信号、运行 Runner 并写出报告。
//
// 设计要点：
//   - 测试入口拆分为 loadAndApply / runDryRun / runDryRunWith / runReal /
//     resolveSeed / formatSize 多个内部函数，便于在 main_test.go 中
//     直接驱动单测，避免依赖进程退出码；
//   - dry-run 与 real 共享同一份 config.Load + ApplyDefaults + Validate 路径，
//     配置错误统一在 loadAndApply / runDryRun 阶段以 error 返回；
//   - real 模式下 runner.Config 显式填入 MultipartPartSize
//     （pre-flight T17 ruling #2），将 config 的 multipart 分片大小
//     透传给 runner.multipartPartSize()。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"object_upload_test_tools/pkg/config"
	"object_upload_test_tools/pkg/content"
	"object_upload_test_tools/pkg/runner"
	"object_upload_test_tools/pkg/uploader"
)

// main 是 CLI 进程入口。realMain 返回 error 时以非零码退出。
func main() {
	if err := realMain(os.Args[1:], os.Stderr, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// realMain 解析 flag 并分发到 dry-run 或 real 路径。
// stderr / stdout 由参数注入，便于测试时捕获输出。
// flag 定义统一在 loadAndApply 内完成，realMain 通过 fs.Lookup 读取
// dry-run / report-results / log-level 三个分支参数。
func realMain(args []string, stderr, stdout io.Writer) error {
	fs := flag.NewFlagSet("upload-tool", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg, err := loadAndApply(fs, args)
	if err != nil {
		return err
	}

	logLevel := flagString(fs, "log-level", "info")
	level := slog.LevelInfo
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	if flagBool(fs, "dry-run", false) {
		return runDryRunWith(*cfg, stderr)
	}

	return runReal(*cfg, flagBool(fs, "report-results", false), logger, stderr)
}

// flagString 从已 Parse 的 FlagSet 中读取字符串 flag；不存在时返回 def。
func flagString(fs *flag.FlagSet, name, def string) string {
	if f := fs.Lookup(name); f != nil {
		return f.Value.String()
	}
	return def
}

// flagBool 从已 Parse 的 FlagSet 中读取 bool flag；不存在时返回 def。
func flagBool(fs *flag.FlagSet, name string, def bool) bool {
	if f := fs.Lookup(name); f != nil {
		if v, err := strconv.ParseBool(f.Value.String()); err == nil {
			return v
		}
	}
	return def
}

// loadAndApply 在 fs 上定义全部 flag、解析 args、加载 config 并应用 CLI 覆盖。
// 仅对显式 set 的 flag 进行覆盖（通过 fs.Visit 枚举），
// 未传入的 flag 保持 config.Load / ApplyDefaults 后的值。
// 覆盖完成后调用 config.Validate 做最终校验，任一规则不满足即返回 error。
// flag 定义放在本函数内部，使测试可直接传入空 FlagSet 而无需重复注册。
func loadAndApply(fs *flag.FlagSet, args []string) (*config.Config, error) {
	fs.String("c", "", "config file path (required)")
	fs.Int("n", 0, "override runner.total")
	fs.Int("concurrency", 0, "override runner.concurrency")
	fs.String("methods", "", "override methods (comma-separated)")
	fs.Bool("report-results", false, "write full results.json")
	fs.String("log-level", "info", "debug|info|warn|error")
	fs.Bool("dry-run", false, "validate config and preview tasks without uploading")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfgPath := ""
	n := 0
	conc := 0
	methods := ""
	// 第一轮 Visit：提取 -c / -n / --concurrency / --methods 的显式值。
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "c":
			cfgPath = f.Value.String()
		case "n":
			n, _ = strconv.Atoi(f.Value.String())
		case "concurrency":
			conc, _ = strconv.Atoi(f.Value.String())
		case "methods":
			methods = f.Value.String()
		}
	})
	if cfgPath == "" {
		return nil, fmt.Errorf("-c <config> is required")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	// 第二轮 Visit：把 flag 覆盖写回 cfg。
	// 仅覆盖显式 set 的 flag，避免把零值误写回 config。
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "n":
			cfg.Runner.Total = n
		case "concurrency":
			cfg.Runner.Concurrency = conc
		case "methods":
			parts := strings.Split(methods, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			// 过滤掉首尾或连续逗号产生的空字符串，保证 methods 干净。
			filtered := parts[:0]
			for _, p := range parts {
				if p != "" {
					filtered = append(filtered, p)
				}
			}
			cfg.Methods = filtered
		}
	})

	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// runDryRun 加载并校验 config 后进入 dry-run 预览路径。
// 失败（Load / Validate）时直接返回 error，由调用方决定退出码。
func runDryRun(cfgPath string, w io.Writer) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	return runDryRunWith(*cfg, w)
}

// runDryRunWith 接收已加载的 config，向 w 打印 dry-run 摘要：
// final config 关键字段、Sampler 采样后的首 10 条 task 预览。
// task 预览包含 Method、Key（KeyPrefix + 时间戳 + 4 位序号）、采样 Size。
func runDryRunWith(cfg config.Config, w io.Writer) error {
	fmt.Fprintln(w, "=== Dry Run ===")
	fmt.Fprintln(w, "Config OK")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Final Config:")
	fmt.Fprintf(w, "  endpoint: %s (%s)\n", cfg.Endpoint.Name, cfg.Endpoint.URL)
	fmt.Fprintf(w, "  bucket: %s\n", cfg.Bucket)
	fmt.Fprintf(w, "  methods: %v\n", cfg.Methods)
	fmt.Fprintf(w, "  sizes: %v\n", cfg.Sizes)
	fmt.Fprintf(w, "  total: %d\n", cfg.Runner.Total)
	fmt.Fprintf(w, "  concurrency: %d\n", cfg.Runner.Concurrency)
	fmt.Fprintf(w, "  verify: %v\n", cfg.Runner.Verify)
	fmt.Fprintf(w, "  cleanup: %v\n", cfg.Runner.Cleanup)
	fmt.Fprintln(w)

	// 通过 Sampler 复现真实运行时的大小采样，便于用户预判对象大小分布。
	sampler := runner.NewSampler(cfg.Sizes, resolveSeed(cfg.Content.Seed))
	fmt.Fprintln(w, "First 10 tasks (sample):")
	startedAt := time.Now()
	for i := 0; i < 10 && i < cfg.Runner.Total; i++ {
		method := cfg.Methods[i%len(cfg.Methods)]
		size := sampler.Sample(i)
		key := fmt.Sprintf("%s%s-%04d", cfg.Runner.KeyPrefix, startedAt.Format("20060102-150405"), i)
		fmt.Fprintf(w, "  #%d %-10s %s  %s\n", i+1, method, key, formatSize(size))
	}
	return nil
}

// runReal 执行真实上传：构造 s3.Client、Generator、runner.Config，
// 绑定 SIGINT/SIGTERM 的 signal.NotifyContext，运行 Runner，
// 再由 Reporter 写出文件并打印 Console 汇总。
// runner.Config 显式填入 MultipartPartSize（pre-flight T17 ruling #2），
// 使 runner.multipartPartSize() 能读到 config 的分片大小。
func runReal(cfg config.Config, reportResults bool, logger *slog.Logger, stderr io.Writer) error {
	s3Client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint.URL),
		Region:       cfg.Endpoint.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.Endpoint.AccessKey, cfg.Endpoint.SecretKey, ""),
		UsePathStyle: cfg.Endpoint.UsePathStyle,
	})

	baseSeed := resolveSeed(cfg.Content.Seed)
	gen := content.NewFromConfig(cfg.Content)
	if rg, ok := gen.(*content.RandomGenerator); ok {
		rg.BaseSeed = baseSeed
	}

	rcfg := runner.Config{
		EndpointName:      cfg.Endpoint.Name,
		EndpointURL:       cfg.Endpoint.URL,
		Bucket:            cfg.Bucket,
		Methods:           cfg.Methods,
		Sizes:             cfg.Sizes,
		Content:           gen,
		ContentSeed:       baseSeed,
		Total:             cfg.Runner.Total,
		Concurrency:       cfg.Runner.Concurrency,
		KeyPrefix:         cfg.Runner.KeyPrefix,
		Verify:            cfg.Runner.Verify,
		Cleanup:           cfg.Runner.Cleanup,
		Timeout:           cfg.Runner.Timeout,
		OutPrefix:         cfg.Runner.OutPrefix,
		MultipartPartSize: cfg.MethodOptions.Multipart.PartSize, // pre-flight T17 ruling #2
		S3Client:          s3Client,
		UploaderFactory:   uploader.NewAwsUploaderFactory(),
	}

	r := runner.New(rcfg, logger)

	// 绑定 SIGINT / SIGTERM：收到信号后 ctx 取消，Runner 的 producer / worker
	// 会通过 select 响应 ctx.Done() 退出，已收集的 Result 仍会汇总到 Report。
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rep, err := r.Run(ctx)
	if err != nil {
		return err
	}

	outDir := fmt.Sprintf("%s-%s", cfg.Runner.OutPrefix, time.Now().Format("20060102-150405"))
	reporter := runner.NewReporter(outDir, reportResults, logger)
	if err := reporter.Write(rep); err != nil {
		return err
	}
	reporter.PrintConsoleSummary(rep, stderr)
	return nil
}

// resolveSeed 把零值 seed 解析为当前时间纳秒，确保不同 Run 之间种子互异。
// 非零 seed 原样返回，保证可复现性。
func resolveSeed(s int64) int64 {
	if s == 0 {
		return time.Now().UnixNano()
	}
	return s
}

// formatSize 按量级把字节数格式化为人类可读字符串（1024 进制）。
// 用于 dry-run 预览，与 Reporter.formatBytes 风格保持一致但更精简。
func formatSize(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}
