package content

import (
	"io"
	"object_upload_test_tools/pkg/config"
)

// Generator 描述测试内容的生成器接口。
// Type 返回内容类型标识（"random"|"fixed"|"file"）；
// Generate 按给定 size 与 taskID 返回一个流式 Reader，
// 调用方负责完整读取并关闭 Reader。
type Generator interface {
	Type() string
	Generate(size int64, taskID int) (io.Reader, error)
}

// NewFromConfig 根据 cfg 的 Type 字段选择并返回对应的 Generator 实现。
// 当前已实现 "random"、"fixed" 与 "file" 三种类型：未识别的 Type
// 退化为 RandomGenerator，以保证默认行为可重现且无副作用。
func NewFromConfig(cfg config.ContentConfig) Generator {
	switch cfg.Type {
	case "fixed":
		return &FixedCharGenerator{Char: byte(cfg.Char)}
	case "file":
		return &FileGenerator{Path: cfg.Path}
	default:
		return &RandomGenerator{BaseSeed: cfg.Seed}
	}
}
