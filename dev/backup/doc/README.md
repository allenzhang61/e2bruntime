# 定时文件系统备份

这是随两台自建 Runtime 部署的 Go 备份服务，通过公开 API 为所属 team 的所有 `running` 沙箱创建 `memory:false` 快照。无需修改 API 的请求调度；每台 VM 各有一个 systemd 服务，单循环顺序执行，不并发重叠快照。

首次启动立即备份，之后每 2 分钟执行一轮。每个沙箱在 S3 独立保留最近 10 个成功归档（不将两台或多个沙箱的数量合并）；上传和 HEAD 大小/元数据检查成功后，删除超出的最早归档。失败不触发删除旧备份，下一轮重试。周期执行超过间隔时跳过积压 tick，不堆积任务。

归档采用 Research 的原生 V3 层导出逻辑，排除 memfile/snapfile。不变的 rootfs 父层在 `backups/<runtime>/layers/<buildID>.tar.gz` 单独上传一次；每次备份保存本次增量层、header、metadata 和 `external_build_archives` 依赖清单。共享基础层不参加每沙箱 10 份的删除策略，因此删除其他旧备份不会破坏新备份；不上传全节点目录、数据库或凭据。归档中有逐文件 SHA-256 manifest，S3 对象元数据有整个归档 SHA-256。HEAD 检查大小和元数据，不等同于下载逐字节校验。

对象路径：`backups/<runtime>/<sandboxID>/filesystem/<UTC时间>-<buildID>.tar.gz`。归档成功或失败后都会尝试删除此次临时快照模板，源沙箱继续运行。服务当前用于本地 file:// V3 存储，归档与其共享基础层还需兼容 Runtime 和模板登记流程才能跨节点恢复；此服务不实现自动故障接管。共享基础层目前保留，不做引用计数垃圾回收。每个沙箱最多保留 10 个快照归档，bucket 还会有基础层对象。恢复时先下载依赖清单中的基础层，再叠加该归档；不能只下载一个增量归档。原有 Research exporter 位于 `src/e2bembed/export.go`，本目录导出代码是其独立移植。

## 目录结构

```text
dev/backup/
├── src/                # Go 服务、命令及测试代码
├── scripts/            # 部署、安装、运行及验证脚本
├── doc/                # 使用说明和历史验证报告
├── out/                # 本地验证生成的数据
├── go.mod / go.sum     # 独立 Go module
├── config.env.example  # 配置示例
└── service             # systemd 单元定义
```

在 `dev/backup` 下构建使用 `GOWORK=off go build -o /tmp/e2b-backup ./src`；测试使用 `GOWORK=off go test -short -race ./...`。脚本会自行定位 module 根目录，可从任意工作目录调用。VM 中的安装路径仍是 `/opt/e2b-runtime/backup`。

## 配置与部署

```bash
./dev/backup/scripts/deploy.sh up
./dev/backup/scripts/deploy.sh status
./dev/backup/scripts/deploy.sh logs
./dev/backup/scripts/deploy.sh stop
```

`up` 交叉编译 Go 服务并安装到 runtime1/runtime2；使用现有 RustFS 配置，各创建一个 metadata `purpose=periodic-backup-demo` 的演示沙箱。重复执行复用正在运行的演示沙箱。它们每 30 秒向 `/home/user/backup-demo.log` 追加 UTC 时间；每轮备份将这两个沙箱的 TTL 续到 30 分钟。其他沙箱只备份，不修改生命周期。服务正常运行时演示沙箱持续运行；服务停止或长期故障后会按 TTL 到期，不是永久禁用超时。

配置集中在 `configInit`，guest `/opt/e2b-runtime/backup/config.env`（root-only）：

| 变量 | 默认或示例 | 用途 |
|---|---|---|
| BACKUP_INTERVAL | `2m` | 周期间隔 |
| BACKUP_KEEP | `10` | 每个沙箱保留数量，至少 1 |
| BACKUP_NODE | `runtime1` / `runtime2` | 节点对象前缀，必须唯一 |
| BACKUP_API_URL | `http://127.0.0.1:3000` | 本机 Runtime API |
| BACKUP_PROXY_URL | `http://127.0.0.1:3002` | 演示文件写入代理 |
| BACKUP_STORAGE_ROOT | `/var/lib/e2b/storage/templates` | 本地制品目录 |
| E2B_API_KEY | 从 root-only `sdk.env` 加载 | 本实例 team key |
| RUSTFS_ENDPOINT | `http://192.168.1.211:19000` | S3 endpoint |
| RUSTFS_BUCKET_NAME | `runtime-snapshots` | 已存在的 bucket |
| RUSTFS_REGION | `us-east-1` | 签名 region |
| RUSTFS_ACCESS_KEY / RUSTFS_SECRET_KEY | 从 root-only `s3.env` 加载 | S3 凭据，不输出 |

