# Runtime Web Admin

前端基于官方 Dashboard v0.2.1（`f031ba7ec20910d5b09582d6531d7d220cf30c7f`），自定义改动保存于 [dashboard.patch](../assets/web/dashboard.patch)。

项目根目录执行 `bash dev/control/scripts/web-deploy.sh`，在 211 Docker 构建镜像，并安装到 runtime1、runtime2。部署只更新 Dashboard 与 `e2b-backup-admin.service`，保留现有沙箱和 S3 数据。生成的 upstream 源码目录在每次部署前清空，保证删除的页面不会因 tar 覆盖残留。

目录与部署职责：

- `assets/web/dashboard.patch`：官方 Dashboard 的自定义补丁。
- `assets/web/service`：节点管理服务的 systemd 单元。
- `scripts/web-deploy.sh`：构建、传输与部署入口。
- `scripts/web-host.sh` / `scripts/web-guest.sh`：211 宿主和 VM 的安装脚本。
- 节点管理 API 的 Go 实现仍由 `dev/backup/src` 提供；Control 调用它进行查询和恢复。

211 上生成的 Dashboard 源码和安装包位于 `/home/zyl/e2bruntime/dev/control/web-admin`，独立于 Control 服务的 Docker 构建目录。`scripts/deploy.sh` 更新 Control，`scripts/web-deploy.sh` 更新两台 Runtime 的 Web Admin。

保留的 UI 功能：

- Sandbox 表格的 S3 Snapshots 数量列，点击查看当前实例的保留快照。
- 快照列表显示 UTC 时间、类型、归档大小、对象名，以及独立 Metadata 的大小和 JSON 链接。
- Metadata 页面核对对象大小、SHA-256、归档绑定后展示格式化 JSON。

原“导出展示”“导入”按钮、页面及其 Dashboard 代理路由已移除。Logic ID 清单、Runtime 切换和跨节点恢复统一放到 [Logic Control](README.md)。节点备份管理 API 的归属及恢复接口仍保留，供独立控制服务内部调用。

两个 Dashboard 使用 upstream 已有的服务端 `E2B_API_KEY` 模式：部署从 VM 本地 root-only `sdk.env` 仅提取该变量到 root-only `backup/dashboard.env`，避免传入 SDK 的废弃 Dashboard 变量。浏览器不需要 API-key Cookie。Dashboard 仅监听 VM `127.0.0.1:3001`，通过私有 SSH 隧道访问；CSP 仅允许自身及 `http://localhost:13021`、`http://127.0.0.1:13021` 内嵌。

Mac 入口保持：

- Runtime1：`http://127.0.0.1:13001/sandboxes`。
- Runtime2：`http://localhost:13011/sandboxes`。
- 统一页面：`http://localhost:13021`。

使用 `bash dev/control/scripts/connect.sh` 建立 Mac 转发；211 的 systemd 私有隧道将 host 14001/14011 转发到 VM 回环 Dashboard。节点管理服务仍在 VM 回环 3012，按其配置 team key 验证请求。AK/SK 保存在 root-only 文件，不传到浏览器。

验证：Go race/vet 与 Next.js TypeScript/生产构建通过；两个 Web Admin 内嵌实例表已实际打开，原导入导出入口不再出现。独立恢复和唯一性验证记录见 [validation.md](validation.md)。
