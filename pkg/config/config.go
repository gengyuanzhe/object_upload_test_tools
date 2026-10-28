package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 表示整个工具的顶层配置结构。
// 各字段通过 YAML tag 与配置文件一一对应。
type Config struct {
	Endpoint      EndpointConfig `yaml:"endpoint"`
	Bucket        string         `yaml:"bucket"`
	Methods       []string       `yaml:"methods"`
	MethodOptions MethodOptions  `yaml:"method_options"`
	Sizes         []SizeBucket   `yaml:"sizes"`
	Content       ContentConfig  `yaml:"content"`
	Runner        RunnerConfig   `yaml:"runner"`
}

// EndpointConfig 描述 S3 兼容服务的连接信息。
type EndpointConfig struct {
	Name         string `yaml:"name"`
	URL          string `yaml:"url"`
	Region       string `yaml:"region"`
	AccessKey    string `yaml:"access_key"`
	SecretKey    string `yaml:"secret_key"`
	UsePathStyle bool   `yaml:"use_path_style"`
}

// MethodOptions 聚合各上传方法相关的子选项。
type MethodOptions struct {
	Multipart MultipartOptions `yaml:"multipart"`
	Post      PostOptions      `yaml:"post"`
	Presigned PresignedOptions `yaml:"presigned"`
}

// MultipartOptions 控制 multipart 上传行为。
type MultipartOptions struct {
	PartSize int64 `yaml:"part_size"`
}

// PostOptions 控制 presigned POST 上传行为。
type PostOptions struct {
	SuccessActionStatus int               `yaml:"success_action_status"`
	ExtraFields         map[string]string `yaml:"extra_fields"`
}

// PresignedOptions 控制 presigned URL 上传行为。
type PresignedOptions struct {
	ExpiresIn time.Duration `yaml:"expires_in"`
	Method    string        `yaml:"method"`
}

// SizeBucket 定义单个对象大小区间及其采样权重。
type SizeBucket struct {
	Min    int64
	Max    int64
	Weight int
}

// ContentConfig 描述测试内容的生成方式。
type ContentConfig struct {
	Type string `yaml:"type"`
	Seed int64  `yaml:"seed"`
	Char int    `yaml:"char"`
	Path string `yaml:"path"`
}

// RunnerConfig 描述运行器并发、校验、清理等运行期参数。
type RunnerConfig struct {
	Total       int           `yaml:"total"`
	Concurrency int           `yaml:"concurrency"`
	KeyPrefix   string        `yaml:"key_prefix"`
	Verify      bool          `yaml:"verify"`
	Cleanup     bool          `yaml:"cleanup"`
	Timeout     time.Duration `yaml:"timeout"`
	OutPrefix   string        `yaml:"out_prefix"`
}

// ApplyDefaults 将零值字段填充为默认值。
// 仅在字段为零值时写入默认值，已显式设置的值不会被覆盖。
func ApplyDefaults(c *Config) {
	if c.Endpoint.Region == "" {
		c.Endpoint.Region = "us-east-1"
	}
	if len(c.Methods) == 0 {
		c.Methods = []string{"put"}
	}
	if c.MethodOptions.Multipart.PartSize == 0 {
		c.MethodOptions.Multipart.PartSize = 5242880
	}
	if c.MethodOptions.Post.SuccessActionStatus == 0 {
		c.MethodOptions.Post.SuccessActionStatus = 201
	}
	if c.MethodOptions.Presigned.ExpiresIn == 0 {
		c.MethodOptions.Presigned.ExpiresIn = 3600 * time.Second
	}
	if c.MethodOptions.Presigned.Method == "" {
		c.MethodOptions.Presigned.Method = "put"
	}
	if c.Content.Type == "" {
		c.Content.Type = "random"
	}
	if c.Runner.Concurrency == 0 {
		c.Runner.Concurrency = 8
	}
	// bool 类型零值无法与「用户显式设置为 false」区分开来：
	// false 既是零值也是合法的显式取值，因此本函数不能在此层通过零值检测
	// 决定是否把 Verify 默认为 true。
	// 解决方式：本函数保持 Verify 不变；由 NewDefault() 显式将 Verify 设为 true；
	// Task 4 的 Load() 通过先解码到 raw map 再判断 runner.verify 是否显式存在
	// 来实现 round-trip 还原，仅在配置文件未显式给出 verify 时才回填 true。
	// 此处保留空分支以记录这一已知限制，便于后续维护者理解为何不在本函数处理。
	if c.Runner.Verify == false && c.Runner.Total == 0 {
		// 仅当 Runner 完全未设置时才考虑默认 true，但 bool 零值即 false 的语义
		// 使本层无法可靠区分「未设置」与「显式 false」。故此处不修改 Verify。
		// 默认值由 NewDefault() 与 Load() 协同处理（详见上文注释）。
	}
	if c.Runner.Timeout == 0 {
		c.Runner.Timeout = 300 * time.Second
	}
	if c.Runner.OutPrefix == "" {
		c.Runner.OutPrefix = "out"
	}
}

