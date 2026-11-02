package runner

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// Progress 负责在长耗时上传过程中向 io.Writer 渲染进度信息。
//
// 渲染模式分两种：
//   - TTY 模式：原地刷新单行进度条，包含 bar/done/total/pct/success/failed/rate/ETA。
//   - 非 TTY 模式：每个 tick 输出一行纯文本进度，便于日志采集。
//
// 通过 golang.org/x/term.IsTerminal 自动检测 TTY；调用方可通过 isTTY 字段覆盖。
type Progress struct {
	total        int64
	success      atomic.Int64
	failed       atomic.Int64
	bytes        atomic.Int64
	startedAt    time.Time
	w            io.Writer
	isTTY        bool
	tickInterval time.Duration
	stopCh       chan struct{}
	done         chan struct{}
	stopOnce     sync.Once // 守卫 stopCh，防止 Stop 重复调用 panic
}

// NewProgress 创建一个 Progress 实例。
// total 为目标完成数；w 为输出 sink。
// 当 w 是 *os.File 时，通过 term.IsTerminal 自动判定 TTY 模式。
func NewProgress(total int, w io.Writer) *Progress {
	isTTY := false
	if f, ok := w.(*os.File); ok {
		isTTY = term.IsTerminal(int(f.Fd()))
	}
	return &Progress{
		total:        int64(total),
		w:            w,
		isTTY:        isTTY,
		tickInterval: 200 * time.Millisecond,
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// IncSuccess 原子地累加成功计数。
func (p *Progress) IncSuccess(n int64) { p.success.Add(n) }

// IncFailed 原子地累加失败计数。
func (p *Progress) IncFailed(n int64) { p.failed.Add(n) }

// IncBytes 原子地累加上传字节数，用于计算速率。
func (p *Progress) IncBytes(n int64) { p.bytes.Add(n) }

// Start 记录起始时间并启动 ticker goroutine。
func (p *Progress) Start() {
	p.startedAt = time.Now()
	go p.loop()
}

// Stop 通知 ticker goroutine 退出并等待其结束。
// final 为 true 时额外渲染一次最终状态行。
// 通过 sync.Once 守卫，支持多次调用幂等，避免 CLI defer 与 signal handler 同时触发时 panic。
func (p *Progress) Stop(final bool) {
	p.stopOnce.Do(func() { close(p.stopCh) })
	<-p.done
	if final {
		p.render(true)
	}
}

// loop 是 Progress 的主循环 goroutine。
// 监听 stopCh 与 ticker，每个 tick 调用 render 渲染进度。
func (p *Progress) loop() {
	defer close(p.done)
	t := time.NewTicker(p.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.render(false)
		}
	}
}

// render 渲染一次进度行。
// final 标记是否为终止渲染（TTY 模式末尾追加换行）。
func (p *Progress) render(final bool) {
	done := p.success.Load() + p.failed.Load()
	bytes := p.bytes.Load()
	elapsed := time.Since(p.startedAt).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(bytes) / (1024 * 1024) / elapsed
	}
	pct := 0.0
	if p.total > 0 {
		pct = 100 * float64(done) / float64(p.total)
	}
	eta := 0.0
	if rate > 0 && done < p.total {
		eta = (float64(p.total-done) / float64(done+1)) * elapsed
	}

	if p.isTTY {
		bar := renderBar(pct)
		line := fmt.Sprintf("\r%s %d/%d (%.1f%%) | ✓ %d ✗ %d | %.1f MB/s | ETA %.0fs",
			bar, done, p.total, pct, p.success.Load(), p.failed.Load(), rate, eta)
		if final {
			line += "\n"
		}
		_, _ = io.WriteString(p.w, line)
	} else {
		line := fmt.Sprintf("progress: %d/%d (%.1f%%) success=%d failed=%d rate=%.1fMB/s\n",
			done, p.total, pct, p.success.Load(), p.failed.Load(), rate)
		_, _ = io.WriteString(p.w, line)
	}
}

// renderBar 根据 pct 渲染固定宽度为 12 的进度条。
// 已完成部分使用 █，未完成部分使用 ░。
func renderBar(pct float64) string {
	const width = 12
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}
