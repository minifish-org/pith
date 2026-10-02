// This file ports the mixed-model runtime surface of Pi's models.ts: the
// any-based runtime narrowing helpers (`modelsAreEqual`, `hasApi`) plus the
// optional provider capabilities (`getAllModels`, `classify`) that let one
// provider expose chat, image and classifier models at once.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Go notes:
//   - The tagged union lives in types.AnyModel; this file keeps the collection
//     layer independent of the concrete provider implementations.
//   - Runtime identity always includes the model type, so chat/image/classifier
//     entries that share an id do not collide.
package ai

import (
	"context"
	"encoding/json"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
)

// AllModelsProvider is implemented by a provider that lists models of every
// kind. Providers with only chat models may omit it; the collection then falls
// back to GetModels.
type AllModelsProvider interface {
	GetAllModels() []types.AnyModel
}

// FilterAllModelsProvider is the optional credential-specific availability
// policy across every model kind. Without it, GetAllAvailable applies
// FilterModels to chat models and keeps every other kind.
type FilterAllModelsProvider interface {
	FilterAllModels(models []types.AnyModel, credential authtypes.Credential) []types.AnyModel
}

// ClassifierImplementation is one classifier operation bound to a provider API.
type ClassifierImplementation func(ctx context.Context, model types.ClassifierModel, request types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult

// ClassifierProvider is implemented by a provider that supports structured
// classification.
type ClassifierProvider interface {
	Classify(ctx context.Context, model types.ClassifierModel, request types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult
}

// anyChatModels boxes a chat-only list into the mixed union.
func anyChatModels(models []types.Model) []types.AnyModel {
	if models == nil {
		return nil
	}
	out := make([]types.AnyModel, 0, len(models))
	for _, model := range models {
		out = append(out, types.NewAnyChatModel(model))
	}
	return out
}

// mergeAnyModels overlays by (type, id): a later model replaces the earlier one
// with the same kind and id, otherwise it is appended.
func mergeAnyModels(base, overlay []types.AnyModel) []types.AnyModel {
	merged := append([]types.AnyModel(nil), base...)
	for _, model := range overlay {
		incoming, ok := identityOf(model)
		if !ok {
			merged = append(merged, model)
			continue
		}
		index := -1
		for i := range merged {
			existing, ok := identityOf(merged[i])
			if ok && existing.kind == incoming.kind && existing.id == incoming.id {
				index = i
				break
			}
		}
		if index >= 0 {
			merged[index] = model
		} else {
			merged = append(merged, model)
		}
	}
	return merged
}

// filterAnyModelsByType returns the models of one kind, in list order.
func filterAnyModelsByType(models []types.AnyModel, kind string) []types.AnyModel {
	out := make([]types.AnyModel, 0, len(models))
	for _, model := range models {
		if types.GetModelType(model) == kind {
			out = append(out, model)
		}
	}
	return out
}

// anyModelsToChat extracts the chat models in list order.
func anyModelsToChat(models []types.AnyModel) []types.Model {
	out := make([]types.Model, 0, len(models))
	for _, model := range models {
		if types.GetModelType(model) != types.ModelTypeChat {
			continue
		}
		if model.Chat != nil {
			out = append(out, *model.Chat)
			continue
		}
		if raw := model.Raw(); len(raw) > 0 {
			var chat types.Model
			if err := json.Unmarshal(raw, &chat); err == nil {
				out = append(out, chat)
			}
		}
	}
	return out
}

// modelIdentity is the runtime identity of a model: its kind, provider, id and
// api.
type modelIdentity struct {
	kind     string
	provider string
	id       string
	api      string
}

// identityOf extracts the runtime identity from any model representation. It
// accepts the concrete DTOs (by value or pointer), the tagged union and raw or
// decoded JSON objects. A nil, empty or unparseable value yields false.
func identityOf(model any) (modelIdentity, bool) {
	switch value := model.(type) {
	case nil:
		return modelIdentity{}, false
	case types.Model:
		return modelIdentity{kind: types.ModelTypeChat, provider: string(value.Provider), id: value.Id, api: string(value.Api)}, true
	case *types.Model:
		if value == nil {
			return modelIdentity{}, false
		}
		return modelIdentity{kind: types.ModelTypeChat, provider: string(value.Provider), id: value.Id, api: string(value.Api)}, true
	case types.ImagesModel:
		return modelIdentity{kind: types.ModelTypeImage, provider: string(value.Provider), id: value.Id, api: string(value.Api)}, true
	case *types.ImagesModel:
		if value == nil {
			return modelIdentity{}, false
		}
		return modelIdentity{kind: types.ModelTypeImage, provider: string(value.Provider), id: value.Id, api: string(value.Api)}, true
	case types.ClassifierModel:
		return modelIdentity{kind: types.ModelTypeClassifier, provider: string(value.Provider), id: value.Id, api: string(value.Api)}, true
	case *types.ClassifierModel:
		if value == nil {
			return modelIdentity{}, false
		}
		return modelIdentity{kind: types.ModelTypeClassifier, provider: string(value.Provider), id: value.Id, api: string(value.Api)}, true
	case types.AnyModel:
		return identityOfAnyModel(value)
	case *types.AnyModel:
		if value == nil {
			return modelIdentity{}, false
		}
		return identityOfAnyModel(*value)
	case json.RawMessage:
		return identityFromJSON(value)
	case []byte:
		return identityFromJSON(value)
	case string:
		return identityFromJSON([]byte(value))
	case map[string]any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return modelIdentity{}, false
		}
		return identityFromJSON(encoded)
	case map[string]json.RawMessage:
		encoded, err := json.Marshal(value)
		if err != nil {
			return modelIdentity{}, false
		}
		return identityFromJSON(encoded)
	default:
		return modelIdentity{}, false
	}
}

func identityOfAnyModel(model types.AnyModel) (modelIdentity, bool) {
	kind := types.GetModelType(model)
	switch kind {
	case types.ModelTypeChat:
		if model.Chat != nil {
			return modelIdentity{kind: kind, provider: string(model.Chat.Provider), id: model.Chat.Id, api: string(model.Chat.Api)}, true
		}
	case types.ModelTypeImage:
		if model.Image != nil {
			return modelIdentity{kind: kind, provider: string(model.Image.Provider), id: model.Image.Id, api: string(model.Image.Api)}, true
		}
	case types.ModelTypeClassifier:
		if model.Classifier != nil {
			return modelIdentity{kind: kind, provider: string(model.Classifier.Provider), id: model.Classifier.Id, api: string(model.Classifier.Api)}, true
		}
	}
	if raw := model.Raw(); len(raw) > 0 {
		identity, ok := identityFromJSON(raw)
		if !ok {
			return modelIdentity{}, false
		}
		if kind != "" {
			identity.kind = kind
		}
		return identity, true
	}
	return modelIdentity{}, false
}

// identityFromJSON reads the identity from a raw JSON model object.
func identityFromJSON(raw []byte) (modelIdentity, bool) {
	kind := types.GetModelType(raw)
	if kind == "" {
		return modelIdentity{}, false
	}
	var probe struct {
		Id       string `json:"id"`
		Api      string `json:"api"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return modelIdentity{}, false
	}
	if probe.Id == "" && probe.Provider == "" && probe.Api == "" {
		return modelIdentity{}, false
	}
	return modelIdentity{kind: kind, provider: probe.Provider, id: probe.Id, api: probe.Api}, true
}
