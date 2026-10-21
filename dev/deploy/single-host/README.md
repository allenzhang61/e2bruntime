# 单宿主 Runtime 部署

在 x86_64 Linux 宿主部署一个 Runtime、周期备份与可选 Dashboard Web Admin 代理，不创建 Vagrant VM。Control 已迁入 si-sandbox，本流程不构建、配置或重启 Control。

需要 Docker Compose、systemd、无交互 sudo、KVM/TUN 和 Embed 宿主环境。复用已有 S3 服务；不部署或清理 RustFS。

将 `config.env.example` 作为服务器配置模板，设置 `E2BRUNTIME_CONFIG` 指向实际配置文件。服务器已有源码时运行：

```bash
export E2BRUNTIME_CONFIG=$HOME/e2bruntime-single.env
bash dev/deploy/single-host/deploy.sh build
bash dev/deploy/single-host/deploy.sh deploy
bash dev/deploy/single-host/deploy.sh status
bash dev/deploy/single-host/deploy.sh verify
bash dev/deploy/single-host/deploy.sh smoke
# 明确确认后才允许删除当前 Runtime 数据，S3 与 Control 数据保留。
bash dev/deploy/single-host/deploy.sh rebuild --delete-runtime-data
```

`remote.sh` 可拉取指定 `E2BRUNTIME_REPO` / `E2BRUNTIME_REF` 的临时 checkout，调用相同部署实现，退出后清理。Git SSH key 仅用于宿主获取源码，不进入 Docker。

`home-server/services/e2bruntime` 上传本地 Runtime 源码（包括未提交的迁移），调用本目录入口；传入 `E2BRUNTIME_SOURCE_COMMIT` 标记源码所属提交，其他直接 Git checkout 调用自动读取 HEAD。构建使用 golang:1.26.8-bookworm 容器，复用 Go 缓存，输出位于 `E2BRUNTIME_WORK/out`。部署前先测试构建及 S3 PUT/GET/HEAD/DELETE 校验，之后才更新 Runtime。

安装位置 `/opt/e2b-runtime`，Compose 项目为 `e2b`；普通更新保留既有 `.env`、数据库、备份配置。`deploy` 重启 orchestrator 并终止运行沙箱，应在业务空闲时执行。`rebuild` 仅清理当前 e2b 项目与官方 Runtime 宿主数据，不清理旧安装、VM、隧道或全局 Docker 数据。

Web Admin 资源位于 `dev/web-admin`。局域网配置 `WEB_ADMIN_LISTEN`、`WEB_ADMIN_ORIGINS` 与 `PUBLIC_SANDBOX_URL`；`WEB_ADMIN_ENABLED=0` 关闭代理。Dashboard 保留官方 team API key 登录，key 位于 root-only `/opt/e2b-runtime/sdk.env`。Nginx 使用指定 frame-ancestors 限制嵌入来源，Runtime 沿用 Embed host-network。

Control 的部署和单节点测试由 si-sandbox/home-server 的独立入口执行。Runtime 更新后重新部署 Control，刷新服务端连接凭据。默认 `verify` 不依赖 Control；`smoke` 覆盖创建沙箱、命令、代理和销毁。周期备份、S3 恢复及机器重启恢复需另行验证。
