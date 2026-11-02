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
// 当前已实现 "random" 与 "fixed" 两种类型；"file" 的具体实现由
// Task 7（FileGenerator）补全，届时将把对应 case 的返回值替换为真实实现。
func NewFromConfig(cfg config.ContentConfig) Generator {
	switch cfg.Type {
	case "fixed":
		return &FixedCharGenerator{Char: byte(cfg.Char)}
	case "file":
		// TODO: Task 7 将补全 FileGenerator{Path: cfg.Path}。
		return nil
	default:
		return &RandomGenerator{BaseSeed: cfg.Seed}
	}
}
