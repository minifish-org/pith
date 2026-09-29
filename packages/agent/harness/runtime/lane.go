// This file carries the lane mutation line of
// packages/agent/src/harness/runtime/lane.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// One Lane owns one Session mutation line. Effect-free commands run under the
// lane lock; external callbacks (hooks, providers, tools) are never invoked
// while the lock is held. Durable writes and the in-memory projection are
// updated together so a reader never observes a half-applied transition.
package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	harnesshooks "github.com/minifish-org/pith/packages/agent/harness/hooks"
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// laneInvariantError reports a durable-state invariant violation.
type laneInvariantError struct{ message string }

func (e *laneInvariantError) Error() string { return e.message }

// Runtime errors surfaced without leaking implementation detail.
var (
	errOnlyFailedCarriesError      = errors.New("Only a failed operation result may carry an error")
	errRunOperationHasNoTip        = errors.New("Run operation has no Branch tip")
	errPendingEntryMissingPayload  = errors.New("Pending entry is missing its payload")
	errPendingEntryNotMessage      = errors.New("Pending entry is not a message")
	errQueuedMessageMissingPayload = errors.New("Queued message is missing its payload")
	errLaneClosed                  = errors.New("lane is closed")
	errNoCurrentOperation          = errors.New("lane has no current operation")
)

// Models resolves and streams configured provider models. The harness assembly
// injects the concrete implementation; the runtime never creates a provider.
type Models interface {
	// GetModel returns the configured model or false when it is unavailable.
	GetModel(provider string, modelID string) (*aitypes.Model, bool)
	// StreamAssistant opens one assistant stream. The caller owns the returned
	// stream and must consume it to completion or end it.
	StreamAssistant(model *aitypes.Model, requestContext any, options *aitypes.SimpleStreamOptions, ctx harnesstypes.Context) (*aitypes.AssistantMessageEventStream, error)
}

// Lane is the durable, serialized execution owner of one session branch.
type Lane struct {
	Name    string
	Session harnesstypes.Session[harnesstypes.SessionMetadata]
	Models  Models
	Hooks   *harnesshooks.HookRegistry

	// Emit publishes a batch of runtime events. It is invoked after the lane
	// lock is released.
	Emit func(events []Event, ctx harnesstypes.Context) error

	mu     sync.Mutex
	state  harnesstypes.RuntimeLaneState
	config harnesstypes.Config[any]
	closed error
	change chan struct{}
}

// NewLane installs a restored lane state on a session mutation line.
func NewLane(
	name string,
	session harnesstypes.Session[harnesstypes.SessionMetadata],
	models Models,
	hooks *harnesshooks.HookRegistry,
	state harnesstypes.RuntimeLaneState,
	config harnesstypes.Config[any],
	emit func(events []Event, ctx harnesstypes.Context) error,
) *Lane {
	return &Lane{
		Name:    name,
		Session: session,
		Models:  models,
		Hooks:   hooks,
		Emit:    emit,
		state:   state,
		config:  config,
		change:  make(chan struct{}),
	}
}

// LaneState returns the current in-memory projection. Callers must treat the
// returned value as read-only.
func (l *Lane) LaneState() *harnesstypes.RuntimeLaneState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &l.state
}

// ReadConfig returns the current process-local configuration.
func (l *Lane) ReadConfig() harnesstypes.Config[any] {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.config
}

// SetConfig replaces the process-local configuration.
func (l *Lane) SetConfig(config harnesstypes.Config[any]) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.config = config
}

// Close seals the lane. Further commands fail with the supplied error.
func (l *Lane) Close(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed != nil {
		return
	}
	l.closed = err
	if l.Hooks != nil {
		l.Hooks.Close(err)
	}
	close(l.change)
}

// ClosedError returns the terminal close error, or nil.
func (l *Lane) ClosedError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// EmitBatch publishes events after releasing the lane lock.
func (l *Lane) EmitBatch(events []Event, ctx harnesstypes.Context) error {
	if len(events) == 0 || l.Emit == nil {
		return nil
	}
	return l.Emit(events, ctx)
}

// Commit writes a durable transaction. The lane lock must not be held when
// external effects run, but Commit itself only touches the session.
func (l *Lane) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	if len(writes) == 0 {
		return harnesstypes.CommitResult{}, nil
	}
	mutation, err := l.Session.BeginMutation(ctx)
	if err != nil {
		return harnesstypes.CommitResult{}, err
	}
	result, commitErr := mutation.Commit(writes, ctx)
	endErr := mutation.End(ctx)
	if commitErr != nil {
		return harnesstypes.CommitResult{}, commitErr
	}
	if endErr != nil {
		return harnesstypes.CommitResult{}, endErr
	}
	return result, nil
}

// LanePlanner is one effect-free lane command.
type LanePlanner func(state *harnesstypes.RuntimeLaneState, reader harnesstypes.SessionReader) (harnesstypes.LaneCommand[any], error)

