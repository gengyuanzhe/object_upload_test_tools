package config

import "time"

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
