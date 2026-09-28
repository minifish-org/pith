# 最小行为验收清单

48 个场景已编写为独立 Go 测试，状态为 **authored**；尚无正式 Go 候选通过验收。测试位于 `judges/<任务ID>/`，接口见 `API.md`，自动关联见 `workflow.json`。这里区分三种证据：原 TS 的固定行为、Pith 新增或刻意改变的行为、真实服务的非确定性冒烟。`check-plan.mts` 只校验任务准备，不执行下列验收。

每个任务先固定输入、输出、错误和事件序列，再生成候选。去除动态时间戳等不稳定值时必须有明确的归一化规则，不能忽略关键事件或工具参数。Go 独立测试名以 `TestPortsmithJudge` 开头，和模型可编辑的候选自测分开；至少用一种故意错误实现验证测试能阻止错误通过。

## P01-eventstream：通用事件流

原 TS 测试覆盖缓冲顺序、等待者次序、显式结束和无结果结束；Result 重读、零值、回调重入和取消另补场景。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/ai/src/utils/event-stream.ts`
- `packages/ai/test/event-stream.test.ts`

| 场景   | 验收要求                                                                         | 状态     |
| ------ | -------------------------------------------------------------------------------- | -------- |
| P01-01 | FIFO缓冲和等待者次序一致；完成事件可读，完成后忽略Push。                         | authored |
| P01-02 | End后排空缓冲；End(nil)唤醒等待者但不确定Result；之后End(0)仍能确定结果。        | authored |
| P01-03 | 第一次结果获胜；Go新增取消不会吞掉已经确定的结果。                               | authored |
| P01-04 | 多次Result读取一致；不持锁调用可重入的外部回调，回调失败和并发行为先刻画并记录。 | authored |
| P01-05 | 验证原TS场景基线、已知错误实现会失败，再运行Go同场景与race检查。                 | authored |

## P02-contracts：消息、事件与模型边界

从消息类型及序列化场景提取 fixtures。快照所有权、精度策略属于 Pith 设计，不冒充原 TS 逐位等价。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/ai/src/types.ts`
- `packages/ai/test/message-types.test.ts`

| 场景   | 验收要求                                                             | 状态     |
| ------ | -------------------------------------------------------------------- | -------- |
| P02-01 | JSON往返保留role、toolCallId、toolName、参数、错误状态和毫秒时间戳。 | authored |
| P02-02 | 缺失、null、零值和空数组可区分；不把false或0当作未提供。             | authored |
| P02-03 | 参数数字不经float64静默丢精度，边界与JS数值差异显式记录。            | authored |
| P02-04 | 流开始、增量、唯一终结事件语义明确；失败可发生在start之前。          | authored |
| P02-05 | Go公开流事件是消费时快照，不暴露会被后续修改的共享partial对象。      | authored |

## P03-transcript：会话整理与工具声明

参考 Agent 的初始声明与工具变更测试，为选定辅助函数建立直接 TS oracle。只比较普通工具，不引入厂商专有锚点。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/ai/src/utils/transcript.ts`
- `packages/ai/src/utils/text.ts`
- `packages/agent/test/agent.test.ts`
- `packages/ai/test/transcript-tool-changes.test.ts`

| 场景   | 验收要求                                                              | 状态     |
| ------ | --------------------------------------------------------------------- | -------- |
| P03-01 | 空prompt加空tools不制造system消息；有工具时创建正确的初始system声明。 | authored |
| P03-02 | system文本按上游分隔方式累积，命名sections新增、替换、删除保持顺序。  | authored |
| P03-03 | 工具先移除后新增，同名重定义产生正确增量；声明中不包含执行函数。      | authored |
| P03-04 | 传给模型的工具来自规范化会话，且与本轮可执行集合一致。                | authored |
| P03-05 | Go map不能随机改变提示词、工具数组或sections顺序。                    | authored |

## P04-tool-arguments：工具参数校验与转换

原 validation.test.ts 已有 primitive coercion、optional null、引用 nullable、anyOf/oneOf 和非法转换场景；选库后逐项对齐，并补深拷贝/额外字段场景。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/ai/src/utils/validation.ts`
- `packages/ai/test/validation.test.ts`

