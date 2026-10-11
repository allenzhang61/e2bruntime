# 从零重建记录

2026-10-11 07:10 UTC，使用当前工作区代码在 zyl@192.168.1.211 完成重建。

- 删除并重新创建 runtime1/runtime2 Vagrant VM，旧数据库、沙箱和本地快照随磁盘删除。
- 重新编译安装 API、orchestrator 和 migrator，启用文件系统快照。
- 删除并重新创建 RustFS，bucket `runtime-snapshots` 实测为 0 个对象。
- 删除统一管理服务数据卷并重新部署，Logic ID 列表和操作记录均为空。
- 两台 Runtime 的创建、命令执行、代理访问和删除 smoke 均通过，测试沙箱已删除。
- 两台备份 worker/admin 均 active；每 2 分钟备份、最多保留 10 个，不自动创建示例沙箱。
- 13021 健康检查正常，13001/13011 Web Admin 均 HTTP 200，Mac 转发已重连并使用新 VM 地址。

重建入口：`bash dev/deploy/reset.sh --all-data`。RustFS 访问配置保留，Runtime API key 重新生成并同步给相关服务；不输出凭据。最终宿主磁盘约剩余 29 GiB。旧基准测试 VM 和其他宿主项目不在本次环境重建范围内。
