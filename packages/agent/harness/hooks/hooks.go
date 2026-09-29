// Package harnesshooks is the Go port of
// packages/agent/src/harness/hooks.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The registry keeps hook handlers in registration order, snapshots the
// registration list before an aggregate run, and never invokes a handler while
// holding its internal lock. Handler failures are reported through the supplied
// reporter and do not stop the remaining handlers, except for the fail-closed
// `before_drive` hook.
package harnesshooks

import (
	"context"
	"errors"
	"sync"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnessexecution "github.com/minifish-org/pith/packages/agent/harness/execution"
	harnesstelemetry "github.com/minifish-org/pith/packages/agent/harness/telemetry"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// Context is the invocation-scoped harness context.
type Context = harnesscontext.Context

// HookName is the stable hook discriminator.
type HookName string

// Hook names.
const (
	HookBeforeRun        HookName = "before_run"
	HookBeforeDrive      HookName = "before_drive"
	HookBeforeRunEnd     HookName = "before_run_end"
	HookTransformContext HookName = "transform_context"
	HookBeforeRequest    HookName = "before_request"
	HookBeforePayload    HookName = "before_payload"
	HookAfterResponse    HookName = "after_response"
	HookBeforeTool       HookName = "before_tool"
	HookAfterTool        HookName = "after_tool"
	HookBeforeCompaction HookName = "before_compaction"
	HookBeforeNavigation HookName = "before_navigation"
)

// HookHandler handles one hook invocation. Events and results are the typed
// values defined below.
type HookHandler func(ctx Context, event any) (any, error)

// HookErrorReporter reports a handler failure.
type HookErrorReporter func(ctx Context, err error, hook HookName, lane string)

// HookRegistrationOptions are optional registration metadata.
type HookRegistrationOptions struct {
	ID *string
}

type hookRegistration struct {
	id      *string
	handler HookHandler
}

// HookRegistry is an ordered hook registry.
type HookRegistry struct {
	mu            sync.Mutex
	registrations map[HookName][]*hookRegistration
	reportError   HookErrorReporter
	closedErr     error
}

// NewHookRegistry creates an empty registry.
func NewHookRegistry(reportError HookErrorReporter) *HookRegistry {
	return &HookRegistry{registrations: map[HookName][]*hookRegistration{}, reportError: reportError}
}

// On registers a handler for one hook and returns an unsubscribe function.
// Options mirror the upstream optional registration metadata.
func (r *HookRegistry) On(name HookName, handler HookHandler, options ...*HookRegistrationOptions) func() {
	r.mu.Lock()
	if r.closedErr != nil {
		err := r.closedErr
		r.mu.Unlock()
		panic(err)
	}
	registration := &hookRegistration{handler: handler}
	if len(options) > 0 && options[0] != nil {
		registration.id = options[0].ID
	}
	r.registrations[name] = append(r.registrations[name], registration)
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		current := r.registrations[name]
		for index, candidate := range current {
			if candidate == registration {
				r.registrations[name] = append(current[:index], current[index+1:]...)
				return
			}
		}
	}
}

// Has reports whether at least one handler is registered for name.
func (r *HookRegistry) Has(name HookName) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.registrations[name]) != 0
}

// Close seals the registry.
func (r *HookRegistry) Close(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closedErr == nil {
		r.closedErr = err
	}
}

