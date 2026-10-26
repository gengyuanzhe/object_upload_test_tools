# Object Upload Test Tools - Design Spec

**Date**: 2026-10-26
**Status**: Design (pending implementation plan)
**Owner**: TBD

## 1. Purpose

Build a tool suite for testing S3-compatible object upload. The suite covers four AWS-style upload methods (PutObject, POST form upload, Multipart Upload, Presigned URL upload), runs them concurrently with controllable object size and content, and emits structured reports. The upload library is isolated from the calling code so the framework can be extended later (e.g., a compatibility test suite).

## 2. Scope

### In scope

- Single Go module `object_upload_test_tools`
- Four upload methods against any S3-compatible endpoint
- Per-endpoint YAML configuration (`configs/<name>.yaml`)
- Multi-object concurrent upload via worker pool
- Three content generators: random bytes, fixed-byte fill, local file
- Object size controllable per task; weighted size distribution across tasks
- HEAD-based verification (MD5 vs ETag)
- Structured reports (JSON + CSV + console summary) in a per-run output directory
- Real-time progress on stderr
- Dry-run mode
- Graceful Ctrl+C shutdown

### Out of scope (this revision)

- MinIO SDK upload implementations (architecture reserves room for them)
- Compatibility test suite `pkg/compat` (placeholder only)
- Single-object multipart concurrency (parts are uploaded serially)
- Retry logic (failures are recorded, not retried)
- HTTPS certificate skip (production-safe default; not exposed)
- Interactive shell / sub-commands / cross-endpoint concurrent comparison

## 3. Architecture

### 3.1 Repository layout

```
object_upload_test_tools/
├── cmd/upload-tool/           # CLI entry: arg parse and assembly only
│   └── main.go
├── pkg/
│   ├── config/                # Config struct + Load + Validate
│   │   └── config.go
│   ├── content/               # Object content generation
│   │   ├── generator.go       # Generator interface + selector
│   │   ├── random.go          # RandomGenerator (math/rand/v2, streaming)
│   │   ├── fixed.go           # FixedCharGenerator
│   │   └── file.go            # FileGenerator (local file, size ignored)
│   ├── uploader/              # Upload library (AWS SDK V2 implementations)
│   │   ├── uploader.go        # Interfaces: Uploader + UploadSpec/UploadResult
│   │   ├── aws_putter.go      # AwsPutter
│   │   ├── aws_poster.go      # AwsPoster (presigned POST policy)
│   │   ├── aws_multipart.go   # AwsMultipartUploader (serial parts)
│   │   └── aws_presigned.go   # AwsPresignedUploader
│   ├── runner/                # Concurrent execution + verify + report + progress
│   │   ├── runner.go          # Runner: worker pool scheduling
│   │   ├── task.go            # Task / Result / ResultStatus
│   │   ├── verifier.go        # HEAD + MD5 / ETag comparison
│   │   ├── reporter.go        # JSON + CSV report writers
│   │   ├── progress.go        # TTY progress on stderr
│   │   ├── sizes.go           # Size distribution sampler
│   │   └── errors.go          # Sentinel errors
│   └── compat/                # Reserved for compatibility suite (placeholder)
│       └── README.md
├── configs/                   # One YAML per endpoint
│   ├── aws.yaml
│   └── minio.yaml
├── docs/superpowers/specs/    # This spec and future specs
├── go.mod
└── README.md
```

### 3.2 Dependency direction

```
cmd/upload-tool → runner → uploader
                        → content
                        → config
```

- `uploader` and `content` do not depend on each other; `runner` assembles them.
- `cmd/upload-tool` is a thin shell: parse args, load config, build runner, run.
- `pkg/compat` (future) imports `uploader` + `content` directly, bypassing `runner`.

### 3.3 AWS SDK boundary

`github.com/aws/aws-sdk-go-v2` is imported only inside `pkg/uploader` AWS implementation files. The `Uploader` interface and `UploadSpec`/`UploadResult` types live in `uploader.go` and reference only standard library types, so future MinIO implementations can be added in the same package without leaking SDK types into the interface.

## 4. Component Design

### 4.1 Config (`pkg/config`)

