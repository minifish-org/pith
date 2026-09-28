# 第一阶段固定 Go 接口

这是实现与独立测试共同使用的接口约定，不是已经完成的 Go 实现。所有导出名、参数顺序和 JSON 字段必须按此实现。每个任务只新增自己的文件；前置代码只读。

## P01 / internal/eventstream

`StreamItem[T] { Value T; Done bool }`；`EventStream[T,R]`。
`NewEventStream[T,R](isComplete func(T) bool, extract func(T) R) *EventStream[T,R]`。
方法：`Push(T)`、`Next() <-chan StreamItem[T]`、`End(*R)`、`Result(context.Context) (R,error)`。

Next 在返回前登记消费者，返回容量 1 的单次结果通道，结束也发送 Done=true。FIFO、完成事件仍可读、结束后排空旧事件。End(nil) 不确定结果，之后 End(&zero) 可确定；首次结果获胜，已确定结果优先于已取消 context。所有方法并发安全。允许 isComplete/extract 回调重入 Next/End/Result（Result 必须使用有限 context）；禁止在回调持有内部锁。并发 Push 的顺序按内部接收顺序；回调 panic 原样传播，但流不能死锁。

## P02 / ai：公共结构和快照

以下字段必须存在，JSON 名为相应 lowerCamelCase；可选字符串、bool、int 字段可 omitempty；Arguments 不使用 omitempty，nil 表示 JSON null，`{}` 与 `0` 保留原值。

```go
type Content struct { Type string; Text string; ID string; Name string; Arguments json.RawMessage }
type ToolDeclaration struct { Name string; Description string; Parameters json.RawMessage }
type ToolReference struct { Name string }
type Section struct { Name string; Value *string } // nil 删除；slice 保留顺序
type Usage struct { Input int64; Output int64; TotalTokens int64; Cost *float64 }
type Message struct {
 Role string; Content []Content; Timestamp int64
 ToolCallID string; ToolName string; IsError bool
 StopReason string; RawStopReason string; Model string; Usage *Usage
 ToolsAdded []ToolDeclaration; ToolsRemoved []ToolReference; Sections []Section
}
type Context struct { SystemPrompt string; Tools []ToolDeclaration; Messages []Message }
type Event struct { Type string; Message *Message; Delta string; Index int; Error string }
type StreamFn func(context.Context, Context) (<-chan Event, error)
func CloneMessage(Message) Message
func CloneEvent(Event) Event
func ValidateEvent(Event) error
```

Usage.Cost=nil 表示未提供价格；不能编造免费或模型 usage。Message.Content 用统一 block 数组代替 Pi 的字符串或数组联合；System Sections 用有序 slice 代替 JS 对象。JSON role/toolCallId 等协议字段保留。Clone 必须深拷贝 raw JSON、sections、工具 schema、usage 和 nested slices。ValidateEvent 接受 start/text_delta/text_end/toolcall_start/toolcall_delta/toolcall_end/done/error；done 必须有消息，error 可发生在 start 之前且必须有 Error 或消息中的 error/aborted stopReason。未知事件明确报错。

## P03 / ai：会话

```go
func CreateInitialSystemMessage(prompt string, tools []ToolDeclaration) *Message
func NormalizeContext(Context) Context // SystemPrompt/Tools 清空并转成初始消息
func GetCurrentSystemMessage([]Message) *Message
func GetCurrentSystemPrompt([]Message) string
func GetCurrentTools([]Message) []ToolDeclaration
func ToToolDeclaration(ToolDeclaration) ToolDeclaration // 深拷贝 schema
func GetToolStateChanges(previous, current []ToolDeclaration) (added []ToolDeclaration, removed []ToolReference)
```

system 内容以两个换行连接，再按 section 顺序连接非空 section 内容。先删工具再加，重定义保持原位置；已移除后重加移动到末尾。空 prompt 和空 tools 不创建 system 消息。输入不变，输出不得共享可变 schema。

## P04 / ai：参数

`ValidateArguments(tool ToolDeclaration, arguments json.RawMessage) (json.RawMessage,error)`。
错误类型 `ValidationError { Path string; Reason string }`，实现 error，字段路径以 `/` 分隔。使用固定的 jsonschema/v6 负责最终校验，补齐 Pi 原始类型转换、optional nonnullable null 删除、nullable/$ref 和已匹配 union 分支保留。raw 输入不可修改；输出是完整 JSON。无法支持的 schema 明确报错。保留 JSON 大整数（decoder.UseNumber），不能通过 float64 静默舍入。

固定库 v6.0.2 的入口是 `jsonschema.NewCompiler()`、`AddResource(url string, doc any) error`、`Compile(url string) (*Schema,error)` 和 `schema.Validate(value any) error`。AddResource 接受已解析的 JSON 对象，不是 io.Reader；Validate 支持 json.Number。限制为本地已注册资源和 schema 内部引用，禁止校验阶段向外抓取任意 $ref。

## P05 / agent：无状态循环