// NewDefault 返回一个填充完整默认值的 Config。
// 由于 ApplyDefaults 无法为 bool 字段设置默认 true，此函数在调用
// ApplyDefaults 之后再显式将 Runner.Verify 设为 true。
func NewDefault() Config {
	c := Config{}
	ApplyDefaults(&c)
	c.Runner.Verify = true
	return c
}

// validMethods 记录允许的上传方法名称集合。
var validMethods = map[string]bool{"put": true, "post": true, "multipart": true, "presigned": true}

// validContentTypes 记录允许的 content.type 取值集合。
var validContentTypes = map[string]bool{"random": true, "fixed": true, "file": true}

// Validate 按规则逐项校验 Config，任一规则不满足即返回错误。
// 校验顺序与字段声明顺序基本一致，先校验基础必填项，再校验取值范围与互斥条件。
func Validate(c *Config) error {
	if _, err := url.Parse(c.Endpoint.URL); err != nil || c.Endpoint.URL == "" {
		return fmt.Errorf("endpoint.url invalid: %q", c.Endpoint.URL)
	}
	if c.Endpoint.AccessKey == "" || c.Endpoint.SecretKey == "" {
		return errors.New("endpoint.access_key and endpoint.secret_key required")
	}
	if c.Bucket == "" {
		return errors.New("bucket required")
	}
	if len(c.Methods) == 0 {
		return errors.New("methods required (at least one of put/post/multipart/presigned)")
	}
	for _, m := range c.Methods {
		if !validMethods[m] {
			return fmt.Errorf("invalid method %q", m)
		}
	}
	if c.MethodOptions.Multipart.PartSize < 5242880 {
		return fmt.Errorf("method_options.multipart.part_size must be >= 5MiB, got %d", c.MethodOptions.Multipart.PartSize)
	}
	if len(c.Sizes) == 0 {
		return errors.New("sizes required (at least one bucket)")
	}
	sum := 0
	for i, b := range c.Sizes {
		if b.Min < 0 {
			return fmt.Errorf("sizes[%d].min must be >= 0", i)
		}
		if b.Min > b.Max {
			return fmt.Errorf("sizes[%d].min (%d) > max (%d)", i, b.Min, b.Max)
		}
		sum += b.Weight
	}
	if sum < 99 || sum > 101 {
		return fmt.Errorf("sizes weights must sum to 100 (±1), got %d", sum)
	}
	if !validContentTypes[c.Content.Type] {
		return fmt.Errorf("content.type must be random|fixed|file, got %q", c.Content.Type)
	}
	if c.Content.Type == "fixed" {
		if c.Content.Char < 0 || c.Content.Char > 255 {
			return fmt.Errorf("content.char must be 0..255, got %d", c.Content.Char)
		}
	}
	if c.Content.Type == "file" {
		if c.Content.Path == "" {
			return errors.New("content.path required when content.type=file")
		}
		if _, err := os.Stat(c.Content.Path); err != nil {
			return fmt.Errorf("content.path not accessible: %w", err)
		}
	}
	if c.Runner.Total <= 0 {
		return fmt.Errorf("runner.total must be > 0, got %d", c.Runner.Total)
	}
	if c.Runner.Concurrency <= 0 {
		return fmt.Errorf("runner.concurrency must be > 0, got %d", c.Runner.Concurrency)
	}
	if c.Runner.Timeout <= 0 {
		return fmt.Errorf("runner.timeout must be > 0, got %v", c.Runner.Timeout)
	}
	if strings.TrimSpace(c.Runner.OutPrefix) == "" {
		return errors.New("runner.out_prefix required")
	}
	return nil
}

// UnmarshalYAML 支持 SizeBucket 的两种 YAML 形态：
// 数组形态 [min, max, weight]（3 元素序列）与结构体形态。
// 当节点是含 3 个元素的 sequence 时按数组解析，否则按结构体解析。
func (s *SizeBucket) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.SequenceNode && len(value.Content) == 3 {
		var arr [3]int64
		if err := value.Decode(&arr); err != nil {
			return err
		}
		s.Min = arr[0]
		s.Max = arr[1]
		s.Weight = int(arr[2])
		return nil
	}
	type alias SizeBucket
	var a alias
	if err := value.Decode(&a); err != nil {
		return err
	}
	*s = SizeBucket(a)
	return nil
}

// Load 从给定路径读取 YAML 配置文件，解析后应用默认值并校验。
// 为解决 bool 默认值问题（ApplyDefaults 无法区分「未设置」与「显式 false」），
// 本函数先解码到 raw map 检测 runner.verify 是否显式存在：
// 若未显式给出 verify，则强制将 Verify 设为 true。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// 先解码到 raw map 以检测显式 bool 取值。
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	ApplyDefaults(c)
	// 若 YAML 未显式给出 verify，则还原 Verify 默认值 true。
	if rawRunner, ok := raw["runner"].(map[string]any); ok {
		if _, set := rawRunner["verify"]; !set {
			c.Runner.Verify = true
		}
	} else {
		c.Runner.Verify = true
	}
	if err := Validate(c); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return c, nil
}