// RunWithGate invokes one accepted-operation aggregate after the effect gate
// admits it.
func (r *HookRegistry) RunWithGate(name HookName, event any, gate harnessexecution.Gate, ctx Context) (any, error) {
	var result any
	err := gate.Admit(func() error {
		admitted := admittedContext(gate, ctx)
		if admitted.Err() != nil {
			return admitted.Err()
		}
		value, runErr := r.runAdmitted(name, event, admitted)
		if runErr != nil {
			return runErr
		}
		result = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// RunToolWithGate invokes a tool-hook aggregate with one telemetry span per
// registered handler.
func (r *HookRegistry) RunToolWithGate(name HookName, event any, gate harnessexecution.Gate, ctx Context) (any, error) {
	var result any
	err := gate.Admit(func() error {
		admitted := admittedContext(gate, ctx)
		if admitted.Err() != nil {
			return admitted.Err()
		}
		value, runErr := r.aggregate(name, event, admitted, true)
		if runErr != nil {
			return runErr
		}
		result = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func admittedContext(gate harnessexecution.Gate, ctx Context) Context {
	signal := gate.Signal()
	if signal == nil {
		return ctx
	}
	signalContext, cancel := context.WithCancel(context.Background())
	go func() {
		<-signal
		cancel()
	}()
	return harnesscontext.WithAbortSignal(signalContext, ctx)
}

func (r *HookRegistry) runAdmitted(name HookName, event any, ctx Context) (any, error) {
	r.mu.Lock()
	closed := r.closedErr
	r.mu.Unlock()
	if closed != nil {
		return nil, closed
	}
	return r.aggregate(name, event, ctx, false)
}

func (r *HookRegistry) registrationsFor(name HookName) []*hookRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*hookRegistration(nil), r.registrations[name]...)
}

func (r *HookRegistry) aggregate(name HookName, event any, ctx Context, telemetry bool) (any, error) {
	switch name {
	case HookBeforeRun:
		return r.beforeRun(event, ctx, telemetry)
	case HookBeforeDrive:
		return nil, r.invokeAllFailClosed(event, ctx)
	case HookBeforeRunEnd:
		var followUp *string
		r.invokeAll(name, event, ctx, telemetry, func(value any) {
			result, ok := value.(*BeforeRunEndResult)
			if ok && result != nil && result.FollowUp != nil {
				followUp = result.FollowUp
			}
		})
		if followUp == nil {
			return nil, nil
		}
		return &BeforeRunEndResult{FollowUp: followUp}, nil
	case HookTransformContext:
		return r.transformContext(event, ctx, telemetry)
	case HookBeforeRequest:
		return r.beforeRequest(event, ctx, telemetry)
	case HookBeforePayload:
		return r.beforePayload(event, ctx, telemetry)
	case HookAfterResponse:
		return r.afterResponse(event, ctx, telemetry)
	case HookBeforeTool:
		return r.beforeTool(event, ctx, telemetry)
	case HookAfterTool:
		return r.afterTool(event, ctx, telemetry)
	case HookBeforeCompaction:
		return r.firstStructural(name, event, ctx, "compaction", telemetry)
	case HookBeforeNavigation:
		return r.firstStructural(name, event, ctx, "summary", telemetry)
	default:
		return nil, nil
	}
}

func (r *HookRegistry) beforeRun(event any, ctx Context, telemetry bool) (any, error) {
	beforeRunEvent, ok := event.(*BeforeRunEvent)
	if !ok {
		return nil, errors.New("harnesshooks: before_run event type mismatch")
	}
	prompt := append([]agenttypes.AgentMessage(nil), beforeRunEvent.Prompt...)
	injected := []agenttypes.AgentMessage{}
	for _, registration := range r.registrationsFor(HookBeforeRun) {
		current, err := r.invokeOne(HookBeforeRun, registration, &BeforeRunEvent{Prompt: prompt, Resources: beforeRunEvent.Resources}, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookBeforeRun, laneOf(beforeRunEvent.Lane))
			continue
		}
		result, ok := current.(*BeforeRunResult)
		if !ok || result == nil || result.Messages == nil {
			continue
		}
		injected = append(injected, result.Messages...)
		prompt = append(prompt, result.Messages...)
	}
	if len(injected) == 0 {
		return nil, nil
	}
	return &BeforeRunResult{Messages: injected}, nil
}

func (r *HookRegistry) beforeTool(event any, ctx Context, telemetry bool) (any, error) {
	beforeToolEvent, ok := event.(*BeforeToolEvent)
	if !ok {
		return nil, errors.New("harnesshooks: before_tool event type mismatch")
	}
	args := beforeToolEvent.Args
	var block *BlockResult
	for _, registration := range r.registrationsFor(HookBeforeTool) {
		current, err := r.invokeToolRegistration(HookBeforeTool, registration, &BeforeToolEvent{
			ToolCallID: beforeToolEvent.ToolCallID,
			ToolName:   beforeToolEvent.ToolName,
			Args:       args,
			Lane:       beforeToolEvent.Lane,
			RunID:      beforeToolEvent.RunID,
		}, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookBeforeTool, laneOf(beforeToolEvent.Lane))
			block = &BlockResult{Reason: err.Error()}
			break
		}
		result, ok := current.(*BeforeToolResult)
		if !ok || result == nil {
			continue
		}
		if result.Args != nil {
			args = result.Args
		}
		if result.Block != nil {
			block = result.Block
			break
		}
	}
	if args == nil && block == nil {
		return nil, nil
	}
	return &BeforeToolResult{Args: args, Block: block}, nil
}

func (r *HookRegistry) transformContext(event any, ctx Context, telemetry bool) (any, error) {
	transformEvent, ok := event.(*TransformContextEvent)
	if !ok {
		return nil, errors.New("harnesshooks: transform_context event type mismatch")
	}
	messages := append([]agenttypes.AgentMessage(nil), transformEvent.Messages...)
	systemPrompt := transformEvent.SystemPrompt
	for _, registration := range r.registrationsFor(HookTransformContext) {
		current, err := r.invokeOne(HookTransformContext, registration, &TransformContextEvent{
			Messages:     messages,
			SystemPrompt: systemPrompt,
			Lane:         transformEvent.Lane,
			RunID:        transformEvent.RunID,
		}, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookTransformContext, laneOf(transformEvent.Lane))
			continue
		}
		result, ok := current.(*TransformContextResult)
		if !ok || result == nil {
			continue
		}
		if result.Messages != nil {
			messages = result.Messages
		}
		if result.SystemPrompt != nil {
			systemPrompt = *result.SystemPrompt
		}
	}
	return &TransformContextResult{Messages: messages, SystemPrompt: &systemPrompt}, nil
}

func (r *HookRegistry) beforeRequest(event any, ctx Context, telemetry bool) (any, error) {
	requestEvent, ok := event.(*BeforeRequestEvent)
	if !ok {
		return nil, errors.New("harnesshooks: before_request event type mismatch")
	}
	streamOptions := requestEvent.StreamOptions
	changed := false
	for _, registration := range r.registrationsFor(HookBeforeRequest) {
		current, err := r.invokeOne(HookBeforeRequest, registration, &BeforeRequestEvent{
			Model:         requestEvent.Model,
			Step:          requestEvent.Step,
			Attempt:       requestEvent.Attempt,
			StreamOptions: streamOptions,
			Lane:          requestEvent.Lane,
			RunID:         requestEvent.RunID,
		}, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookBeforeRequest, laneOf(requestEvent.Lane))
			continue
		}
		result, ok := current.(*BeforeRequestResult)
		if !ok || result == nil || result.StreamOptions == nil {
			continue
		}
		streamOptions = ApplyStreamOptionsPatch(streamOptions, *result.StreamOptions)
		changed = true
	}
	if !changed {
		return nil, nil
	}
	patch := createStreamOptionsPatch(requestEvent.StreamOptions, streamOptions)
	return &BeforeRequestResult{StreamOptions: &patch}, nil
}

