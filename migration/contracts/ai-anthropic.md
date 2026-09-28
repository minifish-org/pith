# ai-anthropic: executable behavior contract

Pinned source: f07218c4d4bbc12bef056a7058c3dd49dfe41abe. Entire batch behavior: thinking/adaptive thinking/signature、cache、原生工具与 tool search; eager tool input、system 更新、工具声明和自定义请求选项.
Acceptance scope: 原 TS 兼容测试矩阵、SSE、空签名、thinking on/off、缓存计费、预生成失败.

Generate internal/conformance/ai_anthropic/adapter_test.go in package conformance. Its ONLY test bridge is:

func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error)

Translate each input operation into calls to the REAL exported Go SDK; do not implement the SDK behavior in this bridge. Use test-local HTTP servers, filesystem dirs and fixed clocks as in testdata/evaluate.ts.txt. Input.op=call invokes the corresponding source function with input.args; catalog reads its actual embedded data; protocol invokes the real streaming protocol and normalizes only fields stated by the evaluator. Go errors need not match JS framework wording, but successful/error classification must match. Undefined fields use {"$undefined":true}, distinct from null. Never read expected results, golden files, TS source or invoke Node in the adapter. Do not hard-code vectors or provider/model results. Use read_judge to inspect operation evaluator and cases; it is read-only.

The production API is typed and idiomatic Go, not a JSON dispatch interpreter. The bridge is test-only and never ships in the binary.

## Complete implementation and export mapping

Port ALL in-scope behavior in the listed sources, not just the fixture examples. Source of truth is the pinned TS implementation and its tests. Translate the upstream tests in testdata/reference-ledger.json into Go self-tests; reference-context entries are context, not executable tests. Live credentials/cloud hardware tests become deterministic fake HTTP/env scenarios with the limitation recorded. Keep the exact enum/wire JSON semantics.

Fill internal/conformance/ai_anthropic/source_map.json with one row per frozen source symbol in testdata/surface.json: {source, upstream, goPackage, goSymbol, reason}. goPackage is repo-relative (packages/...), goSymbol is an actual exported top-level declaration. Generic types and function aliases are valid; empty marker declarations are not substitutes for functionality. Barrel symbols map to their real public equivalent. Prefix colliding protocol stream names and OAuth login/refresh names by provider. Do not create import cycles. Exact Go function signatures are selected during translation under the type rules below and compiled by Go; they are not falsely claimed to have been precompiled now. Fields/methods remain part of their enclosing type's complete behavior.

## Read and write scope

Primary sources:
- packages/ai/src/api/anthropic-messages.lazy.ts
- packages/ai/src/api/anthropic-messages.ts

Allowed new outputs:
- packages/ai/api/anthropic_messages_lazy.go
- packages/ai/api/anthropic_messages.go
- internal/conformance/ai_anthropic/source_map.json
- internal/conformance/ai_anthropic/adapter_test.go
- packages/ai/api/ai_anthropic_test.go

Current-module earlier candidates may be fixed for integration. Prior modules are immutable. Public option/compaction/session DTOs referenced by later implementations belong in the early shared types packages now. Read direct type references before selecting their shape. Go compile failures are repairable candidate failures, not instructions to drop a later API.

Dependencies are pinned in candidate go.mod/go.sum. No additional dependency installation by the generator. Use net/http, encoding/json and standard crypto for HTTP/SSE/OAuth; AWS SDK v2 for Bedrock, coder/websocket for WS, jsonschema/v6 for schema, goccy/go-yaml, go-diff and go-gitignore for resource/tool behavior. Preserve explicit coercion/repair logic that these libraries do not supply.


## Architecture and conversion rules

# Go 接口与目录契约

这是完整迁移的架构与类型转换契约。全部批次已提供冻结行为验收和来源符号清单。通用 EventStream 的 Go 签名预先固定；其余 SDK 的具体签名由生成器按照本文规则选择，在生成过程中编译检查，并写入各批次的 `source_map.json`。

这项选择避免先手写一遍完整 SDK 接口才能启动移植。**冻结的是行为、公共符号范围、包边界和测试桥接签名，不虚称完整 SDK 签名已经编译。** 独立 judge 通过 test-only `RunCase(context.Context, json.RawMessage) (json.RawMessage, error)` 适配器调用真实 SDK；生产代码仍需提供完整、可嵌入的强类型 API。适配器不能代替产品实现。

## 目录原则

单 Go module：`github.com/minifish-org/pith`。保留 `packages/ai`、`packages/agent`、`harness/{session,runtime,compaction,tools,env}` 等功能目录；去掉 `src`；`foo-bar.ts` 通常为 `foo_bar.go`。测试放实现包旁，来源用例路径记录在测试 manifest；二进制 fixtures 放 `testdata`，运行时 catalog 数据用显式 embed 目录。

`inventory.json` 的 targets 是初始文件映射。一个来源可拆到多个目标；多个来源若需合并，必须增加合并理由与符号范围，不能用一个覆盖另一个。新增 Go 文件（CLI、适配、测试、build tags）登记为 generated-from-plan，不编造上游来源。

## 必须保持的包边界

