// This file carries the runtime drive contract of
// packages/agent/src/harness/runtime/types.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A drive pass owns a process-local effect gate, a value-preserving copy of the
// invocation context and an independent close signal. The upstream Promise
// completion is modeled as a buffered channel carrying either the settled
// outcome or the terminal error.
package harnesstypes

import (
	"context"
	"sync"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnessexecution "github.com/minifish-org/pith/packages/agent/harness/execution"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// SliceNotImplemented reports that an operation belongs to a later harness
// slice.
type SliceNotImplemented struct {
	Operation string
}

func (e *SliceNotImplemented) Error() string {
	return e.Operation + " is not implemented until its later AgentHarness slice"
}

// Config is the current process-local harness configuration.
type Config[TContext any] struct {
	Tools              []AgentHarnessTool[TContext, any, any]
	Resources          Resources
	StreamOptions      AgentHarnessStreamOptions
	RetryPolicy        RetryPolicy
	Compaction         CompactionSettings
	SteeringMode       agenttypes.QueueMode
	FollowUpMode       agenttypes.QueueMode
	ToolExecution      agenttypes.ToolExecutionMode
	ToolContext        *AgentHarnessToolContextSource[TContext]
	SystemPrompt       *string
	SystemPromptFn     func(toolContext TContext, ctx Context) (string, error)
	ToProviderMessages func(messages []agenttypes.AgentMessage, ctx Context) ([]aitypes.Message, error)
	EntryProjectors    map[string]EntryProjector
}

// RuntimeLaneState is the current durable state owned by one lane. It is the
// runtime projection; the session LaneState is the persisted subset.
type RuntimeLaneState struct {
	TipID           *string           `json:"tipId"`
	Configuration   LaneConfiguration `json:"configuration"`
	Inbox           []InboxItem       `json:"inbox"`
	LastOperationID *string           `json:"lastOperationId"`
	Operation       *Operation        `json:"operation"`
}

// LaneCommand is one effect-free decision made on a lane's serialized mutation
// line.
type LaneCommand[TResult any] struct {
	Kind   string
	Writes []Write
	Next   *RuntimeLaneState
	Result *TResult
	Error  error
}

// Lane command kinds.
const (
	LaneCommandCommit = "commit"
	LaneCommandReturn = "return"
	LaneCommandReject = "reject"
)

// ContinueOperationResult is the outcome of continuing an installed operation.
type ContinueOperationResult[TResult any] struct {
	Kind  string
	Value *TResult
}

// Continue operation kinds.
const (
	ContinueOperationCancelRequested = "cancel_requested"
	ContinueOperationResultKind      = "result"
)

// OperationCommand is one durable operation transition.
type OperationCommand[TResult any] struct {
	Kind           string
	Writes         []Write
	Record         *OperationResultRecord
	OperationState OperationState
	Lane           *RuntimeLaneState
	Result         *TResult
	Error          error
}

// Operation command kinds.
const (
	OperationCommandCommit = "commit"
	OperationCommandFinish = "finish"
	OperationCommandReturn = "return"
)

// DriveCompletion is the result of one drive pass.
type DriveCompletion struct {
	Outcome DriveOutcome
	Err     error
}

// Drive is one installed process-local drive pass.
type Drive struct {
	OperationID     string
	Completion      chan DriveCompletion
	Gate            harnessexecution.Gate
	Context         Context
	WaitForRetry    bool
	CloseSignal     <-chan struct{}
	DeferredPermits int

	mu          sync.Mutex
	control     harnessexecution.GateControl
	closeCancel context.CancelFunc
	settled     bool
}

// NewDrive installs a new drive pass for options. The invocation context keeps
// its values but ignores caller cancellation; cleanup must remain possible
// after the caller aborts.
func NewDrive(options DriveOptions, ctx Context) *Drive {
	gate, control := harnessexecution.CreateGate()
	driveContext := harnesscontext.WithoutAbortSignal(ctx)
	closeContext, closeCancel := context.WithCancel(context.Background())
	drive := &Drive{
		OperationID: options.OperationID,
		Completion:  make(chan DriveCompletion, 1),
		Gate:        gate,
		Context:     driveContext,
		CloseSignal: closeContext.Done(),
		control:     control,
		closeCancel: closeCancel,
	}
	if options.WaitForRetry != nil {
		drive.WaitForRetry = *options.WaitForRetry
	}
	if options.PollDeferred != nil && *options.PollDeferred {
		drive.DeferredPermits = 1
	}
	return drive
}

func (d *Drive) settleOnce(completion DriveCompletion) {
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return
	}
	d.settled = true
	d.mu.Unlock()
	d.Completion <- completion
	close(d.Completion)
}

// Settle resolves the completion with a settled outcome.
func (d *Drive) Settle(outcome DriveOutcome) {
	d.settleOnce(DriveCompletion{Outcome: outcome})
}

// Fail rejects the completion with an error.
func (d *Drive) Fail(err error) {
	d.settleOnce(DriveCompletion{Err: err})
}

// BeginAbort requests abort of the gate's admitted effects.
func (d *Drive) BeginAbort(cancellation <-chan struct{}) {
	d.control.BeginAbort(cancellation)
}

// SignalAbort closes the public gate signal for an aborting gate.
func (d *Drive) SignalAbort() {
	d.control.SignalAbort()
}

// CloseGate seals the gate, closes the close signal and rejects the completion.
func (d *Drive) CloseGate(err error) {
	d.control.Close(err)
	d.closeCancel()
	d.settleOnce(DriveCompletion{Err: err})
}

// ProcedureResult is the outcome of running one installed operation procedure.
type ProcedureResult struct {
	Kind    string
	Outcome *DriveOutcome
	Record  *OperationResultRecord
}

// Procedure result kinds.
const (
	ProcedureResultContinue = "continue"
	ProcedureResultWaiting  = "waiting"
	ProcedureResultSettled  = "settled"
)
