# 单宿主部署

在 x86_64 Linux 宿主部署一个 Runtime、Control、周期备份和可选 Web Admin
代理，不创建 Vagrant VM。需要 Git、Docker Compose、systemd、无交互 sudo、
KVM/TUN 和已准备好的 Embed 宿主环境。复用已有 S3 服务，不部署或清理 RustFS。

## 配置与运行

将 `config.env.example` 作为服务器配置模板，设置 `E2BRUNTIME_CONFIG` 指向实际
配置文件。机器地址、S3 凭据文件位置及挂载检查由该文件提供，不写入源码。
Git 仓库 SSH key 和 known_hosts 仅由宿主 Git 使用，不挂载到构建容器。

```bash
export E2BRUNTIME_CONFIG=$HOME/e2bruntime-single.env
bash dev/deploy/single-host/remote.sh build
bash dev/deploy/single-host/remote.sh deploy
bash dev/deploy/single-host/remote.sh control-build
bash dev/deploy/single-host/remote.sh control-deploy
bash dev/deploy/single-host/remote.sh status
bash dev/deploy/single-host/remote.sh verify
bash dev/deploy/single-host/remote.sh smoke
# 明确确认后才允许删除 Runtime 数据。S3 与 Control 数据保留。
bash dev/deploy/single-host/remote.sh rebuild --delete-runtime-data
```

`remote.sh` 在服务器拉取指定 `E2BRUNTIME_REPO` / `E2BRUNTIME_REF` 的临时 checkout，
执行该版本的 `deploy.sh` 后清理源码。也可在服务器已有的临时 checkout 中
直接调用 `deploy.sh`。构建使用临时 `golang:1.26.8-bookworm` 容器，Go 缓存保留；
产物和 commit 保存至 `E2BRUNTIME_WORK/out` 与 `E2BRUNTIME_WORK/commit`。
`control-build` / `control-deploy` 只测试编译 Control，不编译 Runtime 或 backup。

安装位置固定为 `/opt/e2b-runtime` 和 `/opt/e2b-control`，沿用 Embed/backup 的
宿主目录契约。Runtime Compose 项目为 `e2b`；Control 数据卷为
`e2b-control-single-data`。普通更新保留 `.env`、数据库、备份配置与 Control 历史。
重建仅清理当前 `e2b` 项目及官方 Runtime 宿主数据，不清理旧安装、VM、隧道
或全局 Docker 数据。需要迁移历史环境时，应由操作者独立执行。

完整部署先构建、测试，再执行现有 `dev/deploy/rustfs/TestS3` 验证目标 bucket
及 PUT/GET/HEAD/DELETE，之后才停止服务。验证会按需创建 bucket 并清理临时对象。
`deploy` 重启 orchestrator，会终止其运行中的沙箱，应在业务空闲时执行。
`control-deploy` 更新 Control、Dashboard 和代理，不重启 orchestrator。

## 访问边界

默认 Control 和 Web Admin 仅监听回环。局域网使用时必须同时设置
`CONTROL_LISTEN`、`CONTROL_URL`、`CONTROL_ALLOWED_HOSTS`、`RUNTIME1_WEB_URL`、
`WEB_ADMIN_LISTEN`、`WEB_ADMIN_ORIGINS` 和 `PUBLIC_SANDBOX_URL`。
`CONTROL_ALLOWED_HOSTS` 为精确的 `host:port` 列表；POST 保留同源校验。
Control 无登录认证，Host/Origin 校验不构成认证，禁止开放到公网。

Web Admin 保留官方 team API key 登录，key 在 root-only
`/opt/e2b-runtime/sdk.env` 中。Nginx 仅去掉 Dashboard 的 `X-Frame-Options`，
以指定 `frame-ancestors` 限制嵌入来源。模板不会替换其他响应安全策略。
`WEB_ADMIN_ENABLED=0` 不启动代理，可直接使用 Dashboard 回环端口或 SSH 隧道。
Runtime 继承 Embed 的 host-network 端口，需要额外的网络访问边界。

Control 支持 `CONTROL_RUNTIMES=runtime1` 或多个唯一 `runtimeN` 名称，默认
仍为 `runtime1,runtime2`，兼容原双 VM 部署。导航、Debug 选择和 view 校验均
来自配置列表，额外节点必须配置各自 API/admin/proxy/web 地址与 key。

## 验证

```bash
make -C dev/control test
# 对单节点 Control 验证文件、命令和完整周期 S3 备份，创建并删除一个测试实例。
# 通过 SSH 转发的 localhost URL 运行；备份保留。无需 Runtime/S3 key。
CONTROL_E2E=1 CONTROL_SINGLE_HOST_E2E=1 \
  go -C dev/control test ./test -run '^TestSingleHostEndToEnd$' -v -count=1
```

脚本不启动上述深度验证，以避免普通状态检查创建实例。恢复、机器重启恢复和
跨节点故障切换不在单宿主冒烟测试范围内。
