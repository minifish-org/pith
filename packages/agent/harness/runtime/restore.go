// This file carries the lane restoration of
// packages/agent/src/harness/runtime/restore.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Restoration is classification plus validation: a half-written lane is an
// invariant violation, and an operation whose durable intent no longer matches
// its state must never be silently adopted.
package agentruntime

import (
	"encoding/json"
	"strings"

	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// ClassifiedLaneStorage is the classified durable storage of one lane.
type ClassifiedLaneStorage struct {
	Kind string

	Tip           *harnesstypes.StoredValue
	Configuration *harnesstypes.StoredValue
	LaneState     *harnesstypes.StoredValue
}

// Classified lane storage kinds.
const (
	ClassifiedLaneAbsent = "absent"
	ClassifiedLaneBranch = "branch"
	ClassifiedLaneFull   = "lane"
)

func valuePtr(stored harnesstypes.StoredValue, ok bool) *harnesstypes.StoredValue {
	if !ok {
		return nil
	}
	copy := stored
	return &copy
}

func classifyLaneStorage(lane string, tip, configuration, laneState *harnesstypes.StoredValue) (ClassifiedLaneStorage, error) {
	if tip == nil && configuration == nil && laneState == nil {
		return ClassifiedLaneStorage{Kind: ClassifiedLaneAbsent}, nil
	}
	if tip != nil && configuration == nil && laneState == nil {
		return ClassifiedLaneStorage{Kind: ClassifiedLaneBranch, Tip: tip}, nil
	}
	if tip == nil {
		return ClassifiedLaneStorage{}, &laneInvariantError{message: "Lane " + quote(lane) + " is missing branch.tip"}
	}
	if configuration == nil {
		return ClassifiedLaneStorage{}, &laneInvariantError{message: "Lane " + quote(lane) + " is missing lane.config"}
	}
	if laneState == nil {
		return ClassifiedLaneStorage{}, &laneInvariantError{message: "Lane " + quote(lane) + " is missing lane.state"}
	}
	return ClassifiedLaneStorage{Kind: ClassifiedLaneFull, Tip: tip, Configuration: configuration, LaneState: laneState}, nil
}

func quote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "\"" + value + "\""
	}
	return string(encoded)
}

// ReadLaneStorage reads the three durable lane values.
func ReadLaneStorage(
	reader harnesstypes.SessionReader,
	lane string,
	ctx harnesstypes.Context,
) (ClassifiedLaneStorage, error) {
	tip, tipOK, err := reader.GetValue(harnesssession.BranchTip(lane), ctx)
	if err != nil {
		return ClassifiedLaneStorage{}, err
	}
	configuration, configOK, err := reader.GetValue(harnesssession.LaneConfig(lane), ctx)
	if err != nil {
		return ClassifiedLaneStorage{}, err
	}
	laneState, stateOK, err := reader.GetValue(harnesssession.LaneState(lane), ctx)
	if err != nil {
		return ClassifiedLaneStorage{}, err
	}
	return classifyLaneStorage(lane, valuePtr(tip, tipOK), valuePtr(configuration, configOK), valuePtr(laneState, stateOK))
}

func isSummaryState(state harnesstypes.OperationState) bool {
	if state == nil {
		return false
	}
	return strings.HasPrefix(string(state.StateAt()), "summary.")
}

func summaryBoundary(state harnesstypes.OperationState) (string, *harnesstypes.ResultBoundary) {
	switch typed := state.(type) {
	case *harnesstypes.SummaryDecidingOperation:
		return typed.Task.Boundary.Kind, &typed.Task.Boundary
	case *harnesstypes.SummaryReadyOperation:
		return typed.Task.Boundary.Kind, &typed.Task.Boundary
	case *harnesstypes.SummaryEffectPendingOperation:
		return typed.Task.Boundary.Kind, &typed.Task.Boundary
	case *harnesstypes.SummaryRetryWaitOperation:
		return typed.Task.Boundary.Kind, &typed.Task.Boundary
	default:
		return "", nil
	}
}

