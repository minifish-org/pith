// Session events, usage accounting and the small headless helper services of
// the embedded SDK.
//
// This file ports the event/statistics half of
// packages/coding-agent/src/core/agent-session.ts, event-bus.ts,
// usage-totals.ts and telemetry.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// The upstream session emits a large discriminated union (compaction, retry,
// queue and extension events). The embedded SDK publishes the subset that is
// meaningful headless: agent/message/turn/tool lifecycle and retry markers.
// Queue/settled names are vocabulary, not currently emitted session events.
// Every emitted SessionEvent carries the frozen fields
// Type/ToolName/ToolCallID/Text/IsError/Message so callers can switch on Type
// without importing an event hierarchy.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// SessionEventType is the discriminator of a SessionEvent.
type SessionEventType = string

// Session event type vocabulary. The names match the upstream AgentEvent names
// so an event stream is self-describing across the SDK boundary.
const (
	SessionEventAgentStart          SessionEventType = "agent_start"
	SessionEventAgentEnd            SessionEventType = "agent_end"
	SessionEventAgentSettled        SessionEventType = "agent_settled"
	SessionEventTurnStart           SessionEventType = "turn_start"
	SessionEventTurnEnd             SessionEventType = "turn_end"
	SessionEventMessageStart        SessionEventType = "message_start"
	SessionEventMessageUpdate       SessionEventType = "message_update"
	SessionEventMessageEnd          SessionEventType = "message_end"
	SessionEventToolExecutionStart  SessionEventType = "tool_execution_start"
	SessionEventToolExecutionUpdate SessionEventType = "tool_execution_update"
	SessionEventToolExecutionEnd    SessionEventType = "tool_execution_end"
	SessionEventQueueUpdate         SessionEventType = "queue_update"

	// Retry lifecycle. A retry event carries the attempt number, budget and
	// sanitized diagnostic in Text; no provider secret is ever included.
	SessionEventAutoRetryStart SessionEventType = "auto_retry_start"
	SessionEventAutoRetryEnd   SessionEventType = "auto_retry_end"

	// SessionEventProviderStreamEvent is a transient notification carrying a
	// parsed provider stream event. It is published to subscribers and never
	// persisted as assistant content or session transcript.
	SessionEventProviderStreamEvent SessionEventType = "provider_stream_event"
)

// SessionEvent is the public, structured event published by AgentSession.
type SessionEvent struct {
	Type       string                   `json:"type"`
	ToolName   string                   `json:"toolName,omitempty"`
	ToolCallID string                   `json:"toolCallId,omitempty"`
	Text       string                   `json:"text,omitempty"`
	IsError    bool                     `json:"isError,omitempty"`
	Message    *agenttypes.AgentMessage `json:"message,omitempty"`

	// Provider, API and Model identify the request that produced a
	// provider_stream_event. They are read from the model of the current
	// request, never from a construction-time snapshot. They are empty for
	// every other event type.
	Provider string `json:"provider,omitempty"`
	API      string `json:"api,omitempty"`
	Model    string `json:"model,omitempty"`
	// Data is the parsed adapter event exactly as handed to the observer. It is
	// owned by the provider adapter and must be treated as read-only. It is
	// transient: it is never serialized into the durable transcript.
	Data any `json:"data,omitempty"`
}

// AgentSessionEvent is the upstream name of SessionEvent.
type AgentSessionEvent = SessionEvent

// AgentSessionEventListener is the upstream name of a session subscriber.
type AgentSessionEventListener = func(SessionEvent)

// RunResult is the detached outcome of one synchronous Prompt.
type RunResult struct {
	StopReason aitypes.StopReason        `json:"stopReason"`
	Turns      int                       `json:"turns"`
	Messages   []agenttypes.AgentMessage `json:"messages"`
	Usage      aitypes.Usage             `json:"usage"`
}

// ModelCycleResult is the outcome of selecting the next model.
type ModelCycleResult struct {
	Model         *aitypes.Model           `json:"model"`
	ThinkingLevel agenttypes.ThinkingLevel `json:"thinkingLevel"`
	IsScoped      bool                     `json:"isScoped"`
}