| 场景   | 验收要求                                                                | 状态     |
| ------ | ----------------------------------------------------------------------- | -------- |
| P04-01 | 参数先深拷贝；校验和转换不改变模型原始toolCall.arguments。              | authored |
| P04-02 | 验证必填、类型、枚举及额外字段；失败不得调用工具。                      | authored |
| P04-03 | 覆盖原测试中的普通JSON schema原始值转换，非法字符串不能被误接受为数字。 | authored |
| P04-04 | 可选非nullable属性null按缺失处理；nullable与引用schema允许的null保留。  | authored |
| P04-05 | 联合schema已有匹配分支不额外转换；错误有稳定路径和原因。                | authored |

## P05-agent-loop：顺序工具调用循环

使用预编排的模型流与假工具，比较完整事件和下一轮请求。顺序模式与 Pi 显式 sequential 比较；轮次上限是 Pith 新行为。重点保留 length 截断禁止执行工具的场景。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/agent/src/agent-loop.ts`
- `packages/agent/src/types.ts`
- `packages/agent/src/stream-fn.ts`
- `packages/agent/test/agent-loop.test.ts`

| 场景   | 验收要求                                                                           | 状态     |
| ------ | ---------------------------------------------------------------------------------- | -------- |
| P05-01 | 固定模型响应下，完整比较agent/turn/message/tool事件和最终会话。                    | authored |
| P05-02 | 工具调用参数经P04处理；未知工具、校验失败或工具抛错都形成isError结果并按约定继续。 | authored |
| P05-03 | 多个工具按助手源顺序执行、持久化和回传；结果与toolCallId一一对应。                 | authored |
| P05-04 | length截断的助手工具调用全部失败，绝不能执行不完整参数。                           | authored |
| P05-05 | 模型error/aborted后不执行工具；只发一个agent_end。                                 | authored |
| P05-06 | continue拒绝空会话和assistant结尾；transformContext先于convertToLlm。              | authored |
| P05-07 | finishTurn结束时不再发请求；轮次上限和取消不会留下悬挂的消费者。                   | authored |

## P06-agent-state：Agent状态与生命周期

参考 agent.test.ts 的 busy/reset、async subscribers、thrown failures、late tool updates；Go 并发和回调重入另测。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/agent/src/agent.ts`
- `packages/agent/test/agent.test.ts`

| 场景   | 验收要求                                                                   | 状态     |
| ------ | -------------------------------------------------------------------------- | -------- |
| P06-01 | 初始prompt、tools和messages一致；Reset恢复基线。                           | authored |
| P06-02 | Prompt/Continue正在运行时再次调用必须拒绝；运行中Reset拒绝且不能损坏会话。 | authored |
| P06-03 | 正常结束、工具错误、provider抛错、取消均清理isStreaming和pending工具。     | authored |
| P06-04 | 异步订阅的完成计入Prompt/WaitForIdle；工具结束后的迟到更新被忽略。         | authored |
| P06-05 | 同一Agent串行运行；共享配置/消息的Go所有权和回调重入边界有race测试。       | authored |

## P07-deepseek：DeepSeek Chat Completions流适配

