package runner

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Verifier 是基于 S3 HEAD 请求的远端 ETag 校验器。
//
// 它只持有一个 *s3.Client，无任何可变状态，因此同一个 Verifier
// 可被多 goroutine 安全并发调用 Verify 方法。
// Verify 通过 HeadObject 拉取远端对象 ETag，并将其与本地 MD5（单次上传）
// 或 multipart 复算 ETag（分片上传）比对，从而判断上传后的数据一致性。
type Verifier struct {
	client *s3.Client
}

// NewVerifier 构造一个绑定到指定 s3.Client 的 Verifier。
// 调用方负责 client 的初始化（Region、Credentials、BaseEndpoint 等）。
func NewVerifier(client *s3.Client) *Verifier {
	return &Verifier{client: client}
}

// Verify 通过 HEAD 请求获取远端 ETag 并与本地校验值比对。
//
// 参数：
//   - bucket / key：目标对象位置；
//   - localMD5：单次上传（ms == nil）场景下由调用方计算的本地内容 MD5 十六进制串；
//   - ms：multipart 状态；非 nil 时按 S3 规范复算 multipart ETag。
//
// 行为：
//   - 调用 s3.Client.HeadObject 拉取远端 ETag；
//   - ETag 以 *string 返回，使用 aws.ToString 安全解引用（nil 时得到 ""），
//     再用 strings.Trim 去掉 S3 默认包裹的双引号；
//   - ms == nil：直接比较 localMD5 == remoteETag；
//   - ms != nil：逐个 hex.Decode 每个 PartMD5 拼接为 concat，
//     计算 md5(concat) 的十六进制串并拼接 "-" + strconv.Itoa(PartCount)，
//     再与 remoteETag 比对（即 S3 multipart ETag 规范）。
//
// 返回 (remoteETag, matched, err)：
//   - HeadObject 失败时 err 非 nil（如 404 映射为 *types.NotFound）；
//   - 任一 PartMD5 解码失败时 err 非 nil；
//   - 比对结果通过 matched 返回，比对口径不一致时 err 仍为 nil（业务失败而非系统错误）。
func (v *Verifier) Verify(ctx context.Context, bucket, key, localMD5 string, ms *multipartState) (string, bool, error) {
	out, err := v.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", false, err
	}
	// aws.ToString 对 nil *string 返回 ""，避免服务器未返回 ETag 头时解引用 panic；
	// strings.Trim 去掉 S3 HEAD/PUT/Complete 响应统一包裹的双引号。
	remoteETag := strings.Trim(aws.ToString(out.ETag), `"`)

	if ms == nil {
		return remoteETag, remoteETag == localMD5, nil
	}

	var concat []byte
	for _, pmd5 := range ms.PartMD5s {
		b, err := hex.DecodeString(pmd5)
		if err != nil {
			return remoteETag, false, err
		}
		concat = append(concat, b...)
	}
	sum := md5.Sum(concat)
	expected := hex.EncodeToString(sum[:]) + "-" + strconv.Itoa(ms.PartCount)
	return remoteETag, remoteETag == expected, nil
}
