# 从零搭建开发环境

本指南使用当前仓库 `dev/` 下的脚本，在一台 Linux 宿主上创建两台 Runtime VM，并部署 RustFS、定时备份、Runtime Web Admin 和统一 Control 服务。所有 `make -C dev/...` 命令均在本地仓库根目录执行；标注“宿主”或“VM”的命令在对应机器执行。

这是功能开发与恢复验证环境：两个 Runtime 和 RustFS 在同一物理机上；恢复是从文件系统备份冷启动，不恢复进程和内存，也不自动故障接管。

## 1. 目录与服务分工

| 目录 | 用途 | 命令索引 |
|---|---|---|
| `dev/deploy` | 创建 VM、编译安装 Runtime、启动 RustFS | `make -C dev/deploy help` |
| `dev/backup` | 两台 VM 的定时备份、S3 导出与节点恢复 API | `make -C dev/backup help` |
| `dev/control` | 统一 Gin 控制服务、Runtime Web Admin 部署与端到端测试 | `make -C dev/control help` |

本地执行部署脚本；编译 Runtime 和构建 Dashboard 镜像在 Linux 宿主 Docker 中完成。备份 Go 程序和 Control Go 程序在本地交叉编译。宿主不需要手工克隆整个仓库，脚本会上传必要文件。

## 2. 准备本地与宿主

本地需要 Git、Make、Bash、SSH/SCP、tar、curl、OpenSSL，以及 **Go 1.25 或更新版本**。`backup`、`control` 使用独立 Go module，Makefile 设置 `GOWORK=off`、`GOTOOLCHAIN=local`；运行前确认 `go version` 满足要求。Mac 使用系统 OpenSSH 和支持 `--no-xattrs` 的 tar。

宿主建议使用 x86_64 Ubuntu 24.04，SSH 用户有无交互 sudo 权限。准备 Docker Engine、Docker Compose 插件、Vagrant、vagrant-libvirt、libvirt/KVM；配置该用户可访问 libvirt。基础检查在宿主执行：

```bash
sudo -n true
docker --version
sudo -n docker compose version
vagrant --version
vagrant plugin list
virsh -c qemu:///system list --all
ls -l /dev/kvm
```

若未安装 vagrant-libvirt，在宿主安装对应发行版的 libvirt 开发依赖后执行 `vagrant plugin install vagrant-libvirt`。Docker 在宿主用于构建和运行服务，VM 内的 Docker 由 provisioning 自动安装。

BIOS/UEFI 必须开启 AMD SVM 或 Intel VT-x，并让 KVM 向 guest 暴露嵌套虚拟化。宿主若本身是 VM，还需上层宿主允许嵌套。AMD/Intel 对应检查：

```bash
cat /sys/module/kvm_amd/parameters/nested   # AMD
# 或：cat /sys/module/kvm_intel/parameters/nested
```

两台 VM 各分配 4 vCPU、10 GiB RAM、64 GiB 稀疏磁盘；另需容纳 Runtime 编译（4 GiB 上限）、Dashboard 构建（6 GiB 上限）和 RustFS tmpfs（4 GiB 上限）。建议宿主至少 32 GiB RAM，并预留至少 60 GiB 可用磁盘；镜像、编译缓存和快照增长时需要更多空间。宿主及 guest 应能访问 Ubuntu/Docker 软件源、GitHub 和 Runtime artifact 下载地址；本地应能访问宿主 SSH 和 S3 endpoint。

## 3. 新机器必须调整的配置

当前脚本默认部署到 `zyl@192.168.1.211`，并使用 `/home/zyl/e2bruntime`。**这些脚本尚未完全参数化；只改 `RUNTIME_SSH_HOST` 不足以适配不同用户和目录。**

