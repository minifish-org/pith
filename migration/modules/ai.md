# ai：完整 Pi AI 层

当前执行顺序：ai-foundation → ai-utils → ai-auth → ai-models → ai-openai → ai-anthropic → ai-google → ai-bedrock → ai-other-protocols → ai-providers → ai-surface。源码/输出数量以 plan.json 为准。

完整接入层；不能用 DeepSeek 的兼容协议替代其他协议。所有 provider 工厂和认证路径均以 inventory 为清单。

模块只在全部批次与最终验收通过后提交一次。下列批次是 Portsmith 内部执行顺序，不需要用户逐一输入命令。完整模块尚未生成；全部批次的执行材料已准备，详见 workflow.json。

| 内部批次             | 范围                                         | 直接前置                                                           | 来源文件 |
| -------------------- | -------------------------------------------- | ------------------------------------------------------------------ | -------- |
| `ai-foundation`      | 公共消息、模型选项与遥测契约                 | 前置模块已接受                                                     | 7        |
| `ai-utils`           | 事件流与协议辅助                             | ai-foundation                                                      | 27       |
| `ai-auth`            | 凭据、认证与全部 OAuth                       | ai-utils                                                           | 18       |
| `ai-models`          | 模型集合与会话资源                           | ai-auth                                                            | 4        |
| `ai-openai`          | Chat Completions / Responses / Codex / Azure | ai-models                                                          | 11       |
| `ai-anthropic`       | Anthropic Messages                           | ai-models                                                          | 2        |
| `ai-google`          | Google 与 Vertex                             | ai-models                                                          | 5        |
| `ai-bedrock`         | Bedrock Converse Stream                      | ai-models                                                          | 2        |
| `ai-other-protocols` | Mistral、Pi Messages、Cloudflare 与图像生成  | ai-models                                                          | 11       |
| `ai-providers`       | 全部 provider 工厂与模型数据                 | ai-openai, ai-anthropic, ai-google, ai-bedrock, ai-other-protocols | 94       |
| `ai-surface`         | 完整 AI 入口、兼容层与 CLI                   | ai-providers                                                       | 8        |

## ai-foundation / 公共消息、模型选项与遥测契约

行为：

- 完整 Message/Content/Usage/Model/Images 类型；absent/null/zero 与数值
- Thinking、签名、deferred、structured output、cache、transport 选项
- Telemetry 的 no-op、memory 与 conformance；事件流基础

验收：

- 类型往返、所有 content variant、usage/cost、首次终结、FIFO、取消、快照及 race

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-utils / 事件流与协议辅助

行为：

- transcript、tool state、validation/coercion
- partial JSON、错误、重试、超限、token estimate、Unicode、HTTP 代理
- assistant frame、UUID、headers、取消与资源释放

验收：

- 复用原 TS 对照；JSON 分片/无效尾部；模型切换后的会话；schema 联合与 null；重试不得重复输出

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-auth / 凭据、认证与全部 OAuth

行为：

- credential store/read/modify、显式/存储/环境的优先级
- PKCE、device code、callback、token refresh 与所有 OAuth provider
- 取消、刷新互斥、锁内复查、退出登录竞争和错误脱敏

验收：

- 固定时钟/随机数和假 HTTP；并发只刷新一次；失败不能退回环境凭据；所有 OAuth 模块均有用例

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-models / 模型集合与会话资源

行为：

- models/store、image models、provider registration
- 模型校验、source/cache、运行时替换、费用分层
- session affinity 与资源销毁、无隐式 eager import 副作用

验收：

- 重复注册/删除/动态模型、并发、价格缺失与零价、资源恰好释放一次

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-openai / Chat Completions / Responses / Codex / Azure

行为：

- 共享请求转换、tool ID、reasoning replay、缓存与 structured output
- SSE、Codex WebSocket、会话亲和、HTTP/WS 取消与重连
- DeepSeek 等兼容路径；完整原始 stop reason/headers/usage

验收：

- 每种协议用捕获请求与分片响应比对；WS 回退与终结；多工具交错；跨 provider 历史

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-anthropic / Anthropic Messages

行为：