func stateMatchesIntent(intent harnesstypes.OperationIntent, state harnesstypes.OperationState) bool {
	if intent.Kind == "compaction" {
		boundary, _ := summaryBoundary(state)
		return isSummaryState(state) && boundary == "finish"
	}
	if intent.Kind == "navigation" {
		if state != nil && state.StateAt() == harnesstypes.OperationAtNavigationReadyToCommit {
			ready, ok := state.(*harnesstypes.NavigationReadyToCommitOperation)
			if !ok {
				return false
			}
			return (intent.Summarize == nil || !*intent.Summarize) &&
				stringPtrEqual(ready.TargetID, intent.TargetID) &&
				stringPtrEqual(ready.Label, intent.Label)
		}
		boundary, value := summaryBoundary(state)
		if intent.Summarize == nil || !*intent.Summarize || !isSummaryState(state) || boundary != "commit_navigation" {
			return false
		}
		if value == nil {
			return false
		}
		var customInstructions *string
		if typed, ok := state.(*harnesstypes.SummaryDecidingOperation); ok {
			customInstructions = typed.Task.CustomInstructions
		}
		return stringPtrEqual(value.TargetID, intent.TargetID) &&
			stringPtrEqual(value.Label, intent.Label) &&
			stringPtrEqual(customInstructions, intent.CustomInstructions)
	}
	if state != nil && state.StateAt() == harnesstypes.OperationAtNavigationReadyToCommit {
		return false
	}
	if !isSummaryState(state) {
		return true
	}
	boundary, _ := summaryBoundary(state)
	return boundary == "resume_checkpoint"
}

func stringPtrEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// RestoreLaneState restores one lane from classified storage without starting
// work or interpreting its state.
func RestoreLaneState(
	reader harnesstypes.SessionReader,
	lane string,
	stored ClassifiedLaneStorage,
	ctx harnesstypes.Context,
) (harnesstypes.RuntimeLaneState, error) {
	laneState, ok := stored.LaneState.Value.(harnesstypes.LaneState)
	if !ok {
		return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Lane " + quote(lane) + " has an invalid lane.state"}
	}
	var operation *harnesstypes.Operation
	if laneState.CurrentOperationID != nil {
		operationID := *laneState.CurrentOperationID
		metaStored, metaOK, err := reader.GetValue(harnesssession.OperationMeta(operationID), ctx)
		if err != nil {
			return harnesstypes.RuntimeLaneState{}, err
		}
		if !metaOK {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " is missing op.meta"}
		}
		stateStored, stateOK, err := reader.GetValue(harnesssession.OperationState(operationID), ctx)
		if err != nil {
			return harnesstypes.RuntimeLaneState{}, err
		}
		if !stateOK {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " is missing op.state"}
		}
		meta, metaCast := metaStored.Value.(harnesstypes.OperationMeta)
		if !metaCast {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " has invalid op.meta"}
		}
		if meta.OperationID != operationID {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " metadata names operation " + quote(meta.OperationID)}
		}
		if meta.Lane != lane {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " belongs to lane " + quote(meta.Lane) + ", not " + quote(lane)}
		}
		state, stateCast := stateStored.Value.(harnesstypes.OperationState)
		if !stateCast {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " has invalid op.state"}
		}
		if !stateMatchesIntent(meta.Intent, state) {
			return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Operation " + operationID + " intent " + meta.Intent.Kind + " does not match state " + string(state.StateAt())}
		}
		operation = &harnesstypes.Operation{Meta: meta, State: state}
	}
	tipID, err := tipValue(stored.Tip)
	if err != nil {
		return harnesstypes.RuntimeLaneState{}, err
	}
	configuration, ok := stored.Configuration.Value.(harnesstypes.LaneConfiguration)
	if !ok {
		return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "Lane " + quote(lane) + " has an invalid lane.config"}
	}
	return harnesstypes.RuntimeLaneState{
		TipID:           tipID,
		Configuration:   configuration,
		Inbox:           laneState.Inbox,
		LastOperationID: laneState.LastOperationID,
		Operation:       operation,
	}, nil
}

