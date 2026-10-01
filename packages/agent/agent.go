// This file is a Go port of packages/agent/src/agent.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Agent is the stateful wrapper around the low-level agent loop: it owns the
// transcript, emits lifecycle events, executes tools and exposes steering and
// follow-up queues. Go concurrency replaces the single-threaded JS event loop,
// so mutable state, queues and run lifecycle are guarded by mutexes; external
// callbacks (listeners and hooks) are never invoked while an internal lock is
// held.
package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// AgentInitialState seeds an Agent. SystemPrompt and Tools become the leading
// system message unless Messages already starts with one.
type AgentInitialState struct {
	SystemPrompt  *string
	Model         *aitypes.Model
	ThinkingLevel agenttypes.ThinkingLevel
	Tools         []agenttypes.AgentTool[any, any]
	Messages      []agenttypes.AgentMessage
}

// AgentOptions configures an Agent. StreamFn is required unless a process-wide
// default has been installed with SetDefaultStreamFn.
type AgentOptions struct {
	InitialState     *AgentInitialState
	ConvertToLlm     func(messages []agenttypes.AgentMessage) ([]aitypes.Message, error)
	TransformContext func(messages []agenttypes.AgentMessage, signal <-chan struct{}) ([]agenttypes.AgentMessage, error)
	StreamFn         agenttypes.StreamFn
	GetApiKey        func(provider string) (string, bool, error)
	OnPayload        func(payload any, model *aitypes.Model) (any, error)
	OnResponse       func(response aitypes.ProviderResponse, model *aitypes.Model)
	// OnProviderStreamEvent observes each parsed provider stream event before
	// Pi normalization. It is forwarded verbatim through every request's loop
	// configuration into the provider SimpleStreamOptions; a returned error
	// reaches the same provider/agent failure path as any other stream error.
	OnProviderStreamEvent      func(data any, model *aitypes.Model) error
	BeforeToolCall             func(context agenttypes.BeforeToolCallContext, signal <-chan struct{}) (*agenttypes.BeforeToolCallResult, error)
	AfterToolCall              func(context agenttypes.AfterToolCallContext, signal <-chan struct{}) (*agenttypes.AfterToolCallResult, error)
	FinishTurn                 agenttypes.FinishTurn
	PrepareRequest             agenttypes.PrepareRequest
	PrepareNextTurn            func(signal <-chan struct{}) (*agenttypes.AgentLoopTurnUpdate, error)
	PrepareNextTurnWithContext func(context agenttypes.PrepareNextTurnContext, signal <-chan struct{}) (*agenttypes.AgentLoopTurnUpdate, error)
	SteeringMode               agenttypes.QueueMode
	FollowUpMode               agenttypes.QueueMode
	SessionId                  string
	ThinkingBudgets            *aitypes.ThinkingBudgets
	Transport                  aitypes.Transport
	MaxRetryDelayMs            *int
	ToolExecution              agenttypes.ToolExecutionMode
}

// AgentListener receives agent lifecycle events in subscription order.
type AgentListener func(event agenttypes.AgentEvent, signal <-chan struct{}) error

type agentListenerEntry struct {
	id       uint64
	listener AgentListener
}

type activeRun struct {
	done      chan struct{}
	abort     chan struct{}
	abortOnce sync.Once
}

func (r *activeRun) signal() <-chan struct{} { return r.abort }

func (r *activeRun) abortRun() {
	r.abortOnce.Do(func() { close(r.abort) })
}

type agentState struct {
	messages         []agenttypes.AgentMessage
	model            *aitypes.Model
	thinkingLevel    agenttypes.ThinkingLevel
	tools            []agenttypes.AgentTool[any, any]
	isStreaming      bool
	streamingMessage *agenttypes.AgentMessage
	pendingToolCalls map[string]bool
	errorMessage     *string
}

