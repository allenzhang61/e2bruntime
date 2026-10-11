# Runtime Go 端到端测试

在 `runtime1`、`runtime2` 内分别创建一个 **Firecracker 沙箱 microVM**，不是再创建外层 Vagrant VM。测试写入 `/home/user/e2e.txt`，读回逐字节比较并记录 SHA-256，保存本地副本，然后删除沙箱并确认 GET 返回 404。失败时也会尝试清理，沙箱超时设置为 120 秒。

官方 [E2B 文档](https://docs.e2b.dev/) 列出 JavaScript/TypeScript、Python SDK。Runtime 中生成的 Go REST 客户端位于 `packages/api/internal/api`，受 Go internal 导入规则限制。`dev/control/src/clientgo/client.go` 封装公开 REST 与 envd 文件接口，走与官方 SDK 相同的 API；它是测试客户端，不是官方 Go SDK，不直接依赖 Runtime 服务内部实现。

## 运行

本机需要 Go 1.25+ 和 SSH，能够免交互登录目标机；两台 Runtime VM 必须已启动。

```bash
cd dev/test
GOWORK=off go test -v -count=1 -timeout=5m
```

此目录是独立 Go module，`GOWORK=off` 避免父仓库 workspace/toolchain 影响；使用本地 Control 模块中的 `src/clientgo` 包（通过 go.mod replace），该客户端包只使用标准库。默认自动建立临时 SSH 隧道到 `zyl@192.168.1.211`，从对应 VM 的 root-only `sdk.env` 读取 key 到内存，测试后关闭隧道。不打印、不落盘凭据。

每次运行生成 `out/<UTC时间>/runtime1.txt`、`runtime2.txt` 和对应 `.json` 报告。报告包含 sandbox ID、文件大小/hash、校验和删除状态，以及创建、写文件、读回校验、删除、删除确认各阶段耗时（毫秒）。这些耗时包括客户端网络往返；结果目录已忽略，不会提交 Git。

可用 `RUNTIME_SSH_HOST`、`RUNTIME_REMOTE_DIR` 覆盖 SSH 目标与 Vagrant 工作目录，用 `RUNTIME_TEST_OUT` 覆盖输出目录。只编译检查而不连接目标机：

```bash
GOWORK=off go test -short ./...
```

也可以使用已有 API/代理地址和 key，不创建 SSH 隧道；必须同时配置两组 `RUNTIME1_API_URL`、`RUNTIME1_SANDBOX_URL`、`RUNTIME1_API_KEY` 与 `RUNTIME2_API_URL`、`RUNTIME2_SANDBOX_URL`、`RUNTIME2_API_KEY`。URL 使用对应环境的真实地址，key 使用各自实例的 key，不要使用 E2B 云平台 key。

## 环境变量配置

所有环境变量集中在 `runtime_test.go` 的 `configInit` 函数中，字段旁也有示例。空值按默认行为处理；示例 key 是占位符，不是真实凭据。

| 环境变量 | 用途 | 默认值/行为 | 示例值 |
|---|---|---|---|
| `RUNTIME_SSH_HOST` | SSH 目标 | `zyl@192.168.1.211` | `zyl@192.168.1.211` |
| `RUNTIME_REMOTE_DIR` | 远端已有 Vagrant 状态目录 | `/home/zyl/e2bruntime/deploy/two-vm` | 同默认值 |
| `RUNTIME_TEST_OUT` | 本地结果目录 | `out/<UTC时间>` | `out/manual-run` |
| `RUNTIME1_API_URL` | runtime1 API 地址 | 空，自动建立隧道 | `http://127.0.0.1:13000` |
| `RUNTIME1_SANDBOX_URL` | runtime1 沙箱代理 | 空，自动建立隧道 | `http://127.0.0.1:13002` |
| `RUNTIME1_API_KEY` | runtime1 的 team key | 空，通过 SSH 从对应 VM 读取 | `e2b_<runtime1-team-key>` |
| `RUNTIME2_API_URL` | runtime2 API 地址 | 空，自动建立隧道 | `http://127.0.0.1:23000` |
| `RUNTIME2_SANDBOX_URL` | runtime2 沙箱代理 | 空，自动建立隧道 | `http://127.0.0.1:23002` |
| `RUNTIME2_API_KEY` | runtime2 的 team key | 空，通过 SSH 从对应 VM 读取 | `e2b_<runtime2-team-key>` |

默认无需设置任何变量。设置任一 API URL 后进入直接连接模式，需要完整填写两组 URL、代理地址和 key；示例里的回环端口需要先按部署说明开好 SSH 隧道。

## 首次实测（2026-10-10，单次样本）

从 Mac 通过临时 SSH 隧道访问 211，两台均通过文件逐字节校验与删除后 404 断言。耗时不包含预先建立隧道及读取 key 的时间；不是多轮性能基准。

| 实例 | 创建 ms | 文件写入 ms | 读回校验 ms | 删除 ms | 删除确认 ms | 文件大小 |
|---|---:|---:|---:|---:|---:|---:|
| runtime1 | 181.08 | 14.65 | 15.81 | 13.50 | 10.43 | 60 字节 |
| runtime2 | 148.79 | 20.27 | 13.61 | 13.57 | 10.49 | 60 字节 |

本次输出在 `out/20261010T032734.810219000Z/`。沙箱内的文件随删除一起销毁，本地读回副本及 JSON 报告保留。

## 文件系统快照语义验证

`filesystem_test.go` 的 `TestFilesystemSnapshot` 使用 共享 `clientgo` 包的 `FilesystemSnapshot` 方法明确提交 `memory:false`。服务端须先部署上述源码版本并在 API/orchestrator 同时启用 `FILESYSTEM_ONLY_CHECKPOINT=true`。未启用时测试会失败，不会将它当成已支持，也不会降级为完整状态快照。

测试在两台 Runtime 分别创建源沙箱、写随机文件并启动带唯一标记的进程，然后做快照。检查源 boot ID 和进程持续存在；修改源文件后从快照创建新沙箱，断言旧文件内容仍在、新 boot ID 不同、原进程不存在。最后删除两台沙箱和快照模板。结果保存为 `out/<UTC时间>/<runtime>-filesystem.json`。

```bash
GOWORK=off go test -run '^TestFilesystemSnapshot$' -v -count=1 -timeout=5m
```

这些测试在各自独立 Runtime 内恢复，验证文件系统快照语义；不代表已完成 runtime1 到 runtime2 的跨实例导入或 S3 恢复。完整状态容灾仍需 `memory:true` 的 checkpoint 和持久化恢复链路。

### 文件系统快照实测（2026-10-10，单次样本）

两台开发 VM 已部署此 fork 的 API、orchestrator 和同源数据库迁移，并启用开关。完整 `go test -v -count=1 -timeout=5m` 通过，包括原有创建、文件读写、删除测试与新增快照测试。

| 实例 | 文件系统快照 API 耗时 ms | 从快照创建新沙箱 ms | 文件内容恢复 | 源进程继续运行 | 恢复为新 boot ID、旧进程不存在 |
|---|---:|---:|---|---|---|
| runtime1 | 201.79 | 1,526.95 | 通过 | 通过 | 通过 |
| runtime2 | 104.94 | 1,032.52 | 通过 | 通过 | 通过 |

报告位于 `out/20261010T070307.408867000Z/`，测试沙箱与快照模板已清理。快照耗时是从客户端发送请求到返回 snapshot ID；恢复耗时是创建请求到返回 sandbox，不包含随后文件、boot ID、进程断言。数据包含 Mac 到目标机的 SSH 隧道网络往返，只有一个样本；未记录内部 freeze、rootfs 导出、存储上传的分段耗时，不是跨节点恢复或持久化 S3 基准。

接口调用方式：

```text
POST /sandboxes/{sandboxID}/snapshots
X-API-Key: <对应 Runtime 的 key>
Content-Type: application/json

{"memory":false}
```

响应的 `snapshotID` 可作为 `POST /sandboxes` 的 `templateID`，创建一个从该文件系统冷启动的新沙箱。必须明确传 `memory:false`；默认快照包含内存，语义不同。
