# object_upload_test_tools

面向 S3 兼容对象存储的高并发上传测试工具。支持对任意 S3 兼容 endpoint
验证四种上传方法（PutObject、POST form、Multipart、Presigned），通过
HEAD + ETag 校验对象一致性，并在每次运行后输出 JSON/CSV/console 报告。

## 安装

    go build -o upload-tool ./cmd/upload-tool

## 配置

编辑 `configs/<endpoint>.yaml`。完整示例见 `configs/minio.yaml`。
所有字段及其默认值已在
`docs/superpowers/specs/2026-10-26-object-upload-test-tools-design.md`
中详细说明。仓库内置两个示例：

- `configs/aws.yaml` — AWS S3（`https://s3.us-east-1.amazonaws.com`），需替换 `access_key` / `secret_key`。
- `configs/minio.yaml` — 本地 MinIO（`http://localhost:9000`），默认凭据为 `minioadmin`。

## 运行

    ./upload-tool -c configs/minio.yaml
    ./upload-tool -c configs/aws.yaml -n 5000 --concurrency 64 --methods put,multipart
    ./upload-tool -c configs/minio.yaml --dry-run
    ./upload-tool -c configs/minio.yaml --report-results

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

参见 `docs/superpowers/specs/2026-10-26-object-upload-test-tools-design.md`
与 `docs/superpowers/plans/2026-10-26-object-upload-test-tools.md`。

模块划分：

- `pkg/config` — YAML 配置加载、默认值与校验
- `pkg/content` — 内容生成器（random / fixed / file）
- `pkg/uploader` — S3 上传实现与 factory（put / post / multipart / presigned）
- `pkg/runner` — 并发 runner、size 采样、verifier、reporter、progress、graceful shutdown
- `cmd/upload-tool` — CLI 入口
