// Package main 是 upload-tool CLI 的入口点。
// 本测试文件覆盖 flag override 与 dry-run 行为。
package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig 在临时目录下写入一份 YAML 配置文件并返回其路径。
func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// baseYAML 是测试用基础配置：合法、可被 config.Load 解析与 Validate 通过。
const baseYAML = `
endpoint:
  url: http://localhost:9000
  access_key: ak
  secret_key: sk
bucket: b
methods: [put]
sizes:
  - [1, 100, 100]
runner:
  total: 5
  out_prefix: out
`

// TestApplyFlagOverrides 验证 loadAndApply 将命令行 flag 覆盖到配置：
// -n 覆盖 runner.total，--concurrency 覆盖 runner.concurrency，
// --methods 以逗号分隔覆盖 methods 列表。
func TestApplyFlagOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "c.yaml", baseYAML)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg, err := loadAndApply(fs, []string{"-c", cfgPath, "-n", "10", "--concurrency", "4", "--methods", "put,post"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runner.Total != 10 {
		t.Errorf("total = %d, want 10 (override)", cfg.Runner.Total)
	}
	if cfg.Runner.Concurrency != 4 {
		t.Errorf("concurrency = %d", cfg.Runner.Concurrency)
	}
	if len(cfg.Methods) != 2 {
		t.Errorf("methods = %v", cfg.Methods)
	}
}

// TestDryRunPrintsConfig 验证 dry-run 模式输出包含预期标记与首 10 条 task 摘要。
func TestDryRunPrintsConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "c.yaml", baseYAML)
	var buf bytes.Buffer
	if err := runDryRun(cfgPath, &buf, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"=== Dry Run ===", "Config OK", "First 10 tasks"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q\nGot:\n%s", want, out)
		}
	}
}

// TestBadConfigExitsNonZero 验证无效配置（access_key/secret_key 为空）
// 在 dry-run 阶段被 config.Validate 拒绝并返回 error。
// pre-flight T21 ruling #1：已删除原 brief 中无意义的 `_ = context.Background`
// 死代码行，并相应移除未使用的 "context" import。
func TestBadConfigExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	bad := `
endpoint:
  url: http://x
  access_key: ""
  secret_key: ""
bucket: b
sizes:
  - [1, 100, 100]
runner:
  total: 5
`
	cfgPath := writeConfig(t, dir, "bad.yaml", bad)
	if err := runDryRun(cfgPath, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Errorf("expected error from invalid config")
	}
}
