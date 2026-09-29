// This file carries the session storage contract of
// packages/agent/src/harness/session/types.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Go adaptation notes: interfaces cannot declare generic methods, so the
// generic value accessors are modeled with a non-generic Value address and an
// any payload. The durable operation state remains a flat union with one
// family-neutral discriminator per dispatcher leaf.
package harnesstypes

import (
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// JsonValue is the strict-JSON value union.
type JsonValue = any

// SettledAssistantMessage is an assistant message whose turn has settled.
type SettledAssistantMessage = aitypes.AssistantMessage

// EntryType discriminates one session entry.
type EntryType string

// Entry types.
const (
	EntryTypeMessage       EntryType = "message"
	EntryTypeCompaction    EntryType = "compaction"
	EntryTypeBranchSummary EntryType = "branch_summary"
	EntryTypeCustom        EntryType = "custom"
)

// EntryBase carries the fields shared by every session entry.
type EntryBase struct {
	ID         string    `json:"id"`
	ParentID   *string   `json:"parentId"`
	Seq        int       `json:"seq"`
	Timestamp  float64   `json:"timestamp"`
	Type       EntryType `json:"type"`
	CustomType *string   `json:"customType,omitempty"`
}

// EntryKind returns the entry discriminator.
func (e EntryBase) EntryKind() EntryType { return e.Type }

// MessageEntry is a transcript message entry.
type MessageEntry struct {
	EntryBase
	Message   agenttypes.AgentMessage `json:"message"`
	Terminate *bool                   `json:"terminate,omitempty"`
}

// CompactionEntry is a compaction summary entry.
type CompactionEntry struct {
	EntryBase
	Summary      string                    `json:"summary"`
	RetainedTail []agenttypes.AgentMessage `json:"retainedTail"`
	TokensBefore float64                   `json:"tokensBefore"`
	Details      JsonValue                 `json:"details,omitempty"`
	Usage        *aitypes.Usage            `json:"usage,omitempty"`
	FromHook     bool                      `json:"fromHook"`
}

// BranchSummaryEntry is a branch summary entry.
type BranchSummaryEntry struct {
	EntryBase
	FromID   *string        `json:"fromId"`
	Summary  string         `json:"summary"`
	Details  JsonValue      `json:"details,omitempty"`
	Usage    *aitypes.Usage `json:"usage,omitempty"`
	FromHook bool           `json:"fromHook"`
}

// CustomEntry is an application-defined entry.
type CustomEntry struct {
	EntryBase
	CustomType string    `json:"customType"`
	Data       JsonValue `json:"data,omitempty"`
}

// EntryProjector converts an application-defined custom entry into model
// context. A nil message slice means the entry contributes nothing.
type EntryProjector func(entry CustomEntry, ctx Context) ([]agenttypes.AgentMessage, error)

// Entry is one session entry.
type Entry interface {
	EntryKind() EntryType
}

// NewEntry is an entry supplied before storage assigns sequence and timestamp.
// Go has no structural Omit, so the pending form is the full entry; the storage
// assigns Seq/Timestamp on commit.
type NewEntry = Entry

// LaneConfiguration is the configured model/thinking/tool snapshot of a lane.
type LaneConfiguration struct {
	Model           ModelIdentity            `json:"model"`
	ThinkingLevel   agenttypes.ThinkingLevel `json:"thinkingLevel"`
	ActiveToolNames []string                 `json:"activeToolNames"`
}

// OperationIntent is the durable intent of an operation.
type OperationIntent struct {
	Kind               string   `json:"kind"`
	PromptEntryIDs     []string `json:"promptEntryIds,omitempty"`
	CustomInstructions *string  `json:"customInstructions,omitempty"`
	TargetID           *string  `json:"targetId,omitempty"`
	Summarize          *bool    `json:"summarize,omitempty"`
	Label              *string  `json:"label,omitempty"`
}

// OperationMeta is the durable metadata of an operation.
type OperationMeta struct {
	OperationID string          `json:"operationId"`
	Lane        string          `json:"lane"`
	SourceTipID *string         `json:"sourceTipId"`
	StartedAt   float64         `json:"startedAt"`
	Intent      OperationIntent `json:"intent"`
}

// Control is the cancellation state of an operation.
type Control struct {
	Status      string   `json:"status"`
	RequestedAt *float64 `json:"requestedAt,omitempty"`
}

// OperationError is the typed error recorded on a terminal operation.
type OperationError struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Details JsonValue `json:"details,omitempty"`
}

