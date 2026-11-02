package runner

import (
	"testing"
	"time"

	"object_upload_test_tools/pkg/content"
	"object_upload_test_tools/pkg/uploader"
)

// TestTaskResultTypesCompile 是 Task 14 的编译/往返测试，
// 验证 Task、Result、multipartState 三个类型的字段可被外部访问与赋值。
// 该用例不包含行为断言，仅保证类型契约可被编译器接受。
func TestTaskResultTypesCompile(t *testing.T) {
	task := Task{
		ID:        1,
		Method:    "put",
		Bucket:    "b",
		Key:       "k",
		Size:      1024,
		Generator: &content.RandomGenerator{BaseSeed: 1},
		Spec:      uploader.UploadSpec{Bucket: "b", Key: "k", Size: 1024},
	}
	_ = task

	res := Result{
		TaskID:      1,
		Method:      "put",
		Key:         "k",
		Size:        1024,
		Status:      StatusSuccess,
		ErrMessage:  "",
		Elapsed:     time.Second,
		LocalMD5:    "abc",
		RemoteETag:  "abc",
		HashMatched: true,
	}
	_ = res

	ms := multipartState{PartMD5s: []string{"a", "b"}, PartCount: 2}
	_ = ms
}

// TestSentinelErrorsExist 验证五个哨兵错误变量存在且可被引用。
func TestSentinelErrorsExist(t *testing.T) {
	_ = ErrConfigInvalid
	_ = ErrContentGenerate
	_ = ErrUploadFailed
	_ = ErrVerifyFailed
	_ = ErrContextCanceled
}

// TestResultStatusConstants 验证三个 ResultStatus 常量的字面量值，
// 防止后续任务在序列化或比较时因常量值漂移而静默通过。
func TestResultStatusConstants(t *testing.T) {
	if StatusSuccess != "success" {
		t.Errorf("StatusSuccess = %q, want %q", StatusSuccess, "success")
	}
	if StatusFailedUpload != "failed-upload" {
		t.Errorf("StatusFailedUpload = %q, want %q", StatusFailedUpload, "failed-upload")
	}
	if StatusFailedVerify != "failed-verify" {
		t.Errorf("StatusFailedVerify = %q, want %q", StatusFailedVerify, "failed-verify")
	}
}