| 项目 | 当前默认 | 新环境操作 |
|---|---|---|
| SSH 目标 | `zyl@192.168.1.211` | 本地 `export RUNTIME_SSH_HOST=user@host` |
| Vagrant 工作目录 | `/home/zyl/e2bruntime/deploy/two-vm` | 入口支持 `RUNTIME_REMOTE_DIR`；其他脚本中的固定路径也要同步调整 |
| Runtime 构建目录 | `/home/zyl/e2bruntime/dev/build` | `two-vm/build.sh` 支持 `RUNTIME_BUILD_DIR` |
| RustFS 部署目录 | `/home/zyl/e2bruntime/dev/deploy/rustfs` | RustFS 入口支持 `RUSTFS_REMOTE_DIR`；Web Admin/Control 读取的路径也要调整 |
| 备份读取 S3 配置 | 宿主 RustFS 目录的 `.env` | `backup/scripts/install-host.sh` 支持 `RUSTFS_ENV_FILE` |
| RustFS 绑定地址与 endpoint | `192.168.1.211:19000` | 提前创建本地 `dev/deploy/rustfs/.env`，修改绑定 IP 和 endpoint |
| Control 及辅助工作目录 | `/home/zyl/e2bruntime/dev/control` 等 | 修改 `control/scripts`、`backup/scripts` 中对应固定路径 |
| SSH 私有隧道用户 | `User=zyl` | 修改 `control/scripts/install-host.sh` 生成的 systemd 用户 |

在仓库根目录定位所有需要适配的位置：

```bash
rg -n '/home/zyl|192\.168\.1\.211|User=zyl' dev --glob '*.sh' --glob '*.example'
export RUNTIME_SSH_HOST=zyl@192.168.1.211  # 改成实际 SSH 目标
ssh "$RUNTIME_SSH_HOST" 'sudo -n true'
```

如果继续使用当前用户和宿主，默认路径无需调整。若改变远端目录，必须让所有部署入口、安装脚本和连接脚本使用一致路径；不要仅修改部分入口变量。

不同宿主的 RustFS 配置可提前准备：

```bash
cp dev/deploy/rustfs/.env.example dev/deploy/rustfs/.env
chmod 600 dev/deploy/rustfs/.env
# 编辑实际绑定 IP、可被本地及两台 VM 访问的 endpoint，以及 AK/SK。
# AK/SK 示例值必须替换；不要提交 .env。
```

使用原默认地址且不存在 `.env` 时，`s3-up` 会自动生成 AK/SK；已有 `.env` 则复用。Runtime 的 API key 在 VM 首次初始化时生成，不需要申请 E2B 云平台的 key。

## 4. 按顺序部署

### 4.1 创建两台 VM

```bash
make -C dev/deploy vm-up
make -C dev/deploy vm-status
make -C dev/deploy verify
```

使用 `bento/ubuntu-24.04` 的固定 libvirt box 版本，首次自动安装 VM Docker 并启动基础 Runtime。VM 内 `/dev/kvm` 必须可用。已有 VM 的 `vm-up` 通常只启动 VM；如果之前删除过 Runtime 容器和数据，需要额外执行 `make -C dev/deploy runtime-up` 重新初始化。

远端 Vagrant 状态位于工作目录的 `.vagrant/`；VM 内安装目录为 `/opt/e2b-runtime`。不要在另一个目录重复创建同名 VM。

### 4.2 编译并安装当前 fork

```bash
make -C dev/deploy build
make -C dev/deploy install
make -C dev/deploy verify
make -C dev/deploy smoke
```

`build` 在宿主 Go 1.26.8 Docker 镜像中编译 API、orchestrator、migrator。`install` 应用同源数据库迁移并通过 `compose.dev.yaml` 挂载自编译产物，启用 `FILESYSTEM_ONLY_CHECKPOINT=true`。这一步不能省略：仅创建 VM 使用的是基础发布版本，不会自动运行本地源码。

安装会替换 orchestrator，可能终止已有运行实例，应在开始业务测试前完成。`smoke` 验证创建、执行命令、代理访问和删除自己的测试沙箱。

### 4.3 启动 RustFS

```bash
make -C dev/deploy s3-up
make -C dev/deploy s3-status
make -C dev/deploy s3-verify
```

自动创建默认 bucket `runtime-snapshots`，使用 region `us-east-1` 和 path-style S3。验证会上传/下载一个临时对象，检查大小、内容和 SHA-256，随后删除测试对象。

当前 RustFS 的数据和日志使用 tmpfs；**停止、删除容器或宿主重启都会丢失全部 S3 数据**。需要保留真实备份时，应先将这部分改成持久存储或独立 S3，不能把本部署当成生产容灾存储。

