> **历史存档，已失效。** 以下是原 MVP 的说明；旧命令、接口和准备状态不适用于新计划。请返回 [当前计划](../../README.md)。

# Pith 最小迁移计划

## 先做出什么

第一阶段只交付一个能独立构建的 Go 内核和命令行演示：

> 用户问“读取 notes.txt，告诉我里面的项目代号” → DeepSeek 发出 read 工具调用 → Pith 校验参数并读取指定目录中的文件 → 把结果交回模型 → 输出回答。

这是文本、单用户、内存会话的最小闭环。先用固定模型响应证明事件、工具调用和会话状态正确，再单独连接一次真实 DeepSeek。Web UI、桌面应用、Computer Use、MCP、持久化、多用户权限和全部模型厂商不在本阶段。

当前已有来源、9 项计划、固定 Go 接口和 48 个独立验收场景，正式 Go 实现仍由你启动生成。`workflow.json` 把任务、测试、输出和自动提交流程连接起来；`plan.json` 定义迁移范围。

## Pi 来源已经补齐

| 项目         | 固定值                                                                    |
| ------------ | ------------------------------------------------------------------------- |
| 官方仓库     | [earendil-works/pi](https://github.com/earendil-works/pi)                 |
| 版本         | v0.87.1                                                                   |
| 提交         | `f07218c4d4bbc12bef056a7058c3dd49dfe41abe`                                |
| 本地完整源码 | `.cache/pi/f07218c4d4bbc12bef056a7058c3dd49dfe41abe/`，相对于 Pith 根目录 |
| 校验         | 压缩包 SHA-256、大小、Git 文件清单与逐文件 blob SHA-1                     |
| 结果         | 1863 个 Git 文件齐全，无缺失、无额外文件                                  |

两份 Windows 启动脚本按上游 `.gitattributes` 的换行规则对比；没有改写下载的源码。完整锁定记录见 [upstream.json](upstream.json)，上游许可证单独保留。

源码缓存不进 Pith Git。新机器可以运行 `source.mts fetch` 重建；`verify` 仅本地校验，不下载。脚本固定到提交和校验值，已有来源不匹配时会报错，不覆盖它。

完整扫描包含 1560 个 TS/JS 文件、370926 行物理行和 10 组文件依赖环；这是上游分析规模，**不是 Pith 要写的代码量**。没有安装 Pi 的依赖，也没有运行其完整测试套件。

剩余 60 处静态分析项已在 [source-audit.json](source-audit.json) 逐项分类：42 处模型目录生成数据、8 处文档示例、2 处未构建入口、8 处动态导入。Git 本来就不保存部分生成物，因此“源码齐全”不等于“已完成上游构建”。

本计划选了 14 个实现来源文件，加上测试及辅助参考，共 35 个文件；这些文件自身没有未解析或动态导入项。但这 **不是完整传递依赖闭包**：索引导出和仅用于 TS 类型的引用会继续引向其他厂商。按 [依赖边界](dependency-map.md) 选择具体行为，不把整个目录递归翻译。

## 分几步做

| 任务 | 产物                              | 前置任务      | 难点                                  |
| ---- | --------------------------------- | ------------- | ------------------------------------- |
| P01  | `internal/eventstream` 通用事件流 | 无            | FIFO、结束、首次结果、取消与并发      |
| P02  | `ai` 消息、事件、模型流接口       | P01           | 空值与零值、工具参数、事件对象所有权  |
| P03  | `ai` 会话整理、system 与工具声明  | P02           | 顺序、增量声明、可执行工具一致性      |
| P04  | `ai` 参数校验与转换               | P02           | 选择 Go schema 库，补齐 Pi 的转换规则 |
| P05  | `agent` 顺序工具调用循环          | P01、P03、P04 | 事件次序、错误、截断、取消、结束控制  |
| P06  | `agent` 状态与生命周期            | P05           | 忙碌拒绝、订阅、重置和状态清理        |
| P07  | `providers/deepseek` 文本流适配   | P02、P03      | SSE 分包、多工具增量、错误与中断      |
| P08  | `tools/readfile` 文本读取         | P04、P05      | 行数/字节截断、UTF-8、受限目录        |
| P09  | `cmd/pith` 演示与独立构建         | P06、P07、P08 | 真正完成一次工具往返                  |

建议按表顺序完成，P07 的固定响应测试也可以在 P03 后提前做。一个任务可以涉及多个 TS 文件，依据可独立验收的行为拆分，不按文件大小机械切块。

`P09` 是新增 Go 集成代码，上游 e2e 文件只是参考，不能声称这部分是直接移植。`plan.json` 的 `files`/`references` 保存准确来源清单和验收要求。

## 学着走第一步

先看上游 `packages/ai/src/utils/event-stream.ts`，只读通用 EventStream 和 FIFO 部分；再看 `packages/ai/test/event-stream.test.ts`。整个来源文件约 110 行，很适合第一次手动移植。

接口已经写在 [API.md](API.md)，独立测试位于 `judges/`。原 TS 对照数据和测试自检记录分别见 `oracle-evidence.json`、`judge-audit.json`。测试自检不代表正式 Go 实现已经通过。

## 本机操作

在 **Pith 根目录**运行：

```sh
# 仅检查准备情况，不调用模型、不提交。
node ../portsmith/dist/cli.js migrate --plan migration --check

# 你启动真实迁移：自动生成、修复、累计验证、集成并逐模块提交。
node ../portsmith/dist/cli.js migrate --plan migration --commit --env-file ../omni-pi/.env --max-turns 24 --timeout 600
```

首次运行可自动提交准备文件，后续每个模块验收并集成后提交一次；不会 push。不再逐个指定 unit/judge/out/task。中断后重跑同一命令继续；先做一个正式模块可加 `--max-units 1`。

完整步骤、命令含义和恢复方式见平行 Portsmith 仓库的 [使用说明书](../../portsmith/docs/user-manual.md)。此链接适用于本机两个仓库平行放置的情况。

维护来源和验收数据时使用以下命令；正常迁移无需反复运行：

```sh
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/source.mts fetch
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/source.mts verify
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/oracles.mts
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/audit-judges.mts
```

后两条会重建固定数据或测试审计报告，应在迁移开始前维护。不要重跑自动 plan 命令覆盖已收敛的计划，也不要在运行期间修改接口、judge 或依赖。源缓存和临时运行记录不提交；正式 Go 文件、独立测试和结果记录由流水线逐项纳入仓库。

Pith 的模块路径已固定为 `github.com/minifish-org/pith`。参数校验库固定为 `github.com/santhosh-tekuri/jsonschema/v6 v6.0.2`，传递依赖和校验值保存在根 go.mod/go.sum。race 验证使用系统 C 编译工具；默认构建和普通测试不依赖 CGO。

## 这一阶段怎样算完成

9 个任务独立检查通过；`go test ./...`、关键并发路径的 race 检查和 `go build ./cmd/pith` 通过；无 Node/Portsmith/源码缓存的环境能运行生成的程序；固定响应完整工具往返通过；真实 DeepSeek 冒烟单独成功；所有已支持能力、差异和配置都有说明。

这里承诺的是指定行为的 Go 实现。以后补全 Pi 其他能力时，继续扩充来源清单、验收场景和明确的任务，不预先宣称全量兼容。
