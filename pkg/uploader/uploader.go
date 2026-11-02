// Package uploader 定义 S3 对象上传测试工具的上传抽象层。
//
// 本文件仅依赖标准库类型（context、io、time），不引入任何 AWS SDK 依赖。
// 具体的 AWS SDK 实现由后续任务（tasks 9-13）在独立的实现文件中提供，
// 通过工厂函数按 Method 选择具体实现。
package uploader

import (
	"context"
	"io"
	"time"
)

// Uploader 是所有上传实现的统一抽象接口。
//
// Method 返回该实现所采用的上传方式标识（如 "simple"、"multipart"、"presigned"、"post"），
// 用于配置校验、日志输出以及工厂函数分发。
//
// Upload 执行一次上传请求，依据 UploadSpec 中携带的可选子规格
// （Multipart、Presigned、Post）由具体实现决定实际行为。
type Uploader interface {
	Method() string
	Upload(ctx context.Context, spec UploadSpec) (UploadResult, error)
}

// UploadSpec 描述一次上传请求的全部输入。
//
// Bucket 与 Key 指定目标对象位置；Content 为待上传数据的 io.Reader；
// Size 为可选的预估字节数，便于分片或进度展示；Metadata 为对象元数据键值对。
//
// Multipart、Presigned、Post 三个指针字段互斥使用：
//   - Multipart 非空时采用 multipart 分片上传策略；
//   - Presigned 非空时采用预签名 URL 方式（PUT 或 POST）；
//   - Post 非空时采用浏览器表单 POST 上传方式。
//
// 具体实现负责校验三者之间的语义互斥性。
type UploadSpec struct {
	Bucket   string
	Key      string
	Content  io.Reader
	Size     int64
	Metadata map[string]string

	Multipart *MultipartSpec
	Presigned *PresignedSpec
	Post      *PostSpec
}

// MultipartSpec 描述 multipart 分片上传的相关参数。
//
// PartSize 为每个分片的目标大小（字节数）。
// S3 最小分片大小为 5 MiB（5242880 字节），具体实现应校验该下限。
type MultipartSpec struct {
	PartSize int64
}

// PresignedSpec 描述预签名 URL 上传的参数。
//
// ExpiresIn 为预签名 URL 的有效期，单位由 time.Duration 决定（通常为秒）。
// Method 指定 HTTP 方法，取值为 "put" 或 "post"。
type PresignedSpec struct {
	ExpiresIn time.Duration
	Method    string // "put" 或 "post"
}

// PostSpec 描述浏览器表单 POST 上传的参数。
//
// SuccessActionStatus 为上传成功时 S3 返回的 HTTP 状态码（如 200、201、204）。
// ExtraFields 用于附加额外的表单字段（如 Content-Type、acl 等）。
type PostSpec struct {
	SuccessActionStatus int
	ExtraFields         map[string]string
}

// UploadResult 描述一次上传请求的结果与度量信息。
//
// Location 为对象最终访问 URL（如适用）；ETag 为 S3 返回的实体标签；
// VersionID 为启用版本控制时的对象版本 ID；Elapsed 为上传耗时；
// BytesIn 为实际写入的字节数；Details 用于携带实现特定的扩展信息。
type UploadResult struct {
	Location  string
	ETag      string
	VersionID string
	Elapsed   time.Duration
	BytesIn   int64
	Details   map[string]any
}