// TerminalStatus is the terminal status of an operation.
type TerminalStatus string

// Terminal statuses.
const (
	TerminalStatusCompleted TerminalStatus = "completed"
	TerminalStatusDeclined  TerminalStatus = "declined"
	TerminalStatusAborted   TerminalStatus = "aborted"
	TerminalStatusFailed    TerminalStatus = "failed"
)

// OperationResultRecord is an immutable lane-lived observation record.
type OperationResultRecord struct {
	OperationID string          `json:"operationId"`
	Kind        string          `json:"kind"`
	Status      TerminalStatus  `json:"status"`
	Error       *OperationError `json:"error,omitempty"`
	FromTipID   *string         `json:"fromTipId"`
	TipID       *string         `json:"tipId"`
	StartedAt   float64         `json:"startedAt"`
	EndedAt     float64         `json:"endedAt"`
}

// Continuation is the checkpoint continuation decision.
type Continuation struct {
	Kind                  string `json:"kind"`
	OverflowRecoveryUsed  *bool  `json:"overflowRecoveryUsed,omitempty"`
	IncludeFinalAssistant *bool  `json:"includeFinalAssistant,omitempty"`
}

// CheckpointData is the checkpoint payload.
type CheckpointData struct {
	Continuation   Continuation `json:"continuation"`
	TriggerEntryID string       `json:"triggerEntryId"`
}

// InboxItemKind discriminates one inbox item.
type InboxItemKind string

// Inbox item kinds.
const (
	InboxItemSteer    InboxItemKind = "steer"
	InboxItemFollowUp InboxItemKind = "followUp"
	InboxItemNextRun  InboxItemKind = "nextRun"
	InboxItemWrite    InboxItemKind = "write"
)

// InboxItem is one queued inbox item.
type InboxItem struct {
	EntryID string        `json:"entryId"`
	Kind    InboxItemKind `json:"kind"`
}

// NormalizedRetryPolicy is the normalized retry policy carried by an
// operation.
type NormalizedRetryPolicy struct {
	MaxAttempts     int     `json:"maxAttempts"`
	BaseDelayMs     float64 `json:"baseDelayMs"`
	MaxAgentDelayMs float64 `json:"maxAgentDelayMs"`
}

// GenerationContext is the durable state of one assistant generation.
type GenerationContext struct {
	StepID               string                    `json:"stepId"`
	TriggerEntryID       string                    `json:"triggerEntryId"`
	Configuration        LaneConfiguration         `json:"configuration"`
	StreamOptions        AgentHarnessStreamOptions `json:"streamOptions"`
	RetryPolicy          NormalizedRetryPolicy     `json:"retryPolicy"`
	OverflowRecoveryUsed bool                      `json:"overflowRecoveryUsed"`
}

// ToolCall is one planned or completed tool call.
type ToolCall struct {
	SourceIndex   int    `json:"sourceIndex"`
	ResultEntryID string `json:"resultEntryId"`
	Status        string `json:"status"`
	Replay        string `json:"replay,omitempty"`
	Terminate     *bool  `json:"terminate,omitempty"`
}

// ToolBatch is the nested tool-call collection state.
type ToolBatch struct {
	AssistantEntryID string            `json:"assistantEntryId"`
	Configuration    LaneConfiguration `json:"configuration"`
	TurnID           string            `json:"turnId"`
	Calls            []ToolCall        `json:"calls"`
}

