// This file carries the progress channels of
// packages/agent/src/harness/runtime/progress.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Progress writes are serialized with lane mutations. A channel seals when the
// owning operation stops matching, so late frames never pollute a later turn.
package agentruntime

import (
	"sync"

	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// ProgressChannel is the ordered writer for one progress stream.
type ProgressChannel[T any] interface {
	Write(item T)
	Seal()
	Drain() error
}

// ReadAssistantFrames reads the bounded pending frame list of one response.
func ReadAssistantFrames(
	reader harnesstypes.SessionReader,
	operationID string,
	responseEntryID string,
	ctx harnesstypes.Context,
) ([]aiutils.AssistantMessageFrame, error) {
	frames := []aiutils.AssistantMessageFrame{}
	var cursor *harnesstypes.ListCursor
	for {
		limit := 1000
		order := "asc"
		options := &harnesstypes.ListReadOptions{Limit: &limit, Order: &order, Cursor: cursor}
		page, err := reader.ReadList(harnesssession.PendingAssistantFrames(operationID, responseEntryID), options, ctx)
		if err != nil {
			return nil, err
		}
		for _, element := range page {
			frame, ok := element.Value.(aiutils.AssistantMessageFrame)
			if !ok {
				if pointer, pointerOK := element.Value.(*aiutils.AssistantMessageFrame); pointerOK && pointer != nil {
					frame = *pointer
				} else {
					continue
				}
			}
			frames = append(frames, frame)
		}
		if len(page) < limit {
			return frames, nil
		}
		last := page[len(page)-1]
		cursor = &harnesstypes.ListCursor{Seq: last.Seq}
	}
}

type progressState struct {
	sealed bool
	last   harnesstypes.CommitResult
}

// openProgress builds one serialized progress channel. Writes are dropped once
// the channel is sealed or the owning operation no longer matches.
func openProgress[T any](
	lane *Lane,
	drive *harnesstypes.Drive,
	commitWrite func(item T) harnesstypes.Write,
	stillOwns func(state *harnesstypes.RuntimeLaneState) bool,
) ProgressChannel[T] {
	channel := &progressChannel[T]{
		lane:        lane,
		drive:       drive,
		commitWrite: commitWrite,
		stillOwns:   stillOwns,
	}
	return channel
}

type progressChannel[T any] struct {
	lane        *Lane
	drive       *harnesstypes.Drive
	commitWrite func(item T) harnesstypes.Write
	stillOwns   func(state *harnesstypes.RuntimeLaneState) bool

	mu     sync.Mutex
	sealed bool
	last   error
}

// syncMutex is a small alias so progress.go stays free of an extra import name.

// Write serializes one progress commit.
func (c *progressChannel[T]) Write(item T) {
	c.mu.Lock()
	if c.sealed {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	_, err := c.lane.Command(func(state *harnesstypes.RuntimeLaneState, reader harnesstypes.SessionReader) (harnesstypes.LaneCommand[any], error) {
		if !c.stillOwns(state) {
			return harnesstypes.LaneCommand[any]{Kind: harnesstypes.LaneCommandReturn}, nil
		}
		return harnesstypes.LaneCommand[any]{
			Kind:   harnesstypes.LaneCommandCommit,
			Writes: []harnesstypes.Write{c.commitWrite(item)},
			Next:   state,
		}, nil
	}, c.drive.Context)
	c.mu.Lock()
	c.last = err
	c.mu.Unlock()
}

// Seal stops accepting writes.
func (c *progressChannel[T]) Seal() {
	c.mu.Lock()
	c.sealed = true
	c.mu.Unlock()
}

// Drain waits for the most recent write and returns its error.
func (c *progressChannel[T]) Drain() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// OpenFrameProgress opens the ordered frame writer for one response.
func OpenFrameProgress(
	lane *Lane,
	drive *harnesstypes.Drive,
	responseEntryID string,
) ProgressChannel[aiutils.AssistantMessageFrame] {
	address := harnesssession.PendingAssistantFrames(drive.OperationID, responseEntryID)
	return openProgress(
		lane,
		drive,
		func(frame aiutils.AssistantMessageFrame) harnesstypes.Write {
			return harnesssession.AppendList(address, frame)
		},
		func(state *harnesstypes.RuntimeLaneState) bool {
			if state.Operation == nil {
				return false
			}
			at := state.Operation.State.StateAt()
			switch typed := state.Operation.State.(type) {
			case *harnesstypes.AssistantEffectPendingOperation:
				return (at == harnesstypes.OperationAtAssistantEffectPending || at == harnesstypes.OperationAtDeferredEffectPending) &&
					typed.ResponseEntryID == responseEntryID
			case *harnesstypes.DeferredEffectPendingOperation:
				return (at == harnesstypes.OperationAtAssistantEffectPending || at == harnesstypes.OperationAtDeferredEffectPending) &&
					typed.ResponseEntryID == responseEntryID
			default:
				return false
			}
		},
	)
}

// OpenToolProgress opens the latest-only tool progress writer for one
// invocation.
func OpenToolProgress(
	lane *Lane,
	drive *harnesstypes.Drive,
	turnID string,
	sourceIndex int,
	invocationID string,
) ProgressChannel[agenttypes.AgentToolResult[any]] {
	address := harnesssession.PendingToolOutput(drive.OperationID, invocationID)
	return openProgress(
		lane,
		drive,
		func(snapshot agenttypes.AgentToolResult[any]) harnesstypes.Write {
			return harnesssession.SetValue(address, snapshot)
		},
		func(state *harnesstypes.RuntimeLaneState) bool {
			if state.Operation == nil {
				return false
			}
			tools, ok := state.Operation.State.(*harnesstypes.ToolsOperation)
			if !ok || tools.At != harnesstypes.OperationAtTools {
				return false
			}
			if tools.Batch.TurnID != turnID {
				return false
			}
			for _, call := range tools.Batch.Calls {
				if call.SourceIndex == sourceIndex && call.ResultEntryID == invocationID && call.Status == "effect_pending" {
					return true
				}
			}
			return false
		},
	)
}
