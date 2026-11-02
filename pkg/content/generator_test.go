package content

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
)

// TestRandomGeneratorDeterministic 验证相同的 BaseSeed 与 taskID 产生完全一致的字节序列。
// 这是后续断言与重试校验的基础：同一 (seed, taskID) 必须可重现。
func TestRandomGeneratorDeterministic(t *testing.T) {
	g := &RandomGenerator{BaseSeed: 42}
	r1, err := g.Generate(1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := g.Generate(1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	h1 := sha256.Sum256(mustReadAll(t, r1))
	h2 := sha256.Sum256(mustReadAll(t, r2))
	if hex.EncodeToString(h1[:]) != hex.EncodeToString(h2[:]) {
		t.Errorf("same seed/taskID produced different bytes")
	}
}

// TestRandomGeneratorDifferentTaskIDs 验证不同 taskID 在同一 BaseSeed 下产生不同的字节序列，
// 确保并发上传时各 task 的内容互不相同。
func TestRandomGeneratorDifferentTaskIDs(t *testing.T) {
	g := &RandomGenerator{BaseSeed: 42}
	r1, _ := g.Generate(1024, 0)
	r2, _ := g.Generate(1024, 1)
	h1 := sha256.Sum256(mustReadAll(t, r1))
	h2 := sha256.Sum256(mustReadAll(t, r2))
	if hex.EncodeToString(h1[:]) == hex.EncodeToString(h2[:]) {
		t.Errorf("different taskIDs produced same bytes")
	}
}

// TestRandomGeneratorSizeHonored 验证 Generate 返回的 Reader 恰好产生 size 字节，
// 涵盖从 1 字节到 1MiB 的多种量级。
func TestRandomGeneratorSizeHonored(t *testing.T) {
	g := &RandomGenerator{BaseSeed: 42}
	for _, sz := range []int64{1, 100, 4096, 1 << 20} {
		r, _ := g.Generate(sz, 0)
		n, err := io.Copy(io.Discard, r)
		if err != nil {
			t.Errorf("size %d: %v", sz, err)
		}
		if n != sz {
			t.Errorf("size %d: read %d bytes", sz, n)
		}
	}
}

// TestRandomGeneratorType 验证类型标识为 "random"。
func TestRandomGeneratorType(t *testing.T) {
	if (&RandomGenerator{}).Type() != "random" {
		t.Errorf("Type() != random")
	}
}

// mustReadAll 读取 r 的全部内容，出错时终止测试。
// 该辅助函数集中处理 io.ReadAll 的错误，使测试主体保持简洁。
func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
