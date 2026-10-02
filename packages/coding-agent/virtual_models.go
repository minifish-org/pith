// Virtual model routing for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/virtual-models.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. A virtual model is a
// catalog entry that routes each logical request to a physical model: the
// selection (`ModelOptions.Model`, model changes) may name the virtual entry,
// but every request below the routing step only sees the physical model a
// provider can stream. A virtual model never reaches a provider.
//
// Router state is persisted as a `pi.virtual-model-state` custom entry on the
// active session branch so a resumed session keeps its routing memory. Direct
// requests (compaction summaries, extension calls) neither read nor write that
// state.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// VirtualModelAPI is the api id of virtual catalog entries. A request for it
// fails unless it is routed to a physical model first.
const VirtualModelAPI aitypes.Api = "pi-virtual"

// VirtualModelStateEntry is the custom entry type that stores router state on
// the session branch.
const VirtualModelStateEntry = "pi.virtual-model-state"

// Model route reasons.
const (
	// ModelRouteReasonUser is the first request after a message the user wrote.
	ModelRouteReasonUser = "user"
	// ModelRouteReasonContinuation is any other request in the agent loop.
	ModelRouteReasonContinuation = "continuation"
	// ModelRouteReasonRetry is an automatic retry after a failed request.
	ModelRouteReasonRetry = "retry"
	// ModelRouteReasonDirect is a request outside the agent loop.
	ModelRouteReasonDirect = "direct"
)

var allThinkingLevels = []aitypes.ModelThinkingLevel{
	aitypes.ThinkingOff,
	aitypes.ThinkingMinimal,
	aitypes.ThinkingLow,
	aitypes.ThinkingMedium,
	aitypes.ThinkingHigh,
	aitypes.ThinkingXHigh,
	aitypes.ThinkingMax,
}

// VirtualModelStateData is the payload of a `pi.virtual-model-state` entry.
type VirtualModelStateData struct {
	Provider string          `json:"provider"`
	ModelID  string          `json:"modelId"`
	State    json.RawMessage `json:"state,omitempty"`
}

// ModelRoutePrevious carries the physical model and thinking level of a
// previous request. Failed is set for a retry to describe the failed assistant
// message; it is nil when only the successful model/level are known.
type ModelRoutePrevious struct {
	Model         aitypes.Model
	ThinkingLevel aitypes.ModelThinkingLevel
	Failed        *aitypes.AssistantMessage
}

// ModelRouteRequest is the input to a virtual model's Route callback.
type ModelRouteRequest struct {
	// Model is the selected virtual model.
	Model aitypes.Model
	// ThinkingLevel is the selected thinking level; its meaning is up to the
	// router.
	ThinkingLevel aitypes.ModelThinkingLevel
	// Reason is one of the ModelRouteReason constants.
	Reason string
	// Previous is the physical model and thinking level of the latest
	// successful response in Messages.
	Previous *ModelRoutePrevious
	// Failed is the failed request for a retry.
	Failed *ModelRoutePrevious
	// State is the router state last returned on this session branch. It is
	// empty before the first state and for direct requests.
	State json.RawMessage
	// Messages is the conversation for this request, including system messages.
	Messages []aitypes.Message
}

// ModelRoute is the physical model and thinking level for one request.
type ModelRoute struct {
	Model         aitypes.Model
	ThinkingLevel aitypes.ModelThinkingLevel
	// State is the new router state, stored on the branch unless it equals the
	// request state. It is ignored for direct requests.
	State json.RawMessage
}

// VirtualModelDefinition configures one virtual model. The zero ThinkingLevels
// defaults to ["off"].
type VirtualModelDefinition struct {
	Provider       string
	ID             string
	Name           string
	ThinkingLevels []aitypes.ModelThinkingLevel
	ContextWindow  int
	MaxTokens      int
	Route          func(context.Context, ModelRouteRequest) (ModelRoute, error)
}