// Command runs one effect-free command on the serialized lane mutation line.
// A commit publishes the new projection before the caller sees the result.
func (l *Lane) Command(plan LanePlanner, ctx harnesstypes.Context) (any, error) {
	l.mu.Lock()
	if l.closed != nil {
		err := l.closed
		l.mu.Unlock()
		return nil, err
	}
	decision, err := plan(&l.state, l.Session)
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	switch decision.Kind {
	case harnesstypes.LaneCommandReturn:
		var result any
		if decision.Result != nil {
			result = *decision.Result
		}
		l.mu.Unlock()
		return result, nil
	case harnesstypes.LaneCommandReject:
		l.mu.Unlock()
		return nil, decision.Error
	case harnesstypes.LaneCommandCommit:
		next := decision.Next
		if next == nil {
			next = &l.state
		}
		commit, err := l.Commit(decision.Writes, ctx)
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
		_ = commit
		l.state = *next
		if l.change != nil {
			close(l.change)
			l.change = make(chan struct{})
		}
		l.mu.Unlock()
		var result any
		if decision.Result != nil {
			result = *decision.Result
		}
		return result, nil
	default:
		l.mu.Unlock()
		return nil, errors.New("unknown lane command kind")
	}
}

// OperationPlanner is one durable operation transition.
type OperationPlanner func(
	state *harnesstypes.RuntimeLaneState,
	current harnesstypes.OperationState,
	meta harnesstypes.OperationMeta,
	reader harnesstypes.SessionReader,
) (harnesstypes.OperationCommand[any], error)

// SettleOperation runs one operation transition even after cancellation is
// requested, so admitted effects can settle.
func (l *Lane) SettleOperation(
	capability harnesstypes.OperationState,
	plan OperationPlanner,
	ctx harnesstypes.Context,
) (any, error) {
	return l.Command(func(state *harnesstypes.RuntimeLaneState, reader harnesstypes.SessionReader) (harnesstypes.LaneCommand[any], error) {
		if state.Operation == nil {
			return harnesstypes.LaneCommand[any]{Kind: harnesstypes.LaneCommandReject, Error: errNoCurrentOperation}, nil
		}
		operation := state.Operation
		decision, err := plan(state, operation.State, operation.Meta, reader)
		if err != nil {
			return harnesstypes.LaneCommand[any]{}, err
		}
		switch decision.Kind {
		case harnesstypes.OperationCommandCommit:
			writes := append([]harnesstypes.Write{}, decision.Writes...)
			writes = append(writes, harnesssession.SetValue(harnesssession.OperationState(operation.Meta.OperationID), decision.OperationState))
			next := *state
			if decision.Lane != nil {
				next.TipID = decision.Lane.TipID
				next.Inbox = decision.Lane.Inbox
				next.Configuration = decision.Lane.Configuration
			}
			writes = append(writes, harnesssession.SetValue(harnesssession.LaneState(l.Name), durableLaneState(operation.Meta.OperationID, next.Inbox, next.LastOperationID)))
			next.Operation = &harnesstypes.Operation{Meta: operation.Meta, State: decision.OperationState}
			return harnesstypes.LaneCommand[any]{
				Kind:   harnesstypes.LaneCommandCommit,
				Writes: writes,
				Next:   &next,
				Result: decision.Result,
			}, nil
		case harnesstypes.OperationCommandFinish:
			writes := append([]harnesstypes.Write{}, decision.Writes...)
			if decision.Record != nil {
				writes = append(writes, harnesssession.SetValue(harnesssession.OperationResult(operation.Meta.OperationID), *decision.Record))
			}
			next := *state
			if decision.Lane != nil {
				next.TipID = decision.Lane.TipID
				next.Inbox = decision.Lane.Inbox
				next.Configuration = decision.Lane.Configuration
			}
			lastID := operation.Meta.OperationID
			next.LastOperationID = &lastID
			next.Operation = nil
			writes = append(writes, harnesssession.SetValue(harnesssession.LaneState(l.Name), durableLaneState("", next.Inbox, next.LastOperationID)))
			return harnesstypes.LaneCommand[any]{
				Kind:   harnesstypes.LaneCommandCommit,
				Writes: writes,
				Next:   &next,
				Result: decision.Result,
			}, nil
		default:
			return harnesstypes.LaneCommand[any]{
				Kind:   harnesstypes.LaneCommandReturn,
				Result: decision.Result,
			}, nil
		}
	}, ctx)
}

// ContinueOperation runs an ordinary operation command only while durable
// control is running.
func (l *Lane) ContinueOperation(
	capability harnesstypes.OperationState,
	plan OperationPlanner,
	ctx harnesstypes.Context,
) (harnesstypes.ContinueOperationResult[any], error) {
	scope := harnesstypes.OperationScopeOf(capability)
	if scope.Control.Status == "cancel_requested" {
		return harnesstypes.ContinueOperationResult[any]{Kind: harnesstypes.ContinueOperationCancelRequested}, nil
	}
	value, err := l.SettleOperation(capability, plan, ctx)
	if err != nil {
		return harnesstypes.ContinueOperationResult[any]{}, err
	}
	return harnesstypes.ContinueOperationResult[any]{Kind: harnesstypes.ContinueOperationResultKind, Value: &value}, nil
}

// encodeEvent marshals one runtime event into the event bus payload shape.
func encodeEvent(event Event) (json.RawMessage, error) {
	return json.Marshal(event)
}

// durableLaneState builds the stored lane state for one transition.
func durableLaneState(currentOperationID string, inbox []harnesstypes.InboxItem, lastOperationID *string) harnesstypes.LaneState {
	state := harnesstypes.LaneState{Inbox: inbox, LastOperationID: lastOperationID}
	if currentOperationID != "" {
		id := currentOperationID
		state.CurrentOperationID = &id
	}
	return state
}

var _ = context.Background
var _ = agenttypes.AgentMessage{}
