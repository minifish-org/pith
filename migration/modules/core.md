# core：完整稳定 Agent 内核与 harness

当前执行顺序：core-contracts → core-loop → core-resources → core-session → core-compaction → core-runtime → core-harness。源码/输出数量以 plan.json 为准。

包括固定版本 pi-agent-core 的稳定 harness，含会话、存储、压缩和恢复；experimental/pico3 明确排除。具体工具通过接口注入，core 可用假工具独立验收。

模块只在全部批次与最终验收通过后提交一次。下列批次是 Portsmith 内部执行顺序，不需要用户逐一输入命令。全部批次的执行材料已准备，尚未生成产品代码。

| 内部批次          | 范围                           | 直接前置        | 来源文件 |
| ----------------- | ------------------------------ | --------------- | -------- |
| `core-contracts`  | 完整 Agent 和 harness 共享契约 | 前置模块已接受  | 8        |
| `core-loop`       | 完整循环、队列、钩子与状态     | core-contracts  | 4        |
| `core-session`    | 会话、存储、fork 和兼容读取    | core-loop       | 25       |
| `core-compaction` | 压缩与分支摘要                 | core-session    | 3        |
| `core-runtime`    | 可恢复 harness 运行时          | core-compaction | 23       |
| `core-resources`  | 技能、模板、提示词、配置与搜索 | core-runtime    | 10       |
| `core-harness`    | 稳定 harness 组装与模块验收    | core-resources  | 2        |

## core-contracts / 完整 Agent 和 harness 共享契约

行为：

- agent/types、harness/session/runtime/compaction 的公共数据类型
- chord Context/JsonValue/JsonRepresentation 的 Go 适配；shared contracts 打破包环
- ExecutionEnv/FileSystem/Shell/Tool 接口归 core，不依赖具体工具

验收：

- 所有导出类型及 JSON roundtrip；context 生命周期与 race；Go 包图无环

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## core-loop / 完整循环、队列、钩子与状态

行为：

- sequential/parallel、steering/follow-up 队列模式
- prepareRequest/prepareNextTurn、before/afterToolCall、finishTurn
- 动态配置、agent subscriptions、stream fn/default injection、proxy

验收：

- 原 agent/agent-loop/proxy 测试所有场景；并发顺序、拒绝、替换、错误/取消、迟到 update、重入

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## core-session / 会话、存储、fork 和兼容读取

行为：

- memory 与 JSONL session/storage/repo；事务、分支和 fork
- mutation line、cursor/scan、usage、context、旧 v3 格式兼容
- conformance 和测试辅助保留；benchmark 数据单独归档

验收：

- 相同 conformance 同时跑 memory/JSONL；故障注入/重启/截断行/多写者；fork 不串改

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## core-compaction / 压缩与分支摘要

行为：

- token estimation、cut point、prepare/generate/commit
- branch summary、文件操作收集、错误/取消和 usage

验收：

- 边界切分、无丢失工具配对、固定模型摘要输入、失败不提交半成品

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## core-runtime / 可恢复 harness 运行时

行为：

- reducer/lane/progress/transcript/restore
- generation/tools/deferred/retry/reconcile/checkpoint/structural/terminal
- effect gate、执行原语与操作边界

验收：

- 逐 runtime TS 测试迁移；模型/工具执行前后 crash 矩阵；不能承诺外部副作用 exactly-once；未知结果显式保留

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## core-resources / 技能、模板、提示词、配置与搜索

行为：

- skills、prompt templates、system prompt、messages、config
- SessionSearchService 接口与 telemetry schema/hook/events

验收：

- ignore/frontmatter/YAML、资源顺序、重复/错误文件、提示词渲染、日志无密钥；不捏造搜索实现

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## core-harness / 稳定 harness 组装与模块验收

行为：

- AgentHarness/result 与 runtime 接口整合
- 使用注入的假工具即可验收，不依赖 tools 实现

验收：

- 独立 consumer 使用 agent+harness；checkpoint 恢复、分支、压缩、hooks；全部稳定 core 测试通过

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。
