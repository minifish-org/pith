package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	agentruntime "github.com/minifish-org/pith/packages/agent/harness/runtime"
)

// undefinedMarker is the evaluator's strict encoding for a JS undefined value.
type undefinedMarker struct {
	Undefined bool `json:"$undefined"`
}

// RunCase is the test-only bridge between the frozen core-runtime fixtures and
// the real exported Go SDK. It never contains SDK behavior itself; it decodes
// the evaluator operation, calls the production API and normalizes only the
// fields the evaluator states.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var header struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(input, &header); err != nil {
		return nil, err
	}
	switch header.Op {
	case "reducer":
		return runReducerCase(ctx, input)
	default:
		return nil, fmt.Errorf("core-runtime adapter: unsupported operation %q", header.Op)
	}
}

func runReducerCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Snapshot agentruntime.LaneSnapshot `json:"snapshot"`
		Events   []json.RawMessage         `json:"events"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, err
	}
	reductions := make([]any, 0, len(args.Events))
	for _, raw := range args.Events {
		var event agentruntime.LaneEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		reduction := agentruntime.ReduceLaneSnapshot(&args.Snapshot, &event)
		if reduction == agentruntime.LaneSnapshotReductionRebase {
			reductions = append(reductions, "rebase")
		} else {
			reductions = append(reductions, undefinedMarker{Undefined: true})
		}
	}
	return json.Marshal(struct {
		Snapshot   agentruntime.LaneSnapshot `json:"snapshot"`
		Reductions []any                     `json:"reductions"`
	}{Snapshot: args.Snapshot, Reductions: reductions})
}
