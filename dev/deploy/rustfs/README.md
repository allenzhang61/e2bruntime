# 临时 RustFS S3

使用 **211 宿主机的 Docker daemon** 启动单节点 RustFS，不装进 Runtime VM。用于开发阶段的快照上传/下载验证，默认 bucket 为 `runtime-snapshots`，region 为 `us-east-1`，客户端使用 path-style S3。

```bash
./dev/deploy/rustfs/deploy.sh up
./dev/deploy/rustfs/deploy.sh status
./dev/deploy/rustfs/deploy.sh verify
./dev/deploy/rustfs/deploy.sh down
```

首次 `up` 自动生成 AK/SK 到此目录的 `.env`（权限 600，Git 忽略），上传到目标机 `/home/zyl/e2bruntime/dev/deploy/rustfs/.env`，并通过 Go AWS SDK 测试初始化 bucket。AK/SK 后续启动复用，不输出到部署日志。公开示例见 `.env.example`；需要手工设置时填入实际值。

| 配置 | 默认值/行为 |
|---|---|
| S3 endpoint | `http://192.168.1.211:19000` |
| Web Console | `http://192.168.1.211:19001`，用 `.env` 中 AK/SK 登录 |
| `RUSTFS_BUCKET_NAME` | `runtime-snapshots` |
| `RUSTFS_REGION` | `us-east-1` |
| `RUSTFS_ACCESS_KEY` / `RUSTFS_SECRET_KEY` | 首次随机生成，保存在 `.env` |
| `RUSTFS_IMAGE` | 首次 `latest`，拉取后记录实际 digest 以便复现 |
| `RUSTFS_BIND_IP` | `192.168.1.211` |
| `RUNTIME_SSH_HOST` | `zyl@192.168.1.211` |
| `RUSTFS_REMOTE_DIR` | `/home/zyl/e2bruntime/dev/deploy/rustfs` |

本地需要 Go 1.24+、SSH、OpenSSL；目标机需要 Docker Compose 和无交互 sudo。上传/下载测试全部写在 `s3_test.go`，不使用 Python 脚本：创建 bucket，上传 1 MiB 随机对象，检查长度，下载比较字节和 SHA-256，删除并确认 404；测试对象清理后 bucket 保留。测试环境变量集中在 `configInit`。

对象存于 4 GiB tmpfs，没有宿主数据目录或持久卷；**停止/重建容器后所有快照会丢失**。容器不自动重启，关闭容器日志存储以避免落盘启动凭据。Docker 镜像缓存及小型部署配置保留；`down` 仅删除本 Compose 项目的服务/网络，不执行全局 prune。已有 Runtime VM 不受影响。

Runtime1/2 与此 S3 在同一物理宿主机，这不是独立容灾存储。真正的故障恢复需要把快照上传到独立故障域中的持久 S3。这次只搭建 S3，不会自动把 Runtime 的存储配置或定时 checkpoint 接到此服务。

启动方式参考 [RustFS 官方 Docker 文档](https://docs.rustfs.com/en/installation/container/docker)。

## 实测（2026-10-10）

容器 `e2bruntime-s3-rustfs-1` 已在 211 宿主 Docker 运行，S3 `/health` 返回 200。runtime1/runtime2 均能访问 `http://192.168.1.211:19000/health`。默认 bucket 已创建；Go AWS SDK 以同一 AK/SK 完成 1,048,576 字节对象上传、大小检查、下载逐字节/SHA-256 校验、删除及删除确认。单次 PUT 89.94 ms，GET+校验 88.29 ms（Mac 到 211，非性能基准）。测试对象已删除，空 bucket 保留供快照测试使用。
