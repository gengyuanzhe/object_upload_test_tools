package content

import (
	"io"
	"os"
)

// FileGenerator 以本地文件作为内容来源的 Generator 实现。
// Path 指向待上传的文件路径；Generate 通过 os.Open 打开该文件，
// 返回的 *os.File 实现了 io.Reader，调用方负责在读取完毕后关闭。
type FileGenerator struct {
	Path string
}

// Type 返回内容类型标识 "file"。
func (g *FileGenerator) Type() string { return "file" }

// Generate 打开 Path 指定的文件并返回其 *os.File 句柄。
// size 与 taskID 参数被忽略：runner 负责通过 os.Stat 单独获取
// 文件大小并填充 UploadSpec.Size，以保证与磁盘真实大小一致。
func (g *FileGenerator) Generate(size int64, taskID int) (io.Reader, error) {
	f, err := os.Open(g.Path)
	if err != nil {
		return nil, err
	}
	return f, nil
}
