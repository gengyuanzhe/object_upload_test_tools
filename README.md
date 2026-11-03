# object_upload_test_tools

面向 S3 兼容对象存储的高并发上传测试工具。支持对任意 S3 兼容 endpoint
验证四种上传方法（PutObject、POST form、Multipart、Presigned），通过
HEAD + ETag 校验对象一致性，并在每次运行后输出 JSON/CSV/console 报告。

## 安装

需要本地已安装 Go 1.27+（`go version` 验证）。源码构建：

```bash
# 当前主机架构（默认）
go build -o upload-tool ./cmd/upload-tool

# Linux x86-64
GOOS=linux GOARCH=amd64 go build -o upload-tool-linux-amd64 ./cmd/upload-tool

# Linux ARM64
GOOS=linux GOARCH=arm64 go build -o upload-tool-linux-arm64 ./cmd/upload-tool
```

构建产物为单文件静态二进制（CGO 默认关闭），可直接拷贝到目标机器运行，
无需 Go 运行时或额外动态库。验证安装：

```bash
./upload-tool -c configs/minio.yaml --dry-run
```

## 配置

编辑 `configs/<endpoint>.yaml`，仓库内置两个示例：

- `configs/aws.yaml` — AWS S3（`https://s3.us-east-1.amazonaws.com`），需替换 `access_key` / `secret_key`。
- `configs/minio.yaml` — 本地 MinIO（`http://localhost:9000`），默认凭据为 `minioadmin`。

配置字段说明：

| Section | 字段 | 说明 |
|---|---|---|
| `endpoint` | `name` / `url` / `region` / `access_key` / `secret_key` / `use_path_style` | S3 兼容 endpoint 的连接信息与寻址方式 |
| `bucket` | — | 目标 bucket 名 |
| `methods` | — | 启用的上传方法列表，可选 `put` / `post` / `multipart` / `presigned` |
| `method_options` | `multipart.part_size` / `post.success_action_status` / `post.extra_fields` / `presigned.expires_in` / `presigned.method` | 各上传方法的可选参数 |
| `sizes` | `[min, max, weight]` 三元组列表 | 对象大小加权采样 bucket |
| `content` | `type`（`random` / `fixed` / `file`）/ `seed` / `path` / `char` | 内容生成器配置 |
| `runner` | `total` / `concurrency` / `key_prefix` / `verify` / `cleanup` / `timeout` / `out_prefix` / `log_level` | 运行参数 |

## 运行

```bash
./upload-tool -c configs/minio.yaml
./upload-tool -c configs/aws.yaml -n 5000 --concurrency 64 --methods put,multipart
./upload-tool -c configs/minio.yaml --dry-run
./upload-tool -c configs/minio.yaml --report-results
```

## Flags

- `-c <path>` — config 文件路径（必填）
- `-n <int>` — 覆盖 `runner.total`
- `--concurrency <int>` — 覆盖 `runner.concurrency`
- `--methods <csv>` — 覆盖 `methods`（如 `put,post`）
- `--report-results` — 写出完整 `results.json`（默认关闭；`summary.json`、`summary.csv`、`results.csv` 始终写出）
- `--log-level <level>` — `debug`|`info`|`warn`|`error`（默认 `info`）
- `--dry-run` — 仅校验 config 并预览首 10 条 task，不执行上传

## 输出

每次运行会创建目录 `<out_prefix>-<YYYYMMDD-HHMMSS>/`，包含：

- `summary.json` — 仅 meta + summary
- `summary.csv` — meta + summary 的 CSV 形式
- `results.csv` — 每个对象一行（始终写出）
- `results.json` — 完整报告，包含每一个 Result（仅在指定 `--report-results` 时写出）

Console summary 输出到 stderr。

## 架构

模块划分：

- `pkg/config` — YAML 配置加载、默认值与校验
- `pkg/content` — 内容生成器（random / fixed / file）
- `pkg/uploader` — S3 上传实现与 factory（put / post / multipart / presigned）
- `pkg/runner` — 并发 runner、size 采样、verifier、reporter、progress、graceful shutdown
- `cmd/upload-tool` — CLI 入口

上传方法说明：

- `put` — PutObject 单次 PUT 上传，适合小对象（< 5 MiB）
- `post` — POST policy + SigV4 签名表单上传，适合浏览器 / 移动端场景
- `multipart` — CreateMultipartUpload + UploadPart × N + CompleteMultipartUpload，适合大对象（≥ 5 MiB），分片大小由 `method_options.multipart.part_size` 控制
- `presigned` — 预签名 URL 上传（默认 PUT 方法），允许第三方无凭据上传

每个对象上传后通过 HEAD 请求拉取 ETag，与本地 MD5 比对验证一致性（multipart ETag 用 `md5hex(concat(md5(part_i)))+ "-"+partCount` 公式重建比对）。
