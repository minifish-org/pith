// Package harness is the Go port of Pi v1.0.0 packages/durable/src/harness at
// revision a13d35a742c6ef8462812a28fbe1d8c8b7431c32. It owns the scheduling and
// model/tool runtime of the optional Durable SDK: registry, scheduler,
// conversations, submissions, generation, tools, compaction, prompts, hooks,
// live views, events, task graph and usage.
//
// The port keeps the semantics of the implemented upstream version, not the
// later Pico5 rewrite. Context arguments come first, operation errors are
// returned, IDs are durable.ID aliases, and all JSON values are detached valid
// JSON. No JavaScript runtime or CGo is introduced.
package harness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
)

// ModelRef is a provider and model ID resolved through the model runner.
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// ModelRequest is one provider request prepared by the generation task.
type ModelRequest struct {
	ConversationID durable.ConversationID
	Model          ModelRef
	Messages       []types.Message
	Tools          []types.Tool
	ThinkingLevel  types.ThinkingLevel
	Options        ConversationStreamOptions
	// Summary marks a compaction summarization request.
	Summary bool
	// Instructions carries the summarization instructions, when any.
	Instructions string
}

// ModelRunner is the production model access the harness uses. Resolve returns
// the model descriptor for a reference or an error when it is unavailable. Run
// streams one request, invoking emit for each committed partial, and returns
// the terminal assistant message.
type ModelRunner interface {
	Resolve(context.Context, ModelRef) (*types.Model, error)
	Run(context.Context, ModelRequest, func(types.AssistantMessage) error) (types.AssistantMessage, error)
}

// DeferredModelRunner adds the deferred-response operations a capable runner
// implements. The harness reports an unsupported deferred response as a durable
// error.
type DeferredModelRunner interface {
	Poll(context.Context, ModelRef, types.DeferredHandle) (types.AssistantMessage, error)
	Cancel(context.Context, ModelRef, types.DeferredHandle) error
}

