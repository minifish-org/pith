// This file is a Go port of packages/ai/src/utils/model-operations.ts from Pi at
// the frozen target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The runtime model-type helpers live on the shared types package
// (types.GetModelType/types.IsModelType). This file re-exports them so callers
// that reach for the upstream utils module get the same vocabulary, and adds
// the assert/narrowing helpers and the provider-error result builders.
package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

// GetModelType returns the kind of a model (see types.GetModelType). Models
// without a `type` are chat models.
func GetModelType(model any) string { return types.GetModelType(model) }

// IsModelType reports whether a model is of the given kind.
func IsModelType(model any, kind string) bool { return types.IsModelType(model, kind) }

// AssertChatModel narrows a model to a chat model, returning a ModelsError when
// it is not.
func AssertChatModel(model any) (*types.Model, error) {
	if !types.IsModelType(model, types.ModelTypeChat) {
		return nil, NewModelsError(ModelsErrorProvider, fmt.Sprintf("Model %s is not a chat model", describeModel(model)), nil)
	}
	switch value := model.(type) {
	case types.Model:
		copy := value
		return &copy, nil
	case *types.Model:
		return value, nil
	case types.AnyModel:
		if value.Chat == nil {
			return nil, NewModelsError(ModelsErrorProvider, fmt.Sprintf("Model %s is not a chat model", describeModel(model)), nil)
		}
		return value.Chat, nil
	default:
		chat, err := narrowChat(model)
		if err != nil {
			return nil, err
		}
		return chat, nil
	}
}

// AssertImageModel narrows a model to an image model.
func AssertImageModel(model any) (*types.ImagesModel, error) {
	if !types.IsModelType(model, types.ModelTypeImage) {
		return nil, NewModelsError(ModelsErrorProvider, fmt.Sprintf("Model %s is not an image model", describeModel(model)), nil)
	}
	switch value := model.(type) {
	case types.ImagesModel:
		copy := value
		return &copy, nil
	case *types.ImagesModel:
		return value, nil
	case types.AnyModel:
		if value.Image == nil {
			return nil, NewModelsError(ModelsErrorProvider, fmt.Sprintf("Model %s is not an image model", describeModel(model)), nil)
		}
		return value.Image, nil
	default:
		image, err := narrowImage(model)
		if err != nil {
			return nil, err
		}
		return image, nil
	}
}

// AssertClassifierModel narrows a model to a classifier model.
func AssertClassifierModel(model any) (*types.ClassifierModel, error) {
	if !types.IsModelType(model, types.ModelTypeClassifier) {
		return nil, NewModelsError(ModelsErrorProvider, fmt.Sprintf("Model %s is not a classifier model", describeModel(model)), nil)
	}
	switch value := model.(type) {
	case types.ClassifierModel:
		copy := value
		return &copy, nil
	case *types.ClassifierModel:
		return value, nil
	case types.AnyModel:
		if value.Classifier == nil {
			return nil, NewModelsError(ModelsErrorProvider, fmt.Sprintf("Model %s is not a classifier model", describeModel(model)), nil)
		}
		return value.Classifier, nil
	default:
		classifier, err := narrowClassifier(model)
		if err != nil {
			return nil, err
		}
		return classifier, nil
	}
}

