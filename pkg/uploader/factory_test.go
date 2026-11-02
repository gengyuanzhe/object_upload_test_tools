package uploader

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestNewAwsUploaderFactoryReturnsAllMethods 验证 NewAwsUploaderFactory 返回的
// 工厂 map 覆盖四种上传方式（put/post/multipart/presigned），每个闭包接受
// 一个 *s3.Client 并返回实现了 Uploader 接口的实例，且其 Method() 与
// 工厂 key 一致。
//
// 在 brief 给定用例的基础上，额外断言 len(f) == 4，确保既不缺失也不多余
// 任何 key（防止后续任务意外新增或遗漏上传方式时静默通过）。
func TestNewAwsUploaderFactoryReturnsAllMethods(t *testing.T) {
	f := NewAwsUploaderFactory()

	// 额外断言：工厂 map 的 key 数量必须严格等于 4。
	if len(f) != 4 {
		t.Errorf("factory map length = %d, want 4", len(f))
	}

	for _, m := range []string{"put", "post", "multipart", "presigned"} {
		fn, ok := f[m]
		if !ok {
			t.Errorf("missing factory for %q", m)
			continue
		}
		if fn == nil {
			t.Errorf("factory for %q is nil", m)
			continue
		}
		uploader := fn(s3.New(s3.Options{}))
		if uploader.Method() != m {
			t.Errorf("factory for %q returned Method=%q", m, uploader.Method())
		}
	}
}