func (r *HookRegistry) beforePayload(event any, ctx Context, telemetry bool) (any, error) {
	payloadEvent, ok := event.(*BeforePayloadEvent)
	if !ok {
		return nil, errors.New("harnesshooks: before_payload event type mismatch")
	}
	payload := payloadEvent.Payload
	for _, registration := range r.registrationsFor(HookBeforePayload) {
		current, err := r.invokeOne(HookBeforePayload, registration, &BeforePayloadEvent{
			Model:   payloadEvent.Model,
			Payload: payload,
			Lane:    payloadEvent.Lane,
			RunID:   payloadEvent.RunID,
		}, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookBeforePayload, laneOf(payloadEvent.Lane))
			continue
		}
		result, ok := current.(*BeforePayloadResult)
		if !ok || result == nil {
			continue
		}
		payload = result.Payload
	}
	return &BeforePayloadResult{Payload: payload}, nil
}

func (r *HookRegistry) afterResponse(event any, ctx Context, telemetry bool) (any, error) {
	responseEvent, ok := event.(*AfterResponseEvent)
	if !ok {
		return nil, errors.New("harnesshooks: after_response event type mismatch")
	}
	message := responseEvent.Message
	for _, registration := range r.registrationsFor(HookAfterResponse) {
		current, err := r.invokeOne(HookAfterResponse, registration, &AfterResponseEvent{
			Status:  responseEvent.Status,
			Headers: responseEvent.Headers,
			Message: message,
			Lane:    responseEvent.Lane,
			RunID:   responseEvent.RunID,
		}, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookAfterResponse, laneOf(responseEvent.Lane))
			continue
		}
		result, ok := current.(*AfterResponseResult)
		if !ok || result == nil || result.Message == nil {
			continue
		}
		message = *result.Message
	}
	return &AfterResponseResult{Message: &message}, nil
}

