# 双 VM Runtime 开发环境

`dev/` 用于本项目的二次开发，部署定义和脚本集中放在 `dev/deploy/`。本地目录调整不改变已运行 VM 的远端工作目录。

在 Linux 目标机上用 Vagrant + libvirt/KVM 创建两台独立 Ubuntu VM，每台部署一套官方 Embed Compose（Runtime HTTP API、orchestrator、client-proxy、数据库及本地存储）。这是高可用开发的基础环境；目前两套数据库、快照存储、API key 和沙箱路由分别独立，尚未实现故障接管。

| VM | libvirt domain | CPU / RAM | 磁盘 | 目标机回环端口 |
|---|---|---|---|---|
| runtime1 | e2bruntime-haruntime1 | 4 vCPU / 10 GiB | 64 GiB，稀疏磁盘 | API 13000，沙箱代理 13002 |
| runtime2 | e2bruntime-haruntime2 | 4 vCPU / 10 GiB | 64 GiB，稀疏磁盘 | API 23000，沙箱代理 23002 |

Box 固定为 `bento/ubuntu-24.04` 的 `202508.03.0` libvirt 版本。启用 host-passthrough 和嵌套虚拟化，使 VM 内的 Firecracker 可以访问 `/dev/kvm`。无共享目录，不导入旧 VM 的磁盘或快照。每台预留 4 GiB hugepages；10 GiB 配置适合少量沙箱验证，官方推荐生产评估时至少 12 GiB。

## 部署

目标机先安装 Vagrant、vagrant-libvirt、libvirt/KVM，当前默认目标为 `zyl@192.168.1.211`，要求 SSH 可用及 guest provisioning 的 sudo 权限。目标机必须开启 AMD SVM / Intel VT-x，且有至少 20 GiB 可用内存、足够实际磁盘空间（建议至少 40 GiB 空闲），并能访问 Docker、Google artifact/storage 和 GitHub 的 HTTPS 下载地址。

在本项目根目录执行：

```bash
./dev/deploy/two-vm/deploy.sh up
./dev/deploy/two-vm/deploy.sh verify
./dev/deploy/two-vm/deploy.sh smoke
./dev/deploy/two-vm/deploy.sh status
```

`up` 只上传 Vagrant 定义、部署脚本及仓库 `embed/compose/compose.yaml` / `.env` 的公开版本配置，不上传项目私有凭据。远端工作目录为 `/home/zyl/e2bruntime/deploy/two-vm`；Vagrant 状态保存在其 `.vagrant/` 中。环境变量 `RUNTIME_SSH_HOST` / `RUNTIME_REMOTE_DIR` 可覆盖目标；对已有环境不要随意更改路径。

首次运行创建全新 VM；后续 `up` 保留现有 VM 与数据；更改项目部署脚本或官方版本配置后，运行 `./dev/deploy/two-vm/deploy.sh provision` 重新上传并应用配置，保留数据库和存储。旧测试环境 `/home/zyl/agent-sandbox-research/deploy/two-vm` 的 `vm1` / `vm2` 不受影响。

每台 VM 内部署在 `/opt/e2b-runtime`。Docker 开机自启，Compose 常驻服务使用原定义的重启策略。VM 重启后如需重新执行初始化/ready 阶段，运行 `./dev/deploy/two-vm/deploy.sh runtime-up`。

本环境使用仓库官方配置固定的已发布容器/二进制版本，不会自动编译当前开发分支。未来修改 Runtime 源码后，需要构建并更新对应镜像/二进制版本，才能在 VM 中验证代码改动。

## 访问 HTTP API

端口通过 Vagrant/libvirt 的 SSH 转发绑定到目标机回环地址，避免直接暴露到局域网。Mac 再开 SSH 隧道（保持该终端运行）：

```bash
ssh -N -o ExitOnForwardFailure=yes \
  -L 13000:127.0.0.1:13000 -L 13002:127.0.0.1:13002 \
  -L 23000:127.0.0.1:23000 -L 23002:127.0.0.1:23002 \
  zyl@192.168.1.211
```

然后访问：

- VM 1 API：`http://127.0.0.1:13000`，健康检查 `/health`。
- VM 2 API：`http://127.0.0.1:23000`，健康检查 `/health`。
- SDK 分别设置 `E2B_API_URL` 为对应 API，`E2B_SANDBOX_URL` 为 `http://127.0.0.1:13002` / `http://127.0.0.1:23002`，并使用对应 VM 的 key。

每台自动生成的 SDK key 存在 guest 的 `/opt/e2b-runtime/sdk.env`，仅 root 可读。部署输出不显示 key；需要时登录对应 VM 后用 sudo 查看。不要提交该文件或完整 Compose 日志，`ready` 容器日志也包含 key。