```go
type Config struct {
    Endpoint      EndpointConfig `yaml:"endpoint"`
    Bucket        string          `yaml:"bucket"`
    Methods       []string        `yaml:"methods"`
    MethodOptions MethodOptions   `yaml:"method_options"`
    Sizes          []SizeBucket   `yaml:"sizes"`
    Content        ContentConfig  `yaml:"content"`
    Runner         RunnerConfig    `yaml:"runner"`
}

type EndpointConfig struct {
    Name          string `yaml:"name"`
    URL           string `yaml:"url"`
    Region        string `yaml:"region"`
    AccessKey     string `yaml:"access_key"`
    SecretKey     string `yaml:"secret_key"`
    UsePathStyle  bool   `yaml:"use_path_style"`
}

type MethodOptions struct {
    Multipart MultipartOptions `yaml:"multipart"`
    Post      PostOptions       `yaml:"post"`
    Presigned PresignedOptions  `yaml:"presigned"`
}

type MultipartOptions struct {
    PartSize int64 `yaml:"part_size"`
}

type PostOptions struct {
    SuccessActionStatus int               `yaml:"success_action_status"`
    ExtraFields         map[string]string `yaml:"extra_fields"`
}

type PresignedOptions struct {
    ExpiresIn time.Duration `yaml:"expires_in"`
    Method    string         `yaml:"method"` // "put" or "post"
}

type SizeBucket struct {
    Min    int64
    Max    int64
    Weight int
}

type ContentConfig struct {
    Type string `yaml:"type"`  // "random" | "fixed" | "file"
    Seed int64 `yaml:"seed"`   // 0 = use start timestamp
    Char int   `yaml:"char"`   // 0..255, fixed type only
    Path string `yaml:"path"`  // file type only
}

type RunnerConfig struct {
    Total       int            `yaml:"total"`
    Concurrency int            `yaml:"concurrency"`
    KeyPrefix   string         `yaml:"key_prefix"`
    Verify      bool           `yaml:"verify"`
    Cleanup     bool           `yaml:"cleanup"`
    Timeout     time.Duration  `yaml:"timeout"`
    OutPrefix   string         `yaml:"out_prefix"`  // default "out"
}
```

#### Defaults

| Field | Default |
|---|---|
| `endpoint.region` | `us-east-1` |
| `endpoint.use_path_style` | `false` |
| `methods` | `["put"]` |
| `method_options.multipart.part_size` | `5242880` (5 MiB) |
| `method_options.post.success_action_status` | `201` |
| `method_options.presigned.expires_in` | `3600s` |
| `method_options.presigned.method` | `put` |
| `content.type` | `random` |
| `content.seed` | `0` (runtime: start timestamp) |
| `runner.concurrency` | `8` |
| `runner.key_prefix` | `""` |
| `runner.verify` | `true` |
| `runner.cleanup` | `false` |
| `runner.timeout` | `300s` |
| `runner.out_prefix` | `out` |

#### Validation rules

1. `endpoint.url` parses as URL
2. `endpoint.access_key` and `endpoint.secret_key` non-empty
3. `bucket` non-empty
4. `methods` has at least one entry; each in `{put, post, multipart, presigned}`
5. `method_options.multipart.part_size >= 5 MiB` (S3 minimum)
6. `sizes` has at least one bucket; sum of weights == 100 (±1 tolerance)
7. Each `sizes[i].min <= sizes[i].max`; `min >= 0`
8. `content.type` in `{random, fixed, file}`
9. `fixed` type: `char` in `[0, 255]`
10. `file` type: `path` exists on disk
11. `runner.total > 0`
12. `runner.concurrency > 0`
13. `runner.timeout > 0`
14. `runner.out_prefix` non-empty

#### Sample config

```yaml
endpoint:
  name: minio-local
  url: http://localhost:9000
  region: us-east-1
  access_key: minioadmin
  secret_key: minioadmin
  use_path_style: true

bucket: test-bucket

methods: [put, post, multipart, presigned]

method_options:
  multipart:
    part_size: 5242880
  post:
    success_action_status: 201
    extra_fields: {}
  presigned:
    expires_in: 3600s
    method: put

sizes:
  - [1, 1024, 50]
  - [1024, 1048576, 30]
  - [1048576, 10485760, 20]

content:
  type: random
  seed: 0

runner:
  total: 1000
  concurrency: 32
  key_prefix: "test/"
  verify: true
  cleanup: false
  timeout: 300s
  out_prefix: out
```

### 4.2 Content (`pkg/content`)

```go
type Generator interface {
    Type() string
    Generate(size int64, taskID int) (io.Reader, error)
}
```