func (r *HookRegistry) afterTool(event any, ctx Context, telemetry bool) (any, error) {
	toolEvent, ok := event.(*AfterToolEvent)
	if !ok {
		return nil, errors.New("harnesshooks: after_tool event type mismatch")
	}
	current := AfterToolResult{
		Content: toolEvent.Content,
		Details: toolEvent.Details,
		IsError: &toolEvent.IsError,
		Usage:   toolEvent.Usage,
	}
	aggregate := &AfterToolResult{}
	hasAggregate := false
	for _, registration := range r.registrationsFor(HookAfterTool) {
		invocation := &AfterToolEvent{
			ToolCallID: toolEvent.ToolCallID,
			ToolName:   toolEvent.ToolName,
			Args:       toolEvent.Args,
			Content:    current.Content,
			Details:    current.Details,
			IsError:    *current.IsError,
			Usage:      current.Usage,
			Lane:       toolEvent.Lane,
			RunID:      toolEvent.RunID,
		}
		value, err := r.invokeToolRegistration(HookAfterTool, registration, invocation, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, HookAfterTool, laneOf(toolEvent.Lane))
			continue
		}
		result, ok := value.(*AfterToolResult)
		if !ok || result == nil {
			continue
		}
		hasAggregate = true
		if result.Content != nil {
			aggregate.Content = result.Content
			current.Content = result.Content
		}
		if result.Details != nil {
			aggregate.Details = result.Details
			current.Details = result.Details
		}
		if result.IsError != nil {
			aggregate.IsError = result.IsError
			current.IsError = result.IsError
		}
		if result.Usage != nil {
			aggregate.Usage = result.Usage
			current.Usage = result.Usage
		}
		if result.Terminate != nil {
			aggregate.Terminate = result.Terminate
		}
	}
	if !hasAggregate {
		return nil, nil
	}
	return aggregate, nil
}

func (r *HookRegistry) firstStructural(name HookName, event any, ctx Context, resultField string, telemetry bool) (any, error) {
	for _, registration := range r.registrationsFor(name) {
		value, err := r.invokeOne(name, registration, event, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, name, laneOfEvent(event))
			continue
		}
		declined, hasDecline, fieldValue, hasField := structuralResult(value, resultField)
		if hasDecline && declined && hasField {
			r.report(ctx, errors.New(string(name)+" hook cannot return both decline and "+resultField), name, laneOfEvent(event))
			continue
		}
		if (hasDecline && declined) || hasField {
			return fieldValue, nil
		}
	}
	return nil, nil
}

func (r *HookRegistry) invokeAllFailClosed(event any, ctx Context) error {
	for _, registration := range r.registrationsFor(HookBeforeDrive) {
		if _, err := r.invokeOne(HookBeforeDrive, registration, event, ctx, false); err != nil {
			r.report(ctx, err, HookBeforeDrive, laneOfEvent(event))
			return err
		}
	}
	return nil
}

func (r *HookRegistry) invokeAll(name HookName, event any, ctx Context, telemetry bool, apply func(any)) {
	for _, registration := range r.registrationsFor(name) {
		value, err := r.invokeOne(name, registration, event, ctx, telemetry)
		if err != nil {
			r.report(ctx, err, name, laneOfEvent(event))
			continue
		}
		apply(value)
	}
}