用 httptest 捕获真实请求与固定 SSE 字节流，不只测试孤立解析函数。先建 TS 适配器生成选定响应的预期事件，再检查 Pith 差异；未知/中断终止的严格策略单独标记。真实 DeepSeek 检查不作为可重放行为证据。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/ai/src/api/openai-completions.ts`
- `packages/ai/src/utils/json-parse.ts`
- `packages/ai/src/providers/deepseek.ts`
- `packages/ai/test/openai-completions-empty-tools.test.ts`
- `packages/ai/test/openai-completions-tool-choice.test.ts`
- `packages/ai/test/openai-completions-raw-stop-reason.test.ts`
- `packages/ai/test/openai-completions-response-model.test.ts`

| 场景   | 验收要求                                                                                         | 状态     |
| ------ | ------------------------------------------------------------------------------------------------ | -------- |
| P07-01 | 假HTTP服务捕获实际请求：system/user/assistant/tool次序正确；空工具省略，DeepSeek使用max_tokens。 | authored |
| P07-02 | SSE在任意字节边界拆分仍可解析，多tool index的ID、name和JSON参数独立累计。                        | authored |
| P07-03 | 只有完整最终参数进入工具执行；增量raw JSON可以显示，残缺JSON不能伪装成功。                       | authored |
| P07-04 | 保留response model、usage与原始finish_reason，未知终止或无终止断流作为错误。                     | authored |
| P07-05 | 畸形SSE、未知终止或无终止断流均报错；部分输出后不自动重放整次请求。                              | authored |
| P07-06 | 覆盖HTTP401/429/5xx和ctx取消；固定响应测试不访问真实服务。                                       | authored |

## P08-read-text：受限目录文本读取工具

文件和字节 fixtures 对照上游 read/truncate 的文本路径；root 越界、符号链接和图片拒绝是 Pith 自己的边界检查。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/agent/src/harness/tools/read.ts`
- `packages/agent/src/harness/utils/truncate.ts`
- `packages/agent/test/harness/tools.test.ts`
- `packages/agent/test/harness/truncate.test.ts`

| 场景   | 验收要求                                                                       | 状态     |
| ------ | ------------------------------------------------------------------------------ | -------- |
| P08-01 | 固定文本文件、空文件、offset/limit、越界和不存在文件产生可解释结果。           | authored |
| P08-02 | 按上游默认2000行/50KiB限制输出；UTF-8、CRLF、末尾换行和超长首行有独立场景。    | authored |
| P08-03 | 只访问指定root内普通文件，拒绝目录、越界路径和链接逃逸；取消和资源关闭可验证。 | authored |
| P08-04 | 受支持的文本场景与Pi对照；权限边界、图片拒绝与输入限制按Pith新增行为测试。     | authored |
| P08-05 | 工具执行不依赖Node、Pi harness或可执行shell。                                  | authored |

## P09-cli-smoke：命令行闭环与独立构建

新增集成验收，证明文件内容进入下一轮模型请求。上游 e2e 只提供场景参考，不从自然语言回答推断全部兼容。

来源/参考（均相对固定的 Pi 源码根目录）：

- `packages/agent/test/e2e.test.ts`
- `packages/agent/test/utils/calculate.ts`
- `packages/agent/test/utils/get-current-time.ts`

| 场景   | 验收要求                                                                     | 状态     |
| ------ | ---------------------------------------------------------------------------- | -------- |
| P09-01 | 离线假模型完成用户消息→read调用→Go结果→最终回答，工具只执行一次。            | authored |
| P09-02 | 受限root中的唯一标记能经工具结果进入下一次请求，证明真的完成工具往返。       | authored |
| P09-03 | 本地假服务验收凭据不出现在stdout/stderr；真实DeepSeek冒烟另行显式执行。      | authored |
| P09-04 | go test ./...与Go独立构建通过；运行binary时不需要Node、Portsmith或源码缓存。 | authored |
| P09-05 | 缺少provider配置时非零退出；文档记录单用户内存会话、限制和最小配置。         | authored |

## 已完成的测试准备

- 固定接口与 48 个命名场景均已编写；工作流强制检查每个测试名实际通过，跳过测试不能采纳。
- 原 TS 基线覆盖事件流、会话、参数转换、循环事件序列及文本截断；数据与源码哈希记录在 oracle-evidence.json。
- 全部 judge 与固定 API 的编译检查、9 个无效样本拒绝检查，以及 EventStream 的 FIFO 行为变异检查，见 judge-audit.json。
- 这些准备证据不代表 Pith 实现已通过；正式候选还需要独立测试、适用的 race 检查和累计集成测试。

## 开始实际迁移

在 Pith 根目录运行说明书中的 migrate 命令。先 --check；正式运行加 --commit，工具自动推进并逐模块提交。真实 DeepSeek 冒烟在 Go CLI 生成后单独进行，不在默认 Go 测试里访问真实服务。
