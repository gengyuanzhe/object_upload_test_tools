package runner

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestProgressNonTTYOutputsPlainText 验证非 TTY 模式下 Progress 输出纯文本进度行。
// 测试覆盖：total=100，IncSuccess(50)，期望输出包含 "/100"。
func TestProgressNonTTYOutputsPlainText(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(100, &buf)
	p.isTTY = false
	p.tickInterval = 50 * time.Millisecond
	p.Start()
	p.IncSuccess(50)
	time.Sleep(120 * time.Millisecond)
	p.Stop(true)
	out := buf.String()
	if !strings.Contains(out, "/100") {
		t.Errorf("expected total in output, got %q", out)
	}
}

// TestProgressTTYOutputsBar 验证 TTY 模式下 Progress 输出带进度条的单行。
// 测试覆盖：total=100，IncSuccess(80)，期望输出包含 "100"。
func TestProgressTTYOutputsBar(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(100, &buf)
	p.isTTY = true
	p.tickInterval = 50 * time.Millisecond
	p.Start()
	p.IncSuccess(80)
	time.Sleep(120 * time.Millisecond)
	p.Stop(true)
	out := buf.String()
	if !strings.Contains(out, "100") {
		t.Errorf("expected total in output, got %q", out)
	}
}

// TestProgressStopIdempotent 验证 Stop 多次调用不会 panic。
// 锁定 sync.Once 守卫机制，防止 CLI 在 defer 和 signal handler 中重复调用导致崩溃。
func TestProgressStopIdempotent(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(100, &buf)
	p.isTTY = false
	p.tickInterval = 50 * time.Millisecond
	p.Start()
	p.IncSuccess(10)
	time.Sleep(60 * time.Millisecond)
	// 第一次 Stop 应正常完成并渲染 final 行
	p.Stop(true)
	// 第二次 Stop 不应 panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Stop should not panic, got %v", r)
		}
	}()
	p.Stop(true)
}