- `taskID` is the per-run sequential index `0..N-1`. Random generator derives its seed as `baseSeed + int64(taskID)` so each task yields distinct bytes under the same seed, and the same task yields identical bytes across two runs that share a fixed seed.

#### Implementations

**`RandomGenerator`** — streaming, memory-bounded.

```go
type RandomGenerator struct {
    BaseSeed int64
}

func (g *RandomGenerator) Type() string { return "random" }
func (g *RandomGenerator) Generate(size int64, taskID int) (io.Reader, error) {
    seed := g.BaseSeed + int64(taskID)
    prng := rand.New(rand.NewSource(seed))
    return &randReader{prng: prng, remaining: size}, nil
}

type randReader struct {
    prng      *rand.Rand
    remaining int64
}

func (r *randReader) Read(p []byte) (int, error) {
    if r.remaining <= 0 {
        return 0, io.EOF
    }
    n := int64(len(p))
    if n > r.remaining {
        n = r.remaining
    }
    r.prng.Read(p[:n])
    r.remaining -= n
    return int(n), nil
}
```

**`FixedCharGenerator`** — fills buffer with a single byte.

```go
type FixedCharGenerator struct {
    Char byte
}
func (g *FixedCharGenerator) Generate(size int64, taskID int) (io.Reader, error) {
    buf := make([]byte, size)
    for i := range buf { buf[i] = g.Char }
    return bytes.NewReader(buf), nil
}
```

**`FileGenerator`** — opens the configured path each call. `size` parameter is ignored; the actual file size is used. Caller (`runner`) is responsible for `os.Stat` to fill `UploadSpec.Size`.

```go
type FileGenerator struct {
    Path string
}
func (g *FileGenerator) Generate(size int64, taskID int) (io.Reader, error) {
    return os.Open(g.Path)
}
```

#### Content config → generator mapping

| `content.type` | Generator | Notes |
|---|---|---|
| `random` | `RandomGenerator{BaseSeed: cfg.Seed}` | If `Seed == 0`, runner replaces it with `time.Now().UnixNano()` at start |
| `fixed` | `FixedCharGenerator{Char: byte(cfg.Char)}` | |
| `file` | `FileGenerator{Path: cfg.Path}` | |

### 4.3 Uploader (`pkg/uploader`)

```go
type Uploader interface {
    Method() string
    Upload(ctx context.Context, spec UploadSpec) (UploadResult, error)
}

type UploadSpec struct {
    Bucket    string
    Key       string
    Content   io.Reader
    Size      int64
    Metadata  map[string]string

    Multipart *MultipartSpec
    Presigned *PresignedSpec
    Post      *PostSpec
}

type MultipartSpec struct {
    PartSize int64
}

type PresignedSpec struct {
    ExpiresIn time.Duration
    Method    string // "put" or "post"
}

type PostSpec struct {
    SuccessActionStatus int
    ExtraFields         map[string]string
}

type UploadResult struct {
    Location  string
    ETag      string
    VersionID string
    Elapsed   time.Duration
    BytesIn   int64
    Details   map[string]any
}
```

#### Implementations (all AWS SDK V2)

| Type | Method() | Implementation notes |
|---|---|---|
| `AwsPutter` | `put` | `s3.PutObject` |
| `AwsPoster` | `post` | Presigned POST policy via `s3.PresignClient`, then `net/http` multipart form POST (AWS SDK V2 has no direct PostObject API) |
| `AwsMultipartUploader` | `multipart` | `CreateMultipartUpload` → `UploadPart` (serial loop) → `CompleteMultipartUpload`. No concurrency within a single object. |
| `AwsPresignedUploader` | `presigned` | `PresignPutObject` (or presigned POST policy when `PresignedSpec.Method == "post"`) → `net/http` PUT/POST |

#### Uploader factory

Runner holds a `map[string]func(*s3.Client) Uploader` keyed by method name. `runner.NewAwsUploaderFactory()` returns this map for the AWS implementations. Future MinIO implementations add another factory.

### 4.4 Runner (`pkg/runner`)

#### Task & Result