| 包组                                                                              | 职责 / 依赖限制                                                                              |
| --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| `packages/telemetry`                                                              | 遥测基本协议、no-op/memory；不依赖 ai/agent                                                  |
| `packages/ai/utils/eventstream`                                                   | 泛型队列和流，不导入 ai/types                                                                |
| `packages/ai/types`、`auth/types`                                                 | 公共消息/事件/模型/协议选项/认证 DTO；不能导入模型实现、provider 或 SDK facade               |
| `packages/ai/utils`                                                               | 转换、校验、assistant stream；可依赖 types 和泛型流                                          |
| `packages/ai/api`                                                                 | 协议实现依赖 types/utils/auth 契约；不得导入 providers 或 sdk                                |
| `packages/ai`                                                                     | Models 集合、运行时模型配置；通过接口注入 provider，不导入 providers                         |
| `packages/ai/providers`、`catalog`                                                | 具体工厂与静态数据；catalog 是数据叶子，不依赖工厂；provider 可使用 ai 集合接口              |
| `packages/ai/sdk`、`compat`                                             | 高层导出/兼容组装，不能被上述基础包反向导入                                                  |
| `packages/agent/types`                                                            | AgentTool/Message/Event/State/loop 配置，依赖 ai/types                                       |
| `packages/agent`                                                                  | Agent、loop、proxy；不导入 harness/tools 或 SDK facade                                       |
| `packages/agent/harness/types`                                                    | Session/Storage/Runtime/Compaction/ExecutionEnv 等共享数据和接口；不能依赖实际存储或 runtime |
| `harness/context`、`config`、`telemetry`、`result`、`messages`、`hooks`、`events` | 供各叶子包共享的能力；不能回指 harness 组装包                                                |
| `harness/session`、`session/jsonl`、`compaction`、`runtime`                       | 使用共享契约；存储/摘要器/执行器通过接口注入                                                 |
| `harness`                                                                         | AgentHarness 组装与资源协调；不依赖具体 tools/env                                            |
| `harness/tools`、`harness/env`                                                    | 具体工具和本地环境；只依赖 core 公共契约与辅助包                                             |
| `packages/agent/sdk`、`cmd/pith`                                                  | 最后组装 ai/core/tools；叶子永不导入它们                                                     |

TS 的 `import type` 不一定成为 Go import。`types.ts` 中引用 provider Options 的类型必须移到 `ai/types`，由 provider 包提供 aliases；如果照抄它们的位置，Go 会成环。harness/compaction 文件中的公共 DTO 同理上提到 `harness/types`，但实现仍保留原职责目录。

`runtime/drive/` 中相互依赖的实现合入 `runtime/drive_*.go`，保留一对一来源记录；这是 Go 包边界的必要偏离。通用 utils 按功能拆子包，避免 truncate 类型反向依赖 output capture。所有 `index.ts` 都先展开符号真实来源：Go 不机械模拟 barrel；SDK 汇总导出不得使叶子包形成反向依赖。

以上是目标约束，不把 TS 文件图当作已经编译的 Go 包图。每一步都实际编译候选并执行累计回归；早期 shared types 必须读取后续类型引用，提前容纳完整 DTO。编译中的类型冲突可在同模块修复，不能删 API 绕过。

## 必须保留的行为

- Message 的 system/user/assistant/toolResult、text/thinking/image/toolCall、签名、deferred、usage/cost 与 provider metadata 全部建模。保留未知扩展字段需要显式方案；不得吞掉未知内容。
- 区分缺失/null/false/0/空字符串/空数组。协议数字用 `json.Number` 或精确定义类型；超出 JS 可精确表示范围属于登记差异，不能静默损坏参数。
- EventStream 是有序可观察协议，不是随手用 channel 替代 Promise。定义 Push/Next/Result/End 的所有权、终结、取消、慢消费者和首次结果语义；Assistant 流有完整 start/delta/end/done/error 行为。
- 工具参数校验和 coercion 分开；JSON Schema 库只是基础设施。partial JSON 的宽松解析与最终参数严格校验是两条路径；不能把旧 MVP 的“仅最终解析”当完整兼容。
- core 保留 parallel 与 sequential、steering/follow-up、所有钩子、动态配置、proxy、订阅和恢复。对并发做部分序比较，不将随机完成顺序错误地固定为 TS 的某次顺序。
- `context.Context` 负责取消/超时；回调不得持内部锁调用；数据快照和生命周期明确；结束后迟到事件不得污染下一轮。
- 配置、Models、CredentialStore、SessionRepo、ExecutionEnv 都能实例化与注入。上游 default/compat 路径保留独立兼容入口，不能只删除它们。

## 操作系统适配

`NodeExecutionEnv` 对应 `LocalExecutionEnv`，保留 FileSystem/Shell 协议及错误分类；不尝试复刻 Node 模块系统。标准文件操作使用 os/io/fs，进程使用 os/exec 与平台专属终止逻辑。Unix/Windows 的差异文件必须有 build tags；`process_unix.go` 的 unix 标记不能只靠名字生效。

上游 read 可访问普通路径，并支持图片；默认忠实保留。产品可注入受限 FileSystem，作为 Pith 扩展单独验收；**不要把旧 MVP 的 root 限制永久混入兼容工具**。bash 绕过文件访问包装并不受同一个 root 限制保护，不宣称它是权限沙箱。

Cloudflare 的 JS binding 通过 Go 接口注入模拟同等请求/响应，不承诺原生 Go 程序能运行 Workers binding。Bun/ESM 的入口提供可用 Go 等价入口并登记 runtime-adaptation，不能假装原 JS 插件二进制兼容。

## 批次 contract 的字段

每个执行步骤的 `contracts/<step>.md` 固定完整来源、行为、输出、类型转换和测试桥接约定；judge 的 `testdata/surface.json` 固定来源导出。生成器写出的映射必须对应真实的 Go 导出声明，独立 judge 会解析 Go AST 检查。

`exports.json` 的 `behavior-contract-ready-signature-generated` 表示已准备执行约定，具体 Go 符号尚未生成；不代表迁移完成。上游 Chord 中不在既定范围的服务类型、session benchmark 导出仍明确排除。

图像注册适配：images registry 与基础类型是叶子，不能反向 import provider 工厂。TS register-builtins 的副作用改为最终 sdk 入口显式安装内置实现。catalog 数据提前到 ai-foundation，供 OAuth 与模型集合使用。

