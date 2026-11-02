package uploader

import "github.com/aws/aws-sdk-go-v2/service/s3"

// NewAwsUploaderFactory 返回一个按 Method 字符串索引的 AWS 上传器构造工厂 map。
//
// 该工厂 map 以 "put"、"post"、"multipart"、"presigned" 四个字符串作为 key，
// 每个 value 为一个接受 *s3.Client 并返回 Uploader 接口的闭包构造器。
// 运行时（runner）从配置中读取 method 字段后，通过该 map 查找对应构造器，
// 传入预先初始化好的 s3.Client，得到具体 Uploader 实例。
//
// 返回 map（而非 typed struct）是有意为之：上游配置以字符串 key 表达
// 上传方式，map 查找是最直接的分发方式，且便于后续按需扩展。
func NewAwsUploaderFactory() map[string]func(*s3.Client) Uploader {
	return map[string]func(*s3.Client) Uploader{
		"put":       func(c *s3.Client) Uploader { return NewAwsPutter(c) },
		"post":      func(c *s3.Client) Uploader { return NewAwsPoster(c) },
		"multipart": func(c *s3.Client) Uploader { return NewAwsMultipartUploader(c) },
		"presigned": func(c *s3.Client) Uploader { return NewAwsPresignedUploader(c) },
	}
}