```go
type Task struct {
    ID        int
    Method    string
    Bucket    string
    Key       string              // key_prefix + YYYYMMDD-HHMMSS + "-" + zero-padded(id)
    Size      int64
    Generator content.Generator
    Spec      uploader.UploadSpec
}

type ResultStatus string
const (
    StatusSuccess       ResultStatus = "success"
    StatusFailedUpload  ResultStatus = "failed-upload"
    StatusFailedVerify  ResultStatus = "failed-verify"
)

type Result struct {
    TaskID      int
    Method      string
    Key         string
    Size        int64
    Status      ResultStatus
    ErrMessage  string
    Elapsed     time.Duration
    UploadResult uploader.UploadResult
    LocalMD5     string
    RemoteETag   string
    HashMatched  bool
}
```

#### Runner

```go
type Config struct {
    EndpointName   string
    EndpointURL    string
    Bucket         string
    Methods        []string
    Sizes          []SizeBucket
    ContentCfg     content.Config
    Total          int
    Concurrency    int
    KeyPrefix      string
    Verify         bool
    Cleanup        bool
    Timeout        time.Duration
    OutPrefix      string
    S3Client       *s3.Client
    UploaderFactory map[string]func(*s3.Client) uploader.Uploader
}

type Runner struct { cfg Config; logger *slog.Logger }

func (r *Runner) Run(ctx context.Context) (*Report, error)
```

#### Worker pool model

**Task assignment**: when `methods` contains multiple entries, tasks are assigned methods round-robin by task ID (`task.Method = methods[taskID % len(methods)]`). When `methods` is a single entry, every task uses that method.

**Size assignment**: each task's size is sampled independently from the weighted size distribution (see `pkg/runner/sizes.go`): pick a bucket by weight (cumulative weights, then binary search or linear scan), then sample uniformly in `[min, max]`. When `min == max`, the size is fixed.

```go
func (r *Runner) Run(ctx context.Context) (*Report, error) {
    // 1. Sample size distribution for r.cfg.Total tasks (weighted random).
    // 2. Start progress ticker on stderr.
    // 3. Spawn r.cfg.Concurrency workers reading from taskCh.
    // 4. Producer goroutine writes r.cfg.Total tasks to taskCh.
    // 5. Workers write Results to resultCh.
    // 6. Collector gathers Results, updates progress counters.
    // 7. On ctx.Done(): producer stops enqueuing; workers finish in-flight task; collector closes report.
    // 8. Stop progress; assemble Report; write report files; return Report.
}

func (r *Runner) worker(ctx context.Context, id int, taskCh <-chan Task, resultCh chan<- Result)

func (r *Runner) executeTask(ctx context.Context, task Task) Result {
    // 1. reader, _ := task.Generator.Generate(task.Size, task.ID)
    // 2. hash := md5.New(); teed := io.TeeReader(reader, hash)
    // 3. spec.Content = teed
    // 4. uploader := r.cfg.UploaderFactory[task.Method](r.cfg.S3Client)
    // 5. uploadResult, err := uploader.Upload(ctx, spec)
    //    if err != nil: return Result{Status: StatusFailedUpload, ErrMessage: awsErrCode(err)}
    // 6. if r.cfg.Verify:
    //      etag, matched, _ := verifier.Verify(ctx, bucket, key, hash.Sum(nil))
    //      if !matched: return Result{Status: StatusFailedVerify, ...}
    // 7. if r.cfg.Cleanup: _ = deleter.Delete(ctx, bucket, key)  // log warning on failure
    // 8. return Result{Status: StatusSuccess, ...}
}
```

#### Verifier (`pkg/runner/verifier.go`)

```go
type Verifier struct{ client *s3.Client }

// Verify computes nothing; it relies on the local MD5 the runner already
// accumulated via TeeReader. It does HEAD to fetch the remote ETag and
// compares. For multipart, the verifier reconstructs MD5-of-MD5s using
// the configured part_size to match S3's ETag format ("md5hash-N").
func (v *Verifier) Verify(ctx context.Context, bucket, key string, localMD5 hex string, partSize int64, size int64) (remoteETag string, matched bool, err error)
```

Verification algorithm (V1):

- For `put`, `post`, `presigned` (non-multipart): local MD5 == remote ETag.
- For `multipart`: if size <= partSize, ETag is plain MD5. Otherwise, ETag == `md5hex(concat(md5(part_i)...)) + "-" + strconv.Itoa(partCount)`.

