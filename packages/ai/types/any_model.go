// This file ports the tagged model union and the runtime model-type helpers from
// packages/ai/src/utils/model-operations.ts and packages/ai/src/types.ts of Pi
// at the frozen target revision.
//
// Upstream:
//
//	export function getModelType(model: AnyModel): ModelType {
//	    return model.type ?? "chat";
//	}
//	export function isModelType(model, type) { return getModelType(model) === type; }
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Go notes:
//   - The legacy chat DTO stays types.Model; types.ImagesModel is the image DTO
//     and types.ClassifierModel is the classifier DTO.
//   - GetModelType/IsModelType accept the legacy DTOs (by value or pointer), nil,
//     raw JSON (json.RawMessage/[]byte/string) and decoded map values. A missing
//     `type` means chat; an explicit unknown `type` is returned verbatim, it is
//     never coerced to chat. nil is never a chat model.
package types

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Model kinds. These are the values GetModelType returns for the three shipped
// models; any other explicit `type` is preserved verbatim.
const (
	ModelTypeChat       = "chat"
	ModelTypeImage      = "image"
	ModelTypeClassifier = "classifier"
)

// GetModelType returns the kind of a model. Models without a `type` are chat
// models. A nil, empty or unparseable value returns the empty string. Any other
// explicit `type` value is returned unchanged.
func GetModelType(model any) string {
	switch v := model.(type) {
	case nil:
		return ""
	case Model:
		return ModelTypeChat
	case *Model:
		if v == nil {
			return ""
		}
		return ModelTypeChat
	case ImagesModel:
		return ModelTypeImage
	case *ImagesModel:
		if v == nil {
			return ""
		}
		return ModelTypeImage
	case ClassifierModel:
		return ModelTypeClassifier
	case *ClassifierModel:
		if v == nil {
			return ""
		}
		return ModelTypeClassifier
	case AnyModel:
		return v.Type
	case *AnyModel:
		if v == nil {
			return ""
		}
		return v.Type
	case json.RawMessage:
		return modelTypeFromJSON(v)
	case []byte:
		return modelTypeFromJSON(v)
	case string:
		return modelTypeFromJSON([]byte(v))
	case map[string]any:
		return modelTypeFromMap(v)
	case map[string]json.RawMessage:
		return modelTypeFromRawMap(v)
	default:
		return ""
	}
}

// IsModelType reports whether a model is of the given kind. It is the runtime
// narrowing guard used on mixed model lists.
func IsModelType(model any, kind string) bool {
	return GetModelType(model) == kind
}

// modelTypeFromJSON extracts the model kind from a raw JSON object.
func modelTypeFromJSON(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var probe struct {
		Type *json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return ""
	}
	if probe.Type == nil {
		return ModelTypeChat
	}
	return decodeModelType(*probe.Type)
}

// modelTypeFromMap extracts the model kind from a decoded JSON object.
func modelTypeFromMap(value map[string]any) string {
	raw, ok := value["type"]
	if !ok || raw == nil {
		return ModelTypeChat
	}
	if text, ok := raw.(string); ok {
		return normalizeModelType(text)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return decodeModelType(encoded)
}

// modelTypeFromRawMap extracts the model kind from a decoded JSON object whose
// values are still raw.
func modelTypeFromRawMap(value map[string]json.RawMessage) string {
	raw, ok := value["type"]
	if !ok {
		return ModelTypeChat
	}
	return decodeModelType(raw)
}

// decodeModelType decodes a raw JSON `type` value. A JSON null or empty string
// means chat (upstream `model.type ?? "chat"` and an empty tag both behave as
// "no explicit type").
func decodeModelType(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ModelTypeChat
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return ""
	}
	return normalizeModelType(text)
}

// normalizeModelType maps a decoded type string onto the model kind. An empty
// string means chat; anything else is preserved verbatim.
func normalizeModelType(text string) string {
	if strings.TrimSpace(text) == "" {
		return ModelTypeChat
	}
	return text
}

// AnyModel is a tagged union of every model a provider can list.
//
// Exactly one payload is set, selected by Type. The union is modeled as a struct
// rather than an interface so that it round-trips through JSON, preserves the
// raw object of unmodeled kinds and keeps each operation's specific fields
// (chat reasoning/compat, image output, classifier contextWindow). The zero
// value is an empty model.
type AnyModel struct {
	// Type is the model kind: "chat", "image", "classifier" or an explicit
	// provider-defined kind preserved verbatim.
	Type string
	// Chat is set when Type is chat.
	Chat *Model
	// Image is set when Type is image.
	Image *ImagesModel
	// Classifier is set when Type is classifier.
	Classifier *ClassifierModel
	// raw is the verbatim JSON object this model was decoded from, when any.
	raw json.RawMessage
}

// NewAnyChatModel boxed a chat model into the union.
func NewAnyChatModel(model Model) AnyModel {
	copy := model
	return AnyModel{Type: ModelTypeChat, Chat: &copy}
}

// NewAnyImageModel boxes an image model into the union.
func NewAnyImageModel(model ImagesModel) AnyModel {
	copy := model
	return AnyModel{Type: ModelTypeImage, Image: &copy}
}

// NewAnyClassifierModel boxes a classifier model into the union.
func NewAnyClassifierModel(model ClassifierModel) AnyModel {
	copy := model
	return AnyModel{Type: ModelTypeClassifier, Classifier: &copy}
}

// Raw returns a defensive copy of the verbatim JSON object the model was decoded
// from, if it was decoded from JSON.
func (m AnyModel) Raw() json.RawMessage {
	return append(json.RawMessage(nil), m.raw...)
}

// MarshalJSON emits the verbatim object when the model came from JSON, otherwise
// it emits the selected payload.
func (m AnyModel) MarshalJSON() ([]byte, error) {
	if len(m.raw) > 0 {
		return append([]byte(nil), m.raw...), nil
	}
	switch m.Type {
	case ModelTypeChat:
		if m.Chat != nil {
			return json.Marshal(m.Chat)
		}
	case ModelTypeImage:
		if m.Image != nil {
			return json.Marshal(m.Image)
		}
	case ModelTypeClassifier:
		if m.Classifier != nil {
			return json.Marshal(m.Classifier)
		}
	}
	return json.Marshal(nil)
}

// UnmarshalJSON decodes the tagged union, keeping the verbatim object so an
// unknown kind is not lost.
func (m *AnyModel) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*m = AnyModel{}
		return nil
	}
	*m = AnyModel{raw: append(json.RawMessage(nil), trimmed...)}
	m.Type = modelTypeFromJSON(trimmed)
	switch m.Type {
	case ModelTypeChat:
		model := &Model{}
		if err := json.Unmarshal(trimmed, model); err != nil {
			return err
		}
		m.Chat = model
	case ModelTypeImage:
		model := &ImagesModel{}
		if err := json.Unmarshal(trimmed, model); err != nil {
			return err
		}
		m.Image = model
	case ModelTypeClassifier:
		model := &ClassifierModel{}
		if err := json.Unmarshal(trimmed, model); err != nil {
			return err
		}
		m.Classifier = model
	}
	return nil
}
