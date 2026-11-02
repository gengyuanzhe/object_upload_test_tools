package content

import (
	"bytes"
	"io"
)

// FixedCharGenerator 将固定字符填充到指定长度的字节缓冲区，
// 用于需要稳定、可预测内容（例如校验位、占位数据）的场景。
// Char 字段为待填充字节值（0..255）。
type FixedCharGenerator struct {
	Char byte
}

// Type 返回内容类型标识 "fixed"。
func (g *FixedCharGenerator) Type() string { return "fixed" }

// Generate 返回一个 io.Reader，其内容为长度恰为 size 的、
// 每个字节均等于 g.Char 的缓冲区。taskID 被忽略：
// 固定字符内容与具体任务无关，多次调用返回等价的数据。
func (g *FixedCharGenerator) Generate(size int64, taskID int) (io.Reader, error) {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = g.Char
	}
	return bytes.NewReader(buf), nil
}