// CreateVirtualModel builds the catalog entry of a virtual model. The entry
// has api `pi-virtual`, so it must be routed before it can be streamed.
func CreateVirtualModel(definition VirtualModelDefinition) aitypes.Model {
	levels := definition.ThinkingLevels
	if len(levels) == 0 {
		levels = []aitypes.ModelThinkingLevel{aitypes.ThinkingOff}
	}
	supported := map[aitypes.ModelThinkingLevel]bool{}
	for _, level := range levels {
		supported[level] = true
	}
	thinkingMap := aitypes.ThinkingLevelMap{}
	reasoning := false
	for _, level := range allThinkingLevels {
		if level == aitypes.ThinkingOff {
			continue
		}
		if supported[level] {
			value := string(level)
			thinkingMap[level] = &value
			reasoning = true
		} else {
			thinkingMap[level] = nil
		}
	}
	input := []aitypes.ModelInputModality{aitypes.ModelInputText, aitypes.ModelInputImage}
	return aitypes.Model{
		Id:               definition.ID,
		Name:             definition.Name,
		Api:              VirtualModelAPI,
		Provider:         aitypes.ProviderId(definition.Provider),
		Reasoning:        reasoning,
		ThinkingLevelMap: thinkingMap,
		Input:            input,
		ContextWindow:    float64(definition.ContextWindow),
		MaxTokens:        float64(definition.MaxTokens),
	}
}

// IsVirtualModel reports whether a model names a virtual catalog entry. A nil
// model is not virtual.
func IsVirtualModel(model *aitypes.Model) bool {
	return model != nil && model.Api == VirtualModelAPI
}

// FindLatestResponse returns the latest successful assistant message in the
// transcript. Failed or aborted requests are skipped.
func FindLatestResponse(messages []agenttypes.AgentMessage) (aitypes.AssistantMessage, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Message == nil || message.Message.Assistant == nil {
			continue
		}
		assistant := *message.Message.Assistant
		if assistant.StopReason == aitypes.StopReasonError || assistant.StopReason == aitypes.StopReasonAborted {
			continue
		}
		return assistant, true
	}
	return aitypes.AssistantMessage{}, false
}

