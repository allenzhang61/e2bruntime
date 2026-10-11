# 命令索引：make help。也可在仓库根目录执行 make -C dev/control <命令>。
.DEFAULT_GOAL := help
# 使用 Makefile 自身目录定位脚本，不依赖调用者的工作目录。
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
export GOWORK := off
export GOTOOLCHAIN := local

.PHONY: help deploy web-deploy connect test e2e debug-test vet build
help: ## 展示命令及用途（默认，不执行部署）
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' "$(ROOT)/Makefile"

# scripts/install-host.sh、web-host.sh、web-guest.sh 是部署入口自动调用的远端辅助脚本。
# RUNTIME_SSH_HOST 可覆盖默认 SSH 目标；端到端配置参见 doc/e2e.md。

deploy: ## 编译并部署 211 上的 Control Docker 服务
	bash "$(ROOT)/scripts/deploy.sh"

web-deploy: ## 部署两台 Runtime 的 Web Admin 和节点备份管理 API
	bash "$(ROOT)/scripts/web-deploy.sh"

connect: ## 建立 Mac 到 Control、Web Admin 和沙箱代理的 SSH 转发
	bash "$(ROOT)/scripts/connect.sh"

test: ## 运行本地测试及 race 检查，跳过真实环境测试
	cd "$(ROOT)" && go test -short -race ./...

e2e: ## 运行统一视图端到端测试（创建/删除测试实例并保留备份记录）
	cd "$(ROOT)" && CONTROL_E2E=1 go test ./test -run '^TestUnifiedViewEndToEnd$$' -v -count=1 -timeout=15m

debug-test: ## 运行两个 Runtime 的 Debug 端到端测试
	cd "$(ROOT)" && CONTROL_DEBUG_LIVE=1 go test ./src -run '^TestDebugLive$$' -v -count=1

vet: ## 运行 Go 静态检查
	cd "$(ROOT)" && go vet ./...

build: ## 构建 Control 服务到 /tmp/e2b-logic-control
	cd "$(ROOT)" && go build -o /tmp/e2b-logic-control ./src