// Agent is the stateful agent runtime.
type Agent struct {
	mu             sync.Mutex
	emitMu         sync.Mutex
	state          *agentState
	listeners      []agentListenerEntry
	nextListenerID uint64
	steeringQueue  *pendingMessageQueue
	followUpQueue  *pendingMessageQueue

	convertToLlm               func(messages []agenttypes.AgentMessage) ([]aitypes.Message, error)
	transformContext           func(messages []agenttypes.AgentMessage, signal <-chan struct{}) ([]agenttypes.AgentMessage, error)
	streamFunction             agenttypes.StreamFn
	getApiKey                  func(provider string) (string, bool, error)
	onPayload                  func(payload any, model *aitypes.Model) (any, error)
	onResponse                 func(response aitypes.ProviderResponse, model *aitypes.Model)
	onProviderStreamEvent      func(data any, model *aitypes.Model) error
	beforeToolCall             func(context agenttypes.BeforeToolCallContext, signal <-chan struct{}) (*agenttypes.BeforeToolCallResult, error)
	afterToolCall              func(context agenttypes.AfterToolCallContext, signal <-chan struct{}) (*agenttypes.AfterToolCallResult, error)
	finishTurn                 agenttypes.FinishTurn
	prepareRequest             agenttypes.PrepareRequest
	prepareNextTurn            func(signal <-chan struct{}) (*agenttypes.AgentLoopTurnUpdate, error)
	prepareNextTurnWithContext func(context agenttypes.PrepareNextTurnContext, signal <-chan struct{}) (*agenttypes.AgentLoopTurnUpdate, error)
	activeRun                  *activeRun
	sessionId                  string
	thinkingBudgets            *aitypes.ThinkingBudgets
	transport                  aitypes.Transport
	maxRetryDelayMs            *int
	toolExecution              agenttypes.ToolExecutionMode
}

// NewAgent constructs an Agent. It resolves the default stream function when
// the options omit one, so the returned error is a real construction failure.
func NewAgent(options AgentOptions) (*Agent, error) {
	streamFunction := options.StreamFn
	if streamFunction == nil {
		defaultFn, err := GetDefaultStreamFn()
		if err != nil {
			return nil, err
		}
		streamFunction = defaultFn
	}

	convertToLlm := options.ConvertToLlm
	if convertToLlm == nil {
		convertToLlm = defaultConvertToLlm
	}
	transport := options.Transport
	if transport == "" {
		transport = aitypes.TransportAuto
	}
	toolExecution := options.ToolExecution
	if toolExecution == "" {
		toolExecution = agenttypes.ToolExecutionParallel
	}
	steeringMode := options.SteeringMode
	if steeringMode == "" {
		steeringMode = agenttypes.QueueModeOneAtATime
	}
	followUpMode := options.FollowUpMode
	if followUpMode == "" {
		followUpMode = agenttypes.QueueModeOneAtATime
	}

	agent := &Agent{
		state:                      createMutableAgentState(options.InitialState),
		steeringQueue:              newPendingMessageQueue(steeringMode),
		followUpQueue:              newPendingMessageQueue(followUpMode),
		convertToLlm:               convertToLlm,
		transformContext:           options.TransformContext,
		streamFunction:             streamFunction,
		getApiKey:                  options.GetApiKey,
		onPayload:                  options.OnPayload,
		onResponse:                 options.OnResponse,
		onProviderStreamEvent:      options.OnProviderStreamEvent,
		beforeToolCall:             options.BeforeToolCall,
		afterToolCall:              options.AfterToolCall,
		finishTurn:                 options.FinishTurn,
		prepareRequest:             options.PrepareRequest,
		prepareNextTurn:            options.PrepareNextTurn,
		prepareNextTurnWithContext: options.PrepareNextTurnWithContext,
		sessionId:                  options.SessionId,
		thinkingBudgets:            options.ThinkingBudgets,
		transport:                  transport,
		maxRetryDelayMs:            options.MaxRetryDelayMs,
		toolExecution:              toolExecution,
	}
	return agent, nil
}