// SummaryContext is the durable state of one summary generation.
type SummaryContext struct {
	ResultEntryID string                    `json:"resultEntryId"`
	Configuration LaneConfiguration         `json:"configuration"`
	StreamOptions AgentHarnessStreamOptions `json:"streamOptions"`
	RetryPolicy   NormalizedRetryPolicy     `json:"retryPolicy"`
}

// Cancellable is the cancellation half of an operation scope.
type Cancellable struct {
	Control Control `json:"control"`
}

// RunSettings is the uniform run settings of an operation.
type RunSettings struct {
	Compaction    CompactionSettings           `json:"compaction"`
	SteeringMode  agenttypes.QueueMode         `json:"steeringMode"`
	FollowUpMode  agenttypes.QueueMode         `json:"followUpMode"`
	ToolExecution agenttypes.ToolExecutionMode `json:"toolExecution"`
}

// OperationScope is the uniform scope carried by every operation leaf.
type OperationScope struct {
	Cancellable
	Settings               RunSettings `json:"settings"`
	LatestAssistantEntryID *string     `json:"latestAssistantEntryId"`
}

// RetryWait is the shared backoff data for every retry-wait leaf.
type RetryWait struct {
	NextAttempt  int     `json:"nextAttempt"`
	NotBefore    float64 `json:"notBefore"`
	ErrorMessage string  `json:"errorMessage"`
}

// AssistantGenerationScope is the generation scope of an assistant leaf.
type AssistantGenerationScope struct {
	GenerationContext GenerationContext `json:"generationContext"`
}

// ResultBoundary decides where a summary result is committed.
type ResultBoundary struct {
	Kind        string          `json:"kind"`
	ResumeAfter *CheckpointData `json:"resumeAfter,omitempty"`
	TargetID    *string         `json:"targetId,omitempty"`
	Label       *string         `json:"label,omitempty"`
}

// SummaryTask is one summary task.
type SummaryTask struct {
	TaskID             string         `json:"taskId"`
	Reason             *string        `json:"reason,omitempty"`
	CustomInstructions *string        `json:"customInstructions,omitempty"`
	Boundary           ResultBoundary `json:"boundary"`
}

// SummaryGenerationScope is the generation scope of a summary leaf.
type SummaryGenerationScope struct {
	Task           SummaryTask    `json:"task"`
	SummaryContext SummaryContext `json:"summaryContext"`
}

// SummaryGenerationReady is the ready summary generation state.
type SummaryGenerationReady struct {
	SummaryGenerationScope
	NextAttempt int `json:"nextAttempt"`
}

// SummaryGenerationEffectPending is the effect-pending summary state.
type SummaryGenerationEffectPending struct {
	SummaryGenerationScope
	Attempt  int                `json:"attempt"`
	Request  *SummaryRequestRef `json:"request,omitempty"`
	UsageIDs []string           `json:"usageIds"`
}

// SummaryRequestRef is the in-flight summary request identity.
type SummaryRequestRef struct {
	Index   int    `json:"index"`
	UsageID string `json:"usageId"`
}

// SummaryGenerationRetryWait is the retry-wait summary state.
type SummaryGenerationRetryWait struct {
	SummaryGenerationScope
	RetryWait
}

// DeferredScope is the scope of a deferred generation.
type DeferredScope struct {
	OperationScope
	StepID        string                    `json:"stepId"`
	SourceEntryID string                    `json:"sourceEntryId"`
	Poll          int                       `json:"poll"`
	Configuration LaneConfiguration         `json:"configuration"`
	StreamOptions AgentHarnessStreamOptions `json:"streamOptions"`
}