### 4.4 部署两台备份服务

```bash
BACKUP_SEED_DEMO=0 make -C dev/backup deploy
make -C dev/backup status
```

`BACKUP_SEED_DEMO=0` 避免自动创建演示沙箱。两台 worker 启动，默认周期 2 分钟、每个物理实例最多保留 10 份归档；空环境没有对象是正常情况。运行中的实例且具有备份归属权限才参与备份，休眠实例跳过。

脚本将 S3 凭据配置到各 VM 的 root-only 文件，部署代码在 `dev/backup/src`。归档及独立 Metadata 路径为 `backups/<Logic ID>/<Runtime>/<Instance ID>/filesystem/`；恢复依赖层位于 `backups/_shared/<Runtime>/layers/`，不按实例保留数量直接删除。

### 4.5 部署 Runtime Web Admin

```bash
make -C dev/control web-deploy
```

构建固定版本官方 Dashboard 并应用 `assets/web/dashboard.patch`，安装到两台 VM；同时启动 `e2b-backup-admin.service`，提供受认证的节点备份管理 API（VM 回环 3012）。补丁、脚本及说明已合入 Control，不再有 `dev/admin` 目录。

Dashboard 从 VM 的 `sdk.env` 加载服务端 key。此时不需要把 key 手工复制到 Mac。

### 4.6 部署 Control 并建立本地访问

```bash
make -C dev/control deploy
make -C dev/control connect
curl -fsS http://localhost:13021/health
```

Control 部署脚本从两台 VM 收集 API key，并将其与 RustFS 配置保存到宿主 root-only `/opt/e2b-control/control.env`。Control 使用宿主 Docker daemon，恢复记录及独立实例计划存于 `e2b-control-data` 数据卷。它不会部署到 Runtime VM 内。

| 本地地址 | 用途 |
|---|---|
| `http://localhost:13021/` | 统一视图，Logic ID、归属切换、恢复和实例操作 |
| `http://localhost:13021/backups` | 两台备份 worker 状态、启停配置、间隔和保留数量 |
| `http://localhost:13021/debug` | 创建实例、命令/文件操作、休眠与唤醒等调试 |
| `http://127.0.0.1:13001/sandboxes` | Runtime 1 Web Admin |
| `http://localhost:13011/sandboxes` | Runtime 2 Web Admin |

`connect` 建立 Mac SSH 后台转发；宿主 systemd 隧道负责访问 VM 回环 API/Web Admin。页面无法打开时先确认 VM 和服务正常，再重新执行 `connect`。重建 VM 后地址可能改变，需先重新部署 Control，使隧道配置更新。

源码安装、备份安装和 Web Admin 部署共享宿主 `/home/zyl/e2bruntime/deploy/.runtime-deploy.lock`。不要并行运行这些部署命令；冲突时脚本会在向 VM 上传文件前退出，提示等待当前部署结束后重试。`vm-up`、`vm-provision`、`runtime-up`、`rollback` 也应顺序执行，它们不在这把安装锁的保护范围内。

## 5. 验证整条链路

先在 `/backups` 确认两台定时备份配置为开启，worker 均可访问并正常调度；在 Debug 创建一个实例，写入文件，等待一个备份周期，在统一视图查看归档和独立 Metadata。

自动验证命令：

```bash
make -C dev/backup test
make -C dev/control test
make -C dev/control e2e
```

`e2e` 使用真实 HTTP 服务：创建实例、写入并读回文件、执行独立自动休眠/删除计划、休眠/唤醒、等待 S3 备份、切换归属、跨节点恢复、删除后再次恢复，以及重复恢复拦截。测试只清理自己创建的物理实例，其独立 Logic ID 的备份和操作历史保留便于分析；运行后不是完全空环境。

运行参数、输出报告位置见 [Control 端到端测试](control/doc/e2e.md)。`make -C dev/control debug-test` 是另一套 Debug API 实测；`make -C dev/backup flow` 是带最终文件系统备份的休眠流程验证；`restore` 会安装并测试 Runtime 2，仅在了解其目标环境操作后单独运行。