// Subscribe registers a lifecycle listener. The returned function removes it.
// Listener calls are awaited in subscription order and are part of run
// settlement.
func (a *Agent) Subscribe(listener AgentListener) func() {
	a.mu.Lock()
	id := a.nextListenerID
	a.nextListenerID++
	a.listeners = append(a.listeners, agentListenerEntry{id: id, listener: listener})
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		out := a.listeners[:0]
		for _, entry := range a.listeners {
			if entry.id != id {
				out = append(out, entry)
			}
		}
		a.listeners = out
	}
}

// State returns a snapshot of the current agent state.
func (a *Agent) State() agenttypes.AgentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agenttypes.AgentState{
		SystemPrompt:     aiutils.GetCurrentSystemPrompt(transcriptMessages(a.state.messages)),
		Model:            a.state.model,
		ThinkingLevel:    a.state.thinkingLevel,
		Tools:            append([]agenttypes.AgentTool[any, any](nil), a.state.tools...),
		Messages:         append([]agenttypes.AgentMessage(nil), a.state.messages...),
		IsStreaming:      a.state.isStreaming,
		StreamingMessage: copyAgentMessagePtr(a.state.streamingMessage),
		PendingToolCalls: pendingIDs(a.state.pendingToolCalls),
		ErrorMessage:     copyStringPtr(a.state.errorMessage),
	}
}

// SteeringMode returns how queued steering messages are drained.
func (a *Agent) SteeringMode() agenttypes.QueueMode { return a.steeringQueue.modeOf() }

// SetSteeringMode controls how queued steering messages are drained.
func (a *Agent) SetSteeringMode(mode agenttypes.QueueMode) { a.steeringQueue.setMode(mode) }

// FollowUpMode returns how queued follow-up messages are drained.
func (a *Agent) FollowUpMode() agenttypes.QueueMode { return a.followUpQueue.modeOf() }

// SetFollowUpMode controls how queued follow-up messages are drained.
func (a *Agent) SetFollowUpMode(mode agenttypes.QueueMode) { a.followUpQueue.setMode(mode) }

// Steer queues a message to be injected after the current assistant turn.
func (a *Agent) Steer(message agenttypes.AgentMessage) { a.steeringQueue.enqueue(message) }

// FollowUp queues a message to run only after the agent would otherwise stop.
func (a *Agent) FollowUp(message agenttypes.AgentMessage) { a.followUpQueue.enqueue(message) }

// ClearSteeringQueue removes all queued steering messages.
func (a *Agent) ClearSteeringQueue() { a.steeringQueue.clear() }

// ClearFollowUpQueue removes all queued follow-up messages.
func (a *Agent) ClearFollowUpQueue() { a.followUpQueue.clear() }

// ClearAllQueues removes all queued steering and follow-up messages.
func (a *Agent) ClearAllQueues() {
	a.ClearSteeringQueue()
	a.ClearFollowUpQueue()
}

// HasQueuedMessages reports whether either queue still contains messages.
func (a *Agent) HasQueuedMessages() bool {
	return a.steeringQueue.hasItems() || a.followUpQueue.hasItems()
}

// PeekQueuedMessages previews the messages selected for the next turn without
// consuming them. Steering takes priority over follow-ups.
func (a *Agent) PeekQueuedMessages() []agenttypes.AgentMessage {
	steering := a.steeringQueue.peek()
	if len(steering) > 0 {
		return steering
	}
	return a.followUpQueue.peek()
}

// Signal returns the abort signal for the current run, or nil when idle.
func (a *Agent) Signal() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun == nil {
		return nil
	}
	return a.activeRun.signal()
}

// Abort aborts the current run, if one is active.
func (a *Agent) Abort() {
	a.mu.Lock()
	run := a.activeRun
	a.mu.Unlock()
	if run != nil {
		run.abortRun()
	}
}

