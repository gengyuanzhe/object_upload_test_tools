package config

import (
	"testing"
	"time"
)

func TestApplyDefaultsFillsAllFields(t *testing.T) {
	c := Config{}
	ApplyDefaults(&c)

	if c.Endpoint.Region != "us-east-1" {
		t.Errorf("region default = %q, want us-east-1", c.Endpoint.Region)
	}
	if c.Endpoint.UsePathStyle {
		t.Errorf("use_path_style default = true, want false")
	}
	if len(c.Methods) != 1 || c.Methods[0] != "put" {
		t.Errorf("methods default = %v, want [put]", c.Methods)
	}
	if c.MethodOptions.Multipart.PartSize != 5242880 {
		t.Errorf("part_size default = %d, want 5242880", c.MethodOptions.Multipart.PartSize)
	}
	if c.MethodOptions.Post.SuccessActionStatus != 201 {
		t.Errorf("success_action_status default = %d, want 201", c.MethodOptions.Post.SuccessActionStatus)
	}
	if c.MethodOptions.Presigned.ExpiresIn != 3600*time.Second {
		t.Errorf("expires_in default = %v, want 3600s", c.MethodOptions.Presigned.ExpiresIn)
	}
	if c.MethodOptions.Presigned.Method != "put" {
		t.Errorf("presigned method default = %q, want put", c.MethodOptions.Presigned.Method)
	}
	if c.Content.Type != "random" {
		t.Errorf("content.type default = %q, want random", c.Content.Type)
	}
	if c.Runner.Concurrency != 8 {
		t.Errorf("concurrency default = %d, want 8", c.Runner.Concurrency)
	}
	// 注意：ApplyDefaults 无法将 Runner.Verify 默认设为 true，因为 bool 的零值
	// 即为 false，本层无法区分「未设置」与「显式 false」。Verify=true 的默认值
	// 改由 NewDefault() 显式设置，并由 Task 4 的 Load() 通过 raw map 检测
	// 在解码后回填。此处不断言 Verify，相关断言见 TestNewDefault。
	if c.Runner.Cleanup {
		t.Errorf("cleanup default = true, want false")
	}
	if c.Runner.Timeout != 300*time.Second {
		t.Errorf("timeout default = %v, want 300s", c.Runner.Timeout)
	}
	if c.Runner.OutPrefix != "out" {
		t.Errorf("out_prefix default = %q, want out", c.Runner.OutPrefix)
	}
}

func TestApplyDefaultsDoesNotOverrideSetValues(t *testing.T) {
	c := Config{
		Endpoint: EndpointConfig{Region: "eu-west-1"},
		Runner:  RunnerConfig{Concurrency: 64},
	}
	ApplyDefaults(&c)
	if c.Endpoint.Region != "eu-west-1" {
		t.Errorf("region overridden to %q", c.Endpoint.Region)
	}
	if c.Runner.Concurrency != 64 {
		t.Errorf("concurrency overridden to %d", c.Runner.Concurrency)
	}
}

// TestApplyDefaultsLeavesVerifyFalse 显式记录已知 awkward bit：
// ApplyDefaults 不会将 Runner.Verify 默认设为 true（因为 bool 零值即 false，
// 本层无法区分未设置与显式 false）。Verify=true 的默认值由 NewDefault()
// 与 Task 4 的 Load() 协同处理。
func TestApplyDefaultsLeavesVerifyFalse(t *testing.T) {
	c := Config{}
	ApplyDefaults(&c)
	if c.Runner.Verify != false {
		t.Errorf("verify after ApplyDefaults = %v, want false (ApplyDefaults 不能设置 bool 默认值)", c.Runner.Verify)
	}
}

// TestNewDefault 验证 NewDefault 返回的 Config 中 Verify=true（由 NewDefault
// 在调用 ApplyDefaults 之后显式设置），同时其他默认值也被正确填充。
func TestNewDefault(t *testing.T) {
	c := NewDefault()

	if c.Endpoint.Region != "us-east-1" {
		t.Errorf("region default = %q, want us-east-1", c.Endpoint.Region)
	}
	if len(c.Methods) != 1 || c.Methods[0] != "put" {
		t.Errorf("methods default = %v, want [put]", c.Methods)
	}
	if c.MethodOptions.Multipart.PartSize != 5242880 {
		t.Errorf("part_size default = %d, want 5242880", c.MethodOptions.Multipart.PartSize)
	}
	if c.MethodOptions.Post.SuccessActionStatus != 201 {
		t.Errorf("success_action_status default = %d, want 201", c.MethodOptions.Post.SuccessActionStatus)
	}
	if c.MethodOptions.Presigned.ExpiresIn != 3600*time.Second {
		t.Errorf("expires_in default = %v, want 3600s", c.MethodOptions.Presigned.ExpiresIn)
	}
	if c.MethodOptions.Presigned.Method != "put" {
		t.Errorf("presigned method default = %q, want put", c.MethodOptions.Presigned.Method)
	}
	if c.Content.Type != "random" {
		t.Errorf("content.type default = %q, want random", c.Content.Type)
	}
	if c.Runner.Concurrency != 8 {
		t.Errorf("concurrency default = %d, want 8", c.Runner.Concurrency)
	}
	if c.Runner.Verify != true {
		t.Errorf("verify default = %v, want true (NewDefault 应将 Verify 显式设为 true)", c.Runner.Verify)
	}
	if c.Runner.Cleanup {
		t.Errorf("cleanup default = true, want false")
	}
	if c.Runner.Timeout != 300*time.Second {
		t.Errorf("timeout default = %v, want 300s", c.Runner.Timeout)
	}
	if c.Runner.OutPrefix != "out" {
		t.Errorf("out_prefix default = %q, want out", c.Runner.OutPrefix)
	}
}
