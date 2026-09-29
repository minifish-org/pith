package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	harnessresult "github.com/minifish-org/pith/packages/agent/harness/result"
	agentruntime "github.com/minifish-org/pith/packages/agent/harness/runtime"
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// RunCase is the ONLY bridge the frozen core-harness judge uses. It decodes one
// evaluator operation, calls the real exported Go SDK and normalizes only the
// fields the evaluator states. It never reads expected results, golden files or
// TS sources, and it never invokes Node.
//
// The production API is typed; this bridge exists only so the independent judge
// can exercise the same public surface as an embedder.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var header struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(input, &header); err != nil {
		return nil, fmt.Errorf("core-harness conformance: invalid input: %w", err)
	}
	switch header.Op {
	case "tagged-error":
		return runTaggedError(input)
	case "harness-lifecycle":
		return runHarnessLifecycle(ctx)
	default:
		return nil, fmt.Errorf("core-harness conformance: unsupported operation %q", header.Op)
	}
}

// runTaggedError constructs the requested TaggedError family with the supplied
// properties through the real result factory and reports the upstream
// observable shape.
func runTaggedError(input json.RawMessage) (json.RawMessage, error) {
	var args struct {
		File  string         `json:"file"`
		Tag   string         `json:"tag"`
		Props map[string]any `json:"props"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, fmt.Errorf("core-harness conformance: invalid tagged-error input: %w", err)
	}
	if args.Tag == "" {
		return nil, errors.New("core-harness conformance: tagged-error requires a tag")
	}
	factory := harnessresult.TaggedError(args.Tag)
	value, err := factory.New(args.Props)
	if err != nil {
		return nil, err
	}
	payload := value.ToJSON()
	name, _ := payload["name"].(string)
	plain := errors.New("plain")
	result := map[string]any{
		"tag":         value.Tag(),
		"name":        name,
		"message":     value.Error(),
		"json":        payload,
		"matches":     factory.Is(value),
		"rejectPlain": factory.Is(plain),
	}
	return json.Marshal(result)
}

// runHarnessLifecycle drives the real harness assembly over a deterministic
// in-memory session, mirroring the upstream lifecycle fixture: an empty lane
// set, stable lane identity under concurrent acquisition, and lane inspection.
func runHarnessLifecycle(ctx context.Context) (json.RawMessage, error) {
	now := 1.0
	repo := harnesssession.NewMemorySessionRepo(&harnesssession.MemorySessionRepoOptions{
		Now: func() float64 { return now },
	})
	sessionID := "session"
	session, err := repo.Create(harnesstypes.SessionCreateOptions{ID: &sessionID}, ctx)
	if err != nil {
		return nil, err
	}
	thinking := agenttypes.ThinkingLevel("low")
	options := harnesstypes.AgentHarnessOptions[any]{
		Session:         session,
		ThinkingLevel:   &thinking,
		ActiveToolNames: []string{},
	}
	harness, _, err := agentruntime.CreateAgentHarness(options, ctx)
	if err != nil {
		_ = repo.Close(ctx)
		return nil, err
	}
	before := laneSummaries(harness.Lanes())

	a, err := harness.Lane("main", ctx)
	if err != nil {
		harness.Close(err)
		_ = repo.Close(ctx)
		return nil, err
	}
	b, err := harness.Lane("main", ctx)
	if err != nil {
		harness.Close(err)
		_ = repo.Close(ctx)
		return nil, err
	}

	state := a.LaneState()
	tools := state.Configuration.ActiveToolNames
	if tools == nil {
		tools = []string{}
	}
	result := map[string]any{
		"before":   before,
		"same":     a == b,
		"tip":      state.TipID,
		"thinking": string(state.Configuration.ThinkingLevel),
		"tools":    tools,
		"lanes":    laneSummaries(harness.Lanes()),
	}
	harness.Close(nil)
	if err := repo.Close(ctx); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// laneSummaries reduces the reader-facing lane info to the fields the evaluator
// states and always returns a non-nil slice so an empty lane set encodes as
// `[]`, not `null`.
func laneSummaries(infos []agentruntime.LaneInfo) []map[string]any {
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, map[string]any{"name": info.Name, "tipId": info.TipID})
	}
	return out
}
