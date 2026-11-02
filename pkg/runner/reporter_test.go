// 本文件为 Task 18 Reporter 的单元测试，覆盖：
//   - TestReporterWritesAllFormats：writeResultsJSON=true 时写出 4 个文件；
//   - TestReporterSkipsResultsJSONWhenDisabled：writeResultsJSON=false 时跳过 results.json；
//   - TestReporterConsoleSummary：控制台汇总包含关键行；
//   - TestReporterSortsByMethodAndByError：ByMethod / ByError 在输出前被稳定排序。
package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// sampleReport 构造一份可复用的样例 Report，覆盖成功 / 失败两种 Result。
func sampleReport() *Report {
	return &Report{
		Meta: ReportMeta{
			StartedAt:       time.Date(2026, 10, 26, 10, 48, 12, 0, time.UTC),
			EndedAt:         time.Date(2026, 10, 26, 10, 49, 47, 0, time.UTC),
			DurationSeconds: 95,
			EndpointName:    "minio-local",
			EndpointURL:     "http://localhost:9000",
			Bucket:          "test-bucket",
			Concurrency:     32,
			Methods:         []string{"put", "post"},
			BaseSeed:        12345,
		},
		Summary: ReportSummary{
			Total:          2,
			Success:        1,
			Failed:         1,
			TotalBytes:     100,
			ThroughputMBps: 0.000001,
			ByMethod: []MethodStat{
				{Method: "put", Count: 1, Success: 1, Bytes: 50, AvgLatencyMS: 120},
				{Method: "post", Count: 1, Failed: 1, Bytes: 50, AvgLatencyMS: 380},
			},
			ByError: []ErrorStat{{Error: "RequestTimeout", Count: 1}},
		},
		Results: []Result{
			{TaskID: 0, Method: "put", Key: "k1", Size: 50, Status: StatusSuccess, Elapsed: 120 * time.Millisecond, LocalMD5: "a", RemoteETag: "a", HashMatched: true, ErrMessage: ""},
			{TaskID: 1, Method: "post", Key: "k2", Size: 50, Status: StatusFailedUpload, Elapsed: 380 * time.Millisecond, ErrMessage: "RequestTimeout"},
		},
	}
}

// TestReporterWritesAllFormats 验证 writeResultsJSON=true 时，
// summary.json / summary.csv / results.csv / results.json 四个文件均被写出。
func TestReporterWritesAllFormats(t *testing.T) {
	dir := t.TempDir()
	r := NewReporter(dir, true, nil)
	rep := sampleReport()
	if err := r.Write(rep); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, f := range []string{"summary.json", "summary.csv", "results.csv", "results.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
}

// TestReporterSkipsResultsJSONWhenDisabled 验证 writeResultsJSON=false 时，
// results.json 不被写出。
func TestReporterSkipsResultsJSONWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	r := NewReporter(dir, false, nil)
	if err := r.Write(sampleReport()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "results.json")); !os.IsNotExist(err) {
		t.Errorf("results.json should not exist when writeResultsJSON=false")
	}
}

// TestReporterConsoleSummary 验证控制台汇总输出包含关键行。
func TestReporterConsoleSummary(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(t.TempDir(), false, nil)
	r.PrintConsoleSummary(sampleReport(), &buf)
	out := buf.String()
	for _, want := range []string{"=== Run Summary ===", "Endpoint: minio-local", "Total objects: 2", "Output dir:"} {
		if !strings.Contains(out, want) {
			t.Errorf("console output missing %q\nGot:\n%s", want, out)
		}
	}
}

// TestReporterSortsByMethodAndByError 验证 Reporter 在 Write 时对
// ByMethod 按 Method 升序、ByError 按 Count 降序（Error 升序兜底）排序，
// 保证 summary.json 的输出顺序稳定可复现。
func TestReporterSortsByMethodAndByError(t *testing.T) {
	dir := t.TempDir()
	r := NewReporter(dir, false, nil)
	rep := &Report{
		Meta: ReportMeta{
			StartedAt:       time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC),
			EndedAt:         time.Date(2026, 10, 26, 10, 1, 0, 0, time.UTC),
			DurationSeconds: 60,
			EndpointName:    "minio-local",
			EndpointURL:     "http://localhost:9000",
			Bucket:          "test-bucket",
			Concurrency:     4,
			Methods:         []string{"put", "post"},
			BaseSeed:        1,
		},
		Summary: ReportSummary{
			Total:          4,
			Success:        1,
			Failed:         3,
			TotalBytes:     200,
			ThroughputMBps: 0.0,
			// 故意以非字母序输入，验证排序后应为 ["post", "put"]。
			ByMethod: []MethodStat{
				{Method: "put", Count: 1, Success: 1, Bytes: 100, AvgLatencyMS: 100},
				{Method: "post", Count: 3, Failed: 3, Bytes: 100, AvgLatencyMS: 200},
			},
			// 故意乱序输入：Count 3 的两条在前，Count 1 的一条居中。
			// 排序后期望：[{"a", 3}, {"c", 3}, {"b", 1}]（Count 降序，Error 升序兜底）。
			ByError: []ErrorStat{
				{Error: "b", Count: 1},
				{Error: "a", Count: 3},
				{Error: "c", Count: 3},
			},
		},
		Results: []Result{},
	}

	if err := r.Write(rep); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("read summary.json: %v", err)
	}

	var payload struct {
		Summary ReportSummary `json:"summary"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal summary.json: %v\nraw: %s", err, string(data))
	}

	// 校验 ByMethod 升序。
	gotMethods := make([]string, len(payload.Summary.ByMethod))
	for i, m := range payload.Summary.ByMethod {
		gotMethods[i] = m.Method
	}
	wantMethods := []string{"post", "put"}
	if !sliceEqual(gotMethods, wantMethods) {
		t.Errorf("ByMethod order = %v, want %v", gotMethods, wantMethods)
	}

	// 校验 ByError：Count 降序，Error 升序兜底。
	gotErrors := payload.Summary.ByError
	wantErrors := []ErrorStat{
		{Error: "a", Count: 3},
		{Error: "c", Count: 3},
		{Error: "b", Count: 1},
	}
	if len(gotErrors) != len(wantErrors) {
		t.Fatalf("ByError length = %d, want %d (got %+v)", len(gotErrors), len(wantErrors), gotErrors)
	}
	for i, want := range wantErrors {
		if gotErrors[i] != want {
			t.Errorf("ByError[%d] = %+v, want %+v", i, gotErrors[i], want)
		}
	}

	// 顺便断言 sort 包仍在 imports（防止误删）。
	_ = sort.Strings
}

// sliceEqual 比较两个字符串切片是否完全相等。
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
