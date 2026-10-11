# 三分钟沙箱流程实测

2026-10-10 在 runtime1 执行 TestSandboxFlowLive，通过。沙箱 `i17pwcxnepw52fsbvwrk4` 创建、写文件后等待 180 秒；存在自动备份后执行最终备份并休眠，确认 paused 状态，下载两份归档及其父层进行 SHA-256、大小和逐文件 manifest 校验，再 connect 唤醒并逐字比较文件内容，最后删除沙箱并确认查询返回 404。

| 备份 | S3 对象 key | 保存时间（UTC） | 大小（字节） |
|---|---|---|---:|
| 自动备份 | `backups/runtime1/i17pwcxnepw52fsbvwrk4/filesystem/20261010T082919.157654441Z-bbc56877-523a-4f52-b6f9-80952b3933b4.tar.gz` | 2026-10-10 08:29:19.167 | 49,126 |
| 休眠前最终备份 | `backups/runtime1/i17pwcxnepw52fsbvwrk4/filesystem/20261010T083055.897395106Z-8cb60557-7ac0-44d6-9e19-cdc100c599a2.tar.gz` | 2026-10-10 08:30:55.905 | 49,446 |

最终备份 + 休眠入口耗时 405.90 ms；connect 恢复 119.75 ms；整个测试 198.02 秒（包含 180 秒等待及归档/父层下载校验）。共享基础层已存在，以上大小仅为每次增量归档。S3 对象保留供查看，测试沙箱已删除；原有沙箱及 runtime2 未修改。

周期任务使用全局两分钟时钟；本次自动备份出现在创建后约 84 秒，不保证每次都在创建后的第 120 秒。三分钟流程可能产生一或两份自动备份，再加最终备份。原始 JSON：`out/flow-result.json`。

在项目根目录复现：`./dev/backup/scripts/flow.sh`。流程逻辑见 `../src/flow_test.go`，依赖环境变量集中在 `flowConfigInit` 和 `configInit`；完整说明见 [README](README.md)。
