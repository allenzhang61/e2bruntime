# Logic Control

Go source and tests live in `src/`, alongside the embedded `index.html`. Shell scripts live in `scripts/`, and Markdown documentation lives in `doc/`; module files, Dockerfile and configuration remain in `dev/control/`. Build from this directory with `GOWORK=off GOTOOLCHAIN=local go build -o /tmp/e2b-logic-control ./src`.

Standalone Gin HTTP service deployed as `e2b-logic-control` on the 211 Docker daemon.
Mac URL: http://localhost:13021 (SSH forward required).

- `GET /api/logics`: stable Logic ID, backup ownership, observed running/paused instances, retained archives, separate Metadata and shared dependencies with sizes.
- `POST /api/switch`: `{"logic_id":"workspace-a1b2c3","target_runtime":"runtime2"}`. Browser requests require matching Origin; CLI requests without Origin require `X-Control-Request: 1`.
- `POST /api/restore`: `{"logic_id":"workspace-a1b2c3"}`. Restores on the currently assigned owner Runtime; no target override is accepted.
- `GET /api/jobs`: persisted asynchronous restore outcomes.

Logic IDs are `workspace-` plus six random lowercase letters/digits (for example `workspace-a1b2c3`). `control/workspaces.json` in S3 persists the mapping from original runtime/instance lineage to the public workspace name. Conditional writes keep concurrent assignments stable and names unique; restores preserve the original lineage through reserved sandbox metadata. S3 inventory does not require the source Runtime. The target must be reachable; an existing running or paused instance with the same Logic ID blocks restore. Both controller preflight and node-local import under flock check uniqueness. This protects supported imports; arbitrary direct Runtime creation with forged lineage metadata is outside this controller.

Switch only updates the S3 ownership JSON with an ETag conditional write; it does not create instances and works even when the selected Runtime is offline. Restore is a separate action: it reads the saved owner, cold-boots the latest complete filesystem backup there and verifies the resulting instance exists. Node-local preparation rejects ownership belonging to another Runtime instead of taking it over. It does not stop/delete the source or migrate process/memory state. Old instances are shown explicitly; there is no transparent request-routing layer or automatic failover. A failed partial import is retained for inspection, never blindly recreated. All currently available backups remain retained. Runtime2's existing periodic-backup service remains disabled unless separately enabled.

Configuration examples are in `config.env.example`; `configInit` centralizes every environment variable. Runtime API keys and RustFS AK/SK stay in `/opt/e2b-control/control.env` (root-only) on 211; none enter HTML/JSON. Service binds loopback and is accessed through SSH, not exposed to the LAN. Rootless container uses a read-only root filesystem and persistent Docker volume `e2b-control-data` for jobs. Losing the volume loses operation history; S3 still supplies backup and ownership inventory.

`bash dev/control/scripts/deploy.sh` cross-compiles on Mac, configures persistent private SSH forwards from host to each VM, and builds/runs the Docker service on 211. `dev/admin/deploy.sh` separately updates Runtime Web Admin and its node-local backup API. UI embeds the original Mac Web Admin URLs; existing SSH forwards are required. Dashboard authentication uses the server-only deployment mode described below.

HTTP routes and the loopback/Origin middleware are registered in `src/http.go`; asynchronous restore and job persistence remain in `src/main.go`.

Tests: `cd dev/control && GOWORK=off GOTOOLCHAIN=local go test -race ./...`.

Explicit restore generations allow importing a retained archive again after its
previous instance expired. Idempotency is scoped to archive plus assigned
ownership generation; interrupted/ambiguous creation is blocked for inspection.
Templates remain keyed to the immutable archive and registration is idempotent.
The existing initial 30-minute Runtime TTL is unchanged; control service does not
silently extend sandbox lifetime. Dashboard framing is permitted only for the
controller's localhost:13021 and 127.0.0.1:13021 origins (plus its own origin).

