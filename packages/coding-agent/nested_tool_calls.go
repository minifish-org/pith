// Nested tool calls for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/nested-tool-calls.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. A tool may call other
// tools while it runs (for example a Codemode script calling `tools.x()`). Those
// nested calls are invisible to the agent loop, so the session runs each one
// through the same pipeline the model-issued calls use: schema validation and
// the Before/After permission hooks, via agent.RunToolCall.
//
// The recorder bounds the nested-call record left on the parent tool result and
// sums the nested usage. Go is synchronous where the source is async; exclusive
// nested calls are serialized by a mutex.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	agentcore "github.com/minifish-org/pith/packages/agent"
	usageutil "github.com/minifish-org/pith/packages/agent/harness/utils/usage"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// Nested call record limits, matching the upstream NESTED_CALL_LIMITS.
const (
	nestedMaxCalls                = 256
	nestedMaxArgumentBytesPerCall = 8 * 1024
	nestedMaxArgumentBytesTotal   = 32 * 1024
	nestedMaxErrorChars           = 500
)

// Nested tool-call record statuses.
const (
	nestedStatusUnfinished = "unfinished"
	nestedStatusOK         = "ok"
	nestedStatusError      = "error"
)

// NestedToolCallRecord is one nested call in a parent tool result.
type NestedToolCallRecord struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	ArgumentsBytes int             `json:"argumentsBytes,omitempty"`
	DurationMs     int64           `json:"durationMs,omitempty"`
	Error          string          `json:"error,omitempty"`
}

// NestedToolCalls is the nested-call record of one model-issued call. Complete
// is false when calls or arguments were dropped.
type NestedToolCalls struct {
	Calls    []NestedToolCallRecord `json:"calls"`
	Complete bool                   `json:"complete"`
}

// NestedCallSummary is what nested calls leave on the parent tool result.
type NestedCallSummary struct {
	Calls *NestedToolCalls
	Usage *aitypes.Usage
}

// NestedCallRecorder collects the nested calls of one model-issued tool call.
type NestedCallRecorder struct {
	mu            sync.Mutex
	calls         []NestedToolCallRecord
	startedAt     map[int]time.Time
	complete      bool
	argumentBytes int
	usage         *aitypes.Usage
}

// NewNestedCallRecorder builds an empty recorder.
func NewNestedCallRecorder() *NestedCallRecorder {
	return &NestedCallRecorder{complete: true, startedAt: map[int]time.Time{}}
}

// Start records a call as it starts. The returned index is -1 when the call is
// dropped because the record is full.
func (r *NestedCallRecorder) Start(call aitypes.ToolCall) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) >= nestedMaxCalls {
		r.complete = false
		return -1
	}
	record := NestedToolCallRecord{ID: call.Id, Name: call.Name, Status: nestedStatusUnfinished}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage("{}")
	}
	bytes := len(arguments)
	if bytes > nestedMaxArgumentBytesPerCall || r.argumentBytes+bytes > nestedMaxArgumentBytesTotal {
		record.ArgumentsBytes = bytes
		r.complete = false
	} else {
		record.Arguments = append(json.RawMessage(nil), arguments...)
		r.argumentBytes += bytes
	}
	index := len(r.calls)
	r.calls = append(r.calls, record)
	r.startedAt[index] = time.Now()
	return index
}

// Finish marks a recorded call complete.
func (r *NestedCallRecorder) Finish(index int, isError bool, errorText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.calls) {
		return
	}
	record := &r.calls[index]
	if isError {
		record.Status = nestedStatusError
	} else {
		record.Status = nestedStatusOK
	}
	if started, ok := r.startedAt[index]; ok {
		record.DurationMs = time.Since(started).Milliseconds()
		delete(r.startedAt, index)
	}
	if isError && errorText != "" {
		if len(errorText) > nestedMaxErrorChars {
			errorText = errorText[:nestedMaxErrorChars]
		}
		record.Error = errorText
	}
}

// AddUsage sums the usage of a nested result.
func (r *NestedCallRecorder) AddUsage(usage aitypes.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usage == nil {
		combined := usage
		r.usage = &combined
		return
	}
	combined := usageutil.AddUsage(*r.usage, usage)
	r.usage = &combined
}

// Snapshot returns the record so far, or nil when no nested call was made.
func (r *NestedCallRecorder) Snapshot() *NestedToolCalls {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 && r.complete {
		return nil
	}
	calls := make([]NestedToolCallRecord, len(r.calls))
	copy(calls, r.calls)
	complete := r.complete
	for _, call := range calls {
		if call.Status == nestedStatusUnfinished {
			complete = false
		}
	}
	return &NestedToolCalls{Calls: calls, Complete: complete}
}