func tipValue(stored *harnesstypes.StoredValue) (*string, error) {
	if stored == nil || stored.Value == nil {
		return nil, nil
	}
	value, ok := stored.Value.(string)
	if !ok {
		return nil, &laneInvariantError{message: "branch.tip is not a string"}
	}
	return &value, nil
}

// RestoreSession restores every complete configured lane in one coherent read.
func RestoreSession(
	session harnesstypes.Session[harnesstypes.SessionMetadata],
	ctx harnesstypes.Context,
) (map[string]harnesstypes.RuntimeLaneState, error) {
	value, err := session.Mutate(func(mutator harnesstypes.SessionMutator, innerCtx harnesstypes.Context) (any, error) {
		tips, err := mutator.ScanValues(harnesssession.BranchTipInventoryPrefix(), innerCtx)
		if err != nil {
			return nil, err
		}
		configurations, err := mutator.ScanValues(harnesssession.LaneConfig(""), innerCtx)
		if err != nil {
			return nil, err
		}
		states, err := mutator.ScanValues(harnesssession.LaneState(""), innerCtx)
		if err != nil {
			return nil, err
		}
		tipByLane := map[string]*harnesstypes.StoredValue{}
		for index := range tips {
			value := tips[index]
			tipByLane[value.Address.Key] = &value
		}
		configurationByLane := map[string]*harnesstypes.StoredValue{}
		for index := range configurations {
			value := configurations[index]
			configurationByLane[value.Address.Key] = &value
		}
		stateByLane := map[string]*harnesstypes.StoredValue{}
		for index := range states {
			value := states[index]
			stateByLane[value.Address.Key] = &value
		}
		names := map[string]bool{}
		for name := range tipByLane {
			names[name] = true
		}
		for name := range configurationByLane {
			names[name] = true
		}
		for name := range stateByLane {
			names[name] = true
		}
		restored := map[string]harnesstypes.RuntimeLaneState{}
		for lane := range names {
			stored, err := classifyLaneStorage(lane, tipByLane[lane], configurationByLane[lane], stateByLane[lane])
			if err != nil {
				return nil, err
			}
			if stored.Kind != ClassifiedLaneFull {
				continue
			}
			laneState, err := RestoreLaneState(mutator, lane, stored, innerCtx)
			if err != nil {
				return nil, err
			}
			restored[lane] = laneState
		}
		return restored, nil
	}, ctx)
	if err != nil {
		return nil, err
	}
	restored, ok := value.(map[string]harnesstypes.RuntimeLaneState)
	if !ok {
		return nil, &laneInvariantError{message: "restore session returned an unexpected value"}
	}
	return restored, nil
}

// RestoreLane restores one configured lane without starting work.
func RestoreLane(
	session harnesstypes.Session[harnesstypes.SessionMetadata],
	lane string,
	ctx harnesstypes.Context,
) (harnesstypes.RuntimeLaneState, error) {
	value, err := session.Mutate(func(mutator harnesstypes.SessionMutator, innerCtx harnesstypes.Context) (any, error) {
		stored, err := ReadLaneStorage(mutator, lane, innerCtx)
		if err != nil {
			return nil, err
		}
		if stored.Kind == ClassifiedLaneAbsent {
			return nil, &laneInvariantError{message: "Lane " + quote(lane) + " is missing branch.tip"}
		}
		if stored.Kind == ClassifiedLaneBranch {
			return nil, &laneInvariantError{message: "Lane " + quote(lane) + " is missing lane.config"}
		}
		return RestoreLaneState(mutator, lane, stored, innerCtx)
	}, ctx)
	if err != nil {
		return harnesstypes.RuntimeLaneState{}, err
	}
	restored, ok := value.(harnesstypes.RuntimeLaneState)
	if !ok {
		return harnesstypes.RuntimeLaneState{}, &laneInvariantError{message: "restore lane returned an unexpected value"}
	}
	return restored, nil
}