// ImageErrorResult builds the aborted/error AssistantImages result for an image
// model, mirroring the upstream `imageErrorResult`.
func ImageErrorResult(model types.ImagesModel, err error, aborted bool) types.AssistantImages {
	stopReason := types.ImagesStopReasonError
	if aborted {
		stopReason = types.ImagesStopReasonAborted
	}
	message := errorMessage(err)
	return types.AssistantImages{
		Api:          types.ImagesApi(model.Api),
		Provider:     types.ImagesProviderId(model.Provider),
		Model:        model.Id,
		Output:       []types.ImagesOutputContent{},
		StopReason:   stopReason,
		ErrorMessage: &message,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
}

// ClassifierErrorResult builds the aborted/error ClassifierResult for a
// classifier model, mirroring the upstream `classifierErrorResult`.
func ClassifierErrorResult(model types.ClassifierModel, err error, aborted bool) types.ClassifierResult {
	stopReason := types.ClassifierStopReasonError
	if aborted {
		stopReason = types.ClassifierStopReasonAborted
	}
	message := errorMessage(err)
	return types.ClassifierResult{
		Api:          model.Api,
		Provider:     model.Provider,
		Model:        model.Id,
		Answers:      map[string]types.ClassifierAnswer{},
		StopReason:   stopReason,
		ErrorMessage: &message,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// rawModelJSON extracts a raw JSON object from any of the raw model inputs the
// helpers accept (json.RawMessage/[]byte/string/decoded maps).
func rawModelJSON(model any) (json.RawMessage, bool) {
	switch value := model.(type) {
	case json.RawMessage:
		return meaningfulJSON(value)
	case []byte:
		return meaningfulJSON(value)
	case string:
		return meaningfulJSON([]byte(value))
	case map[string]any:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		return meaningfulJSON(encoded)
	case map[string]json.RawMessage:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		return meaningfulJSON(encoded)
	default:
		return nil, false
	}
}

func meaningfulJSON(raw []byte) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}
	return json.RawMessage(trimmed), true
}

func decodeModelJSON(raw json.RawMessage, target any) error {
	return json.Unmarshal(raw, target)
}

func narrowChat(model any) (*types.Model, error) {
	raw, ok := rawModelJSON(model)
	if !ok {
		return nil, NewModelsError(ModelsErrorProvider, "Model is not a chat model", nil)
	}
	parsed := &types.Model{}
	if err := decodeModelJSON(raw, parsed); err != nil {
		return nil, NewModelsError(ModelsErrorProvider, "Model is not a chat model", err)
	}
	return parsed, nil
}

func narrowImage(model any) (*types.ImagesModel, error) {
	raw, ok := rawModelJSON(model)
	if !ok {
		return nil, NewModelsError(ModelsErrorProvider, "Model is not an image model", nil)
	}
	parsed := &types.ImagesModel{}
	if err := decodeModelJSON(raw, parsed); err != nil {
		return nil, NewModelsError(ModelsErrorProvider, "Model is not an image model", err)
	}
	return parsed, nil
}

func narrowClassifier(model any) (*types.ClassifierModel, error) {
	raw, ok := rawModelJSON(model)
	if !ok {
		return nil, NewModelsError(ModelsErrorProvider, "Model is not a classifier model", nil)
	}
	parsed := &types.ClassifierModel{}
	if err := decodeModelJSON(raw, parsed); err != nil {
		return nil, NewModelsError(ModelsErrorProvider, "Model is not a classifier model", err)
	}
	return parsed, nil
}

func describeModel(model any) string {
	switch value := model.(type) {
	case types.Model:
		return fmt.Sprintf("%s/%s", value.Provider, value.Id)
	case *types.Model:
		if value == nil {
			return "<nil>"
		}
		return fmt.Sprintf("%s/%s", value.Provider, value.Id)
	case types.ImagesModel:
		return fmt.Sprintf("%s/%s", value.Provider, value.Id)
	case *types.ImagesModel:
		if value == nil {
			return "<nil>"
		}
		return fmt.Sprintf("%s/%s", value.Provider, value.Id)
	case types.ClassifierModel:
		return fmt.Sprintf("%s/%s", value.Provider, value.Id)
	case *types.ClassifierModel:
		if value == nil {
			return "<nil>"
		}
		return fmt.Sprintf("%s/%s", value.Provider, value.Id)
	case types.AnyModel:
		return describeAny(value)
	case *types.AnyModel:
		if value == nil {
			return "<nil>"
		}
		return describeAny(*value)
	default:
		return fmt.Sprintf("%v", model)
	}
}

func describeAny(model types.AnyModel) string {
	switch {
	case model.Chat != nil:
		return fmt.Sprintf("%s/%s", model.Chat.Provider, model.Chat.Id)
	case model.Image != nil:
		return fmt.Sprintf("%s/%s", model.Image.Provider, model.Image.Id)
	case model.Classifier != nil:
		return fmt.Sprintf("%s/%s", model.Classifier.Provider, model.Classifier.Id)
	default:
		return model.Type
	}
}
