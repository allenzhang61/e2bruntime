# runtime1 S3 快照恢复到全新 runtime2

2026-10-10 已验证成功。仅销毁并重新创建 runtime2 Vagrant VM，runtime1 沙箱和定时备份保持运行；runtime2 重新部署同版本 fork API/orchestrator，没有启用定时备份。

测试开始时选择的最新文件系统备份时间是 **2026-10-10 07:51:12.449439773 UTC**。测试期间 runtime1 继续产生后续备份，因此这是本次实际使用的最新样本。

| 类型 | 文件名 | 备份时间 / 基础层上传时间（UTC） | 字节数 |
|---|---|---|---:|
| 本次增量备份 | `20261010T075112.449439773Z-3c655cd2-5d80-45c3-ac9d-06002db5f12c.tar.gz` | 2026-10-10 07:51:12.449439773（备份） | 105,678 |
| 共享基础层 | `8201a8f2-d7c1-414f-aa08-297f290279b8.tar.gz` | 2026-10-10 07:31:45（上传） | 166,208 |
| 共享基础层 | `9d22bc42-9b9a-42cc-be8e-db696f94746c.tar.gz` | 2026-10-10 07:31:45（上传） | 480,272,135 |
| 共享基础层 | `d514653f-c1dd-46f6-9b48-a046105439d5.tar.gz` | 2026-10-10 07:31:45（上传） | 1,233,865 |

增量位于 `backups/runtime1/i3boc9qul6uff777pdd2q/filesystem/`，共享基础层位于 `backups/runtime1/layers/`。所有对象实际下载后与 S3 元数据 SHA-256 比较；解包时逐文件校验 manifest SHA-256，拒绝路径穿越、链接和非文件系统制品。恢复过程没有使用 runtime1 的本地存储目录或复制它的数据库。

导入目标本地存储后，测试在目标自己的 team 下登记新模板及 ready build，再通过 Runtime API 创建沙箱。

- 新沙箱：`it3gxjgv1eccvodfaq0ry`
- 目标模板：`restore2b0e3eb5acbe55d4`
- CPU：2；内存：512 MB
- 成功执行 `uname -s`，输出 `Linux`。
- 成功读取 `/home/user/backup-demo.log`，62 条记录，最后一条 `2026-10-10T07:50:58Z`，与本次备份时间吻合。
- 下载、解包、校验：7,358.05 ms；创建 API 返回：1,166.08 ms。

这是文件系统冷启动恢复，不包含旧进程与内存。恢复沙箱已保留，TTL 为创建时起 30 分钟；没有启动 runtime2 备份服务。临时 RustFS 凭据与测试二进制已在测试后从 guest 删除。

## 重现

Go 端到端测试：[restore_test.go](restore_test.go)，导入实现：[restore.go](restore.go)，基础层/路径安全测试：[importer_test.go](importer_test.go)。运行前须有全新 runtime2 和已完成的源码构建产物。

```bash
ssh zyl@192.168.1.211 'cd /home/zyl/e2bruntime/deploy/two-vm && vagrant destroy -f runtime2 && vagrant up runtime2 --provider=libvirt'
./dev/backup/restore.sh
```

第一条会删除 runtime2 全部数据，只在明确重建该开发 VM 时执行。恢复脚本只安装/测试 runtime2；不触碰 runtime1，S3 操作只有列表和下载。结果写入 guest `/opt/e2b-runtime/restore-result.json`、宿主 `/home/zyl/e2bruntime/dev/backup/restore-result.json` 和本地忽略的 `dev/backup/out/runtime2-restore.json`。

本测试针对这两个使用同一默认 `base` 配置的演示实例：验证导入 metadata 的 CPU、kernel/Firecracker 与目标 base 一致，RAM/envd/CPU model 采用目标相同 base 的配置。当前归档尚未保存完整控制面配置，因此任意资源规格的沙箱需另行保存并导入其原始配置，不能直接套用本测试。模板登记是隔离开发环境中的 PostgreSQL 事务，还不是带鉴权的生产导入 HTTP API。
