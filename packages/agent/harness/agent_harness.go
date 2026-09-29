// This file carries the public harness assembly surface of
// packages/agent/src/harness/agent-harness.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream file is almost entirely a type surface: it re-exports the
// tagged result errors, declares the lane/operation/event/hook data types and
// binds `AgentHarness.create` to the durable runtime constructor. The Go port
// keeps that split: this package is the embeddable assembly facade, the
// concrete mutation line lives in harness/runtime, tagged results in
// harness/result, the passive bus in harness/events and the ordered hook
// registry in harness/hooks.
//
// Go has no structural union types, so the upstream discriminated unions are
// modelled as interfaces or as the union member both sides can share; every
// alias below points at a real declaration, never at a placeholder.
package harness

import (
	"encoding/json"

	harnesevents "github.com/minifish-org/pith/packages/agent/harness/events"
	harnesshooks "github.com/minifish-org/pith/packages/agent/harness/hooks"
	harnessresult "github.com/minifish-org/pith/packages/agent/harness/result"
	agentruntime "github.com/minifish-org/pith/packages/agent/harness/runtime"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// --- tagged result errors (re-exported from result.ts) ---------------------

// Closed reports an operation against a closed harness.
type Closed = harnessresult.Closed

// HarnessClosed reports that the harness closed while an operation was active.
type HarnessClosed = harnessresult.HarnessClosed

// HarnessFault wraps an unexpected harness failure with its cause.
type HarnessFault = harnessresult.HarnessFault

// InvalidLane reports an unknown lane name.
type InvalidLane = harnessresult.InvalidLane

// InvalidMessage reports a malformed queued message.
type InvalidMessage = harnessresult.InvalidMessage

// InvalidNavigation reports an invalid navigation target.
type InvalidNavigation = harnessresult.InvalidNavigation

// LaneBusy reports that a lane already owns a different operation.
type LaneBusy = harnessresult.LaneBusy

// NoActiveOperation reports that no operation is active on the lane.
type NoActiveOperation = harnessresult.NoActiveOperation

// NoActiveRun reports that no run is active on the lane.
type NoActiveRun = harnessresult.NoActiveRun

// NothingToCompact reports that the lane has nothing to compact.
type NothingToCompact = harnessresult.NothingToCompact

// NothingToResume reports that the lane has nothing to resume.
type NothingToResume = harnessresult.NothingToResume

// OperationMismatch reports that an operation id does not match the lane.
type OperationMismatch = harnessresult.OperationMismatch

// UnknownSkill reports an unknown skill name.
type UnknownSkill = harnessresult.UnknownSkill

// UnknownTarget reports an unknown navigation target.
type UnknownTarget = harnessresult.UnknownTarget

// UnknownTemplate reports an unknown prompt template name.
type UnknownTemplate = harnessresult.UnknownTemplate

// SliceNotImplemented reports that an operation belongs to a later harness
// slice.
type SliceNotImplemented = harnesstypes.SliceNotImplemented

// --- operation results -----------------------------------------------------

// SuspendedRun observes a run suspended on a deferred handle. It is the
// convenience-only public counterpart of the runtime deferred operation.
type SuspendedRun struct {
	OperationID string                  `json:"operationId"`
	Status      string                  `json:"status"`
	Deferred    *aitypes.DeferredHandle `json:"deferred"`
}

// RunResult is the outcome of one run admission.
//
// Go adaptation: the upstream success union (OperationResultRecord |
// SuspendedRun) and the error union have no structural Go equivalent, so the
// shared Result carrier is parameterised with `any`/`error` and callers use
// MatchError over the tagged error family.
type RunResult = harnessresult.Result[any, error]

// CompactionResult is the outcome of one compaction admission.
type CompactionResult = harnessresult.Result[any, error]

// NavigationResult is the outcome of one tree navigation.
type NavigationResult = harnessresult.Result[any, error]

// ResumeResult is the outcome of resuming a suspended or interrupted run.
type ResumeResult = harnessresult.Result[any, error]

// QueueResult is the outcome of queuing a steering or follow-up message.
type QueueResult = harnessresult.Result[any, error]

// CancelQueuedResult is the outcome of cancelling a queued entry.
type CancelQueuedResult = harnessresult.Result[any, error]

// AbortResult is the outcome of aborting the active operation.
type AbortResult = harnessresult.Result[any, error]

// RecordUsageResult is the outcome of appending a usage row.
type RecordUsageResult = harnessresult.Result[any, error]

// AbortRequestResult is the outcome of requesting an abort on a lane.
type AbortRequestResult = harnessresult.Result[any, error]

// DriveResult is the outcome of one drive pass.
type DriveResult = harnessresult.Result[harnesstypes.DriveOutcome, error]

// --- operation requests and admissions -------------------------------------

// NavigateOptions are the optional arguments of a tree navigation.
type NavigateOptions struct {
	Summarize          *bool   `json:"summarize,omitempty"`
	Label              *string `json:"label,omitempty"`
	CustomInstructions *string `json:"customInstructions,omitempty"`
}

// OperationRequest is one admitted operation request.
//
// The upstream discriminated union is flattened into a tagged struct: Kind
// selects the meaningful fields, exactly as the upstream `kind` discriminant
// does. Fields absent for a kind stay at their zero value.
type OperationRequest struct {
	Kind                   string                 `json:"kind"`
	OperationID            *string                `json:"operationId,omitempty"`
	Prompt                 any                    `json:"prompt,omitempty"`
	Images                 []aitypes.ImageContent `json:"images,omitempty"`
	Name                   string                 `json:"name,omitempty"`
	AdditionalInstructions *string                `json:"additionalInstructions,omitempty"`
	Args                   []string               `json:"args,omitempty"`
	CustomInstructions     *string                `json:"customInstructions,omitempty"`
	TargetID               *string                `json:"targetId,omitempty"`
	Options                *NavigateOptions       `json:"options,omitempty"`
}

// OperationAdmission is the accepted operation descriptor.
type OperationAdmission struct {
	OperationID string  `json:"operationId"`
	Kind        string  `json:"kind"`
	StartedAt   float64 `json:"startedAt"`
}

// OperationAdmissionError is the union of admission failures.
type OperationAdmissionError = error

// OperationAdmissionResult is one admission outcome.
type OperationAdmissionResult = harnessresult.Result[OperationAdmission, error]

// --- drive and inspection --------------------------------------------------

// DriveOptions are the request options for one installed drive pass.
type DriveOptions = harnesstypes.DriveOptions

type DriveOutcome = harnesstypes.DriveOutcome

// ModelIdentity names a provider/model pair.
type ModelIdentity = harnesstypes.ModelIdentity

// OperationStatus is the live status of an admitted operation.
type OperationStatus string

// Operation statuses.
const (
	OperationStatusRunning  OperationStatus = "running"
	OperationStatusOpen     OperationStatus = "open"
	OperationStatusAborting OperationStatus = "aborting"
)

// CurrentOperationInfo describes the operation currently installed on a lane.
type CurrentOperationInfo struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	StartedAt     float64         `json:"startedAt"`
	Status        OperationStatus `json:"status"`
	CapturedModel *ModelIdentity  `json:"capturedModel,omitempty"`
}