```bash
ssh zyl@192.168.1.211
cd /home/zyl/e2bruntime/deploy/two-vm
vagrant ssh runtime1
sudo cat /opt/e2b-runtime/sdk.env
```

以上文件中的 localhost 端口是 guest 本地端口，Mac SDK 使用上面的隧道端口覆盖 URL。Dashboard 服务仍在 guest 的 3001；此定义只转发 API 和沙箱代理。

## 管理

```bash
./dev/deploy/two-vm/deploy.sh halt       # 停机，保留磁盘
./dev/deploy/two-vm/deploy.sh up         # 启动已有 VM
./dev/deploy/two-vm/deploy.sh runtime-up # 重新协调两套 Runtime
```

如需彻底重建干净 VM，先在目标机新工作目录执行 `vagrant destroy runtime1 runtime2`（删除这两台的所有数据），再从本地执行 `up`。不要在旧基准测试目录执行删除命令。

失败时 VM 内查看 root-only `/var/log/e2b-runtime-provision.log`、`/var/log/e2b-runtime-up.log` 或 `/var/log/e2b-runtime-smoke.log`。当前官方发布 API 镜像的 `/openapi.json` 返回 404，接口定义可查看仓库 `spec/openapi.yml`。`verify` 检查 API、orchestrator 与 client-proxy；`smoke` 使用官方容器里的 JS SDK 验证创建沙箱、运行命令、代理访问与销毁。

## 本次验证（2026-10-10）

目标机 `zyl@192.168.1.211` 已创建并保持运行 `runtime1`、`runtime2`。两台均通过 `/dev/kvm`、API/orchestrator/client-proxy 健康检查，以及官方 JS SDK smoke：创建 Firecracker 沙箱、运行命令、访问沙箱 HTTP 端口并销毁。目标机 `13000` / `23000` 的 API `/health` 均返回 200。

Vagrant 2.4.9、vagrant-libvirt 0.12.2；guest Docker Engine 29.9.0、Compose 5.6.0。旧 `two-vm_vm1` / `two-vm_vm2` 保留且仍关闭。两台新 VM 部署后宿主根磁盘剩余约 43 GiB，后续构建大量模板/快照前需留意容量。

Go 的创建/写文件/读回校验/删除测试见 [dev/test](../../test/README.md)，可在 Mac 上自动建立 SSH 隧道访问这两套 Runtime。

## 部署此 fork 的文件系统快照能力

```bash
./dev/deploy/two-vm/build.sh build
./dev/deploy/two-vm/build.sh install
cd dev/test
GOWORK=off go test -v -count=1 -timeout=5m
```

`build` 将源码（不含 `.env` 凭据文件）传到 211，在受 CPU/内存限制的 Go 1.26.8 Bookworm 容器内原生编译 API、db-migrator 和 orchestrator，并执行相关开关/节点单元测试。源码及产物位于 `/home/zyl/e2bruntime/dev/build`；Go 构建缓存保存在专用 Docker volumes，避免每次重新下载。

`install` 只处理 `runtime1`、`runtime2`。先将数据库备份到 guest root-only `/opt/e2b-runtime/backups/pre-source-<UTC时间>.sql`，运行同源迁移，再用 `/opt/e2b-runtime/compose.dev.yaml` 挂载 fork 二进制和 launcher；公开镜像、基础模板和数据卷保留。替换 orchestrator 会终止当时运行中的沙箱，应在开发测试之间部署。两服务都设置 `FILESYSTEM_ONLY_CHECKPOINT=true`，默认部署入口仍为官方 pinned artifacts。

`./dev/deploy/two-vm/build.sh rollback` 禁用 override、切回官方服务；新增数据库迁移不会自动回退，备份文件包含敏感数据，不要提交或输出到日志。

`FILESYSTEM_ONLY_CHECKPOINT` 只启用 `memory:false` 文件系统快照：源 VM 继续运行，从快照创建的 VM 冷启动。它不实现周期调度、跨节点导入或 RustFS 上传。

源码安装和文件系统快照验证已在两台 VM 完成（2026-10-10）。完整 Go E2E 测试通过，包含源沙箱持续运行、文件快照隔离和恢复冷启动断言；单次耗时见 [测试结果](../../test/README.md)。容器列表仍显示固定基础镜像标签，实际 API/orchestrator 使用 override 中挂载的 fork 二进制。

## 全量重建

在仓库根目录执行 `bash dev/deploy/reset.sh --all-data`。该命令删除当前 runtime1/runtime2 虚拟机、RustFS 中的全部对象和统一管理服务的操作记录，并从当前工作区源码重新构建、部署。重新生成 Runtime API key；RustFS AK/SK 配置保留。重建后两个 Runtime 无沙箱实例，备份间隔为 2 分钟、保留 10 个，两个节点均启用备份。

旧基准 VM 和宿主上的其他项目不在清理范围。操作不可逆，执行前应确认不再需要当前测试数据。
