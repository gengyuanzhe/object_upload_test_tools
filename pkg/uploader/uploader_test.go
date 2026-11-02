package uploader

import (
	"context"
	"io"
	"testing"
)

func TestTypesCompile(t *testing.T) {
	var _ Uploader = (*mockUploader)(nil)
	s := UploadSpec{
		Bucket:    "b",
		Key:       "k",
		Content:   io.Reader(nil),
		Size:      1,
		Multipart: &MultipartSpec{PartSize: 5242880},
		Presigned: &PresignedSpec{ExpiresIn: 3600, Method: "put"},
		Post:      &PostSpec{SuccessActionStatus: 201},
	}
	_ = s
	r := UploadResult{ETag: "x", BytesIn: 1}
	_ = r
	_ = context.Background()
}

type mockUploader struct{}

func (m *mockUploader) Method() string { return "mock" }
func (m *mockUploader) Upload(ctx context.Context, spec UploadSpec) (UploadResult, error) {
	return UploadResult{}, nil
}
