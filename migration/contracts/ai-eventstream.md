# ai / ai-foundation / eventstream：已冻结的首个执行步骤

正式目标包：`github.com/minifish-org/pith/packages/ai/utils/eventstream`，package `eventstream`。

来源：固定 Pi `f07218c4d4bbc12bef056a7058c3dd49dfe41abe` 的 `packages/ai/src/utils/event-stream.ts` 中 **FifoQueue 与通用 EventStream**；测试参考 `packages/ai/test/event-stream.test.ts`。完整来源文件可读，但本步骤不实现 AssistantMessageEventStream/createAssistantMessageEventStream；它们仍在 ai-foundation 的后续范围内，必须等完整 AI 消息类型就绪。不得创建假 wrapper 或将整个 ai 标记完成。

```go
type StreamItem[T any] struct { Value T; Done bool }
type EventStream[T any, R any] struct { /* implementation-owned state */ }
func NewEventStream[T any, R any](isComplete func(T) bool, extractResult func(T) R) *EventStream[T,R]
func (*EventStream[T,R]) Push(T)
func (*EventStream[T,R]) Next() <-chan StreamItem[T]
func (*EventStream[T,R]) End(*R)
func (*EventStream[T,R]) Result(context.Context) (R, error)
```

- 保留 FIFO 缓冲和等待消费者次序，不广播。Next 在返回之前登记消费者，返回容量1的单次结果 channel；不能只关闭空 channel 当作 Done。
- 完成事件本身仍可消费，完成之后 Push 被忽略。End 后先排空队列，再返回 Done。End(nil) 唤醒当前等待者但不确定最终结果；后续 End(&zero) 仍可提供结果。第一次结果获胜，多次 Result 相同。
- Go 适配：Result 支持 context 取消；已经确定的结果优先于已取消 context。R 本身可为指针，End 的外层 nil 表示未提供，内层 nil 仍是合法结果。
- 方法可并发使用，无数据竞争。并发 Push 的总顺序由内部接收顺序决定；不能丢失/重复事件。外部回调不持内部状态锁调用；允许 isComplete/extractResult 重入 Next/End，不能因回调 panic 留住锁。回调 panic 可原样向调用者传播。
- 不要求调用者从外部关闭任何 channel。未完成流上 Result 等待依赖 context；不得忙循环或每个查询泄漏 goroutine。
- FifoQueue 可内置在该文件，无须导出；用不会每次删除首元素搬动整个数组的实现。长期消费应释放已出队对象引用。
- 不引入第三方依赖。正式 go.mod/go.sum 由准备阶段提供且不可修改。保留 Pi MIT 归属和来源路径。

输出：`event_stream.go`、`event_stream_test.go`（位于上面的正式包目录），可附 NOTES.md。自测使用普通 Test 前缀，不能使用保留的 TestPortsmithJudge。

独立 judge 共7个具名入口，含7条重新从固定 TS 运行得到的 trace，以及 Go 的取消/并发/泛型扩展检查。judge 编译、错误样本、FIFO 变异和保留行为正例的审计见 `../readiness/ai-eventstream.json`。这些是验收材料自检，**不是正式 Go 实现已完成**。
