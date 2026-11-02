package content

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
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

// TestFixedCharGeneratorFillsByte 验证 FixedCharGenerator 将 size 字节缓冲区
// 全部填充为 Char 指定的字节值。覆盖 0x00、'a'、0xFF 三个典型取值，
// 每次生成 256 字节并逐字节比对，确保填充行为与具体字节无关。
func TestFixedCharGeneratorFillsByte(t *testing.T) {
	for _, c := range []byte{0x00, 'a', 0xFF} {
		g := &FixedCharGenerator{Char: c}
		r, _ := g.Generate(256, 0)
		b := mustReadAll(t, r)
		if len(b) != 256 {
			t.Errorf("len = %d, want 256", len(b))
		}
		for i, x := range b {
			if x != c {
				t.Errorf("byte[%d] = %d, want %d", i, x, c)
			}
		}
	}
}

// TestFixedCharGeneratorType 验证类型标识为 "fixed"。
func TestFixedCharGeneratorType(t *testing.T) {
	if (&FixedCharGenerator{}).Type() != "fixed" {
		t.Errorf("Type() != fixed")
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

// TestFileGeneratorOpensFile 验证 FileGenerator 通过 os.Open 打开 Path 指向的文件，
// 并返回完整可读的 io.Reader。size 与 taskID 参数在该实现中被忽略，
// 调用方（runner）负责通过 os.Stat 单独填充 UploadSpec.Size。
func TestFileGeneratorOpensFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.bin")
	content := []byte{0xde, 0xad, 0xbe, 0xef}
	if err := os.WriteFile(p, content, 0644); err != nil {
		t.Fatal(err)
	}
	g := &FileGenerator{Path: p}
	r, err := g.Generate(999, 7) // size param ignored
	if err != nil {
		t.Fatal(err)
	}
	got := mustReadAll(t, r)
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %v want %v", got, content)
	}
}

// TestFileGeneratorType 验证类型标识为 "file"。
func TestFileGeneratorType(t *testing.T) {
	if (&FileGenerator{}).Type() != "file" {
		t.Errorf("Type() != file")
	}
}