The runner's TeeReader accumulates the whole-stream MD5 during upload, which covers the non-multipart case directly. For multipart, the runner additionally tracks per-part MD5 by re-deriving parts from the same Generator (`RandomGenerator` is deterministic under `baseSeed + taskID`; `FixedCharGenerator` reproduces trivially; `FileGenerator` re-opens the file). The verifier receives both the per-part MD5 list and the part count, recomputes the S3-style multipart ETag, and compares to the remote ETag.

This re-derivation is local-only (no network retry). It is bounded: `random` is O(partSize*partCount) CPU; `fixed` is O(1) per part (constant byte); `file` re-reads the file from disk. None of these require retry semantics at the network layer.

**API refinement**: `executeTask` builds a `multipartState{partMD5s []string, partCount int}` alongside the upload and passes it to the verifier. The verifier signature becomes:

```go
func (v *Verifier) Verify(ctx context.Context, bucket, key string, localMD5 string, ms *multipartState) (remoteETag string, matched bool, err error)
```

`ms` is `nil` for non-multipart tasks.

#### Reporter (`pkg/runner/reporter.go`)

```go
type Report struct {
    Meta      ReportMeta
    Summary   ReportSummary
    Results   []Result  // populated only when --report-results=true
}

type ReportMeta struct {
    StartedAt      time.Time
    EndedAt        time.Time
    DurationSeconds int64
    EndpointName   string
    EndpointURL    string
    Bucket         string
    Concurrency    int
    Methods        []string
    Content        content.Config
    BaseSeed       int64  // resolved seed (timestamp or fixed)
}

type ReportSummary struct {
    Total         int
    Success       int
    Failed        int
    TotalBytes    int64
    ThroughputMBps float64
    ByMethod      []MethodStat
    BySizeBucket  []SizeBucketStat
    ByError       []ErrorStat
}

type MethodStat struct {
    Method string
    Count, Success, Failed int
    Bytes int64
    AvgLatencyMS float64
}

type SizeBucketStat struct {
    Range string  // "1B-1KB"
    Count, Success int
    AvgLatencyMS float64
}

type ErrorStat struct {
    Error string
    Count int
}
```

#### Output directory

```
<out_prefix>-<YYYYMMDD-HHMMSS>/
├── summary.json       # Report without Results (always written)
├── summary.csv        # One row per method / size bucket / error
├── results.csv        # One row per object, full fields (always written)
└── results.json       # Full Report including Results (only when --report-results=true)
```

#### Console summary (printed to stderr after run)

```
=== Run Summary ===
Started:  2026-10-26 10:48:12
Ended:    2026-10-26 10:49:47
Duration: 1m 35s
Endpoint: minio-local (http://localhost:9000)
Bucket:   test-bucket

Total objects: 1000
  Success: 952 (95.2%)
  Failed:  48 (4.8%)
Total bytes: 4.2 GB
Throughput:   28.5 MB/s

By Method:
  Method     | Total | Success | Failed | Bytes    | Avg Latency
  -----------|-------|---------|--------|----------|------------
  put        | 250   | 250     | 0      | 250 MB   | 120ms
  post       | 250   | 248     | 2      | 1.0 GB   | 380ms
  multipart  | 250   | 228     | 22     | 2.5 GB   | 1.2s
  presigned  | 250   | 226     | 24     | 480 MB   | 95ms

By Size Bucket: ...
Top 5 Errors: ...

Output dir: out-20261026-104812/
```

#### Progress (`pkg/runner/progress.go`)

Real-time line on stderr, refreshed every 200 ms:

```
[████████░░░░] 832/1000 (83.2%) | ✓ 800 ✗ 32 | 12.4 MB/s | ETA 4s
```

Detects non-TTY (output piped/redirected) and degrades to one line per second as plain text.

### 4.5 CLI (`cmd/upload-tool`)

```bash
upload-tool \
  -c configs/minio.yaml \       # required: config file path
  -n 1000 \                     # override runner.total
  --concurrency 64 \            # override runner.concurrency
  --methods put,post \          # override methods
  --report-results \            # enable results.json (default off)
  --log-level info \            # debug|info|warn|error
  --dry-run                     # validate config + sample tasks, no upload
```

#### Flag precedence

CLI flags override YAML. Implementation walks `flag.Visit` and patches the loaded `Config` for each set flag, then runs `Validate`.

#### Dry-run output