- thinking/adaptive thinking/signature、cache、原生工具与 tool search
- eager tool input、system 更新、工具声明和自定义请求选项

验收：

- 原 TS 兼容测试矩阵、SSE、空签名、thinking on/off、缓存计费、预生成失败

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-google / Google 与 Vertex

行为：

- 共享消息/工具转换、thinking/signatures、多模态
- API key/ADC、Vertex 项目/位置、重试

验收：

- 签名及空 block、图片工具结果、unsigned toolcall、原 stop reason、取消与认证优先级

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-bedrock / Bedrock Converse Stream

行为：

- AWS credentials/profile、SigV4、endpoint、event stream
- 缓存、reasoning/redaction、headers、错误元数据

验收：

- 离线固定签名/凭据链、binary event stream、usage/cost、截断与取消；使用成熟 AWS Go SDK

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-other-protocols / Mistral、Pi Messages、Cloudflare 与图像生成

行为：

- Mistral Conversations、Pi Messages 与 deferred 操作
- Cloudflare REST 与 binding 注入适配
- OpenRouter Images、图像 model registry 与生成结果

验收：

- 各 API 正常/错误/取消/usage/终结矩阵；deferred 恢复/取消；image payload；binding 合约假实现

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-providers / 全部 provider 工厂与模型数据

行为：

- 迁移 inventory 中每个 provider，不能只实现 DeepSeek
- 42 处生成数据引用已通过同版本发布归档补齐，静态目录提前到 ai-foundation
- catalog hydration/generator 规则、routing、headers、模型选项

验收：

- 逐 provider/模型 schema 校验；生成数据来源与时间固定；faux provider；禁止运行时无提示联网更新

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## ai-surface / 完整 AI 入口、兼容层与 CLI

行为：

- root exports、compat、legacy aliases、lazy adapters、OAuth CLI
- Go 不复刻 JS loader；保留可观测行为和配置优先级

验收：

- 公开符号清单逐项 disposition；独立 Go consumer 编译；离线全 AI 回归；CLI 不泄露凭据

完整来源与输出见 plan.json 对应批次。参考输入已由 reference-ledger.json 分派并放入对应步骤只读 testdata；来源与 judge 按行读取。

## Provider 与 OAuth 的显式清单

`providers/all.ts` 中的工厂（不是独立协议数量）：

`amazonBedrockProvider`, `antLingProvider`, `anthropicProvider`, `azureOpenAIResponsesProvider`, `basetenProvider`, `cerebrasProvider`, `cloudflareAIGatewayProvider`, `cloudflareWorkersAIProvider`, `deepseekProvider`, `fireworksProvider`, `githubCopilotProvider`, `googleProvider`, `googleVertexProvider`, `groqProvider`, `huggingfaceProvider`, `kimiCodingProvider`, `metaProvider`, `minimaxCnProvider`, `minimaxProvider`, `mistralProvider`, `moonshotaiCnProvider`, `moonshotaiProvider`, `nvidiaProvider`, `openaiCodexProvider`, `openaiProvider`, `opencodeGoProvider`, `opencodeProvider`, `openrouterImagesProvider`, `openrouterProvider`, `qwenTokenPlanCnProvider`, `qwenTokenPlanIndividualProvider`, `qwenTokenPlanProvider`, `radiusProvider`, `togetherProvider`, `vercelAIGatewayProvider`, `xaiProvider`, `xiaomiProvider`, `xiaomiTokenPlanAmsProvider`, `xiaomiTokenPlanCnProvider`, `xiaomiTokenPlanSgpProvider`, `zaiCodingCnProvider`, `zaiProvider`.

OAuth 专用适配：

`anthropic`, `github-copilot`, `kimi-coding`, `meta`, `openai-codex`, `openrouter`, `radius`, `xai`.

另含 faux provider、图像 provider 注册、模型数据文件以及所有 helpers；以 inventory 为最终文件清单。新增 provider 必须经过持续移植流程，不能依靠这段文字自动猜测。

42 处 generated catalog 引用必须解决 E04；动态 radius 等没有静态目录的情况按源码处理，不创造不存在的数据。