// TotalUsage returns the summed usage, if any.
func (r *NestedCallRecorder) TotalUsage() *aitypes.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

// NestedToolExecutionEvent is a nested tool_execution_* event.
type NestedToolExecutionEvent struct {
	Type             string
	ToolCallID       string
	ToolName         string
	ParentToolCallID string
	Args             json.RawMessage
	Result           *agenttypes.AgentToolResult[any]
	IsError          bool
}

// NestedToolCallHost supplies the tools and pipeline the runner uses.
type NestedToolCallHost interface {
	// ResolveTool returns the registered tool or false when it is unknown.
	ResolveTool(name string) (agenttypes.AgentTool[any, any], bool)
	// IsSequential reports whether nested calls run exclusively.
	IsSequential() bool
	// RunToolCall runs one nested call through the tool pipeline.
	RunToolCall(call aitypes.ToolCall, parentToolCallID string, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) agenttypes.ToolCallOutcome
	// Emit publishes a nested execution event.
	Emit(event NestedToolExecutionEvent)
}

// NestedToolCallRunner runs nested calls on behalf of model-issued calls.
type NestedToolCallRunner struct {
	host NestedToolCallHost

	mu     sync.Mutex
	scopes map[string]*nestedCallScope

	// exclusive serializes nested calls that must not run concurrently.
	exclusive sync.Mutex
}

type nestedCallScope struct {
	recorder  *NestedCallRecorder
	nextID    int
	holdsLock bool
}

// NewNestedToolCallRunner builds a runner for a host.
func NewNestedToolCallRunner(host NestedToolCallHost) *NestedToolCallRunner {
	return &NestedToolCallRunner{host: host, scopes: map[string]*nestedCallScope{}}
}

// Execute runs one nested call on behalf of callerID. The nested call id is
// "<callerID>/<n>". Tool failures come back as IsError outcomes, never panics.
func (r *NestedToolCallRunner) Execute(
	callerID string,
	name string,
	arguments json.RawMessage,
	signal <-chan struct{},
	onUpdate agenttypes.AgentToolUpdateCallback[any],
) agenttypes.ToolCallOutcome {
	r.mu.Lock()
	scope, ok := r.scopes[callerID]
	if !ok {
		scope = &nestedCallScope{recorder: NewNestedCallRecorder(), nextID: 1}
		r.scopes[callerID] = scope
	}
	callID := callerID + "/" + strconv.Itoa(scope.nextID)
	scope.nextID++
	r.mu.Unlock()

	call := aitypes.ToolCall{Id: callID, Name: name, Arguments: arguments}
	record := scope.recorder.Start(call)
	if r.host != nil {
		r.host.Emit(NestedToolExecutionEvent{
			Type: "tool_execution_start", ToolCallID: callID, ToolName: name, ParentToolCallID: callerID, Args: arguments,
		})
	}

	exclusive := r.isExclusive(scope, name)
	if exclusive {
		r.exclusive.Lock()
		defer r.exclusive.Unlock()
	}

	var outcome agenttypes.ToolCallOutcome
	if r.host == nil {
		outcome = agenttypes.ToolCallOutcome{ToolCall: call, IsError: true}
	} else {
		outcome = r.host.RunToolCall(call, callerID, signal, onUpdate)
	}

	scope.recorder.Finish(record, outcome.IsError, toolResultText(ToolResult{Content: outcome.Result.Content, IsError: outcome.IsError}))
	if outcome.Result.Usage != nil {
		scope.recorder.AddUsage(*outcome.Result.Usage)
	}
	if r.host != nil {
		result := outcome.Result
		r.host.Emit(NestedToolExecutionEvent{
			Type: "tool_execution_end", ToolCallID: callID, ToolName: name, ParentToolCallID: callerID,
			Result: &result, IsError: outcome.IsError,
		})
	}
	return outcome
}

func (r *NestedToolCallRunner) isExclusive(scope *nestedCallScope, name string) bool {
	if scope.holdsLock {
		return false
	}
	if r.host == nil {
		return true
	}
	if r.host.IsSequential() {
		return true
	}
	if tool, ok := r.host.ResolveTool(name); ok && tool.ExecutionMode == agenttypes.ToolExecutionSequential {
		return true
	}
	return false
}