func (r *HookRegistry) invokeOne(name HookName, registration *hookRegistration, event any, ctx Context, telemetry bool) (any, error) {
	return registration.handler(ctx, event)
}

func (r *HookRegistry) invokeToolRegistration(name HookName, registration *hookRegistration, event any, ctx Context, telemetry bool) (any, error) {
	if !telemetry {
		return registration.handler(ctx, event)
	}
	attributes := harnesstelemetry.SpanAttributes{
		"pi.lane.name": harnesstelemetry.AttributeValue{Type: "string", String: stringPointer(laneOfEvent(event))},
		"pi.hook.name": harnesstelemetry.AttributeValue{Type: "string", String: stringPointer(string(name))},
	}
	if registration.id != nil {
		attributes["pi.hook.registration_id"] = harnesstelemetry.AttributeValue{Type: "string", String: registration.id}
	}
	return harnesstelemetry.StartHarnessSpan(ctx, "pi.harness.hook", attributes,
		func(spanCtx Context, span harnesstelemetry.HarnessTelemetrySpan) (any, error) {
			result, err := registration.handler(spanCtx, event)
			if err != nil {
				span.SetStatus(harnesstelemetry.SpanStatus{Status: "error"})
				return nil, err
			}
			outcome := "completed"
			if name == HookBeforeTool {
				if before, ok := result.(*BeforeToolResult); ok && before != nil && before.Block != nil {
					outcome = "blocked"
				}
			}
			span.SetAttributes(harnesstelemetry.SpanAttributes{
				"pi.hook.outcome": harnesstelemetry.AttributeValue{Type: "string", String: stringPointer(outcome)},
			})
			return result, nil
		})
}

func (r *HookRegistry) report(ctx Context, err error, hook HookName, lane string) {
	if r.reportError == nil {
		return
	}
	r.reportError(ctx, err, hook, lane)
}

// ApplyStreamOptionsPatch applies a stream-options patch. A non-nil scalar or
// composite value replaces the base field; header/metadata entries with a nil
// value delete the key.
//
// Go adaptation: the patch type uses pointers, so an explicit `undefined` for a
// scalar field cannot be distinguished from an absent field. Callers that need
// to clear a scalar field set it to its zero value explicitly.
func ApplyStreamOptionsPatch(base harnesstypes.AgentHarnessStreamOptions, patch harnesstypes.AgentHarnessStreamOptionsPatch) harnesstypes.AgentHarnessStreamOptions {
	next := base
	if patch.Transport != nil {
		next.Transport = patch.Transport
	}
	if patch.TimeoutMs != nil {
		next.TimeoutMs = patch.TimeoutMs
	}
	if patch.MaxRetries != nil {
		next.MaxRetries = patch.MaxRetries
	}
	if patch.MaxRetryDelayMs != nil {
		next.MaxRetryDelayMs = patch.MaxRetryDelayMs
	}
	if patch.CacheRetention != nil {
		next.CacheRetention = patch.CacheRetention
	}
	if patch.Deferred != nil {
		next.Deferred = patch.Deferred
	}
	if patch.Headers != nil {
		headers := map[string]string{}
		for key, value := range next.Headers {
			headers[key] = value
		}
		for key, value := range patch.Headers {
			if value == nil {
				delete(headers, key)
			} else {
				headers[key] = *value
			}
		}
		next.Headers = headers
	}
	if patch.Metadata != nil {
		metadata := map[string]any{}
		for key, value := range next.Metadata {
			metadata[key] = value
		}
		for key, value := range patch.Metadata {
			if value == nil {
				delete(metadata, key)
			} else {
				metadata[key] = value
			}
		}
		next.Metadata = metadata
	}
	return next
}