If the controller restarts during a restore, it reconciles S3 ownership and the
live target's archive lineage, Logic ID and creation time. It never repeats a
create call. Successful observed recovery updates the persisted operation; an
unverifiable operation fails and requires inspection.

Embedded Dashboards use the upstream server-only `E2B_API_KEY` self-hosted mode
from each VM's existing root-only `sdk.env`. Dashboard now binds VM loopback only;
SSH still supplies access. Browser receives neither team key nor an API-key cookie,
so the two embedded views work without cross-site cookie/session conflicts.

On Mac, `bash dev/control/scripts/connect.sh` establishes all five forwards using a managed
SSH control socket. Dashboard traffic passes through host ports 14001/14011 into
VM loopback, while sandbox proxy forwards retain the original VM destinations.
If the owner Runtime already hosts that Logic ID, restore returns 409 and its Instance
ID; remove the old target instance through its Web Admin before restoring there.
The importer reuses a compatible private snapshot template already registered to
the target team, allowing restoration on a previous source Runtime as well.

## Runtime Debug

`/debug` 提供 Runtime 1/2 选择、操作选择、JSON 参数编辑及结果/耗时展示；统一视图提供入口。`GET /api/debug/actions` 返回示例参数；`POST /api/debug/{action}` 接收 `runtime` 和对应参数。支持 list、create、get、delete、command、write-file、read-file、filesystem-snapshot、delete-snapshot、hibernate、wake、timeout。列表返回 `next_token`，可将其放入下一次 list 请求。

客户端实现位于 `src/clientgo/client.go` 与 `command.go`，由原 dev/test 迁入；原端到端测试通过本地 module replace 共享它。Control 不再依赖测试模块。

`elapsed_ms` 是 Control 处理调用的耗时，页面另显示浏览器往返耗时。Debug 只调用预配置 Runtime，沿用 loopback/Origin 中间件，拒绝 URL/key 等额外字段，屏蔽返回的访问凭据。guest 文件/命令请求通过 211 私有 SSH 代理端口 14013/14023；新增 `RUNTIME1_PROXY_URL`、`RUNTIME2_PROXY_URL` 配置。

hibernate 直接调用 Runtime 的 memory:true pause，不触发 dev/backup 的最终 S3 备份；filesystem-snapshot 是 memory:false 本地快照，不自动上传 S3。wake 使用 v2 connect，并延长 TTL。这里用于单步调试，跨节点镜像恢复使用统一视图中独立的“切换”和“恢复”操作。

端到端验证：`cd dev/control && CONTROL_DEBUG_LIVE=1 GOWORK=off GOTOOLCHAIN=local go test ./src -run '^TestDebugLive$' -v -count=1`。可用 `CONTROL_DEBUG_URL` 指定开发环境 Control（默认 http://localhost:13021）。测试会创建并清理自己的临时沙箱；若要保持 S3 无测试备份，应在验证期间暂停两台周期备份 worker，结束后恢复。

2026-10-11 实测两个 Runtime 全流程通过：创建分别 65.94/69.95 ms，文件系统快照 111.41/115.73 ms，休眠 52.29/60.77 ms，唤醒 139.82/125.96 ms。单次样本为 Control 服务端计时；文件写入、读回、命令、快照模板删除和实例删除均通过，唤醒后读回内容一致。

HTTP 层优先使用 Gin：`ShouldBindJSON` 与 binding tags 解析/验证参数，统一 `AbortWithStatusJSON` 错误，`c.JSON` 返回 API 数据，`c.HTML` 渲染嵌入模板；未知 JSON 字段被拒绝。请求体大小仍由标准库 MaxBytesReader 限制；上游 Runtime HTTP 客户端、持久化序列化和带超时/优雅退出的 http.Server 保留标准库实现。

