# 完整迁移的验收计划

当前状态：完整执行材料已准备。226 条源 TS 行为对照、独立 Go 验收及专用 EventStream/CLI 测试已接到全部步骤；自检记录见 readiness。下表是完整行为范围，不能把有限 fixtures 当作穷尽性证明；上游测试迁移与生成后平台验证仍要按记录完成。

## 四种不同证据

1. 规划检查：清点/哈希/映射/依赖完整，不能证明 Go 代码正确。
2. Judge 自检：编译通过，代表性的错误实现/变异会失败，不能证明正式实现正确。
3. 行为验收：候选通过冻结的独立 judge、完整用例映射和回归；不只看模型自写测试。
4. 真实服务/平台集成：用实际账号/本机平台 smoke，结果单列，不能用随机模型回答当确定性 oracle。

## 先建立用例账本

249 个静态测试/脚本参考输入及附加数据资产在 inventory 逐一登记。对每个 Vitest test/describe/table case 填：源路径和场景位置、源 hash、对应模块/批次、测试方式（TS 对照/协议 golden/Go-only）、Go judge、执行平台、差异 ID、结果及证据 hash。helper、scratch、真实服务 probe、生成器脚本也要说明用途，不假装都是离线单测。

静态文件覆盖率不等于行为覆盖率。每个公开导出也必须指向契约和场景；展开 barrel 和 TS 条件类型需要人工/规划 AI 审阅。没有可对应测试的行为要补新场景，不因为上游漏测就省略。

TS oracle 使用固定源码和依赖，时钟/随机数/UUID/HTTP 均注入或规范化。对行为只规范化不重要的随机字段；不得抹除顺序、toolCallId、签名、usage、取消和错误。增量流在协议层保存原始 bytes 及事件序列，多字节/任意分块必须覆盖。

## ai 门槛

| 领域                     | 必须验证的矩阵                                                                                                                                      |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| 类型/流/会话             | 所有 content/message/终止分支；空值/数字；FIFO/首次结果/多消费者/取消；system/tool 增量与跨模型转化                                                 |
| 校验与 JSON              | nullable/optional/$ref/union/coercion；禁止修改原始参数；partial JSON 与 final JSON 分离；无效参数不执行工具                                        |
| 每种模型协议             | 请求 URL/headers/body，文本/thinking/image/tool，多工具交错，空 tools，usage/price/stop reason，预生成失败、输出后失败、取消、retry、截断与异常 EOF |
| OpenAI 家族              | Completions/Responses/Azure/Codex 分别验证；WS/SSE/回退、affinity、reasoning replay、cache/constrained sampling                                     |
| Anthropic/Google/Bedrock | thinking/signature、cache、原生工具、Vertex auth、Bedrock 凭据/签名/binary stream；不以 OpenAI 假服务代替这些协议                                   |
| 其他协议                 | Mistral、Pi Messages/deferred、Cloudflare/binding、OpenRouter Images 各自完整分支                                                                   |
| 认证                     | 显式/存储/env 优先级、PKCE/device polling、刷新互斥与轮换落盘、logout 竞争、失败不回退、超时取消、无密钥泄漏                                        |
| 模型/provider            | 每个 provider 工厂、headers/routing/compat；catalog 数据来源、schema、价格分层、动态模型、注册/删除、会话资源释放                                   |

只做 DeepSeek smoke 不满足 ai 模块门槛。每个协议/认证/provider 都要有已执行的本地固定响应测试。真实服务测试报告支持 `not-run-no-credentials`，不将它伪装成通过；发布时区分“固定版本行为对照完成”和“当前真实服务已验证”。

## core 门槛

- agent-loop/agent/proxy：新请求/continue、所有事件、parallel/sequential、队列各模式、steering/follow-up、全部 before/after/prepare/finish hooks、动态模型/工具、订阅、取消、错误与截断。并发检查必要偏序和结果映射，不能要求一次偶然完成顺序。
- memory/JSONL：同一 storage/repo conformance 套件，branch/fork、usage、cursor、mutation、原子写入、旧格式读取、损坏/截断/重开及取消。
- runtime：reducer、lane、watch/progress、restore、deferred、retry/reconcile、structural、终结；在 checkpoint/模型/工具效果前后注入崩溃。未知 shell 副作用状态保留为未知，不自动重放后声称 exactly-once。
- compaction：切分点、保留消息/工具配对、summary 输入/输出/usage、失败回滚、分支摘要。
- skills/templates/config/system prompt/telemetry：排序、ignore、frontmatter、重复/非法资源、上下文值、span/event 关闭与 no-op。SessionSearchService 上游是接口，验证接口契约，不额外发明搜索算法作为“已移植”。

## tools 与交付门槛

| 工具/层      | 必须验证                                                                                                                |
| ------------ | ----------------------------------------------------------------------------------------------------------------------- |
| read         | 文本、图片/BMP、注入图片处理、offset/limit、长行/CRLF/UTF-8、2000 行/50KiB 截断、特殊路径、错误与取消                   |
| write        | 新建/覆盖/空文件/父目录/权限失败、并发写入队列、结果语义                                                                |
| edit         | 精确/模糊匹配、重复匹配拒绝、BOM/CRLF、diff/行号、读改写竞争、失败不污染文件                                            |
| bash         | cwd/env、prepare hook、流式输出、退出码、超时/取消/进程树、输出截断与落盘、取消后无悬挂任务                             |
| ExecutionEnv | 同一工具套件跑 fake env 与 local env；错误分类和资源关闭一致；临时隔离目录内执行                                        |
| CLI          | 假 provider 完成 read→write→edit→bash→最终回答，多轮与 session 重启恢复；缺配置非零退出、stdout/stderr 分离、凭据不输出 |
| 分发         | 在不含 TS/cache/npm/Go 工具链的运行目录执行 binary；sdk 独立 consumer 编译；shell 明确为系统依赖                        |

默认测试只使用临时文件和受控子进程，不在真实用户目录执行生成的 shell 操作。macOS/Linux 至少各一个真实 runner 完成进程/文件测试；交叉编译只能证明编译。Windows 单独记录平台缺口，不能用 cross-build 冒充运行验证。

## 每个模块提交前

需要 format/vet/build、全部候选自测、冻结独立 judge、前置模块回归、关键并发 `-race`。测试发现0项、skip/todo、必需测试名缺失均不算通过。原 TS 黄金、Go 自测和 judge 使用不同证据字段；每个模块至少覆盖典型错误变异（FIFO、漏工具校验、重复执行、忽略取消、丢失签名、覆盖竞态等）。

模块结果记录源 revision、plan/contract/依赖/judge hash、前置 Go commit、执行命令/退出码、测试计数/名称、日志摘要、目标文件 hash、平台、允许差异和已知限制。结果文件和实现/测试/映射同一个 commit。三个模块全部 accepted 才能写整体迁移完成。
