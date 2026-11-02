// Package runner 定义 S3 对象上传测试工具的运行层抽象。
//
// 本文件声明 runner 包对外暴露的哨兵错误（sentinel errors），
// 用于在 Task 执行流水线（Task 17 的 executeTask）中对不同失败阶段
// 进行结构化分类：配置校验、内容生成、上传、校验、上下文取消。
// 调用方通过 errors.Is 与这些哨兵比对，从而决定日志格式与重试策略。
package runner

import "errors"

var (
	// ErrConfigInvalid 表示 runner 配置（如并发度、任务数、size 分布等）
	// 校验失败。该错误通常在 runner 启动早期触发，不应进入执行循环。
	ErrConfigInvalid = errors.New("config invalid")

	// ErrContentGenerate 表示 Generator.Generate 在生成上传内容时失败，
	// 例如文件读取错误或随机源初始化失败。
	ErrContentGenerate = errors.New("content generate failed")

	// ErrUploadFailed 表示 Uploader.Upload 返回非 nil 错误，
	// 即上传请求本身失败（网络、鉴权、S3 服务端错误等）。
	ErrUploadFailed = errors.New("upload failed")

	// ErrVerifyFailed 表示上传成功但校验阶段（MD5/ETag 比对）失败，
	// 通常对应数据一致性损坏或远端 ETag 计算口径不一致。
	ErrVerifyFailed = errors.New("verify failed")

	// ErrContextCanceled 表示 runner 接收到 context 取消信号，
	// 任务在执行过程中被优雅终止（如 Ctrl-C 或超时）。
	ErrContextCanceled = errors.New("context canceled")
)