// ConversationStreamOptions preserves the curated provider request options.
type ConversationStreamOptions struct {
	Transport       string            `json:"transport,omitempty"`
	TimeoutMs       *int64            `json:"timeoutMs,omitempty"`
	MaxRetries      *int              `json:"maxRetries,omitempty"`
	MaxRetryDelayMs *int64            `json:"maxRetryDelayMs,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	CacheRetention  string            `json:"cacheRetention,omitempty"`
	Deferred        json.RawMessage   `json:"deferred,omitempty"`
}

// RetryPolicy is the durable generation retry policy.
type RetryPolicy struct {
	Enabled         bool  `json:"enabled"`
	MaxRetries      int   `json:"maxRetries"`
	BaseDelayMs     int   `json:"baseDelayMs"`
	MaxAgentDelayMs int64 `json:"maxAgentDelayMs,omitempty"`
}

// CompactionPolicy is the automatic compaction threshold policy.
type CompactionPolicy struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
	BackgroundTokens int  `json:"backgroundTokens"`
}

// Queue boundary selection modes.
const (
	QueueAll           = "all"
	QueueOneAtATime    = "one-at-a-time"
	QueueSteer         = "steer"
	QueueFollowUp      = "followUp"
	QueueReject        = "reject"
	ToolModeParallel   = "parallel"
	ToolModeSequential = "sequential"
)

// Settings is the effective harness run policy. Settings.ResolveSettings applies
// a source-wire partial patch over the defaults.
type Settings struct {
	// Extensions, when non-nil, is the default extension selection.
	Extensions    []Extension
	Stream        ConversationStreamOptions
	Retry         RetryPolicy
	Compaction    CompactionPolicy
	ToolExecution string
	SteeringMode  string
	FollowUpMode  string
}

// DefaultRetryPolicy mirrors source agent.ts.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 2000, MaxAgentDelayMs: 60000}
}

// DefaultCompactionPolicy mirrors source agent.ts.
func DefaultCompactionPolicy() CompactionPolicy {
	return CompactionPolicy{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000, BackgroundTokens: 32768}
}

// DefaultSettings returns the built-in effective settings.
func DefaultSettings() Settings {
	return Settings{
		Retry:         DefaultRetryPolicy(),
		Compaction:    DefaultCompactionPolicy(),
		ToolExecution: ToolModeParallel,
		SteeringMode:  QueueOneAtATime,
		FollowUpMode:  QueueOneAtATime,
	}
}

type settingsPatch struct {
	Stream *ConversationStreamOptions `json:"stream"`
	Retry  *struct {
		Enabled         *bool  `json:"enabled"`
		MaxRetries      *int   `json:"maxRetries"`
		BaseDelayMs     *int   `json:"baseDelayMs"`
		MaxAgentDelayMs *int64 `json:"maxAgentDelayMs"`
	} `json:"retry"`
	Compaction *struct {
		Enabled          *bool `json:"enabled"`
		ReserveTokens    *int  `json:"reserveTokens"`
		KeepRecentTokens *int  `json:"keepRecentTokens"`
		BackgroundTokens *int  `json:"backgroundTokens"`
	} `json:"compaction"`
	ToolExecution *string `json:"toolExecution"`
	SteeringMode  *string `json:"steeringMode"`
	FollowUpMode  *string `json:"followUpMode"`
}

// ResolveSettings applies a source-wire partial settings patch over the
// defaults. Omitted keys inherit defaults; explicit false/zero are retained.
func ResolveSettings(patch json.RawMessage) (Settings, error) {
	settings := DefaultSettings()
	if len(patch) == 0 || string(patch) == "null" {
		return settings, nil
	}
	var parsed settingsPatch
	if err := json.Unmarshal(patch, &parsed); err != nil {
		return Settings{}, err
	}
	if parsed.Stream != nil {
		settings.Stream = *parsed.Stream
	}
	if parsed.Retry != nil {
		if parsed.Retry.Enabled != nil {
			settings.Retry.Enabled = *parsed.Retry.Enabled
		}
		if parsed.Retry.MaxRetries != nil {
			settings.Retry.MaxRetries = *parsed.Retry.MaxRetries
		}
		if parsed.Retry.BaseDelayMs != nil {
			settings.Retry.BaseDelayMs = *parsed.Retry.BaseDelayMs
		}
		if parsed.Retry.MaxAgentDelayMs != nil {
			settings.Retry.MaxAgentDelayMs = *parsed.Retry.MaxAgentDelayMs
		}
	}
	if parsed.Compaction != nil {
		if parsed.Compaction.Enabled != nil {
			settings.Compaction.Enabled = *parsed.Compaction.Enabled
		}
		if parsed.Compaction.ReserveTokens != nil {
			settings.Compaction.ReserveTokens = *parsed.Compaction.ReserveTokens
		}
		if parsed.Compaction.KeepRecentTokens != nil {
			settings.Compaction.KeepRecentTokens = *parsed.Compaction.KeepRecentTokens
		}
		if parsed.Compaction.BackgroundTokens != nil {
			settings.Compaction.BackgroundTokens = *parsed.Compaction.BackgroundTokens
		}
	}
	if parsed.ToolExecution != nil {
		settings.ToolExecution = *parsed.ToolExecution
	}
	if parsed.SteeringMode != nil {
		settings.SteeringMode = *parsed.SteeringMode
	}
	if parsed.FollowUpMode != nil {
		settings.FollowUpMode = *parsed.FollowUpMode
	}
	return settings, nil
}

// ToolRegistration is one executable tool registered in a registry. Only the
// Declaration fields enter the transcript.
type ToolRegistration struct {
	Declaration types.Tool
	// Replay is "safe" or "unsafe"; empty is unsafe.
	Replay string
	// ExecutionMode overrides the settings' tool execution mode.
	ExecutionMode string
	// PrepareArguments repairs arguments before validation. It must be pure.
	PrepareArguments func(json.RawMessage) (json.RawMessage, error)
	OutputLimits     *OutputLimits
	Execute          func(context.Context, json.RawMessage, ToolAPI) (ToolResult, error)
}

// Name returns the declared tool name.
func (t ToolRegistration) Name() string { return t.Declaration.Name }

// OutputLimits bound one tool's retained output.
type OutputLimits struct {
	MaxBytes int    `json:"maxBytes,omitempty"`
	MaxLines int    `json:"maxLines,omitempty"`
	Retain   string `json:"retain,omitempty"`
}

// ToolDiagnostic is a model-visible remark about a tool call.
type ToolDiagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
}

// ToolControl is a post-tools control requested by a tool result.
type ToolControl struct {
	AddTools  []string `json:"addTools,omitempty"`
	Terminate bool     `json:"terminate,omitempty"`
	Handoff   string   `json:"handoff,omitempty"`
}

// ToolResult is one tool execution result.
type ToolResult struct {
	Content     []types.ContentBlock
	IsError     bool
	Details     json.RawMessage
	Diagnostics []ToolDiagnostic
	Usage       *types.Usage
	Control     *ToolControl
}

// PromptSection is one system prompt section.
type PromptSection struct {
	Key    string
	Render func(context.Context, PromptInput) (string, error)
	// Untagged is true when the source tag=false (raw text, no wrapper).
	Untagged bool
}

// PromptInput is the input of one section render.
type PromptInput struct {
	ConversationID durable.ConversationID
	Agent          Agent
	Env            env.ExecutionEnv
	Shown          map[string]string
	Read           durable.DocumentReader
}

// EnvTarget is what harness Options.Env builds an environment for.
type EnvTarget struct {
	ConversationID durable.ConversationID
	CWD            string
	Read           durable.DocumentReader
}

// Agent is one conversation's resolved agent.
type Agent struct {
	Model         *ModelRef
	ThinkingLevel types.ThinkingLevel
	Extensions    []Extension
	Tools         []ToolRegistration
	Sections      []PromptSection
	Instructions  string
	CWD           string
}

// HookRegistration matches tasks by name.
type HookRegistration struct {
	Task     string
	Handlers map[string]HookHandler
}

// HookHandler is one named hook handler.
type HookHandler func(context.Context, json.RawMessage, HookAPI) (json.RawMessage, error)

// Wrap targets a tool name or a section key.
type Wrap struct {
	Tool        string
	Section     string
	WrapTool    func(ToolRegistration) (ToolRegistration, error)
	WrapSection func(PromptSection) (PromptSection, error)
}

// Extension is a named bundle of code.
type Extension struct {
	Name     string
	Tools    []ToolRegistration
	Sections []PromptSection
	Hooks    []HookRegistration
	Wraps    []Wrap
	Tasks    []durable.TaskDefinition
}

// RegisteredTool is an installed tool with its extension.
type RegisteredTool struct {
	Extension Extension
	Tool      ToolRegistration
}

// RegisteredSection is an installed section with its extension.
type RegisteredSection struct {
	Extension Extension
	Section   PromptSection
}

// RegistryReader is the read side of a registry.
type RegistryReader interface {
	Snapshot() RegistrySnapshot
	Subscribe(func()) func()
}

// RegistrySnapshot is an immutable published registry state.
type RegistrySnapshot interface {
	Installed() []Extension
	Extension(string) (Extension, bool)
	Tools() []RegisteredTool
	Sections() []RegisteredSection
	Tasks() []durable.TaskDefinition
	Task(string) (durable.TaskDefinition, bool)
}

// CreateOptions configures a new conversation.
type CreateOptions struct {
	Ownership durable.ConversationOwnership
	Agent     json.RawMessage
	Init      func(context.Context, durable.Tx, durable.ConversationID) error
}

// RootOptions configures the lazily created root conversation.
type RootOptions struct {
	Agent json.RawMessage
	Init  func(context.Context, durable.Tx, durable.ConversationID) error
}

// AbortOptions selects whether an abort crosses background boundaries.
type AbortOptions struct {
	Background bool
}

// SubmissionDraft is a host submission.
type SubmissionDraft struct {
	// Type is "input" or "write".
	Type      string
	Content   types.UserContent
	Entry     *durable.EntryDraft
	RequestID string
	// WhenBusy is "steer", "followUp" or "reject"; empty defaults followUp.
	WhenBusy string
}

// ContextView is a raw active transcript and its derived model context.
type ContextView struct {
	Head          *durable.EntryRecord
	Entries       []durable.EntryRecord
	Contributions [][]types.Message
	Messages      []types.Message
}

// Options configures Open.
type Options struct {
	Models              ModelRunner
	Registry            RegistryReader
	Settings            func() Settings
	Env                 func(context.Context, EnvTarget) (env.ExecutionEnv, error)
	ConversationCreated func(context.Context, durable.Tx, durable.ConversationRecord) error
	// Now returns the harness clock in milliseconds.
	Now      func() int64
	OnReport func(error)
}

// HookAPI is what a hook may use.
type HookAPI interface {
	durable.DocumentReader
	TaskID() durable.TaskID
	ConversationID() durable.ConversationID
	Memo(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

// ConversationHandle is an invocation-bound conversation surface.
type ConversationHandle interface {
	ID() durable.ConversationID
	Submit(context.Context, SubmissionDraft) (*Submission, error)
	Abort(context.Context, AbortOptions) error
	WaitForIdle(context.Context) error
}

// ToolAPI is the invocation-bound tool execution surface.
type ToolAPI interface {
	durable.DocumentReader
	durable.DocumentObserver
	TaskID() durable.TaskID
	ConversationID() durable.ConversationID
	CallID() string
	Registry() RegistrySnapshot
	Agent(context.Context) (Agent, error)
	Env() env.ExecutionEnv
	Output([]byte) error
	Diagnostic(ToolDiagnostic) error
	Details(context.Context, json.RawMessage) error
	Commit(context.Context, func(durable.Tx) error) error
	Memo(context.Context, string, json.RawMessage) (json.RawMessage, error)
	CreateTask(context.Context, durable.TaskDefinition, json.RawMessage, durable.TaskOptions) (durable.TaskID, error)
	GetTask(context.Context, durable.TaskID) (*durable.TaskRecord, error)
	WaitForTask(context.Context, durable.TaskID) (*durable.TaskRecord, error)
	Conversation(context.Context, durable.ConversationID) (ConversationHandle, error)
}

// UsageState is the ledger of one conversation's own spend.
type UsageState struct {
	Models map[string]types.Usage `json:"models"`
	Tools  map[string]types.Usage `json:"tools"`
}

// ConversationView is the structural view of one conversation.
type ConversationView struct {
	Conversation durable.ConversationRecord
	Entries      []durable.EntryRecord
	Docs         map[string]durable.JsonObject
}

// TaskGraphTaskState is a live task's durable status without payloads.
type TaskGraphTaskState struct {
	Status  string           `json:"status"`
	Phase   string           `json:"phase,omitempty"`
	On      []durable.TaskID `json:"on,omitempty"`
	Policy  string           `json:"policy,omitempty"`
	Outcome string           `json:"outcome,omitempty"`
}

// TaskGraphNode is one live task node.
type TaskGraphNode struct {
	ID             durable.TaskID           `json:"id"`
	Kind           string                   `json:"kind"`
	ConversationID durable.ConversationID   `json:"conversationId"`
	Owner          *durable.TaskID          `json:"owner,omitempty"`
	Background     bool                     `json:"background"`
	AbortRequested bool                     `json:"abortRequested"`
	State          TaskGraphTaskState       `json:"state"`
	Conversations  []durable.ConversationID `json:"conversations"`
}

// TaskGraph is every live task keyed by decimal ID.
type TaskGraph struct {
	Tasks map[string]TaskGraphNode `json:"tasks"`
}

// AgentEvent is one experimental agent event. MarshalJSON/UnmarshalJSON merge
// Type and Payload into the exact source wire shape.
type AgentEvent struct {
	Type    string
	Payload json.RawMessage
}

// MarshalJSON merges Type and Payload at the top level.
func (e AgentEvent) MarshalJSON() ([]byte, error) {
	var fields map[string]json.RawMessage
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &fields)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	typeRaw, _ := json.Marshal(e.Type)
	fields["type"] = typeRaw
	return json.Marshal(fields)
}

// UnmarshalJSON splits the top-level type from the remaining payload.
func (e *AgentEvent) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["type"]; ok {
		_ = json.Unmarshal(raw, &e.Type)
		delete(fields, "type")
	}
	if len(fields) == 0 {
		e.Payload = nil
		return nil
	}
	e.Payload, _ = json.Marshal(fields)
	return nil
}

// AgentEventStream is a serialized stream of one conversation's event batches.
type AgentEventStream interface {
	Snapshot() AgentEvent
	Start(func(context.Context, []AgentEvent) error) error
	Stop() error
	Closed() <-chan durable.WatchEnd
}

// TaskInspectionState is one live task's derived inspection state.
type TaskInspectionState struct {
	Kind     string
	Reason   string
	On       []durable.TaskID
	Migrates bool
	Err      error
}

// TaskInspection is one live task with its derived state.
type TaskInspection struct {
	Record durable.TaskRecord
	State  TaskInspectionState
}

// Inspection is a point-in-time view of live work.
type Inspection struct {
	Scheduling  string
	Tasks       []TaskInspection
	Submissions []durable.SubmissionRecord
}

// chordOp aliases the chord operation type for internal use.
type chordOp = chord.Op

var _ = time.Second