// GetVirtualModelState returns the latest router state a session branch stores
// for a virtual model. The branch is walked from the leaf to the root; the
// first matching entry wins. A non-nil empty result means no state was stored.
func GetVirtualModelState(branch []SessionEntry, provider, modelID string) json.RawMessage {
	for index := len(branch) - 1; index >= 0; index-- {
		entry := branch[index]
		if entry.Type != "custom" {
			continue
		}
		var payload struct {
			CustomType string          `json:"customType"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if payload.CustomType != VirtualModelStateEntry || len(payload.Data) == 0 {
			continue
		}
		var data VirtualModelStateData
		if err := json.Unmarshal(payload.Data, &data); err != nil {
			continue
		}
		if data.Provider == provider && data.ModelID == modelID {
			return append(json.RawMessage(nil), data.State...)
		}
	}
	return nil
}

// BranchSelection is the provider/model a session branch currently selects.
type BranchSelection struct {
	Provider string
	ModelID  string
}

// GetBranchSelection returns the model selection a session branch records. A
// virtual `model_change` holds until the next change because responses name the
// physical models it routed to; otherwise the latest physical response wins.
// getModel resolves a provider/model id to a catalog model and is used to test
// whether a model change still names a registered virtual model.
func GetBranchSelection(
	branch []SessionEntry,
	getModel func(provider, modelID string) *aitypes.Model,
) (BranchSelection, bool) {
	for index := len(branch) - 1; index >= 0; index-- {
		entry := branch[index]
		if entry.Type == "model_change" {
			var payload struct {
				Provider string `json:"provider"`
				ModelID  string `json:"modelId"`
			}
			if json.Unmarshal(entry.Payload, &payload) == nil && payload.Provider != "" {
				return BranchSelection{Provider: payload.Provider, ModelID: payload.ModelID}, true
			}
			continue
		}
		if entry.Type != "message" {
			continue
		}
		var payload struct {
			Role     string `json:"role"`
			Model    string `json:"model"`
			Provider string `json:"provider"`
			Api      string `json:"api"`
		}
		if json.Unmarshal(entry.Payload, &payload) != nil || payload.Role != aitypes.AssistantMessageRole {
			continue
		}
		if payload.Api == string(VirtualModelAPI) {
			continue
		}
		response := BranchSelection{Provider: payload.Provider, ModelID: payload.Model}
		change, ok := lastModelChange(branch, index)
		if !ok {
			return response, true
		}
		if getModel != nil {
			if model := getModel(change.Provider, change.ModelID); IsVirtualModel(model) {
				return change, true
			}
		}
		return response, true
	}
	return BranchSelection{}, false
}

func lastModelChange(branch []SessionEntry, before int) (BranchSelection, bool) {
	for index := before - 1; index >= 0; index-- {
		entry := branch[index]
		if entry.Type != "model_change" {
			continue
		}
		var payload struct {
			Provider string `json:"provider"`
			ModelID  string `json:"modelId"`
		}
		if json.Unmarshal(entry.Payload, &payload) == nil {
			return BranchSelection{Provider: payload.Provider, ModelID: payload.ModelID}, true
		}
	}
	return BranchSelection{}, false
}

// virtualModelRegistry is the per-session set of virtual model definitions.
type virtualModelRegistry struct {
	byKey map[string]VirtualModelDefinition
	order []string
}

func newVirtualModelRegistry(definitions []VirtualModelDefinition) (*virtualModelRegistry, error) {
	registry := &virtualModelRegistry{byKey: map[string]VirtualModelDefinition{}}
	for _, definition := range definitions {
		if definition.Provider == "" {
			return nil, errors.New("virtual model provider is required")
		}
		if definition.ID == "" {
			return nil, errors.New("virtual model id is required")
		}
		if definition.Route == nil {
			return nil, fmt.Errorf("virtual model %s/%s has no route function", definition.Provider, definition.ID)
		}
		key := virtualModelKey(definition.Provider, definition.ID)
		if _, exists := registry.byKey[key]; exists {
			return nil, fmt.Errorf("virtual model %s is already registered", key)
		}
		registry.byKey[key] = definition
		registry.order = append(registry.order, key)
	}
	return registry, nil
}

func virtualModelKey(provider, id string) string { return provider + "/" + id }

// lookup returns a definition by provider and id.
func (r *virtualModelRegistry) lookup(provider, id string) (VirtualModelDefinition, bool) {
	if r == nil {
		return VirtualModelDefinition{}, false
	}
	definition, ok := r.byKey[virtualModelKey(provider, id)]
	return definition, ok
}

// resolve runs a definition's route callback and validates its result. It
// rejects virtual-to-virtual routing and unavailable physical models.
func (r *virtualModelRegistry) resolve(ctx context.Context, request ModelRouteRequest) (ModelRoute, error) {
	definition, ok := r.lookup(string(request.Model.Provider), request.Model.Id)
	if !ok {
		return ModelRoute{}, fmt.Errorf("virtual model %s/%s is not registered", request.Model.Provider, request.Model.Id)
	}
	route, err := definition.Route(ctx, request)
	if err != nil {
		return ModelRoute{}, err
	}
	if IsVirtualModel(&route.Model) {
		return ModelRoute{}, fmt.Errorf("virtual model %s/%s routed to virtual model %s/%s", request.Model.Provider, request.Model.Id, route.Model.Provider, route.Model.Id)
	}
	if route.Model.Id == "" || route.Model.Provider == "" {
		return ModelRoute{}, fmt.Errorf("virtual model %s/%s routed to an unavailable physical model", request.Model.Provider, request.Model.Id)
	}
	if route.ThinkingLevel == "" {
		route.ThinkingLevel = aitypes.ThinkingOff
	}
	return route, nil
}

// routeReason decides whether a request starts a user turn, continues a turn or
// repeats a failed request. Retry is explicit because the failed message is no
// longer part of the transcript.
func routeReason(messages []agenttypes.AgentMessage, retry bool) string {
	if retry {
		return ModelRouteReasonRetry
	}
	lastResponse := -1
	for index, message := range messages {
		if message.Message != nil && message.Message.Assistant != nil {
			lastResponse = index
		}
	}
	for index := lastResponse + 1; index < len(messages); index++ {
		message := messages[index]
		if message.Message != nil && message.Message.User != nil {
			return ModelRouteReasonUser
		}
	}
	return ModelRouteReasonContinuation
}

// previousFromAssistant converts a successful assistant message into the
// physical model/level a router sees as Previous.
func previousFromAssistant(assistant aitypes.AssistantMessage) *ModelRoutePrevious {
	level := aitypes.ModelThinkingLevel(assistant.ThinkingLevel)
	previous := &ModelRoutePrevious{
		Model: aitypes.Model{
			Id:       assistant.Model,
			Provider: assistant.Provider,
			Api:      assistant.Api,
		},
		ThinkingLevel: level,
	}
	return previous
}

// stateEqual reports whether two router states are byte-identical.
func stateEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return bytes.Equal(a, b)
}

// dynamicProviderStreamFn resolves the native provider for the model passed at
// stream time. It is the fallback for a session whose selection is virtual: the
// router replaces the virtual model with a physical one before the request is
// streamed, so the dispatcher only ever sees a physical model.
func dynamicProviderStreamFn(model *aitypes.Model, context *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
	fn := builtinStreamFnForModel(model)
	if fn == nil {
		reason := aitypes.StopReasonError
		message := "no provider stream is available for model " + model.Id
		stream := aitypes.NewAssistantMessageEventStream()
		assistant := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
		assistant.StopReason = reason
		assistant.ErrorMessage = &message
		stream.Push(aitypes.NewErrorEvent(reason, assistant))
		return stream
	}
	return fn(model, context, options)
}

// prepareRequest is installed on every per-session agent. It routes a virtual
// selection onto a physical model before the request is streamed and persists
// the router state on the active branch. A non-virtual selection is left
// untouched, so models without virtual routing keep the accepted behavior.
func (s *AgentSession) prepareRequest(request agenttypes.PrepareRequestContext, signal <-chan struct{}) (agenttypes.AgentRequestUpdate, error) {
	if s.virtualRoutes == nil {
		return agenttypes.AgentRequestUpdate{}, nil
	}
	// The virtual selection lives in the session, not in the loop config: the
	// loop replaces its model with the routed physical model for the duration of
	// one request, so request.Model is only virtual on the first request of a
	// run. Reading the session selection keeps routing correct for every request
	// and for runs the loop continues after a tool result.
	s.mu.Lock()
	selected := s.model
	selectedThinking := s.thinking
	s.mu.Unlock()
	if !IsVirtualModel(selected) {
		return agenttypes.AgentRequestUpdate{}, nil
	}

	messages, err := ConvertToLlm(request.Context.Messages)
	if err != nil {
		return agenttypes.AgentRequestUpdate{}, err
	}

	failed, retry := s.takeRouteFailure()
	reason := routeReason(request.Context.Messages, retry)
	var previous *ModelRoutePrevious
	if assistant, ok := FindLatestResponse(request.Context.Messages); ok {
		previous = previousFromAssistant(assistant)
	}

	manager := s.manager
	branch := manager.GetBranch("")
	state := GetVirtualModelState(branch, string(selected.Provider), selected.Id)

	routeRequest := ModelRouteRequest{
		Model:         *selected,
		ThinkingLevel: selectedThinking,
		Reason:        reason,
		Previous:      previous,
		Failed:        failed,
		State:         state,
		Messages:      messages,
	}
	route, err := s.virtualRoutes.resolve(contextFromSignal(signal), routeRequest)
	if err != nil {
		return agenttypes.AgentRequestUpdate{}, err
	}

	if !stateEqual(route.State, state) && len(route.State) > 0 {
		data, err := json.Marshal(VirtualModelStateData{
			Provider: string(selected.Provider),
			ModelID:  selected.Id,
			State:    route.State,
		})
		if err != nil {
			return agenttypes.AgentRequestUpdate{}, err
		}
		if _, err := manager.AppendCustomEntry(VirtualModelStateEntry, data); err != nil {
			return agenttypes.AgentRequestUpdate{}, err
		}
	}

	physical := route.Model
	level := route.ThinkingLevel
	return agenttypes.AgentRequestUpdate{Model: &physical, ThinkingLevel: &level}, nil
}

// takeRouteFailure consumes the pending retry description once.
func (s *AgentSession) takeRouteFailure() (*ModelRoutePrevious, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routeFailed == nil {
		return nil, false
	}
	failed := s.routeFailed
	s.routeFailed = nil
	return failed, true
}

// setRouteFailure records the failed request of a retry. It is called by the
// Prompt retry path before the retry run starts.
func (s *AgentSession) setRouteFailure(failed *ModelRoutePrevious) {
	s.mu.Lock()
	s.routeFailed = failed
	s.mu.Unlock()
}
