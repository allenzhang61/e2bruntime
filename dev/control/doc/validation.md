# 验证结果（2026-10-10）

- 211 Docker 容器：`e2b-logic-control`，仅监听 `127.0.0.1:13021`。
- Mac 入口：`http://localhost:13021`，通过 SSH 转发到 211。
- Logic ID：`runtime1/i3boc9qul6uff777pdd2q`。
- 恢复目标：Runtime2，实际 Instance：`iir3okcpkirtde4e6srpv`，已保留。
- 源 Runtime1 实例仍处于 paused，未删除；当前 S3 备份归属为 Runtime2。
- 镜像：`backups/runtime1/i3boc9qul6uff777pdd2q/filesystem/20261010T094036.810811080Z-e76645d8-497e-4b69-8047-0ef78a2f2a7f.tar.gz`，对应独立 `.metadata.json`。
- Metadata 采集时间：`2026-10-10T09:40:36.773770239Z`。
- 实例内 `/home/user/backup-demo.log` 成功恢复，118 行历史记录，最后一行 `2026-10-10T08:18:58Z`；全部记录时间早于备份。验证不假定源进程在快照前持续写入。
- 重复恢复到 Runtime2：HTTP 409，返回已存在的 Instance，未新增实例。
- 临时停掉 211 到 Runtime1 的查询隧道：S3 的 10 份镜像仍可见，Runtime2 当前实例仍显示 running；隧道已恢复。
- 控制服务重启：根据 S3 归属、归档、目标实例 Logic ID 和创建时间复核完成状态，未重放创建。
- Go `go test -race`、`go vet` 通过；Dashboard TypeScript 与 Next.js 生产构建通过。

初始 Runtime TTL 仍为 30 分钟，验证期间 connect 刷新了 TTL。Runtime2 的定时备份仍保持之前的停用设置。文件系统恢复不会重启源沙箱的用户后台进程；Web 页面明确提示冷启动语义。