// LaneExecutionInfo is the execution inspection of one lane.
type LaneExecutionInfo struct {
	Lane            string                `json:"lane"`
	TipID           *string               `json:"tipId"`
	ConfiguredModel ModelIdentity         `json:"configuredModel"`
	Current         *CurrentOperationInfo `json:"current"`
	LastOperationID *string               `json:"lastOperationId"`
}

// --- lane and session snapshots --------------------------------------------

// WatchHandle is one live subscription that starts from a snapshot.
type WatchHandle = harnesevents.WatchHandle[agentruntime.LaneSnapshot]

// LaneInfo is the reader-facing lane summary.
type LaneInfo = agentruntime.LaneInfo

// LaneSnapshotTool is the mutable tool projection of a lane snapshot.
type LaneSnapshotTool = agentruntime.LaneSnapshotTool

// OpenOperation describes one operation restored or created by the harness.
type OpenOperation = agentruntime.OpenOperation

// LaneQueuedItem is one queued inbox projection item.
type LaneQueuedItem = agentruntime.LaneQueuedItem

// LaneSnapshot is the complete observable lane snapshot.
type LaneSnapshot = agentruntime.LaneSnapshot

// SessionSnapshot is the harness-wide snapshot of every lane.
type SessionSnapshot struct {
	Lanes   []LaneInfo `json:"lanes"`
	Faulted bool       `json:"faulted"`
}

// --- events ----------------------------------------------------------------

