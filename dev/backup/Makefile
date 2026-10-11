# 命令索引：make help。也可在仓库根目录执行 make -C dev/backup <命令>。
.DEFAULT_GOAL := help
# 使用 Makefile 自身目录定位脚本，不依赖调用者的工作目录。
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
export GOWORK := off
export GOTOOLCHAIN := local

.PHONY: help deploy status logs stop flow restore test vet build
help: ## 展示命令及用途（默认，不执行部署）
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' "$(ROOT)/Makefile"

# scripts/install-{host,guest}.sh、flow-{host,guest}.sh、restore-{host,guest}.sh
# 是入口脚本调用的远端辅助脚本；scripts/run.sh 在 VM 中加载凭据并启动服务。
# 无需在 Mac 单独运行这些辅助脚本。

deploy: ## 编译并部署两台 Runtime 的备份服务（默认创建演示沙箱）
	bash "$(ROOT)/scripts/deploy.sh" up

status: ## 查看两台 Runtime 的备份服务状态
	bash "$(ROOT)/scripts/deploy.sh" status

logs: ## 查看两台 Runtime 的备份日志
	bash "$(ROOT)/scripts/deploy.sh" logs

stop: ## 停止并禁用两台 Runtime 的备份服务
	bash "$(ROOT)/scripts/deploy.sh" stop

flow: ## 运行创建、等待备份、休眠、唤醒和删除的真实验证
	bash "$(ROOT)/scripts/flow.sh"

restore: ## 在 Runtime 2 安装构建产物并验证 S3 恢复（会修改目标环境）
	bash "$(ROOT)/scripts/restore.sh"

test: ## 运行本地测试及 race 检查，跳过真实环境测试
	cd "$(ROOT)" && go test -short -race ./...

vet: ## 运行 Go 静态检查
	cd "$(ROOT)" && go vet ./...

build: ## 构建备份服务到 /tmp/e2b-backup
	cd "$(ROOT)" && go build -o /tmp/e2b-backup ./src