// StartingOperation is the first durable operation leaf.
type StartingOperation struct {
	OperationScope
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *StartingOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *StartingOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// CheckpointOperation is a checkpoint operation leaf.
type CheckpointOperation struct {
	OperationScope
	CheckpointData
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *CheckpointOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *CheckpointOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// AssistantReadyOperation is the assistant ready leaf.
type AssistantReadyOperation struct {
	OperationScope
	AssistantGenerationScope
	At          OperationAt `json:"at"`
	NextAttempt int         `json:"nextAttempt"`
}

// StateAt returns the operation discriminator.
func (o *AssistantReadyOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *AssistantReadyOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// AssistantEffectPendingOperation is the assistant effect-pending leaf.
type AssistantEffectPendingOperation struct {
	OperationScope
	AssistantGenerationScope
	At                  OperationAt `json:"at"`
	Attempt             int         `json:"attempt"`
	ResponseEntryID     string      `json:"responseEntryId"`
	UsageID             string      `json:"usageId"`
	IntendedOutputLimit float64     `json:"intendedOutputLimit"`
	ContextWindow       float64     `json:"contextWindow"`
}

// StateAt returns the operation discriminator.
func (o *AssistantEffectPendingOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *AssistantEffectPendingOperation) OperationScopeSnapshot() OperationScope {
	return o.OperationScope
}

// AssistantRetryWaitOperation is the assistant retry-wait leaf.
type AssistantRetryWaitOperation struct {
	OperationScope
	AssistantGenerationScope
	RetryWait
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *AssistantRetryWaitOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *AssistantRetryWaitOperation) OperationScopeSnapshot() OperationScope {
	return o.OperationScope
}

// ToolsOperation is the tool execution leaf.
type ToolsOperation struct {
	OperationScope
	At    OperationAt `json:"at"`
	Batch ToolBatch   `json:"batch"`
}

// StateAt returns the operation discriminator.
func (o *ToolsOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *ToolsOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// DeferredSuspendedOperation is the suspended deferred leaf.
type DeferredSuspendedOperation struct {
	DeferredScope
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *DeferredSuspendedOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *DeferredSuspendedOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// DeferredEffectPendingOperation is the effect-pending deferred leaf.
type DeferredEffectPendingOperation struct {
	DeferredScope
	At              OperationAt `json:"at"`
	ResponseEntryID string      `json:"responseEntryId"`
	UsageID         string      `json:"usageId"`
}

// StateAt returns the operation discriminator.
func (o *DeferredEffectPendingOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *DeferredEffectPendingOperation) OperationScopeSnapshot() OperationScope {
	return o.OperationScope
}

// SummaryDecidingOperation is the summary decision leaf.
type SummaryDecidingOperation struct {
	OperationScope
	At   OperationAt `json:"at"`
	Task SummaryTask `json:"task"`
}

// StateAt returns the operation discriminator.
func (o *SummaryDecidingOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *SummaryDecidingOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// SummaryReadyOperation is the summary ready leaf.
type SummaryReadyOperation struct {
	OperationScope
	SummaryGenerationReady
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *SummaryReadyOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *SummaryReadyOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// SummaryEffectPendingOperation is the summary effect-pending leaf.
type SummaryEffectPendingOperation struct {
	OperationScope
	SummaryGenerationEffectPending
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *SummaryEffectPendingOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *SummaryEffectPendingOperation) OperationScopeSnapshot() OperationScope {
	return o.OperationScope
}

// SummaryRetryWaitOperation is the summary retry-wait leaf.
type SummaryRetryWaitOperation struct {
	OperationScope
	SummaryGenerationRetryWait
	At OperationAt `json:"at"`
}

// StateAt returns the operation discriminator.
func (o *SummaryRetryWaitOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *SummaryRetryWaitOperation) OperationScopeSnapshot() OperationScope { return o.OperationScope }

// NavigationReadyToCommitOperation is the navigation ready-to-commit leaf.
type NavigationReadyToCommitOperation struct {
	OperationScope
	At       OperationAt `json:"at"`
	TargetID *string     `json:"targetId"`
	Label    *string     `json:"label,omitempty"`
}

// StateAt returns the operation discriminator.
func (o *NavigationReadyToCommitOperation) StateAt() OperationAt { return o.At }

// OperationScopeSnapshot returns a copy of the uniform scope.
func (o *NavigationReadyToCommitOperation) OperationScopeSnapshot() OperationScope {
	return o.OperationScope
}

// OperationAt discriminates one durable operation leaf.
type OperationAt string

// Operation leaf discriminators.
const (
	OperationAtStarting                OperationAt = "starting"
	OperationAtCheckpoint              OperationAt = "checkpoint"
	OperationAtAssistantReady          OperationAt = "assistant.ready"
	OperationAtAssistantEffectPending  OperationAt = "assistant.effect_pending"
	OperationAtAssistantRetryWait      OperationAt = "assistant.retry_wait"
	OperationAtTools                   OperationAt = "tools"
	OperationAtDeferredSuspended       OperationAt = "deferred.suspended"
	OperationAtDeferredEffectPending   OperationAt = "deferred.effect_pending"
	OperationAtSummaryDeciding         OperationAt = "summary.deciding"
	OperationAtSummaryReady            OperationAt = "summary.ready"
	OperationAtSummaryEffectPending    OperationAt = "summary.effect_pending"
	OperationAtSummaryRetryWait        OperationAt = "summary.retry_wait"
	OperationAtNavigationReadyToCommit OperationAt = "navigation.ready_to_commit"
)

// OperationState is the flat durable operation state.
type OperationState interface {
	StateAt() OperationAt
	OperationScopeSnapshot() OperationScope
}

// OperationScopeOf copies only the uniform operation scope.
func OperationScopeOf(state OperationState) OperationScope {
	if state == nil {
		return OperationScope{}
	}
	return state.OperationScopeSnapshot()
}

// Operation pairs durable metadata with its operation state.
type Operation struct {
	Meta  OperationMeta  `json:"meta"`
	State OperationState `json:"state"`
}

// LaneState is the durable state owned by one session lane.
type LaneState struct {
	CurrentOperationID *string     `json:"currentOperationId"`
	LastOperationID    *string     `json:"lastOperationId"`
	Inbox              []InboxItem `json:"inbox"`
}

// PendingEntry is a pending entry payload.
type PendingEntry struct {
	Type       string                   `json:"type"`
	Payload    *agenttypes.AgentMessage `json:"payload,omitempty"`
	CustomType string                   `json:"customType,omitempty"`
	Data       JsonValue                `json:"data,omitempty"`
}

// DurableFileOperations are the file operations recorded on a preparation.
type DurableFileOperations struct {
	Read    []string `json:"read"`
	Written []string `json:"written"`
	Edited  []string `json:"edited"`
}

// DurableStructuralPreparation is the durable structural preparation union.
type DurableStructuralPreparation struct {
	Kind                string                    `json:"kind"`
	MessagesToSummarize []agenttypes.AgentMessage `json:"messagesToSummarize,omitempty"`
	TurnPrefixMessages  []agenttypes.AgentMessage `json:"turnPrefixMessages,omitempty"`
	RetainedTail        []agenttypes.AgentMessage `json:"retainedTail,omitempty"`
	IsSplitTurn         *bool                     `json:"isSplitTurn,omitempty"`
	TokensBefore        *float64                  `json:"tokensBefore,omitempty"`
	PreviousSummary     *string                   `json:"previousSummary,omitempty"`
	FileOps             *DurableFileOperations    `json:"fileOps,omitempty"`
	Settings            *CompactionSettings       `json:"settings,omitempty"`
	Messages            []agenttypes.AgentMessage `json:"messages,omitempty"`
	TotalTokens         *float64                  `json:"totalTokens,omitempty"`
}

// UsageRow is one persisted usage row.
type UsageRow struct {
	ID         string        `json:"id"`
	Seq        int           `json:"seq"`
	Usage      aitypes.Usage `json:"usage"`
	EntryID    *string       `json:"entryId,omitempty"`
	Adjustment bool          `json:"adjustment"`
	Details    JsonValue     `json:"details,omitempty"`
}

// Write is one requested storage mutation. The concrete variants live with
// the session value helpers; the interface keeps the shared storage contract a
// leaf without importing those helpers. WriteKind mirrors the upstream `kind`
// discriminator.
type Write interface{ WriteKind() string }

// EntryWrite writes one entry.
type EntryWrite struct {
	Entry NewEntry
}

// WriteKind returns the entry discriminator.
func (EntryWrite) WriteKind() string { return "entry" }

// UsageWrite writes one usage row.
type UsageWrite struct {
	Row UsageRow
}

// WriteKind returns the usage discriminator.
func (UsageWrite) WriteKind() string { return "usage" }

// Value is a stored value address.
type Value struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Kind      string `json:"kind"`
}

// ValueList is a stored list address.
type ValueList = Value

// StoredValue is one stored value with its sequence.
type StoredValue struct {
	Address Value `json:"address"`
	Value   any   `json:"value"`
	Seq     int   `json:"seq"`
}

// ListElement is one stored list element.
type ListElement struct {
	Seq   int `json:"seq"`
	Value any `json:"value"`
}

// ListCursor is a list read cursor.
type ListCursor struct {
	Seq int `json:"seq"`
}

// ListReadOptions bounds one list read.
type ListReadOptions struct {
	Cursor *ListCursor `json:"cursor,omitempty"`
	Order  *string     `json:"order,omitempty"`
	Limit  *int        `json:"limit,omitempty"`
}

// ValueWrite is a stored scalar-value mutation.
type ValueWrite interface {
	Write
	ValueOp() string
}

// ValueSetWrite sets one scalar value.
type ValueSetWrite struct {
	Namespace string
	Key       string
	Value     any
}

// WriteKind returns the value discriminator.
func (ValueSetWrite) WriteKind() string { return "value" }

// ValueOp returns the set operation.
func (ValueSetWrite) ValueOp() string { return "set" }

// ValueDeleteWrite deletes one scalar value.
type ValueDeleteWrite struct {
	Namespace string
	Key       string
}

// WriteKind returns the value discriminator.
func (ValueDeleteWrite) WriteKind() string { return "value" }

// ValueOp returns the delete operation.
func (ValueDeleteWrite) ValueOp() string { return "delete" }

// ListWrite is a stored list mutation.
type ListWrite interface {
	Write
	ListOp() string
}

// ListAppendWrite appends one list element.
type ListAppendWrite struct {
	Namespace string
	Key       string
	Value     any
}

// WriteKind returns the list discriminator.
func (ListAppendWrite) WriteKind() string { return "list" }

// ListOp returns the append operation.
func (ListAppendWrite) ListOp() string { return "append" }

// ListDeleteWrite deletes one whole list.
type ListDeleteWrite struct {
	Namespace string
	Key       string
}

// WriteKind returns the list discriminator.
func (ListDeleteWrite) WriteKind() string { return "list" }

// ListOp returns the delete operation.
func (ListDeleteWrite) ListOp() string { return "delete" }

// CommitResult is the outcome of one storage commit.
type CommitResult struct {
	FirstSeq  int          `json:"firstSeq"`
	Seqs      []int        `json:"seqs"`
	Timestamp float64      `json:"timestamp"`
	Stats     SessionStats `json:"stats"`
}

// EntryStructure is the cheap structural projection of one entry.
type EntryStructure struct {
	ID         string    `json:"id"`
	ParentID   *string   `json:"parentId"`
	Seq        int       `json:"seq"`
	Timestamp  float64   `json:"timestamp"`
	Type       EntryType `json:"type"`
	CustomType *string   `json:"customType,omitempty"`
}

// EntryCursor is a branch or entry scan cursor.
type EntryCursor struct {
	Seq int `json:"seq"`
}

// BranchScan is the branch scan query.
type BranchScan struct {
	Start      *string      `json:"start,omitempty"`
	StopAtType *EntryType   `json:"stopAtType,omitempty"`
	StopAtID   *string      `json:"stopAtId,omitempty"`
	Type       *EntryType   `json:"type,omitempty"`
	CustomType *string      `json:"customType,omitempty"`
	Order      *string      `json:"order,omitempty"`
	Limit      *int         `json:"limit,omitempty"`
	Cursor     *EntryCursor `json:"cursor,omitempty"`
}

// StorageBranchScan is a branch scan with an explicit start entry.
type StorageBranchScan struct {
	BranchScan
	Start string `json:"start"`
}

// EntryScan is the entry scan query.
type EntryScan struct {
	Type       *EntryType `json:"type,omitempty"`
	CustomType *string    `json:"customType,omitempty"`
	FromSeq    *int       `json:"fromSeq,omitempty"`
	ToSeq      *int       `json:"toSeq,omitempty"`
	Order      *string    `json:"order,omitempty"`
	Limit      *int       `json:"limit,omitempty"`
}

// UsageScan is the usage scan query.
type UsageScan struct {
	FromSeq *int    `json:"fromSeq,omitempty"`
	ToSeq   *int    `json:"toSeq,omitempty"`
	Order   *string `json:"order,omitempty"`
	Limit   *int    `json:"limit,omitempty"`
}

// SessionStats are the session totals.
type SessionStats struct {
	MessageCount int           `json:"messageCount"`
	Usage        aitypes.Usage `json:"usage"`
}

// Storage is the durable session storage capability.
type Storage interface {
	Commit(writes []Write, ctx Context) (CommitResult, error)
	GetEntries(ids []string, ctx Context) (map[string]Entry, error)
	GetValue(address Value, ctx Context) (StoredValue, bool, error)
	ScanValues(prefix Value, ctx Context) ([]StoredValue, error)
	ReadList(address ValueList, options *ListReadOptions, ctx Context) ([]ListElement, error)
	ScanBranch(query StorageBranchScan, ctx Context) ([]Entry, error)
	ScanBranchStructure(query StorageBranchScan, ctx Context) ([]EntryStructure, error)
	ScanEntries(query EntryScan, ctx Context) ([]Entry, error)
	ScanUsage(query UsageScan, ctx Context) ([]UsageRow, error)
	GetStats(ctx Context) (SessionStats, error)
	Close(ctx Context) error
}

// SessionMetadata is the identity of one session.
type SessionMetadata struct {
	ID                      string  `json:"id"`
	CreatedAt               float64 `json:"createdAt"`
	StorageVersion          int     `json:"storageVersion"`
	Cwd                     *string `json:"cwd,omitempty"`
	ParentSessionID         *string `json:"parentSessionId,omitempty"`
	LegacyParentSessionPath *string `json:"legacyParentSessionPath,omitempty"`
}

// SessionID returns the session identity. It lets backend-specific metadata
// wrappers satisfy the same generic constraint as SessionMetadata.
func (m SessionMetadata) SessionID() string { return m.ID }

// ParentSessionIDRef returns the optional parent session identity.
func (m SessionMetadata) ParentSessionIDRef() *string { return m.ParentSessionID }

// SessionMetadataView is the structural view required by the generic session
// and repository contracts. SessionMetadata implements it directly; backend
// metadata types embed SessionMetadata and therefore inherit it.
type SessionMetadataView interface {
	SessionID() string
	ParentSessionIDRef() *string
}

// IdGenerator produces stable session entry ids.
type IdGenerator interface {
	Next(timestampMs *float64) string
}

// EntryQuery is the entry query used by readers.
type EntryQuery struct {
	Type       *EntryType   `json:"type,omitempty"`
	CustomType *string      `json:"customType,omitempty"`
	Order      *string      `json:"order,omitempty"`
	Limit      *int         `json:"limit,omitempty"`
	Cursor     *EntryCursor `json:"cursor,omitempty"`
}

// SessionReader is the read capability of one session.
type SessionReader interface {
	GetEntries(ids []string, ctx Context) (map[string]Entry, error)
	GetStats(ctx Context) (SessionStats, error)
	GetValue(address Value, ctx Context) (StoredValue, bool, error)
	ScanValues(prefix Value, ctx Context) ([]StoredValue, error)
	ReadList(address ValueList, options *ListReadOptions, ctx Context) ([]ListElement, error)
	ScanBranch(query StorageBranchScan, ctx Context) ([]Entry, error)
}

// SessionMutation is the exclusive keyless mutation barrier of one session.
type SessionMutation interface {
	SessionReader
	Commit(writes []Write, ctx Context) (CommitResult, error)
	End(ctx Context) error
}

// SessionMutator is a callback-scoped mutation capability without the
// authority to release its session barrier.
type SessionMutator interface {
	SessionReader
	Commit(writes []Write, ctx Context) (CommitResult, error)
}

// SessionMutationCallback is the trusted exclusive mutation callback.
type SessionMutationCallback[T any] func(mutator SessionMutator, ctx Context) (T, error)

// Branch is one named branch of a session.
type Branch interface {
	Name() string
	GetTipID(ctx Context) (*string, error)
	FindEntries(query *BranchScan, ctx Context) ([]Entry, error)
	FindEntry(query *BranchScan, ctx Context) (Entry, bool, error)
	AppendMessage(message agenttypes.AgentMessage, ctx Context) (string, error)
	AppendCustomEntry(customType string, data JsonValue, ctx Context) (string, error)
}

// Session is the public session capability.
type Session[TMetadata SessionMetadataView] interface {
	SessionReader
	Metadata() TMetadata
	IDGenerator() IdGenerator
	GetEntry(id string, ctx Context) (Entry, bool, error)
	GetName(ctx Context) (*string, error)
	GetLabel(targetID string, ctx Context) (*string, error)
	FindEntries(query *EntryQuery, ctx Context) ([]Entry, error)
	FindEntry(query *EntryQuery, ctx Context) (Entry, bool, error)
	Branch(name string, ctx Context) (Branch, bool, error)
	CreateBranch(name string, at *string, ctx Context) (Branch, error)
	BeginMutation(ctx Context) (SessionMutation, error)
	Mutate(mutation SessionMutationCallback[any], ctx Context) (any, error)
	SetValue(address Value, next any, ctx Context) error
	DeleteValue(address Value, ctx Context) error
	AppendList(address ValueList, element any, ctx Context) error
	DeleteList(address ValueList, ctx Context) error
	SetName(name *string, ctx Context) error
	SetLabel(targetID string, label *string, ctx Context) error
	Close(ctx Context) error
}

// SessionCreateOptions are the options for creating a session.
type SessionCreateOptions struct {
	ID              *string `json:"id,omitempty"`
	ParentSessionID *string `json:"parentSessionId,omitempty"`
}

// ForkOptions are the options for forking a session.
type ForkOptions struct {
	Scope    string  `json:"scope"`
	Branch   *string `json:"branch,omitempty"`
	EntryID  *string `json:"entryId,omitempty"`
	Position *string `json:"position,omitempty"`
	ID       *string `json:"id,omitempty"`
}

// SessionRepo creates, opens and forks sessions.
type SessionRepo[TMetadata SessionMetadataView, TCreateOptions any, TListOptions any] interface {
	Create(options TCreateOptions, ctx Context) (Session[TMetadata], error)
	Open(metadata TMetadata, ctx Context) (Session[TMetadata], error)
	List(options *TListOptions, ctx Context) ([]TMetadata, error)
	Delete(metadata TMetadata, ctx Context) error
	Fork(source TMetadata, options ForkOptions, ctx Context) (Session[TMetadata], error)
}