// WaitForIdle waits until the current run and all awaited listeners finish, or
// ctx is done.
func (a *Agent) WaitForIdle(ctx context.Context) error {
	a.mu.Lock()
	run := a.activeRun
	a.mu.Unlock()
	if run == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reset clears conversation state and queues while retaining the replayed
// system prompt baseline.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun != nil {
		return errors.New("Agent is already processing. Wait for completion before resetting.")
	}

	baseline := aiutils.GetCurrentSystemMessage(transcriptMessages(a.state.messages))
	if baseline != nil {
		a.state.messages = []agenttypes.AgentMessage{agentMessageFrom(aitypes.NewSystemMessageVariant(*baseline))}
	} else {
		a.state.messages = []agenttypes.AgentMessage{}
	}
	a.state.isStreaming = false
	a.state.streamingMessage = nil
	a.state.pendingToolCalls = map[string]bool{}
	a.state.errorMessage = nil
	a.followUpQueue.clear()
	a.steeringQueue.clear()
	return nil
}

// Prompt starts a new run from a batch of messages.
func (a *Agent) Prompt(messages []agenttypes.AgentMessage) error {
	if a.hasActiveRun() {
		return errors.New("Agent is already processing a prompt. Use steer() or followUp() to queue messages, or wait for completion.")
	}
	return a.runPromptMessages(messages, false)
}

// PromptString starts a new run from text plus optional images.
func (a *Agent) PromptString(text string, images []aitypes.ImageContent) error {
	if a.hasActiveRun() {
		return errors.New("Agent is already processing a prompt. Use steer() or followUp() to queue messages, or wait for completion.")
	}
	return a.runPromptMessages(a.normalizePromptInput(text, images), false)
}

// Continue continues from the current transcript. The last message must be a
// user or tool-result message.
func (a *Agent) Continue() error {
	if a.hasActiveRun() {
		return errors.New("Agent is already processing. Wait for completion before continuing.")
	}

	a.mu.Lock()
	messages := append([]agenttypes.AgentMessage(nil), a.state.messages...)
	allSystem := len(messages) > 0
	for _, message := range messages {
		if messageRole(message) != aitypes.SystemMessageRole {
			allSystem = false
			break
		}
	}
	lastRole := ""
	if len(messages) > 0 {
		lastRole = messageRole(messages[len(messages)-1])
	}
	a.mu.Unlock()

	if len(messages) == 0 || allSystem {
		return errors.New("No messages to continue from")
	}
	if lastRole == aitypes.AssistantMessageRole {
		queuedSteering := a.steeringQueue.drain()
		if len(queuedSteering) > 0 {
			return a.runPromptMessages(queuedSteering, true)
		}
		queuedFollowUps := a.followUpQueue.drain()
		if len(queuedFollowUps) > 0 {
			return a.runPromptMessages(queuedFollowUps, false)
		}
		return errors.New("Cannot continue from message role: assistant")
	}
	return a.runContinuation()
}

func (a *Agent) hasActiveRun() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeRun != nil
}

func (a *Agent) normalizePromptInput(text string, images []aitypes.ImageContent) []agenttypes.AgentMessage {
	content := []aitypes.ContentBlock{aitypes.TextBlock(text)}
	for _, image := range images {
		content = append(content, aitypes.ImageBlock(image.Data, image.MimeType))
	}
	message := aitypes.NewUserMessageBlocks(content, float64(time.Now().UnixMilli()))
	return []agenttypes.AgentMessage{agentMessageFrom(aitypes.NewUserMessageVariant(message))}
}

func (a *Agent) runPromptMessages(messages []agenttypes.AgentMessage, skipInitialSteeringPoll bool) error {
	return a.runWithLifecycle(func(signal <-chan struct{}) error {
		_, err := RunAgentLoop(messages, a.createContextSnapshot(), a.createLoopConfig(skipInitialSteeringPoll), a.processEvents, signal, a.streamFunction)
		return err
	})
}

func (a *Agent) runContinuation() error {
	return a.runWithLifecycle(func(signal <-chan struct{}) error {
		_, err := RunAgentLoopContinue(a.createContextSnapshot(), a.createLoopConfig(false), a.processEvents, signal, a.streamFunction)
		return err
	})
}