实例操作提供“延迟休眠时间”弹窗，输入 1–60 分钟。`POST /api/instances/extend` 接收 runtime、sandbox_id、delay_seconds（60–3600），查询运行中实例的 endAt，将延后时长加到当前截止时间后，调用 Runtime timeout 接口并读回验证；保留原有 lifecycle.onTimeout，不唤醒休眠实例，也不修改周期备份间隔。

“预计到期”列读取运行实例 GET 响应的 endAt 和 lifecycle.onTimeout（列表接口不包含到期策略），每秒显示多久后休眠或删除，悬停显示具体时间。休眠实例显示已休眠；查询失败时显示无法确认，不从备份 Metadata 推断策略。

预计到期列同时展示下次备份倒计时。Control 通过每台节点受认证的 GET /schedule 读取 worker 发布的真实 ticker 调度时间、当前备份实例和本轮已处理实例；仅对运行中且有备份权限的实例显示调度时间。休眠、非归属、服务停止或不可用分别标记，不以最新 S3 备份时间加两分钟推算。到点表示调度开始，顺序执行和耗时可能使实际快照稍后完成。

## 独立实例计划

实例操作分为休眠（休眠、唤醒、设置自动休眠、取消自动休眠）和删除（删除、设置自动删除、取消自动删除）。设置弹窗使用本机时区的具体日期时间，并转成 UTC 提交。POST /api/instances/schedule 接收 runtime、sandbox_id、action（set_sleep/cancel_sleep/set_delete/cancel_delete）及设置时的 at。

Control 在数据卷 instance-plans.json 原子持久化两个独立时间，每 10 秒检查执行，允许暂停实例按删除计划销毁；两项同时到点时优先删除。首次接管保留另一项原生策略，取消一项不改变另一项。运行实例剩余 TTL 不足 5 分钟时申请续期 30 分钟。Runtime 最长存活限制仍有效；Control 或 Runtime 长时间不可用时，续期和定时执行不能保证。计划按具体 Runtime/Instance ID 保存，恢复成新实例不会自动继承。主动休眠仍保存进程/内存，不额外上传 S3。

### 备份状态

`/backups` 汇总两个 Runtime 的备份配置和进程状态，每 5 秒刷新。`GET /api/backups` 返回相同信息。展示间隔、保留数量、Bucket、归档目录、当前实例和下次调度时间；查询服务不可访问与备份 worker 停止分别显示。运行状态不等同于备份成功，页面不启动或停止服务。

新增共享依赖层在 `backups/_shared/<Runtime>/layers/` 中跨 Logic ID 复用；实例归档和 Metadata 仍按 Logic ID 保存。已有备份继续显示其实际引用的旧路径，不伪造或改写历史归档的依赖。

统一表格的当前实例、状态、时间及操作按 S3 归属绑定的机器和实例筛选。切换到未恢复的目标时显示“待恢复”，旧机器实例不再展示为当前；原实例不会因此被删除，仍可在对应 Runtime Web Admin 查看。API 的 `instances` 保留全部实际观测，`current_instances` 用于当前视图。

“Logic ID 操作 → 查看实例”打开 `/logics/<Logic ID>/instances`，列出每个 Runtime 的实际关联实例和备份/归属记录中的实例。当前归属、原始来源与备份来源分开标注，未观测到和 Runtime 查询失败分别显示，便于切换后排查旧实例。每 5 秒刷新。

“备份镜像文件”从主表格移到 Logic ID 操作按钮。点击打开 `/logics/<Logic ID>/backups`，按备份时间从新到旧展示来源实例、归档和 Metadata 的完整 S3 key/大小，以及实际共享依赖层。每 5 秒刷新，空清单单独提示。

### 页面刷新

统一视图、备份状态、关联实例与备份镜像列表默认每 5 秒刷新，提供暂停、5/10/30/60 秒选择和倒计时提示。每个页面的选择保存在浏览器 localStorage；暂停后仍可手动刷新。刷新请求串行执行，完成后再等待所选间隔。Runtime Debug 不自动执行 API 操作，嵌入的 Runtime Web Admin 使用其自身刷新策略。