// PromptOptions configures Prompt, Steer and
// FollowUp. All three methods expand loaded skills/templates by default; set
// ExpandPromptTemplates to false to send literal command text.
type PromptOptions struct {
	Images []aitypes.ImageContent `json:"images,omitempty"`
	// StreamingBehavior applies only to Prompt during an active
	// run. Values are "steer" and "followUp"; empty rejects concurrent input.
	StreamingBehavior     string `json:"streamingBehavior,omitempty"`
	ExpandPromptTemplates *bool  `json:"expandPromptTemplates,omitempty"`
}

// ModelMutationOptions controls how a model/thinking mutation is persisted.
type ModelMutationOptions struct {
	Persist bool `json:"persist,omitempty"`
}

// ExtensionBindings is the headless subset of the upstream extension bindings.
// UI contexts, extension runners, command contexts and error listeners belong
// to the excluded TS/JS host and are not represented.
type ExtensionBindings struct {
	AbortHandler func()
}

// SessionStats is a read-only summary of a session transcript.
type SessionStats struct {
	SessionFile       *string `json:"sessionFile,omitempty"`
	SessionID         string  `json:"sessionId"`
	UserMessages      int     `json:"userMessages"`
	AssistantMessages int     `json:"assistantMessages"`
	ToolCalls         int     `json:"toolCalls"`
	ToolResults       int     `json:"toolResults"`
	TotalMessages     int     `json:"totalMessages"`
	InputTokens       float64 `json:"input"`
	OutputTokens      float64 `json:"output"`
	CacheRead         float64 `json:"cacheRead"`
	CacheWrite        float64 `json:"cacheWrite"`
	TotalTokens       float64 `json:"totalTokens"`
	Cost              float64 `json:"cost"`
}

// ---------------------------------------------------------------------------
// Skill block parsing
// ---------------------------------------------------------------------------

// ParsedSkillBlock is one parsed <skill> command block.
type ParsedSkillBlock struct {
	Name        string `json:"name"`
	Location    string `json:"location"`
	Content     string `json:"content"`
	UserMessage string `json:"userMessage,omitempty"`
}

var skillBlockPattern = regexp.MustCompile(`(?s)^<skill name="([^"]+)" location="([^"]+)">\n(.*?)\n</skill>(?:\n\n(.*))?$`)

// ParseSkillBlock parses a skill block from message text. It returns nil when
// the text does not contain a skill block.
func ParseSkillBlock(text string) *ParsedSkillBlock {
	match := skillBlockPattern.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	parsed := &ParsedSkillBlock{
		Name:     match[1],
		Location: match[2],
		Content:  match[3],
	}
	if user := strings.TrimSpace(match[4]); user != "" {
		parsed.UserMessage = user
	}
	return parsed
}

// ---------------------------------------------------------------------------
// Usage totals
// ---------------------------------------------------------------------------

// UsageTotals aggregates usage across a transcript.
type UsageTotals struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Cost       float64 `json:"cost"`
}

// UsageCostBreakdownEntry is one model's aggregated cost.
type UsageCostBreakdownEntry struct {
	Key    string  `json:"key"`
	Cost   float64 `json:"cost"`
	Tokens float64 `json:"tokens"`
}

// CreateUsageTotals returns an empty accumulator.
func CreateUsageTotals() UsageTotals {
	return UsageTotals{}
}

// AddUsageToTotals folds one usage record into the accumulator.
func AddUsageToTotals(totals *UsageTotals, usage aitypes.Usage) {
	if totals == nil {
		return
	}
	totals.Input += usage.Input
	totals.Output += usage.Output
	totals.CacheRead += usage.CacheRead
	totals.CacheWrite += usage.CacheWrite
	totals.Cost += usage.Cost.Total
}