## 6. 日常修改、故障排查与重建

| 改动或需求 | 命令 |
|---|---|
| 修改 Runtime 源码 | `make -C dev/deploy build`，然后 `make -C dev/deploy install` |
| 修改备份代码 | `BACKUP_SEED_DEMO=0 make -C dev/backup deploy` |
| 修改 Control 页面或 API | `make -C dev/control deploy` |
| 修改 Dashboard 补丁 | `make -C dev/control web-deploy` |
| 修改 VM provisioning | `make -C dev/deploy vm-provision` |
| 查看备份日志 | `make -C dev/backup logs` |
| 停止并禁用周期备份服务 | `make -C dev/backup stop` |
| VM 关机但保留磁盘 | `make -C dev/deploy vm-halt` |
| 删除临时 RustFS 和所有备份 | `make -C dev/deploy s3-down` |

备份状态页的“关闭”只关闭周期调度，保留 worker 进程；不会关闭 RustFS，也不会删除已有备份。重新开启调度不负责启动一个已经被 systemd 停止的服务；可重新部署备份服务启动它。

故障检查位置：

- VM provisioning、Runtime 启动与源码安装日志：VM 内 `/var/log/e2b-runtime-*.log`。
- 周期备份与节点管理 API：VM 内 `sudo journalctl -u e2b-backup.service -u e2b-backup-admin.service`。
- Control：宿主 `sudo docker logs e2b-logic-control`。
- 私有隧道：宿主 `sudo systemctl status e2b-control-runtime1.service e2b-control-runtime2.service`。
- S3：`make -C dev/deploy s3-status`、`s3-verify`；必要时检查 VM 到 endpoint 的网络连通性。

日志和 VM 的 `/opt/e2b-runtime/backups/` 数据库备份可能包含凭据或用户数据，不要提交到仓库。

已有整套环境需要从零重建时才使用：

```bash
make -C dev/deploy reset-all-data
```

该命令会删除两台 VM、全部 S3 备份和 Control 数据卷，然后按上述顺序重新部署。不适合作为新机器的首次安装入口：它假定旧 VM、容器、数据卷和 systemd 服务已存在。新环境按第 4 节逐步安装。

更多说明：[VM 与 Runtime](deploy/two-vm/README.md)、[RustFS](deploy/rustfs/README.md)、[备份服务](backup/doc/README.md)、[Control](control/doc/README.md)、[Runtime Web Admin](control/doc/web-admin.md)。

## 7. 部署实测反馈（2026-10-11）

在 `zyl@192.168.1.211` 从无 VM 状态部署两台 Runtime，安装当前 fork、RustFS、两台备份 worker、Web Admin 和 Control。健康检查、两台 smoke、S3 1 MiB 对象上传/下载/SHA-256 校验、backup/control 单元测试与 race 检查、两台 Debug API 实测均通过。统一视图 E2E 全部 12 阶段通过，耗时 208.29 秒；测试实例清理后，两节点观测实例数为 0。Logic ID `workspace-803nax` 的备份和操作历史保留，结果文件为本地 `dev/control/test/out/deployment-validation-20261011/unified-view.json`。

这次遇到的问题及修复：

- 源码安装与 Web Admin 并行操作同一 Compose 项目，Dashboard 重建导致安装报 `No such container`。顺序重装后通过；安装脚本新增共享宿主锁，在 VM 文件上传前拒绝冲突部署，避免共享 `/tmp` 输入被覆盖。
- 构建源码包不含 `.git` 和 `.last_used_env`，原先调用包 Makefile 会输出缺文件和 `not a git repository`，但编译仍成功。开发构建现直接调用 Go，显式携带本地 HEAD commit，并从同源迁移目录提取 API schema 版本。commit 标识 HEAD；未提交的工作区改动仍会上传构建。
- 双 VM 文档仍引用已移除的 `dev/test`，已更新为 `dev/control` 下的 Debug 与统一视图测试入口。

Vagrant 的 `Unrecognized arguments: libvirt_ip_command` 是此次使用的 vagrant-libvirt/fog 警告；创建、SSH 和全部测试通过，本次没有为消除警告修改插件或 VM 网络。