func (a *Agent) createContextSnapshot() agenttypes.AgentContext {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agenttypes.AgentContext{
		Messages: append([]agenttypes.AgentMessage(nil), a.state.messages...),
		Tools:    append([]agenttypes.AgentTool[any, any](nil), a.state.tools...),
	}
}

func (a *Agent) createLoopConfig(skipInitialSteeringPoll bool) agenttypes.AgentLoopConfig {
	a.mu.Lock()
	model := a.state.model
	level := a.state.thinkingLevel
	sessionId := a.sessionId
	transport := a.transport
	thinkingBudgets := a.thinkingBudgets
	maxRetryDelayMs := a.maxRetryDelayMs
	toolExecution := a.toolExecution
	convertToLlm := a.convertToLlm
	transformContext := a.transformContext
	getApiKey := a.getApiKey
	beforeToolCall := a.beforeToolCall
	afterToolCall := a.afterToolCall
	finishTurn := a.finishTurn
	prepareRequest := a.prepareRequest
	prepareNextTurn := a.prepareNextTurn
	prepareNextTurnWithContext := a.prepareNextTurnWithContext
	onPayload := a.onPayload
	onResponse := a.onResponse
	onProviderStreamEvent := a.onProviderStreamEvent
	a.mu.Unlock()

	options := aitypes.SimpleStreamOptions{
		StreamOptions: aitypes.StreamOptions{
			ProviderRequestOptions: aitypes.ProviderRequestOptions{
				OnPayload:             onPayload,
				OnResponse:            onResponse,
				OnProviderStreamEvent: onProviderStreamEvent,
				MaxRetryDelayMs:       maxRetryDelayMs,
			},
			Transport: transportPtr(transport),
		},
		ThinkingBudgets: thinkingBudgets,
	}
	if sessionId != "" {
		value := sessionId
		options.SessionId = &value
	}
	config := agenttypes.AgentLoopConfig{
		SimpleStreamOptions: options,
		Model:               model,
		ConvertToLlm:        convertToLlm,
		TransformContext:    transformContext,
		GetApiKey:           getApiKey,
		BeforeToolCall:      beforeToolCall,
		AfterToolCall:       afterToolCall,
		FinishTurn:          finishTurn,
		PrepareRequest:      prepareRequest,
		ToolExecution:       toolExecution,
	}
	if level != agenttypes.ThinkingOff {
		value := level
		config.Reasoning = &value
	}
	if prepareNextTurnWithContext != nil || prepareNextTurn != nil {
		config.PrepareNextTurn = func(context agenttypes.PrepareNextTurnContext) (*agenttypes.AgentLoopTurnUpdate, error) {
			if prepareNextTurnWithContext != nil {
				return prepareNextTurnWithContext(context, a.Signal())
			}
			return prepareNextTurn(a.Signal())
		}
	}

	skip := skipInitialSteeringPoll
	config.GetSteeringMessages = func() ([]agenttypes.AgentMessage, error) {
		if skip {
			skip = false
			return []agenttypes.AgentMessage{}, nil
		}
		return a.steeringQueue.drain(), nil
	}
	config.GetFollowUpMessages = func() ([]agenttypes.AgentMessage, error) {
		return a.followUpQueue.drain(), nil
	}
	return config
}

func (a *Agent) runWithLifecycle(executor func(signal <-chan struct{}) error) error {
	a.mu.Lock()
	if a.activeRun != nil {
		a.mu.Unlock()
		return errors.New("Agent is already processing.")
	}
	run := &activeRun{done: make(chan struct{}), abort: make(chan struct{})}
	a.activeRun = run
	a.state.isStreaming = true
	a.state.streamingMessage = nil
	a.state.errorMessage = nil
	a.mu.Unlock()

	err := executor(run.signal())
	if err != nil {
		a.handleRunFailure(err, signalAborted(run.signal()))
	}
	a.finishRun(run)
	return nil
}

