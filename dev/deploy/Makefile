# 命令索引：make help。也可在仓库根目录执行 make -C dev/deploy <命令>。
.DEFAULT_GOAL := help
# 使用 Makefile 自身目录定位脚本，不依赖调用者的工作目录。
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
export GOWORK := off
export GOTOOLCHAIN := local

.PHONY: help vm-up vm-provision vm-status vm-halt runtime-up verify smoke build install rollback s3-up s3-status s3-verify s3-down reset-all-data
help: ## 展示命令及用途（默认，不执行部署）
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' "$(ROOT)/Makefile"

.PHONY: single-build single-deploy single-control-deploy
single-build: ## 在 Linux 宿主 Docker 中构建单节点服务，需要 E2BRUNTIME_CONFIG
	bash "$(ROOT)/single-host/deploy.sh" build

single-deploy: ## 在 Linux 宿主部署一个 Runtime、Control 和备份服务
	bash "$(ROOT)/single-host/deploy.sh" deploy

single-control-deploy: ## 仅构建更新 Control 与 Web Admin，不重启 Runtime
	bash "$(ROOT)/single-host/deploy.sh" control-deploy

# two-vm/{provision,compile,install,install-vm,runtime}.sh 是入口脚本自动传输/调用的辅助脚本。
# RUNTIME_SSH_HOST 可覆盖默认 SSH 目标。reset-all-data 和 s3-down 会删除数据。

vm-up: ## 创建/启动两台 Vagrant VM 并准备 Runtime 环境
	bash "$(ROOT)/two-vm/deploy.sh" up

vm-provision: ## 重新执行两台 VM 的配置脚本
	bash "$(ROOT)/two-vm/deploy.sh" provision

vm-status: ## 查看两台 VM 的状态
	bash "$(ROOT)/two-vm/deploy.sh" status

vm-halt: ## 关闭两台 VM
	bash "$(ROOT)/two-vm/deploy.sh" halt

runtime-up: ## 启动两台 VM 的 Runtime 服务
	bash "$(ROOT)/two-vm/deploy.sh" runtime-up

verify: ## 检查两台 Runtime 的基础环境与服务
	bash "$(ROOT)/two-vm/deploy.sh" verify

smoke: ## 运行两台 Runtime 的沙箱创建/删除冒烟检查
	bash "$(ROOT)/two-vm/deploy.sh" smoke

build: ## 在 211 Docker 中编译当前仓库的 Runtime
	bash "$(ROOT)/two-vm/build.sh" build

install: ## 将编译产物安装到两台 Runtime VM
	bash "$(ROOT)/two-vm/build.sh" install

rollback: ## 停用自编译覆盖配置，回到原 Runtime 镜像
	bash "$(ROOT)/two-vm/build.sh" rollback

s3-up: ## 启动临时 RustFS 并创建/验证 S3 Bucket
	bash "$(ROOT)/rustfs/deploy.sh" up

s3-status: ## 查看 RustFS Docker 服务状态
	bash "$(ROOT)/rustfs/deploy.sh" status

s3-verify: ## 验证 S3 Bucket 及上传下载能力
	bash "$(ROOT)/rustfs/deploy.sh" verify

s3-down: ## 停止 RustFS（tmpfs 中所有备份数据会丢失）
	bash "$(ROOT)/rustfs/deploy.sh" down

reset-all-data: ## 删除两台 VM、S3 备份和 Control 数据，并从零重建
	bash "$(ROOT)/reset.sh" --all-data