func createStreamOptionsPatch(base harnesstypes.AgentHarnessStreamOptions, value harnesstypes.AgentHarnessStreamOptions) harnesstypes.AgentHarnessStreamOptionsPatch {
	patch := harnesstypes.AgentHarnessStreamOptionsPatch{}
	if !sameOptionalInt(base.TimeoutMs, value.TimeoutMs) {
		patch.TimeoutMs = value.TimeoutMs
	}
	if !sameOptionalInt(base.MaxRetries, value.MaxRetries) {
		patch.MaxRetries = value.MaxRetries
	}
	if !sameOptionalInt(base.MaxRetryDelayMs, value.MaxRetryDelayMs) {
		patch.MaxRetryDelayMs = value.MaxRetryDelayMs
	}
	if !sameOptionalString(base.Transport, value.Transport) {
		patch.Transport = value.Transport
	}
	if !sameOptionalString(base.CacheRetention, value.CacheRetention) {
		patch.CacheRetention = value.CacheRetention
	}
	if !sameOptionalDeferred(base.Deferred, value.Deferred) {
		patch.Deferred = value.Deferred
	}
	if !sameStringMap(base.Headers, value.Headers) {
		headers := map[string]*string{}
		for key := range base.Headers {
			if _, ok := value.Headers[key]; !ok {
				headers[key] = nil
			}
		}
		for key, header := range value.Headers {
			if base.Headers[key] != header {
				headerValue := header
				headers[key] = &headerValue
			}
		}
		patch.Headers = headers
	}
	if !sameAnyMap(base.Metadata, value.Metadata) {
		metadata := map[string]any{}
		for key := range base.Metadata {
			if _, ok := value.Metadata[key]; !ok {
				metadata[key] = nil
			}
		}
		for key, metadataValue := range value.Metadata {
			if !sameAny(base.Metadata[key], metadataValue) {
				metadata[key] = metadataValue
			}
		}
		patch.Metadata = metadata
	}
	return patch
}

func sameOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func sameOptionalString[T ~string](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func sameOptionalDeferred(left, right *aitypes.DeferredRequest) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left == right || *left == *right
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func sameAnyMap(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if !sameAny(value, right[key]) {
			return false
		}
	}
	return true
}

func sameAny(left, right any) bool {
	if left == nil || right == nil {
		return left == right
	}
	return stringify(left) == stringify(right)
}

func stringify(value any) string {
	if encoded, ok := value.(string); ok {
		return encoded
	}
	if encoded, ok := value.([]byte); ok {
		return string(encoded)
	}
	return ""
}

func stringPointer(value string) *string { return &value }

func laneOf(lane *string) string {
	if lane == nil {
		return ""
	}
	return *lane
}

func laneOfEvent(event any) string {
	switch typed := event.(type) {
	case *BeforeRunEvent:
		return laneOf(typed.Lane)
	case *BeforeToolEvent:
		return laneOf(typed.Lane)
	case *AfterToolEvent:
		return laneOf(typed.Lane)
	case *BeforeRunEndResult:
		return ""
	}
	return ""
}

func structuralResult(value any, resultField string) (declined bool, hasDecline bool, fieldValue any, hasField bool) {
	switch typed := value.(type) {
	case *BeforeCompactionResult:
		if typed == nil {
			return false, false, nil, false
		}
		return typed.Decline != nil && *typed.Decline, typed.Decline != nil, typed.Compaction, resultField == "compaction" && typed.Compaction != nil
	case *BeforeNavigationResult:
		if typed == nil {
			return false, false, nil, false
		}
		return typed.Decline != nil && *typed.Decline, typed.Decline != nil, typed.Summary, resultField == "summary" && typed.Summary != nil
	default:
		return false, false, nil, false
	}
}

// --- typed hook payloads --------------------------------------------------

// BeforeRunEvent is the before_run invocation.
type BeforeRunEvent struct {
	Prompt    []agenttypes.AgentMessage `json:"prompt"`
	Resources harnesstypes.Resources    `json:"resources"`
	Lane      *string                   `json:"lane,omitempty"`
	RunID     *string                   `json:"runId,omitempty"`
}

// BeforeRunResult injects messages into the run prompt.
type BeforeRunResult struct {
	Messages []agenttypes.AgentMessage `json:"messages,omitempty"`
}

// BeforeRunEndResult requests a follow-up run.
type BeforeRunEndResult struct {
	FollowUp *string `json:"followUp,omitempty"`
}