// TakeRecord removes and returns the record of a model-issued call's nested
// calls.
func (r *NestedToolCallRunner) TakeRecord(toolCallID string) *NestedCallSummary {
	r.mu.Lock()
	scope, ok := r.scopes[toolCallID]
	if ok {
		delete(r.scopes, toolCallID)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return &NestedCallSummary{Calls: scope.recorder.Snapshot(), Usage: scope.recorder.TotalUsage()}
}

// Clear drops every pending scope.
func (r *NestedToolCallRunner) Clear() {
	r.mu.Lock()
	r.scopes = map[string]*nestedCallScope{}
	r.mu.Unlock()
}

// RegistryNestedHost runs nested calls against a ToolRegistry. Every call goes
// through agent.RunToolCall so schema validation and the registry's Before/After
// hooks apply, and the deny list is enforced before execution.
type RegistryNestedHost struct {
	registry *ToolRegistry
	hooks    ToolHooks
	// Events receives nested execution events when non-nil.
	Events func(NestedToolExecutionEvent)
}

// NewRegistryNestedHost builds a nested-call host for a registry.
func NewRegistryNestedHost(registry *ToolRegistry) *RegistryNestedHost {
	host := &RegistryNestedHost{registry: registry}
	if registry != nil {
		host.hooks = registry.Hooks()
	}
	return host
}

// ResolveTool implements NestedToolCallHost.
func (h *RegistryNestedHost) ResolveTool(name string) (agenttypes.AgentTool[any, any], bool) {
	if h.registry == nil {
		return agenttypes.AgentTool[any, any]{}, false
	}
	return h.registry.nestedAgentTool(name)
}

// IsSequential implements NestedToolCallHost. Nested calls default to
// concurrent, matching a parallel tool-execution agent loop.
func (h *RegistryNestedHost) IsSequential() bool { return false }

// RunToolCall implements NestedToolCallHost.
func (h *RegistryNestedHost) RunToolCall(call aitypes.ToolCall, _ string, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) agenttypes.ToolCallOutcome {
	if h.registry != nil && h.registry.isDenied(call.Name) {
		message := "tool " + call.Name + " is denied"
		return agenttypes.ToolCallOutcome{
			ToolCall: call,
			Result:   agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock(message)}, IsError: true},
			IsError:  true,
		}
	}
	tool, ok := h.ResolveTool(call.Name)
	if !ok {
		message := "unknown tool: " + call.Name
		return agenttypes.ToolCallOutcome{
			ToolCall: call,
			Result:   agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock(message)}, IsError: true},
			IsError:  true,
		}
	}
	ctx := contextFromSignal(signal)
	options := agenttypes.RunToolCallOptions{
		Tools:  []agenttypes.AgentTool[any, any]{tool},
		Signal: signal,
	}
	if onUpdate != nil {
		options.OnUpdate = onUpdate
	}
	if h.hooks.Before != nil {
		options.BeforeToolCall = func(c agenttypes.BeforeToolCallContext, _ <-chan struct{}) (*agenttypes.BeforeToolCallResult, error) {
			if err := h.hooks.Before(ctx, ToolCall{ID: c.ToolCall.Id, Name: c.ToolCall.Name, Arguments: c.ToolCall.Arguments}); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	if h.hooks.After != nil {
		options.AfterToolCall = func(c agenttypes.AfterToolCallContext, _ <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			result := ToolResult{
				Content:           c.Result.Content,
				StructuredContent: c.Result.StructuredContent,
				IsError:           c.IsError,
			}
			if details, err := json.Marshal(c.Result.Details); err == nil && string(details) != "null" {
				result.Details = details
			}
			transformed, err := h.hooks.After(ctx, ToolCall{ID: c.ToolCall.Id, Name: c.ToolCall.Name, Arguments: c.ToolCall.Arguments}, result)
			if err != nil {
				return nil, err
			}
			override := &agenttypes.AfterToolCallResult{
				Content:           transformed.Content,
				StructuredContent: transformed.StructuredContent,
			}
			isError := transformed.IsError
			override.IsError = &isError
			if len(transformed.Details) > 0 {
				override.Details = decodeDetails(transformed.Details)
			}
			return override, nil
		}
	}
	return agentcore.RunToolCall(call, options)
}

// Emit implements NestedToolCallHost.
func (h *RegistryNestedHost) Emit(event NestedToolExecutionEvent) {
	if h.Events != nil {
		h.Events(event)
	}
}

func (r *ToolRegistry) isDenied(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deny[name]
}
