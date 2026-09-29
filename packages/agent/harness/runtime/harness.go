// This file carries the harness assembly of
// packages/agent/src/harness/runtime/harness.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The runtime harness attaches to one open session, restores every configured
// lane, and hands out process-local Lane owners. Concrete models, tools and
// hook registrations are injected by the final assembly layer.
package agentruntime

import (
	"sync"

	harnesshooks "github.com/minifish-org/pith/packages/agent/harness/hooks"
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// OpenOperation describes one operation restored or created by the harness.
type OpenOperation struct {
	Lane        string  `json:"lane"`
	OperationID string  `json:"operationId"`
	Kind        string  `json:"kind"`
	StartedAt   float64 `json:"startedAt"`
	Aborting    bool    `json:"aborting,omitempty"`
}

// LaneInfo is the reader-facing lane summary.
type LaneInfo struct {
	Name       string                              `json:"name"`
	TipID      *string                             `json:"tipId"`
	Operation  *LaneSnapshotOperation              `json:"operation"`
	LastResult *harnesstypes.OperationResultRecord `json:"lastResult,omitempty"`
}

// Harness owns the process-local lanes attached to one session.
type Harness struct {
	Session harnesstypes.Session[harnesstypes.SessionMetadata]
	Models  Models
	Hooks   *harnesshooks.HookRegistry

	mu         sync.Mutex
	lanes      map[string]*Lane
	config     harnesstypes.Config[any]
	defaults   harnesstypes.LaneConfiguration
	closed     error
	eventSinks []func(events []Event, ctx harnesstypes.Context) error
}

// CreateAgentHarness attaches the durable harness to one open session.
func CreateAgentHarness(
	options harnesstypes.AgentHarnessOptions[any],
	ctx harnesstypes.Context,
) (*Harness, []OpenOperation, error) {
	if options.Session == nil {
		return nil, nil, &laneInvariantError{message: "AgentHarness requires a session"}
	}
	restored, err := RestoreSession(options.Session, ctx)
	if err != nil {
		return nil, nil, err
	}
	var models Models
	if options.Models != nil {
		if resolved, ok := options.Models.(Models); ok {
			models = resolved
		}
	}
	hooks := harnesshooks.NewHookRegistry(nil)
	harness := &Harness{
		Session:  options.Session,
		Models:   models,
		Hooks:    hooks,
		lanes:    map[string]*Lane{},
		config:   buildRuntimeConfig(options),
		defaults: defaultLaneConfiguration(options),
	}
	for name, state := range restored {
		nameCopy := name
		harness.lanes[name] = NewLane(nameCopy, options.Session, models, hooks, state, harness.config, harness.emit)
	}
	open := []OpenOperation{}
	for name, lane := range harness.lanes {
		if lane.state.Operation != nil {
			scope := harnesstypes.OperationScopeOf(lane.state.Operation.State)
			open = append(open, OpenOperation{
				Lane:        name,
				OperationID: lane.state.Operation.Meta.OperationID,
				Kind:        lane.state.Operation.Meta.Intent.Kind,
				StartedAt:   lane.state.Operation.Meta.StartedAt,
				Aborting:    scope.Control.Status == "cancel_requested",
			})
		}
	}
	return harness, open, nil
}

func buildRuntimeConfig(options harnesstypes.AgentHarnessOptions[any]) harnesstypes.Config[any] {
	config := harnesstypes.Config[any]{
		Tools:              options.Tools,
		SystemPrompt:       options.SystemPrompt,
		SystemPromptFn:     options.SystemPromptFn,
		ToProviderMessages: options.ToProviderMessages,
		EntryProjectors:    options.EntryProjectors,
		SteeringMode:       agenttypes.QueueModeAll,
		FollowUpMode:       agenttypes.QueueModeAll,
		ToolExecution:      agenttypes.ToolExecutionSequential,
		Compaction:         harnesstypes.DefaultCompactionSettings,
		RetryPolicy:        aiutils.RetryPolicy{},
	}
	if options.StreamOptions != nil {
		config.StreamOptions = *options.StreamOptions
	}
	if options.Retry != nil {
		config.RetryPolicy = *options.Retry
	}
	if options.Compaction != nil {
		config.Compaction = *options.Compaction
	}
	if options.SteeringMode != nil {
		config.SteeringMode = *options.SteeringMode
	}
	if options.FollowUpMode != nil {
		config.FollowUpMode = *options.FollowUpMode
	}
	if options.ToolExecution != nil {
		config.ToolExecution = *options.ToolExecution
	}
	if options.Resources != nil {
		config.Resources = *options.Resources
	}
	return config
}

func defaultLaneConfiguration(options harnesstypes.AgentHarnessOptions[any]) harnesstypes.LaneConfiguration {
	configuration := harnesstypes.LaneConfiguration{
		ThinkingLevel:   agenttypes.ThinkingLevel("off"),
		ActiveToolNames: []string{},
	}
	if options.Model != nil {
		configuration.Model = harnesstypes.ModelIdentity{Provider: string(options.Model.Provider), ModelID: options.Model.Id}
	}
	if options.ThinkingLevel != nil {
		configuration.ThinkingLevel = *options.ThinkingLevel
	}
	if options.ActiveToolNames != nil {
		configuration.ActiveToolNames = options.ActiveToolNames
	}
	return configuration
}

// emit forwards a batch to every registered sink.
func (h *Harness) emit(events []Event, ctx harnesstypes.Context) error {
	h.mu.Lock()
	sinks := append([]func([]Event, harnesstypes.Context) error{}, h.eventSinks...)
	h.mu.Unlock()
	for _, sink := range sinks {
		if err := sink(events, ctx); err != nil {
			return err
		}
	}
	return nil
}

// OnEvents registers one event sink.
func (h *Harness) OnEvents(sink func(events []Event, ctx harnesstypes.Context) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventSinks = append(h.eventSinks, sink)
}

// Lane returns the named lane, creating an empty configured lane when absent.
func (h *Harness) Lane(name string, ctx harnesstypes.Context) (*Lane, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed != nil {
		return nil, h.closed
	}
	if lane, ok := h.lanes[name]; ok {
		return lane, nil
	}
	if _, ok, err := h.Session.Branch(name, ctx); err != nil {
		return nil, err
	} else if !ok {
		if _, err := h.Session.CreateBranch(name, nil, ctx); err != nil {
			return nil, err
		}
	}
	configuration := h.defaults
	if len(configuration.ActiveToolNames) == 0 {
		configuration.ActiveToolNames = []string{}
	}
	state := harnesstypes.RuntimeLaneState{Configuration: configuration}
	if err := h.Session.SetValue(harnesssession.LaneConfig(name), configuration, ctx); err != nil {
		return nil, err
	}
	if err := h.Session.SetValue(harnesssession.LaneState(name), durableLaneState("", nil, nil), ctx); err != nil {
		return nil, err
	}
	lane := NewLane(name, h.Session, h.Models, h.Hooks, state, h.config, h.emit)
	h.lanes[name] = lane
	return lane, nil
}

func (h *Harness) defaultConfiguration() harnesstypes.LaneConfiguration {
	return h.defaults
}

// Lanes returns every attached lane summary.
func (h *Harness) Lanes() []LaneInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	infos := make([]LaneInfo, 0, len(h.lanes))
	for name, lane := range h.lanes {
		lane.mu.Lock()
		state := lane.state
		info := LaneInfo{Name: name, TipID: state.TipID}
		if state.Operation != nil {
			info.Operation = &LaneSnapshotOperation{
				ID:           state.Operation.Meta.OperationID,
				Kind:         state.Operation.Meta.Intent.Kind,
				StartedAt:    state.Operation.Meta.StartedAt,
				Status:       harnesstypes.OperationScopeOf(state.Operation.State).Control.Status,
				FromTipID:    state.Operation.Meta.SourceTipID,
				RunningTools: []LaneSnapshotTool{},
			}
		}
		info.LastResult = nil
		lane.mu.Unlock()
		infos = append(infos, info)
	}
	return infos
}

// Close seals every attached lane.
func (h *Harness) Close(err error) {
	h.mu.Lock()
	lanes := make([]*Lane, 0, len(h.lanes))
	for _, lane := range h.lanes {
		lanes = append(lanes, lane)
	}
	if h.closed == nil {
		h.closed = err
	}
	h.mu.Unlock()
	for _, lane := range lanes {
		lane.Close(err)
	}
}