修改 guest 配置后执行 `sudo systemctl restart e2b-backup.service`。日志为 `sudo journalctl -u e2b-backup.service`，记录 sandbox ID、对象 key、上传字节数、SHA-256、快照及解析、归档、上传/HEAD、总耗时、保留和删除数量。单个备份最多 110 秒，避免一个异常任务阻塞所有沙箱。

当前 RustFS 是宿主 Docker 的 4 GiB tmpfs 临时服务，停止或宿主重启会丢失备份；本环境的三个服务也在同一物理机上，属于功能观察环境。长期容灾需换成持久化、独立故障域的 S3。备份服务持有最小测试场景下的 root 读存储权限和 RustFS 凭据，不应直接作为多租户生产部署。

## 验证

```bash
cd dev/backup
GOWORK=off go test -race ./...
# 实际 S3 保留测试需加载上述凭据，另设 BACKUP_LIVE=1。
```

归档测试覆盖父层依赖、逐文件 hash、不包含内存、缺失依赖和未知 header 拒绝。保留测试覆盖超过 10 份时删除最早对象；S3 集成测试在隔离的 `checks/retention-*` 前缀写 11 个小对象，验证只剩最近 10 个，最后清理所有测试对象。它验证删除逻辑，不替代 22 分钟的真实周期观察。

## 已启动的观察环境（2026-10-10）

- runtime1 沙箱：`i3boc9qul6uff777pdd2q`
- runtime2 沙箱：`inyv4ym7yitz5nrb9vuyu`
- 两台均启用 `e2b-backup.service`，间隔 `2m`、每沙箱保留 `10`。
- 已通过目标机上的 S3 保留测试、实际归档/基础层下载 SHA-256 校验，以及两个沙箱持续追加文件的检查。

最终版本首次上传需要建立基础层，单次数据如下（包含基础层导出/上传的时间，不能当作稳态耗时）：

| 节点 | 本次增量字节数 | 快照及解析 ms | 归档/首次基础层上传 ms | 增量上传/HEAD ms | 总耗时 ms |
|---|---:|---:|---:|---:|---:|
| runtime1 | 95,593 | 125.37 | 33,015.37 | 3.45 | 33,146.18 |
| runtime2 | 59,913 | 126.12 | 32,549.72 | 2.72 | 32,680.21 |

每台还保存三个共享基础层，其中最大的基础层约 480 MB，其余约 166–180 KB 和 1.23 MB。之前试运行产生的四份完整归档（每台两份，约 482 MB/份）仍保留，会随正常保留策略逐渐退出；未引用的重复基础层与失败上传临时垃圾已清理，正式备份未删除。新版本只上传增量，避免每轮重复上传完整基础镜像。

当前观察包含真实周期上传；尚未等待常驻沙箱产生第 11 份备份。超过 10 份的删除已在同一个 RustFS 上通过 11 个隔离小对象验证。可继续运行 `deploy.sh status` 查看每个沙箱备份数量、对象大小及运行状态，`deploy.sh logs` 查看每轮结果。

下一轮在 07:33 UTC 自动触发，确认 2 分钟周期正常。基础层已存在时的单次样本：

| 节点 | 增量字节数 | 快照及解析 ms | 归档 ms | 上传/HEAD ms | 总耗时 ms | 本次后保留份数 |
|---|---:|---:|---:|---:|---:|---:|
| runtime1 | 97,585 | 97.14 | 24.72 | 3.60 | 128.33 | 4 |
| runtime2 | 61,975 | 98.45 | 22.42 | 3.70 | 126.89 | 4 |

两服务继续运行，不停止演示沙箱。RustFS 当前占用约 2.7 GiB/4 GiB；这包括初始完整备份和共享层，不是单个增量的大小。耗时来自 VM 内 Go 服务，是单次样本，不包含 Mac SSH 隧道。

### 当前启用状态调整