```
=== Dry Run ===
Config OK

Final Config:
  endpoint: minio-local (http://localhost:9000)
  bucket: test-bucket
  methods: [put, post, multipart, presigned]
  sizes: [[1,1024,50],[1024,1048576,30],[1048576,10485760,20]]
  total: 1000
  concurrency: 32
  verify: true
  cleanup: false

Size distribution (predicted):
  1B-1KB:       ~500 objects
  1KB-1MB:      ~300 objects
  1MB-10MB:     ~200 objects
  Total bytes:  ~2.1 GB

First 10 tasks (sample):
  #1 put        test/20261026-104812-0001  512B
  #2 post       test/20261026-104812-0002  256B
  #3 multipart  test/20261026-104812-0003  8.2MB (2 parts)
  ...
```

## 5. Error Handling

### Sentinels

```go
var (
    ErrConfigInvalid   = errors.New("config invalid")
    ErrContentGenerate = errors.New("content generate failed")
    ErrUploadFailed    = errors.New("upload failed")
    ErrVerifyFailed    = errors.New("verify failed")
    ErrContextCanceled = errors.New("context canceled")
)
```

### Rules

| Failure location | Behavior |
|---|---|
| Config load / validate | Exit with non-zero code, print error, do nothing else |
| Content `Generate` failure | Task → `failed-upload`, record error, continue |
| Upload failure | Task → `failed-upload`, record AWS error code + message, continue |
| HEAD / verify mismatch | Task → `failed-verify`, record mismatch detail, continue |
| Cleanup (DELETE) failure | Log warning, no effect on Result |
| Ctrl+C / SIGTERM | Stop enqueuing new tasks; wait for in-flight workers to finish (bounded by `runner.timeout`); write partial report; exit |

No retry at any layer.

## 6. Dependencies

```
github.com/aws/aws-sdk-go-v2
github.com/aws/aws-sdk-go-v2/config
github.com/aws/aws-sdk-go-v2/credentials
github.com/aws/aws-sdk-go-v2/service/s3
gopkg.in/yaml.v3
```

Standard library only elsewhere (`log/slog`, `crypto/md5`, `encoding/hex`, `math/rand/v2`, `net/http`, `os`, `os/signal`, `context`, `flag`, `time`, `sync/atomic`, `encoding/json`, `encoding/csv`).

## 7. Testing Strategy

### Unit tests (per package, no real network)

| Package | Coverage |
|---|---|
| `pkg/config` | YAML parse, defaults, validation rules, flag-override merging |
| `pkg/content` | Three generators; determinism (same seed → same bytes via SHA256); file-type ignores size and stats path |
| `pkg/uploader` | Interface contract tests using `httptest.Server` as minimal S3 mock; per-implementation Method() and UploadSpec field handling |
| `pkg/runner` | Worker pool scheduling; size distribution accuracy (10k samples within ±5% of weights); error classification; report aggregation; Ctrl+C handling |
| `cmd/upload-tool` | Flag parsing; flag-over-config precedence; dry-run output format |

### Integration tests (`//go:build integration`)

`pkg/uploader/integration_test.go` spins up `httptest.Server` with a minimal S3-compatible handler and exercises all four upload methods end-to-end. Skipped under default `go test`; run with `go test -tags integration ./...`.

### Race detector

All concurrency tests run under `go test -race`. Worker pool must be race-clean.

### Key test cases

1. **Determinism**: same `BaseSeed` → same SHA256 per task ID across runs.
2. **Size distribution**: 10000 samples, each bucket within ±5% of configured weight.
3. **Error classification**: mocked uploader returns known AWS error codes; Result.Status reflects expected category.
4. **Multipart ETag verification**: known content + known part size → expected S3-style ETag string.
5. **Graceful shutdown**: SIGINT mid-run → runner exits within `runner.timeout`; partial report written.
6. **Config validation**: each invalid-config case returns a clear error.

## 8. Future Extensions (out of scope, reserved)

- `pkg/compat`: compatibility test suite built on the `Uploader` interface, asserting behavioral conformance across endpoints.
- MinIO SDK implementations: `MinioPutter`, `MinioPoster`, etc., added to `pkg/uploader` without interface changes.
- Single-object multipart concurrency (re-introduce `MultipartSpec.Concurrency` default 1).
- Retry with exponential backoff.
- Cross-endpoint concurrent comparison mode.

## 9. Open Questions

None at design time. Implementation plan will surface concrete API specifics (e.g., exact SDK call signatures) and may file small clarifications back here.