// HarnessEventPayload is the type-specific body of a harness event.
//
// Go adaptation: the events bus carries every body as strict JSON in
// HarnessEvent.Payload, so the payload union is represented by json.RawMessage
// rather than by one Go type per upstream variant.
type HarnessEventPayload = json.RawMessage

// SpecialEventPayload is a payload delivered independently of a lane.
type SpecialEventPayload = json.RawMessage

// LaneEventPayload is a payload delivered on one lane.
type LaneEventPayload = json.RawMessage

// ConfigEventPayload is a payload describing a configuration change.
type ConfigEventPayload = json.RawMessage

// LaneConfigEventPayload is a lane-scoped configuration change.
type LaneConfigEventPayload = json.RawMessage

// GlobalConfigEventPayload is a harness-scoped configuration change.
type GlobalConfigEventPayload = json.RawMessage

// HandlerErrorPayload is the payload describing a listener or hook failure.
type HandlerErrorPayload = json.RawMessage

// HarnessEvent is one observable harness lifecycle event.
type HarnessEvent = harnesevents.HarnessEvent

// LaneTranscriptSnapshot is a strict-JSON lane snapshot for remote consumers.
type LaneTranscriptSnapshot = json.RawMessage

// LaneWatchEvent is a strict-JSON reducer-relevant event for remote consumers.
type LaneWatchEvent = json.RawMessage

// HarnessEventType is the event type discriminator.
type HarnessEventType = harnesevents.HarnessEventType

// EventListener observes one delivered event.
type EventListener = harnesevents.EventListener

// Events is the passive subscription surface of the event bus.
type Events = harnesevents.Events

// Resources are the harness prompt resources.
type Resources = harnesstypes.Resources

// --- hooks -----------------------------------------------------------------

// HookName is the stable hook discriminator.
type HookName = harnesshooks.HookName

// HookHandler handles one hook invocation.
type HookHandler = harnesshooks.HookHandler

// HookMap maps each hook name to its invocation result surface.
//
// Go adaptation: the typed event/result pairs live in the hooks package; the
// map is kept for callers that dispatch hooks dynamically.
type HookMap map[HookName]any

// HookInvocation is one hook invocation enriched with lane and run identity.
type HookInvocation[TEvent any] struct {
	Event TEvent `json:"event"`
	Lane  string `json:"lane"`
	RunID string `json:"runId"`
}

// Hooks is the ordered hook registration surface.
type Hooks interface {
	On(name HookName, handler HookHandler, options ...*harnesshooks.HookRegistrationOptions) func()
}

// --- assembly --------------------------------------------------------------

// EntryProjector projects a custom entry into provider messages.
type EntryProjector = harnesstypes.EntryProjector

// AgentHarnessOptions are the process-local harness construction options.
type AgentHarnessOptions[TContext any] = harnesstypes.AgentHarnessOptions[TContext]

// AgentLane is the durable, serialized execution owner of one session branch.
type AgentLane = agentruntime.Lane

// AcquireLaneOptions are the optional arguments of lane acquisition.
type AcquireLaneOptions struct {
	CreateAt *string `json:"createAt,omitempty"`
}

// AgentHarness owns the process-local lanes attached to one open session.
type AgentHarness = agentruntime.Harness

// AgentHarnessConstructor creates a harness around one open session.
type AgentHarnessConstructor interface {
	Create(options harnesstypes.AgentHarnessOptions[any], ctx harnesstypes.Context) (*AgentHarness, []OpenOperation, error)
}

type agentHarnessConstructor struct{}

func (agentHarnessConstructor) Create(options harnesstypes.AgentHarnessOptions[any], ctx harnesstypes.Context) (*AgentHarness, []OpenOperation, error) {
	return agentruntime.CreateAgentHarness(options, ctx)
}

// AgentHarnessFactory is the default harness constructor, the Go equivalent of
// the upstream `AgentHarness` value.
var AgentHarnessFactory AgentHarnessConstructor = agentHarnessConstructor{}

// CreateAgentHarness attaches the durable harness to one open session.
func CreateAgentHarness(options harnesstypes.AgentHarnessOptions[any], ctx harnesstypes.Context) (*AgentHarness, []OpenOperation, error) {
	return agentruntime.CreateAgentHarness(options, ctx)
}

// Ensure the concrete types keep satisfying the shared hook surface.
var _ Hooks = (*harnesshooks.HookRegistry)(nil)
var _ Events = (*harnesevents.HarnessEventBus)(nil)