func (a *Agent) handleRunFailure(runErr error, aborted bool) {
	a.mu.Lock()
	model := a.state.model
	a.mu.Unlock()

	api := aitypes.Api("")
	provider := aitypes.ProviderId("")
	modelID := ""
	if model != nil {
		api = model.Api
		provider = model.Provider
		modelID = model.Id
	}
	stopReason := aitypes.StopReasonError
	if aborted {
		stopReason = aitypes.StopReasonAborted
	}
	errorMessage := ""
	if runErr != nil {
		errorMessage = runErr.Error()
	}
	failure := aitypes.AssistantMessage{
		Role:         aitypes.AssistantMessageRole,
		Content:      []aitypes.ContentBlock{aitypes.TextBlock("")},
		Api:          api,
		Provider:     provider,
		Model:        modelID,
		Usage:        emptyUsage(),
		StopReason:   stopReason,
		ErrorMessage: &errorMessage,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
	failureMessage := agentMessageFrom(aitypes.NewAssistantMessageVariant(failure))
	_ = a.processEvents(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &failureMessage})
	_ = a.processEvents(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageEnd, Message: &failureMessage})
	_ = a.processEvents(agenttypes.AgentEvent{Type: agenttypes.AgentEventTurnEnd, Message: &failureMessage, ToolResults: []aitypes.ToolResultMessage{}})
	_ = a.processEvents(agenttypes.AgentEvent{Type: agenttypes.AgentEventAgentEnd, Messages: []agenttypes.AgentMessage{failureMessage}})
}

func (a *Agent) finishRun(run *activeRun) {
	a.mu.Lock()
	a.state.isStreaming = false
	a.state.streamingMessage = nil
	a.state.pendingToolCalls = map[string]bool{}
	if a.activeRun == run {
		a.activeRun = nil
	}
	a.mu.Unlock()
	close(run.done)
}

func (a *Agent) processEvents(event agenttypes.AgentEvent) error {
	a.emitMu.Lock()
	defer a.emitMu.Unlock()

	a.mu.Lock()
	switch event.Type {
	case agenttypes.AgentEventMessageStart, agenttypes.AgentEventMessageUpdate:
		a.state.streamingMessage = copyAgentMessagePtr(event.Message)
	case agenttypes.AgentEventMessageEnd:
		a.state.streamingMessage = nil
		if event.Message != nil {
			a.state.messages = append(a.state.messages, *event.Message)
		}
	case agenttypes.AgentEventToolExecutionStart:
		if event.ToolCallId != nil {
			a.state.pendingToolCalls[*event.ToolCallId] = true
		}
	case agenttypes.AgentEventToolExecutionEnd:
		if event.ToolCallId != nil {
			delete(a.state.pendingToolCalls, *event.ToolCallId)
		}
	case agenttypes.AgentEventTurnEnd:
		if message, ok := assistantMessageOf(event.Message); ok && message.ErrorMessage != nil {
			text := *message.ErrorMessage
			a.state.errorMessage = &text
		}
	case agenttypes.AgentEventAgentEnd:
		a.state.streamingMessage = nil
	}
	var signal <-chan struct{}
	if a.activeRun != nil {
		signal = a.activeRun.signal()
	}
	listeners := append([]agentListenerEntry(nil), a.listeners...)
	a.mu.Unlock()

	if signal == nil {
		return errors.New("Agent listener invoked outside active run")
	}
	for _, entry := range listeners {
		if err := entry.listener(event, signal); err != nil {
			return err
		}
	}
	return nil
}

// pendingMessageQueue is a mutex-guarded queue of queued agent messages.
type pendingMessageQueue struct {
	mu       sync.Mutex
	mode     agenttypes.QueueMode
	messages []agenttypes.AgentMessage
}

func newPendingMessageQueue(mode agenttypes.QueueMode) *pendingMessageQueue {
	return &pendingMessageQueue{mode: mode}
}

func (q *pendingMessageQueue) modeOf() agenttypes.QueueMode {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.mode
}

func (q *pendingMessageQueue) setMode(mode agenttypes.QueueMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.mode = mode
}

func (q *pendingMessageQueue) enqueue(message agenttypes.AgentMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, message)
}

func (q *pendingMessageQueue) hasItems() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.messages) > 0
}