// TransformContextEvent is the transform_context invocation.
type TransformContextEvent struct {
	Messages     []agenttypes.AgentMessage `json:"messages"`
	SystemPrompt string                    `json:"systemPrompt"`
	Lane         *string                   `json:"lane,omitempty"`
	RunID        *string                   `json:"runId,omitempty"`
}

// TransformContextResult replaces context fields.
type TransformContextResult struct {
	Messages     []agenttypes.AgentMessage `json:"messages,omitempty"`
	SystemPrompt *string                   `json:"systemPrompt,omitempty"`
}

// BeforeRequestEvent is the before_request invocation.
type BeforeRequestEvent struct {
	Model         *aitypes.Model                         `json:"model"`
	Step          string                                 `json:"step"`
	Attempt       int                                    `json:"attempt"`
	StreamOptions harnesstypes.AgentHarnessStreamOptions `json:"streamOptions"`
	Lane          *string                                `json:"lane,omitempty"`
	RunID         *string                                `json:"runId,omitempty"`
}

// BeforeRequestResult patches the request stream options.
type BeforeRequestResult struct {
	StreamOptions *harnesstypes.AgentHarnessStreamOptionsPatch `json:"streamOptions,omitempty"`
}

// BeforePayloadEvent is the before_payload invocation.
type BeforePayloadEvent struct {
	Model   *aitypes.Model `json:"model"`
	Payload any            `json:"payload"`
	Lane    *string        `json:"lane,omitempty"`
	RunID   *string        `json:"runId,omitempty"`
}

// BeforePayloadResult replaces the provider payload.
type BeforePayloadResult struct {
	Payload any `json:"payload"`
}

// AfterResponseEvent is the after_response invocation.
type AfterResponseEvent struct {
	Status  *int                     `json:"status,omitempty"`
	Headers map[string]string        `json:"headers,omitempty"`
	Message aitypes.AssistantMessage `json:"message"`
	Lane    *string                  `json:"lane,omitempty"`
	RunID   *string                  `json:"runId,omitempty"`
}

// AfterResponseResult replaces the settled assistant message.
type AfterResponseResult struct {
	Message *aitypes.AssistantMessage `json:"message,omitempty"`
}

// BeforeToolEvent is the before_tool invocation.
type BeforeToolEvent struct {
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Args       map[string]any `json:"args"`
	Lane       *string        `json:"lane,omitempty"`
	RunID      *string        `json:"runId,omitempty"`
}

// BlockResult blocks a tool invocation.
type BlockResult struct {
	Reason    string `json:"reason"`
	Terminate *bool  `json:"terminate,omitempty"`
}

// BeforeToolResult patches args or blocks the tool call.
type BeforeToolResult struct {
	Args  map[string]any `json:"args,omitempty"`
	Block *BlockResult   `json:"block,omitempty"`
}

// AfterToolEvent is the after_tool invocation.
type AfterToolEvent struct {
	ToolCallID string                 `json:"toolCallId"`
	ToolName   string                 `json:"toolName"`
	Args       map[string]any         `json:"args"`
	Content    []aitypes.ContentBlock `json:"content"`
	Details    any                    `json:"details,omitempty"`
	IsError    bool                   `json:"isError"`
	Usage      *aitypes.Usage         `json:"usage,omitempty"`
	Lane       *string                `json:"lane,omitempty"`
	RunID      *string                `json:"runId,omitempty"`
}

// AfterToolResult patches the tool result.
type AfterToolResult struct {
	Content   []aitypes.ContentBlock `json:"content,omitempty"`
	Details   any                    `json:"details,omitempty"`
	IsError   *bool                  `json:"isError,omitempty"`
	Usage     *aitypes.Usage         `json:"usage,omitempty"`
	Terminate *bool                  `json:"terminate,omitempty"`
}

// BeforeCompactionResult can decline or replace a compaction.
type BeforeCompactionResult struct {
	Decline    *bool `json:"decline,omitempty"`
	Compaction any   `json:"compaction,omitempty"`
}

// BeforeNavigationResult can decline or replace a branch summary.
type BeforeNavigationResult struct {
	Decline *bool `json:"decline,omitempty"`
	Summary any   `json:"summary,omitempty"`
}
