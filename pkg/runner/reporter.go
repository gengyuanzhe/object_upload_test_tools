// 本文件实现 Task 18 的 Reporter，负责把 Report 序列化为
// summary.json / summary.csv / results.csv / results.json（可选），
// 并向任意 io.Writer 打印人类可读的 Console 汇总。
//
// 注意：Report / ReportMeta / ReportSummary / MethodStat / SizeBucketStat /
// ErrorStat 这些类型已在 runner.go 中定义（Task 17 pre-flight ruling），
// 本文件不重复声明，避免 Go "redeclared type" 编译错误。
package runner

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Reporter 负责把 Report 写出到 outDir 下的多格式文件，并向 io.Writer
// 打印 Console 汇总。writeResultsJSON 控制是否额外写出 results.json；
// logger 当前仅作为字段保留，供后续在写出失败时记录日志使用。
type Reporter struct {
	outDir           string
	writeResultsJSON bool
	logger           *slog.Logger
}

// NewReporter 构造一个 Reporter。outDir 为输出目录（不存在会自动创建）；
// writeResultsJSON 为 true 时额外写出 results.json；logger 可为 nil。
func NewReporter(outDir string, writeResultsJSON bool, logger *slog.Logger) *Reporter {
	return &Reporter{outDir: outDir, writeResultsJSON: writeResultsJSON, logger: logger}
}

// Write 将 Report 序列化为文件输出。固定写出 summary.json / summary.csv /
// results.csv 三个文件；当 writeResultsJSON=true 时额外写出 results.json。
//
// 在序列化前调用 sortStats 对 ByMethod / ByError 做稳定排序，
// 保证文件输出顺序可复现（Task 17 cross-task observation #2）。
func (r *Reporter) Write(rep *Report) error {
	sortStats(rep)
	if err := os.MkdirAll(r.outDir, 0755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(r.outDir, "summary.json"), struct {
		Meta    ReportMeta    `json:"meta"`
		Summary ReportSummary `json:"summary"`
	}{rep.Meta, rep.Summary}); err != nil {
		return err
	}
	if err := writeSummaryCSV(filepath.Join(r.outDir, "summary.csv"), rep); err != nil {
		return err
	}
	if err := writeResultsCSV(filepath.Join(r.outDir, "results.csv"), rep); err != nil {
		return err
	}
	if r.writeResultsJSON {
		if err := writeJSON(filepath.Join(r.outDir, "results.json"), rep); err != nil {
			return err
		}
	}
	return nil
}

// PrintConsoleSummary 向 w 打印人类可读的运行汇总，包含起止时间、
// Endpoint、Bucket、Total/Success/Failed、吞吐、By Method 表格、Top Errors
// 与 Output dir。调用前先执行 sortStats 以保证 ByMethod / ByError 顺序稳定。
func (r *Reporter) PrintConsoleSummary(rep *Report, w io.Writer) {
	sortStats(rep)
	fmt.Fprintf(w, "=== Run Summary ===\n")
	fmt.Fprintf(w, "Started:  %s\n", rep.Meta.StartedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "Ended:    %s\n", rep.Meta.EndedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "Duration: %s\n", time.Duration(rep.Meta.DurationSeconds*int64(time.Second)))
	fmt.Fprintf(w, "Endpoint: %s (%s)\n", rep.Meta.EndpointName, rep.Meta.EndpointURL)
	fmt.Fprintf(w, "Bucket:   %s\n\n", rep.Meta.Bucket)
	fmt.Fprintf(w, "Total objects: %d\n", rep.Summary.Total)
	fmt.Fprintf(w, "  Success: %d (%.1f%%)\n", rep.Summary.Success, pct(rep.Summary.Success, rep.Summary.Total))
	fmt.Fprintf(w, "  Failed:  %d (%.1f%%)\n", rep.Summary.Failed, pct(rep.Summary.Failed, rep.Summary.Total))
	fmt.Fprintf(w, "Total bytes: %s\n", formatBytes(rep.Summary.TotalBytes))
	fmt.Fprintf(w, "Throughput:   %.1f MB/s\n\n", rep.Summary.ThroughputMBps)
	fmt.Fprintf(w, "By Method:\n")
	fmt.Fprintf(w, "  %-10s | %-5s | %-7s | %-6s | %-8s | %s\n", "Method", "Total", "Success", "Failed", "Bytes", "Avg Latency")
	for _, m := range rep.Summary.ByMethod {
		fmt.Fprintf(w, "  %-10s | %-5d | %-7d | %-6d | %-8s | %.0fms\n",
			m.Method, m.Count, m.Success, m.Failed, formatBytes(m.Bytes), m.AvgLatencyMS)
	}
	if len(rep.Summary.ByError) > 0 {
		fmt.Fprintf(w, "\nTop Errors:\n")
		for _, e := range rep.Summary.ByError {
			fmt.Fprintf(w, "  %s: %d\n", truncate(e.Error, 60), e.Count)
		}
	}
	fmt.Fprintf(w, "\nOutput dir: %s\n", r.outDir)
}

