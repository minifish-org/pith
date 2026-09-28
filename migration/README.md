# Pith 完整迁移计划：ai → core → tools

本计划将 **Pi v0.87.1 的 AI 层、稳定 Agent/harness 和四个内置工具**迁移为 Go，交付可嵌入 SDK 与可独立运行的 CLI。上游固定为 `f07218c4d4bbc12bef056a7058c3dd49dfe41abe`，不追着实时 main 生成代码。

**完整执行材料已准备，可以启动。** 全部24个批次、26个自动步骤都有来源、输出、契约与独立验收；正式 Go 实现尚未生成。直接看 [启动说明](start.md)。

## 你只需要理解三个模块

| 模块                      | 完整范围                                                                 | 源码规模¹            | 完成后的结果                       |
| ------------------------- | ------------------------------------------------------------------------ | -------------------- | ---------------------------------- |
| [ai](modules/ai.md)       | 消息、流、全部协议/provider、认证/OAuth、模型目录、图像、兼容入口        | 189 文件 / 26,076 行 | 可独立使用的 Go 模型接入层         |
| [core](modules/core.md)   | Agent 循环、并行工具/队列/钩子、proxy、稳定 harness、会话/恢复/压缩/技能 | 75 文件 / 22,744 行  | 可注入模型和工具的 Go Agent SDK    |
| [tools](modules/tools.md) | read/write/edit/bash、本地执行环境、SDK 完整入口和新增 CLI               | 16 文件 / 2,724 行   | 可实际完成文件和命令任务的 Go 程序 |

¹ 包含必要支持源码；这是上游物理行数，不是预计 Go 行数。根导出入口由 tools 最后组装；通用截断能力为避免反向依赖归入 core 契约批次。模块名称是交付归属，不等于上游 npm 包的物理边界。

三个模块内有 **24 个内部批次（11 / 7 / 6）**。批次用于限制上下文、保存进度和定位失败，不要求你逐个选任务。一个 provider 批次还可按 provider 文件组分段处理。每个模块全部通过后提交一次，不按每个文件提交。

## 完整到什么程度

包括 `packages/ai/src` 的稳定行为，以及 `packages/agent/src` 的稳定公开能力。这个版本的 `pi-agent-core` 已经把 harness、JSONL session、恢复、压缩、技能等纳入稳定导出，因此它们在 core 范围内，不能只移植四个文件就称为完整 core。

明确排除：上游 `experimental/pico3`、性能 benchmark 样本、Chord 插件/服务/打包系统（只适配必需 Context/JsonValue/JsonRepresentation），以及其他 Pi 包中的 TUI、Web、桌面、MCP、Computer Use。无 Node/Go 运行时的 binary 不等于零系统依赖：bash 工具仍需要本机 shell；网络模型需要用户配置凭据。

完整目标指上述稳定范围的行为迁移，**不是 TS API 的逐字复制，也不是整个 Pi monorepo 的产品克隆**。任何行为差异必须进入差异表和验收记录，不得以“Go 更惯用”为由静默删功能。

## 目录如何对应

```text
Pi                                        Pith
packages/ai/src/                     →     packages/ai/
packages/ai/src/api/                 →     packages/ai/api/
packages/ai/src/providers/           →     packages/ai/providers/
packages/ai/src/auth/                →     packages/ai/auth/
packages/agent/src/                  →     packages/agent/
packages/agent/src/harness/session/  →     packages/agent/harness/session/
packages/agent/src/harness/tools/    →     packages/agent/harness/tools/
packages/agent/src/harness/env/      →     packages/agent/harness/env/
新增运行入口                         →     cmd/pith/
```

只去掉构建用的 `src`，文件名改为 Go 可用形式。公共类型、barrel 导出和部分互引目录必须拆/合包，详见 [API 与包边界](API.md)。**实际逐文件对应以 [inventory.json](inventory.json) 为准**，不是靠路径猜测。它清点了 308 个来源文件：280 个进入迁移/适配，28 个有明确排除或仅参考理由。文件拆分、合并、删除都要保留记录。

## 从哪里看

| 文件                                                                    | 用途                                             |
| ----------------------------------------------------------------------- | ------------------------------------------------ |
| [plan.json](plan.json)                                                  | 三模块、内部批次、依赖、行为和验收要求           |
| [inventory.json](inventory.json) / [exports.json](exports.json)         | 每个来源文件、哈希、目标路径、导出符号、处理方式 |
| [API.md](API.md) / [dependency-map.md](dependency-map.md)               | Go 包边界、接口冻结规则、依赖替换                |
| [validation.md](validation.md)                                          | 独立测试、对照、模块与最终交付验收               |
| [workflow.json](workflow.json) / [execution.md](execution.md)           | Portsmith 执行协议及已准备的执行能力               |
| [continuous-port.md](continuous-port.md)                                | 后续版本怎样做增量迁移                           |
| [upstream.json](upstream.json) / [source-audit.json](source-audit.json) | 来源锁定、完整扫描及生成物缺口                   |
| [legacy/mvp-1/ARCHIVED.md](legacy/mvp-1/ARCHIVED.md)                    | 旧最小计划、48 个测试和对照证据存档              |

`analysis.json` 现在保留完整上游静态扫描，不再只是原来 35 个文件的投影。独立 judge 冻结来源符号与行为；具体 Go 签名在迁移中按规则选择并编译，不将静态清单当作 Go 实现。

## 现在可以运行的检查

在 Pith 根目录，使用旁边 Portsmith 已安装的 tsx：

```sh
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/check-plan.mts
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/check-plan.mts --self-test
```

它检查来源哈希、文件覆盖、映射、模块/批次依赖和计划一致性，不调用模型，不生成 Go，不提交。报告会同时显示 `planning-verified` 与 `executionReady: true`；这是两个不同结论。

现在可以使用已更新并构建的 Portsmith：

```sh
node ../portsmith/dist/cli.js migrate --plan migration --check
node ../portsmith/dist/cli.js migrate --plan migration --commit --env-file ../omni-pi/.env --max-turns 80 --timeout 1800
```

当前 `--check` 应报告 ready、canStart: true、readyBatches: 24、preparedSteps: 26、blocked: []。失败时根据诊断处理后重跑同一命令，不需要逐步选择任务。

## 最后怎样算完成

三个模块状态都为 accepted；所有纳入范围的来源/导出/测试场景有对应结果；依赖和生成目录固定；各模块及跨模块测试、race、CLI 离线工具往返、目标平台构建/原生测试通过；真实模型集成状态单独报告。可从一个普通 Go consumer 导入 SDK，也可只拿 binary 使用。

当前已完成模块执行器和全部批次的执行准备，没有调用真实模型、生成产品 Go 代码或提交/push Pith。正式生成仍由你启动。