按用户要求，runtime2 的 `e2b-backup.service` 已停止并禁用，RustFS 中 `backups/runtime2/` 的 7 份备份及 3 个专属基础层已全部通过 S3 API 删除，并确认该前缀无对象。runtime1 服务保持 enabled/active，所有原有对象保留，每 2 分钟备份、最近 10 份保留策略继续生效。runtime2 沙箱未手动销毁，停止备份续期后按其现有 TTL 到期。通用 `deploy.sh up` 会重新启用两台服务，当前无需再次执行。

### 全新 runtime2 的 S3 恢复验证

已用 runtime1 的 2026-10-10 07:51:12 UTC 增量备份和三个共享基础层，在销毁重建后的 runtime2 成功启动沙箱，读取到备份时的日志内容。文件名、大小、耗时及重现方式见 [恢复实测](restore-result.md)。恢复不会重启 runtime2 的定时备份服务。

### 主动休眠前的最终备份

在 Runtime VM 内使用新版本二进制：

```bash
sudo /opt/e2b-runtime/backup/run.sh hibernate SANDBOX_ID
```

调用方先保存 Agent 任务进度、停止应用写入，并在整个命令执行期间保持停止写入。命令先通过 connect 获取访问凭据并将运行期限至少延长到 10 分钟，再执行 guest sync、filesystem snapshot、增量归档与 S3 上传、HEAD 及下载 SHA-256/大小校验、保留最近 10 份，全部成功后才调用 pause(memory:true)。失败不发送休眠请求；休眠请求网络失败时状态可能不确定，需要查询 Runtime。最终备份和周期任务共享节点文件锁，避免并行上传、快照和清理；等待锁受命令 5 分钟超时控制。日志记录最终对象名、大小、备份与休眠耗时。

本入口保存 filesystem 到 S3，同节点仍使用完整 Hibernate 恢复；异节点沿用 filesystem 冷启动恢复流程。不拦截原生 pause 或超时自动休眠，不自动保存应用内存进度，也不阻止外部客户端或应用继续写入；因此应用未停止写入时，S3 数据可能早于休眠时刻。调用期间应保证沙箱 TTL 足够且没有其他生命周期操作。无需重编译核心 API 或 orchestrator。

TestHibernateLive（HIBERNATE_LIVE=1，VM 本地现有环境变量）在独立测试沙箱验证文件写入、最终备份上传校验、休眠状态、connect 唤醒与文件读取，最后删除该沙箱和该沙箱的 S3 对象；保留共享基础层。默认单元测试验证备份失败或取消时不会调用休眠，以及周期/主动入口互斥。

实测（2026-10-10 08:15:18 UTC，runtime1）：TestHibernateLive 通过，完整测试 0.57 秒；最终备份 + Hibernate 270.74 ms（含 connect/同步等入口开销），snapshot 100.93 ms，归档 20.25 ms，S3 上传/HEAD/下载校验 4.13 ms，备份合计 126.59 ms，pause 123.31 ms。增量对象 26,215 字节；共享基础层已存在，因此不是首次上传耗时。确认 paused 状态、connect 唤醒和文件内容一致后，删除临时沙箱及其 S3 归档。runtime1 已安装新版本并重启定时服务，runtime2 未变更。

### 三分钟沙箱管理流程

在 Mac 项目根目录运行：

```bash
./dev/backup/scripts/flow.sh
```

脚本编译并在 runtime1 执行 Go 测试 TestSandboxFlowLive，不改 runtime2 或周期任务配置：创建独立沙箱、写入唯一文件并 sync、等待三分钟、确认存在周期任务产生的备份、通过 hibernate 执行最终备份并休眠、检查 paused 状态及新增 S3 归档、下载所有该沙箱归档与父层校验、connect 唤醒并逐字比较文件内容、删除沙箱并检查 404。失败也会尝试删除本次沙箱。S3 备份保留供查看；不会删除其他沙箱对象。

环境变量集中在 flowConfigInit（基于 configInit）：FLOW_LIVE=1 启用测试，FLOW_WAIT 默认 3m（3m–10m），FLOW_REPORT 默认 /opt/e2b-runtime/flow-result.json。运行脚本固定 3m，并从 VM 已有 root-only 配置加载 API/S3 凭据。Mac 脚本另使用 RUNTIME_SSH_HOST（默认 zyl@192.168.1.211）。报告复制到 dev/backup/out/flow-result.json，包含沙箱 ID、备份清单/时间/大小、备份与休眠/恢复耗时及校验、删除结果，不含凭据。

周期任务每两分钟按全局时钟触发，不从测试沙箱创建时重新计时；因此三分钟内可能产生一或两份自动归档，再加休眠前最终归档。测试要求至少一份自动归档及一份新增最终归档，不限定恰好两份。该测试验证同节点 Hibernate 唤醒与文件内容；不执行跨节点容灾恢复。