```go
type ToolResult struct { Content []ai.Content; IsError bool }
type Tool struct {
 Declaration ai.ToolDeclaration
 Execute func(context.Context, json.RawMessage, func(ToolResult)) (ToolResult,error)
}
type Event struct { Type string; Message *ai.Message; ToolCallID string; ToolName string; Result *ToolResult }
type LoopConfig struct {
 Stream ai.StreamFn; Tools []Tool; MaxTurns int
 TransformContext func(context.Context, []ai.Message) ([]ai.Message,error)
 ConvertToLLM func([]ai.Message) []ai.Message
 FinishTurn func([]ai.Message) bool
}
func Run(ctx context.Context, messages []ai.Message, config LoopConfig, emit func(Event)) ([]ai.Message,error)
func Continue(ctx context.Context, messages []ai.Message, config LoopConfig, emit func(Event)) ([]ai.Message,error)
```

Run 输入已包含本次 user 消息；Continue 输入必须非空且结尾不是 assistant。返回完整 transcript。MaxTurns<=0 默认 20；上限用尽返回 error，仍恰好一次 agent_end。初始 agent_start、turn_start，消息 message_start/update/end，工具 tool_execution_start/update/end，结果消息，turn_end，最终 agent_end。顺序执行工具并按源顺序写结果。上下文先 transform 再 convert，模型工具声明来自可执行集合。未知工具、校验错误、执行 error/panic 变成 isError 工具结果继续；length 截断不执行任何 toolCall，写出错误结果并结束本次 run。provider error/aborted 不执行工具，保留已收到的助手消息并返回 error。finishTurn 在工具结果之后运行；true 结束。

## P06 / agent：有状态包装

```go
type State struct { Messages []ai.Message; IsStreaming bool; PendingTools []string }
type Options struct { SystemPrompt string; Messages []ai.Message; Loop LoopConfig }
type Agent struct { /* private */ }
func New(Options) *Agent
// methods:
Prompt(context.Context, string) error
Continue(context.Context) error
Abort()
WaitForIdle(context.Context) error
Subscribe(func(context.Context, Event) error) func() // 返回退订函数
Reset() error
State() State // 深拷贝快照
```

Prompt/Continue 同步等待整个 run 和订阅完成。忙碌时再次 Prompt/Continue/Reset 返回 error；Abort 可并发调用。所有退出路径恢复 idle 并清理 pending；Reset 恢复 New 时基线。订阅回调可以重入 State/Abort，不允许同步递归 Prompt/WaitForIdle。订阅错误返回给调用方并清理状态。迟到工具 update 不再发送。State 的修改不得影响内部状态。

## P07 / providers/deepseek

```go
type Config struct { BaseURL string; Model string; APIKey string; MaxTokens int; Client *http.Client }
type Provider struct { /* private */ }
func New(Config) (*Provider,error)
func (p *Provider) Stream(context.Context, ai.Context) (<-chan ai.Event,error)
```

BaseURL 以 /v1 等 API 根路径结尾，追加 /chat/completions。只支持文本与 function tools；请求含 model、messages、stream:true、max_tokens；空 tools 不发 tools/tool_choice，thinking 固定 disabled。模型目录和 OAuth 不读取。HTTP 错误可直接返回 error，也可用 error terminal；不泄露 key。每次流只有一个 done/error terminal，之后关闭；所有事件深拷贝。SSE 支持 LF/CRLF、任意字节拆分、[DONE]、usage chunk、多个 tool index；toolCall JSON 必须完整才进入 done。缺失 finish_reason、未知 finish_reason、畸形 JSON 或断流均失败。stop/tool_calls/length 分别对应 stop/toolUse/length，保留 rawStopReason、model 与 usage；长度截断的残缺工具参数按错误结束。默认不自动重试；部分输出后绝不重放请求。

## P08 / tools/readfile

`New(root string) (agent.Tool,error)`：工具名 read，参数 path:string、offset?:integer>=1、limit?:integer>=1。使用 os.OpenRoot，拒绝绝对/逃逸路径、目录、非普通文件与图片/含 NUL 的二进制数据。读取 root 内相对路径；返回文本 block。默认最多 2000 行、50\*1024 字节，偏移按 1 开始；说明截断及后续 offset。空文件返回空文本；CRLF 原样保留；无尾换行仍按一行；首行超过字节限制返回说明，不截坏 UTF-8。关闭所有资源。context 已取消时不读；执行中的普通文件读取至少在每块/输出前检查取消。

## P09 / cmd/pith

标准 flags：`--root <dir> --prompt <text>`，可选 `--events`。环境变量 `PITH_BASE_URL`、`PITH_MODEL`、`PITH_API_KEY` 必须显式配置，`PITH_MAX_TURNS` 可选默认 20。纯 Go 二进制；stdout 最终回答，events 写 stderr，错误退出非零。events 模式输出不包含密钥。把 DeepSeek、agent.New 和 readfile.New 接起来，不自造第二套 agent loop。不得依赖 Node、Portsmith、migration 或源缓存；所有默认测试使用本地假模型，真实服务检查另行手动执行。