func (q *pendingMessageQueue) peek() []agenttypes.AgentMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.messages) == 0 {
		return nil
	}
	if q.mode == agenttypes.QueueModeAll {
		return append([]agenttypes.AgentMessage(nil), q.messages...)
	}
	return []agenttypes.AgentMessage{q.messages[0]}
}

func (q *pendingMessageQueue) drain() []agenttypes.AgentMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.messages) == 0 {
		return nil
	}
	var drained []agenttypes.AgentMessage
	if q.mode == agenttypes.QueueModeAll {
		drained = append([]agenttypes.AgentMessage(nil), q.messages...)
	} else {
		drained = []agenttypes.AgentMessage{q.messages[0]}
	}
	q.messages = q.messages[len(drained):]
	return drained
}

func (q *pendingMessageQueue) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = nil
}

// --- helpers -------------------------------------------------------------

func defaultConvertToLlm(messages []agenttypes.AgentMessage) ([]aitypes.Message, error) {
	out := []aitypes.Message{}
	for _, message := range messages {
		if message.Message == nil {
			continue
		}
		switch message.Message.Role {
		case aitypes.SystemMessageRole, aitypes.UserMessageRole, aitypes.AssistantMessageRole, aitypes.ToolResultMessageRole:
			out = append(out, *message.Message)
		}
	}
	return out, nil
}

func createMutableAgentState(initial *AgentInitialState) *agentState {
	tools := []agenttypes.AgentTool[any, any]{}
	messages := []agenttypes.AgentMessage{}
	var systemPrompt *string
	if initial != nil {
		if initial.Tools != nil {
			tools = append(tools, initial.Tools...)
		}
		if initial.Messages != nil {
			messages = append(messages, initial.Messages...)
		}
		systemPrompt = initial.SystemPrompt
	}

	declarations := make([]aitypes.Tool, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, aiutils.ToToolDeclaration(tool.Tool))
	}
	initialMessage := aiutils.CreateInitialSystemMessage(systemPrompt, declarations)
	startsWithSystem := len(messages) > 0 && messageRole(messages[0]) == aitypes.SystemMessageRole
	if !startsWithSystem && initialMessage != nil {
		leading := agentMessageFrom(aitypes.NewSystemMessageVariant(*initialMessage))
		messages = append([]agenttypes.AgentMessage{leading}, messages...)
	}

	state := &agentState{
		messages:         messages,
		model:            defaultModel(),
		thinkingLevel:    agenttypes.ThinkingOff,
		tools:            tools,
		pendingToolCalls: map[string]bool{},
	}
	if initial != nil {
		if initial.Model != nil {
			state.model = initial.Model
		}
		if initial.ThinkingLevel != "" {
			state.thinkingLevel = initial.ThinkingLevel
		}
	}
	return state
}

func defaultModel() *aitypes.Model {
	return &aitypes.Model{
		Id:            "unknown",
		Name:          "unknown",
		Api:           aitypes.Api("unknown"),
		Provider:      aitypes.ProviderId("unknown"),
		BaseUrl:       "",
		Reasoning:     false,
		Input:         []aitypes.ModelInputModality{},
		Cost:          aitypes.ModelCost{},
		ContextWindow: 0,
		MaxTokens:     0,
	}
}

func emptyUsage() aitypes.Usage {
	return aitypes.Usage{Cost: aitypes.UsageCost{}}
}

func pendingIDs(pending map[string]bool) []string {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	return ids
}

func copyAgentMessagePtr(message *agenttypes.AgentMessage) *agenttypes.AgentMessage {
	if message == nil {
		return nil
	}
	value := *message
	return &value
}

func copyStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func assistantMessageOf(message *agenttypes.AgentMessage) (aitypes.AssistantMessage, bool) {
	if message == nil || message.Message == nil || message.Message.Assistant == nil {
		return aitypes.AssistantMessage{}, false
	}
	return *message.Message.Assistant, true
}

func transportPtr(transport aitypes.Transport) *aitypes.Transport {
	value := transport
	return &value
}
