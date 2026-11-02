package runner

import (
	"time"

	"object_upload_test_tools/pkg/content"
	"object_upload_test_tools/pkg/uploader"
)

// Task 描述一次上传测试任务的全部静态输入。
//
// ID 为任务在 runner 调度范围内的唯一序号，同时用于派生 RandomGenerator
// 的种子，确保不同任务生成不同内容但同一任务可复现。
// Method 指定上传方式（put/post/multipart/presigned），由 factory 分发
// 到对应 Uploader 实现。
// Bucket 与 Key 为目标对象位置；Size 为本次上传的目标字节数。
// Generator 提供流式内容；Spec 是预构造的 uploader.UploadSpec，
// executeTask 会将 Content 字段在执行时填入由 Generator 产生的 Reader。
type Task struct {
	ID        int
	Method    string
	Bucket    string
	Key       string
	Size      int64
	Generator content.Generator
	Spec      uploader.UploadSpec
}

// ResultStatus 是 Result.Status 的字符串枚举类型。
// 取值固定为 StatusSuccess / StatusFailedUpload / StatusFailedVerify 三者，
// 字面量同时作为 Reporter（Task 18）输出 CSV/JSON 时的状态字段值。
type ResultStatus string

const (
	// StatusSuccess 表示任务上传与校验均成功。
	StatusSuccess ResultStatus = "success"

	// StatusFailedUpload 表示上传阶段失败（ErrUploadFailed 或 ErrContentGenerate）。
	StatusFailedUpload ResultStatus = "failed-upload"

	// StatusFailedVerify 表示上传成功但校验阶段失败（ErrVerifyFailed）。
	StatusFailedVerify ResultStatus = "failed-verify"
)

// Result 描述一次 Task 执行后的全部输出与度量信息。
//
// TaskID/Method/Key/Size 与对应 Task 字段镜像，便于 Reporter
// 在不持有原始 Task 引用时直接基于 Result 输出报表。
// Status 为最终状态分类；ErrMessage 为失败时的可读错误描述（非原始 error）。
// Elapsed 为整次执行（生成 + 上传 + 校验）的墙钟耗时。
// UploadResult 为 Uploader.Upload 返回的结构化结果（含 ETag、BytesIn 等）。
// LocalMD5/RemoteETag/HashMatched 为 Verifier（Task 16）的输出，
// HashMatched=true 即代表数据一致性校验通过。
type Result struct {
	TaskID       int
	Method       string
	Key          string
	Size         int64
	Status       ResultStatus
	ErrMessage   string
	Elapsed      time.Duration
	UploadResult uploader.UploadResult
	LocalMD5     string
	RemoteETag   string
	HashMatched  bool
}

// multipartState 是 runner 内部用于在 executeTask 与 Verifier 之间
// 传递 multipart 分片状态的私有类型（包级私有，不导出）。
//
// PartMD5s 为各分片 MD5 字符串列表（顺序与上传分片顺序一致）；
// PartCount 为实际分片数。Task 17 的 executeTask 会从
// UploadResult.Details["part_md5s"] 等键中提取该结构，
// 并传递给 Verifier.Verify 用于分片级 ETag 复算与一致性校验。
//
// 不导出是为了避免 Reporter 或 CLI 直接依赖 multipart 的内部表示，
// 保持 Verifier 接口为唯一对该结构有解释权的边界。
type multipartState struct {
	PartMD5s  []string
	PartCount int
}