// GetUsageCostBreakdown groups model-attributed usage by provider/model and all
// other usage into a "Tools/summaries" bucket. Entries with zero cost and zero
// tokens are omitted.
func GetUsageCostBreakdown(entries []SessionEntry) []UsageCostBreakdownEntry {
	byKey := map[string]UsageTotals{}
	for _, entry := range entries {
		key, usage := usageOfEntry(entry)
		if key == "" || usage == nil {
			continue
		}
		totals := byKey[key]
		AddUsageToTotals(&totals, *usage)
		byKey[key] = totals
	}
	out := make([]UsageCostBreakdownEntry, 0, len(byKey))
	for key, totals := range byKey {
		tokens := totals.Input + totals.Output + totals.CacheRead + totals.CacheWrite
		if totals.Cost == 0 && tokens == 0 {
			continue
		}
		out = append(out, UsageCostBreakdownEntry{Key: key, Cost: totals.Cost, Tokens: tokens})
	}
	// Stable ordering: highest cost first, then key for ties.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j].Cost > out[j-1].Cost || (out[j].Cost == out[j-1].Cost && out[j].Key < out[j-1].Key) {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

func usageOfEntry(entry SessionEntry) (string, *aitypes.Usage) {
	switch entry.Type {
	case "message":
		var message aitypes.Message
		if err := json.Unmarshal(entry.Payload, &message); err != nil {
			return "", nil
		}
		switch message.Role {
		case aitypes.AssistantMessageRole:
			if message.Assistant == nil {
				return "", nil
			}
			model := message.Assistant.Model
			if message.Assistant.ResponseModel != nil && *message.Assistant.ResponseModel != "" {
				model = *message.Assistant.ResponseModel
			}
			key := string(message.Assistant.Provider) + "/" + model
			usage := message.Assistant.Usage
			return key, &usage
		case aitypes.ToolResultMessageRole:
			if message.ToolResult == nil || message.ToolResult.Usage == nil {
				return "", nil
			}
			return "Tools/summaries", message.ToolResult.Usage
		}
	case "usage":
		var payload struct {
			Provider string        `json:"provider"`
			Model    string        `json:"model"`
			Usage    aitypes.Usage `json:"usage"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return "", nil
		}
		return payload.Provider + "/" + payload.Model, &payload.Usage
	case "branch_summary", "compaction":
		var payload struct {
			Usage *aitypes.Usage `json:"usage"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil || payload.Usage == nil {
			return "", nil
		}
		return "Tools/summaries", payload.Usage
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// Telemetry
// ---------------------------------------------------------------------------

// IsInstallTelemetryEnabled resolves the install-telemetry flag. An explicit
// telemetryEnv value wins; otherwise the settings manager value is used.
func IsInstallTelemetryEnabled(settingsManager *SettingsManager, telemetryEnv *string) bool {
	if telemetryEnv != nil {
		return isTruthyEnvFlag(*telemetryEnv)
	}
	if settingsManager == nil {
		return false
	}
	return settingsManager.GetEnableInstallTelemetry()
}

func isTruthyEnvFlag(value string) bool {
	if value == "" {
		return false
	}
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

// ---------------------------------------------------------------------------
// Event bus
// ---------------------------------------------------------------------------

// EventBus is a minimal headless publish/subscribe channel.
type EventBus interface {
	Emit(channel string, data any)
	On(channel string, handler func(data any)) func()
}

// EventBusController extends EventBus with a Clear operation.
type EventBusController interface {
	EventBus
	Clear()
}

type eventBusEntry struct {
	id      uint64
	handler func(data any)
}

type eventBus struct {
	mu       sync.Mutex
	nextID   uint64
	handlers map[string][]eventBusEntry
}

// CreateEventBus returns an in-process event bus. Handlers for one channel are
// called in subscription order; a panicking handler is isolated so it cannot
// break later handlers.
func CreateEventBus() EventBusController {
	return &eventBus{handlers: map[string][]eventBusEntry{}}
}

func (b *eventBus) On(channel string, handler func(data any)) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.handlers[channel] = append(b.handlers[channel], eventBusEntry{id: id, handler: handler})
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			entries := b.handlers[channel]
			out := entries[:0]
			for _, entry := range entries {
				if entry.id != id {
					out = append(out, entry)
				}
			}
			if len(out) == 0 {
				delete(b.handlers, channel)
			} else {
				b.handlers[channel] = out
			}
		})
	}
}

func (b *eventBus) Emit(channel string, data any) {
	b.mu.Lock()
	entries := append([]eventBusEntry(nil), b.handlers[channel]...)
	b.mu.Unlock()
	for _, entry := range entries {
		func() {
			defer func() { _ = recover() }()
			entry.handler(data)
		}()
	}
}

func (b *eventBus) Clear() {
	b.mu.Lock()
	b.handlers = map[string][]eventBusEntry{}
	b.mu.Unlock()
}