// sortStats 原地排序 rep.Summary.ByMethod 与 rep.Summary.ByError，
// 使文件与 Console 输出顺序稳定可复现。
//
//   - ByMethod：按 Method 字段字母升序；
//   - ByError：先按 Count 降序（高频在前），Count 相同时按 Error 字母升序兜底。
//
// 直接修改 rep 的切片，调用方为一次性消费场景，可接受原地变更。
func sortStats(rep *Report) {
	sort.Slice(rep.Summary.ByMethod, func(i, j int) bool {
		return rep.Summary.ByMethod[i].Method < rep.Summary.ByMethod[j].Method
	})
	sort.Slice(rep.Summary.ByError, func(i, j int) bool {
		if rep.Summary.ByError[i].Count != rep.Summary.ByError[j].Count {
			return rep.Summary.ByError[i].Count > rep.Summary.ByError[j].Count
		}
		return rep.Summary.ByError[i].Error < rep.Summary.ByError[j].Error
	})
}

// writeJSON 以两空格缩进将 v 编码为 JSON 写入 path。
func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeSummaryCSV 把 Report 的 meta / summary / by_method / by_error
// 写成 (section, key, value) 三列的 CSV。
func writeSummaryCSV(path string, rep *Report) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"section", "key", "value"})
	_ = w.Write([]string{"meta", "endpoint_name", rep.Meta.EndpointName})
	_ = w.Write([]string{"meta", "endpoint_url", rep.Meta.EndpointURL})
	_ = w.Write([]string{"meta", "bucket", rep.Meta.Bucket})
	_ = w.Write([]string{"meta", "duration_seconds", strconv.FormatInt(rep.Meta.DurationSeconds, 10)})
	_ = w.Write([]string{"summary", "total", strconv.Itoa(rep.Summary.Total)})
	_ = w.Write([]string{"summary", "success", strconv.Itoa(rep.Summary.Success)})
	_ = w.Write([]string{"summary", "failed", strconv.Itoa(rep.Summary.Failed)})
	_ = w.Write([]string{"summary", "total_bytes", strconv.FormatInt(rep.Summary.TotalBytes, 10)})
	_ = w.Write([]string{"summary", "throughput_mbps", strconv.FormatFloat(rep.Summary.ThroughputMBps, 'f', 3, 64)})
	for _, m := range rep.Summary.ByMethod {
		_ = w.Write([]string{"by_method", m.Method, strconv.Itoa(m.Count)})
	}
	for _, e := range rep.Summary.ByError {
		_ = w.Write([]string{"by_error", e.Error, strconv.Itoa(e.Count)})
	}
	return nil
}

// writeResultsCSV 把每条 Result 写成一行 CSV，包含 id / method / key /
// size_bytes / status / elapsed_ms / etag / local_md5 / matched / error。
func writeResultsCSV(path string, rep *Report) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"id", "method", "key", "size_bytes", "status", "elapsed_ms", "etag", "local_md5", "matched", "error"})
	for _, r := range rep.Results {
		_ = w.Write([]string{
			strconv.Itoa(r.TaskID),
			r.Method,
			r.Key,
			strconv.FormatInt(r.Size, 10),
			string(r.Status),
			strconv.FormatInt(r.Elapsed.Milliseconds(), 10),
			r.RemoteETag,
			r.LocalMD5,
			strconv.FormatBool(r.HashMatched),
			r.ErrMessage,
		})
	}
	return nil
}

// pct 返回 num 占 total 的百分比（0-100）；total 为 0 时返回 0，避免除零。
func pct(num, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(num) / float64(total)
}

// formatBytes 按量级把字节数格式化为 B / KB / MB / GB 字符串（1024 进制）。
func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * 1024
		GB = 1024 * 1024 * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// truncate 把 s 截断到最多 n 个字符；超长时保留前 n-3 个字符并追加 "..."。
// n < 3 时直接截断为 n 个字符（不追加省略号，避免越界）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