### 独立 Metadata 文件（当前格式）

每次周期备份和休眠前最终备份，先在快照前后读取 GET /sandboxes/{id} 的配置，检测期间变化；若变化则失败并等待下一轮重试。配置读取失败也不会发布备份。当前新增备份以两个独立 S3 对象保存，同目录、同时间和 build ID：

```text
<UTC时间>-<buildID>.tar.gz
<UTC时间>-<buildID>.metadata.json
```

Metadata 不再嵌入 tar.gz。JSON 先上传并下载校验，然后上传归档并进行原有 HEAD/按需下载校验；二者验证成功才记为备份完成，并执行保留策略。归档 S3 属性记录 metadata-key，JSON 记录 archive_key、archive_bytes、archive_sha256、共享父层依赖，方便识别正确的一组文件。失败尝试删除这两个新对象；S3 没有跨对象事务，进程突然退出仍可能留下孤立 JSON，后续成功备份的保留阶段会清理。旧归档没有 JSON 的格式继续兼容。

JSON 包含 schema_version、来源节点、原 sandbox ID、snapshot/build ID、配置采集时间、模板、CPU/内存/磁盘、envd 版本、自定义 metadata、生命周期、网络基本策略、卷名称和路径、运行起止时间。起止时间为历史观测值，不能直接作为恢复后的过期时间。API/guest/traffic access token 不保存；可能携带凭据的网络代理和变换规则不导出。API 查询不到的 envVars、team/user 归属、Agent 启动命令、autoPauseMemory 和 IAM 等列在 missing_fields，不把此配置声明为完整容灾恢复记录。

保留数量仍按归档计，每沙箱 10 份归档加对应 JSON；超过限制成对删除，基础层保留。Web Admin 数量仍统计归档，不会因 Metadata 数量翻倍。历史备份不会伪造当时配置或自动补齐 JSON。此前短暂验证的包内配置格式可由 importer 兼容读取，新版本不再生成它。当前 Runtime 2 导入流程仍以文件系统恢复为主，尚未自动使用独立 JSON 重建任意配置。

实测（2026-10-10 08:58:41 UTC，runtime1）：TestMetadataBackupLive 通过，独立归档 20,725 字节、Metadata JSON 1,547 字节，备份合计 161.66 ms。归档内无 sandbox-config.json，JSON 原 sandbox ID、CPU=2、RAM=512 MB、自定义 metadata、归档 key/大小/SHA 绑定正确。TestS3RetentionLive 用 11 组对象确认保留 10 组、最旧两个文件都删除，隔离对象最终清理；测试沙箱删除，实测这一组 S3 对象保留。新版已部署到 runtime1，周期备份与只读查询服务 active；runtime2 未修改。

测试对象前缀：backups/runtime1/iig1su8p1y3iqbzvt0ze4/filesystem/，文件 basename 为 20261010T085841.274158008Z-c1a0c505-24a3-459d-95a3-3575c20caa14，分别加 .tar.gz 和 .metadata.json。

### Logic ID 备份目录

新增归档及独立 Metadata 使用 `backups/<workspace-xxxxxx>/<Runtime>/<Instance ID>/filesystem/`；同一 Logic ID 恢复到其他 Runtime 后仍在同一个 workspace 目录下。共享基础层使用 `backups/_shared/<Runtime>/layers/<buildID>.tar.gz`，同 Runtime 的不同 Logic ID 复用相同 build ID。旧备份引用的旧目录基础层仍保留并兼容恢复；不删除仍被引用的文件。worker 与 Control 共用 `control/workspaces.json` 的条件写入注册表。列表、导入和依赖下载兼容旧目录，历史对象不会自动搬迁；当前目录仍保留最近 10 组归档与 Metadata。

### 动态备份配置

Control 的备份状态页可分别修改 Runtime 的备份间隔（10–86400 秒）和每实例保留数量（1–1000）。节点将配置原子保存到 `backup/settings.json`（0600），优先于环境变量；服务重启后仍生效。运行中的备份完成后采用新配置，空闲时约 1 秒内重新计算调度时间，修改配置不会启动已停止的服务。减少保留数量将在后续成功备份时清理旧备份，不清理共享依赖层。

命令索引：在仓库根目录执行 `make -C dev/backup help`，或在该目录执行 `make help`。每个目标列出用途并调用原有脚本。
