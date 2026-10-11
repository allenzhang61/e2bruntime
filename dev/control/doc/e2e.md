# 统一视图端到端测试

测试位于 `dev/control/test`，通过统一视图按钮调用的真实 Control HTTP 接口，覆盖 Control → Runtime 1/2 → 沙箱 → RustFS 的链路。它不是浏览器 DOM 自动化；现有 `src/*_test.go` 单元测试继续保留。

```bash
cd dev/control
GOWORK=off GOTOOLCHAIN=local go test -short ./...
CONTROL_E2E=1 GOWORK=off GOTOOLCHAIN=local go test -v -count=1 -timeout 15m ./test
```

需要运行中的 Control、两个 Runtime API 和两个定时备份服务。测试无需读取或传输 Runtime API key、RustFS AK/SK。不要与其他切换/恢复测试并发运行，Control 同时只处理一项恢复。

| 环境变量 | 默认值 / 示例 | 用途 |
| --- | --- | --- |
| CONTROL_E2E | `1`（默认不运行） | 启用真实环境测试 |
| CONTROL_E2E_URL | `http://localhost:13021` | 本机 Control 地址；也可在 211 上运行 |
| CONTROL_E2E_TIMEOUT | `12m` | 整个流程超时 |
| CONTROL_E2E_BACKUP_WAIT | `4m` | 每次等待周期备份的上限；备份间隔较长时须增加 |
| CONTROL_E2E_POLL | `3s` | 状态和任务查询间隔 |
| CONTROL_E2E_OUTPUT | `out/<UTC时间>-<PID>` | JSON 结果目录 |

环境变量集中在 `test/config_test.go` 的 `configInit`；示例见 `test/config.env.example`。运行通常需要约 4–7 分钟，取决于备份周期和镜像恢复耗时。

覆盖流程：

1. Runtime 1 创建沙箱，确认节点无关的 `workspace-xxxxxx` Logic ID，写入并读取唯一文件，启动进程标记。
2. 设置/取消独立休眠和删除计划，验证取消一项不会影响另一项；等待定时休眠，再唤醒，确认内存/进程和 boot ID 保留。
3. 等待写文件后的周期备份，确认 tar.gz、独立 Metadata 文件、大小和 Logic ID 目录；访问备份和关联实例诊断页面。
4. 切到 Runtime 2，确认当前实例为空、原实例仍可观测；恢复后确认新实例绑定、文件保留、冷启动且旧进程不存在。重复恢复应被拒绝。
5. Runtime 2 手动休眠/唤醒；等待它自己的备份，删除实例，再从最新同机备份恢复，覆盖“删除后恢复返回 404”的回归场景。
6. 设置自动删除并等待实际删除，再切回 Runtime 1，确认重新绑定原实例。

测试不会改两台 Runtime 的备份配置，不会操作现有用户 Logic ID。退出时清理本次创建的物理实例；专属测试 Logic ID 的 S3 备份、归属和恢复历史保留，方便分析失败。结果写入 `unified-view.json`，包括 Logic ID、测试实例和各步骤端到端耗时（等待阶段包含轮询时间）。

WebShell 会由 Dashboard 的独立终端页面处理，本套不打开它，避免终端连接干扰休眠断言。

## 最近一次实测

2026-10-11 在 211 开发环境运行完整流程，全部 12 个阶段通过，总耗时 248.21 秒（含两次等待周期备份）。测试实例均已删除，现有用户实例未改动。结果位于 `test/out/validated-20261011/unified-view.json`（本地文件，未加入版本控制）。测试 Logic ID：`workspace-4j5vun`，对应 S3 备份保留供问题分析。
