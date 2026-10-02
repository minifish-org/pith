// This file ports the structured-classifier model and request/result DTOs from
// packages/ai/src/types.ts of Pi at the frozen target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A classifier model is the third V1 model kind (see any_model.go). Only
// classify() accepts it; stream()/generateImages() reject it. The DTOs keep
// absent vs zero distinctions: optional numeric fields and the union criteria
// payload stay pointers/raw JSON rather than being flattened to zero values.
package types

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// KnownClassifierApi is the set of classifier API identifiers shipped with the
// SDK. It is an open union: custom provider strings are allowed.
type KnownClassifierApi string

// Known classifier API identifiers.
const (
	ClassifierApiTypesafeSystemOne            = "typesafe-system-one"
	ClassifierApiCloudflareWorkersAISystemOne = "cloudflare-workers-ai-system-one"
	ClassifierApiLlamaCppClassify             = "llama-cpp-classify"
)

// classifierApis lists the known classifier APIs in upstream declaration order.
var classifierApis = []KnownClassifierApi{
	ClassifierApiTypesafeSystemOne,
	ClassifierApiCloudflareWorkersAISystemOne,
	ClassifierApiLlamaCppClassify,
}

// ClassifierApi identifies a classifier API. The untyped string constants above
// are usable as both KnownClassifierApi and ClassifierApi.
type ClassifierApi string

// ClassifierStopReason is why a classification request stopped.
type ClassifierStopReason string

// Classifier stop reasons.
const (
	ClassifierStopReasonStop    ClassifierStopReason = "stop"
	ClassifierStopReasonError   ClassifierStopReason = "error"
	ClassifierStopReasonAborted ClassifierStopReason = "aborted"
)

// ClassifierModel is the model descriptor used by classifier providers.
//
// It is the shared model identity (id, name, api, provider, baseUrl, input,
// inputLimits, cost, headers) plus the classifier-only contextWindow. `type` is
// always "classifier"; omitempty keeps a zero value from fabricating one.
type ClassifierModel struct {
	Type          string               `json:"type,omitempty"`
	Id            string               `json:"id"`
	Name          string               `json:"name"`
	Api           ClassifierApi        `json:"api"`
	Provider      ProviderId           `json:"provider"`
	BaseUrl       string               `json:"baseUrl"`
	Input         []ModelInputModality `json:"input"`
	InputLimits   *ModelInputLimits    `json:"inputLimits,omitempty"`
	Cost          ModelCost            `json:"cost"`
	Headers       map[string]string    `json:"headers,omitempty"`
	ContextWindow float64              `json:"contextWindow"`
}

// NewClassifierModel builds a classifier model with the classifier type set.
func NewClassifierModel(id, name string, api ClassifierApi, provider ProviderId, baseUrl string) ClassifierModel {
	return ClassifierModel{
		Type:     ModelTypeClassifier,
		Id:       id,
		Name:     name,
		Api:      api,
		Provider: provider,
		BaseUrl:  baseUrl,
	}
}

// ClassifierOptions are the options shared by classifier requests.
//
// Upstream narrows the request options to the classifier model; Go reuses the
// shared ProviderRequestOptions and adds the classifier-only temperature knob.
type ClassifierOptions struct {
	ProviderRequestOptions
	// Temperature divides the answer logits before normalization. Values above
	// 1 soften the distribution, below 1 sharpen it. Must be positive; APIs that
	// cannot apply it ignore it.
	Temperature *float64 `json:"temperature,omitempty"`
}

// ClassifierQuestion is one question in a classifier context.
//
// The union shape is preserved as a tagged struct: `criteria` stays raw JSON so
// the choice (`map<string,string>`), score (`[]string`) and bool (`{true,false}`)
// payloads round-trip without being coerced.
type ClassifierQuestion struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

// ClassifierChoiceCriteria is the criteria payload of a choice question.
type ClassifierChoiceCriteria = map[string]string

// ClassifierBoolCriteria is the criteria payload of a bool question.
type ClassifierBoolCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

// ClassifierAnswer is one answer in a classifier result.
//
// As with questions, the union is a tagged struct so each variant's fields are
// available without losing the absent-vs-zero distinction (all optional numbers
// are pointers).
type ClassifierAnswer struct {
	Type          string             `json:"type"`
	Choice        *string            `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probability   *float64           `json:"probability,omitempty"`
}

// ClassifierContext is the input of a classifier request.
//
// The question order is preserved from JSON so that protocols whose prompt or
// response handling depends on `Object.entries` iteration order (llama.cpp's
// overview prompt) are reproducible. A Go-constructed context has no recorded
// order; QuestionIDs then returns a deterministic sorted order.
type ClassifierContext struct {
	State     JsonObject                    `json:"state"`
	Questions map[string]ClassifierQuestion `json:"questions"`
	// questionOrder records the JSON object key order of `questions`.
	questionOrder []string
}

// QuestionIDs returns the question ids in request order. Decoded contexts keep
// their JSON order; Go-constructed contexts fall back to sorted order.
func (c ClassifierContext) QuestionIDs() []string {
	if len(c.questionOrder) > 0 {
		ordered := make([]string, 0, len(c.Questions))
		seen := make(map[string]bool, len(c.questionOrder))
		for _, id := range c.questionOrder {
			if _, ok := c.Questions[id]; ok && !seen[id] {
				ordered = append(ordered, id)
				seen[id] = true
			}
		}
		extra := make([]string, 0)
		for id := range c.Questions {
			if !seen[id] {
				extra = append(extra, id)
			}
		}
		sort.Strings(extra)
		return append(ordered, extra...)
	}
	ids := make([]string, 0, len(c.Questions))
	for id := range c.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// UnmarshalJSON decodes the context and records the question key order.
func (c *ClassifierContext) UnmarshalJSON(data []byte) error {
	var raw struct {
		State     json.RawMessage `json:"state"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.State) > 0 && string(raw.State) != "null" {
		if err := json.Unmarshal(raw.State, &c.State); err != nil {
			return err
		}
	} else {
		c.State = nil
	}
	if len(raw.Questions) > 0 && string(raw.Questions) != "null" {
		order, questions, err := decodeOrderedQuestions(raw.Questions)
		if err != nil {
			return err
		}
		c.Questions = questions
		c.questionOrder = order
	} else {
		c.Questions = nil
		c.questionOrder = nil
	}
	return nil
}

// MarshalJSON emits the context, preserving the recorded question order when
// one was decoded.
func (c ClassifierContext) MarshalJSON() ([]byte, error) {
	if len(c.questionOrder) == 0 {
		type alias ClassifierContext
		return json.Marshal(alias(c))
	}
	questions := make(map[string]json.RawMessage, len(c.Questions))
	for id, question := range c.Questions {
		encoded, err := json.Marshal(question)
		if err != nil {
			return nil, err
		}
		questions[id] = encoded
	}
	return marshalOrderedContext(c.State, c.QuestionIDs(), questions)
}

// decodeOrderedQuestions decodes a JSON object of questions, preserving the key
// order.
func decodeOrderedQuestions(raw json.RawMessage) ([]string, map[string]ClassifierQuestion, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("classifier context: questions must be an object")
	}
	order := make([]string, 0)
	questions := make(map[string]ClassifierQuestion)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, nil, fmt.Errorf("classifier context: expected a question id")
		}
		var question ClassifierQuestion
		if err := decoder.Decode(&question); err != nil {
			return nil, nil, err
		}
		order = append(order, key)
		questions[key] = question
	}
	return order, questions, nil
}

// marshalOrderedContext emits `{ state, questions }` with questions in order.
func marshalOrderedContext(state JsonObject, order []string, questions map[string]json.RawMessage) ([]byte, error) {
	var builder bytes.Buffer
	builder.WriteByte('{')
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	builder.WriteString("\"state\":")
	builder.Write(stateJSON)
	builder.WriteString(",\"questions\":{")
	for index, id := range order {
		if index > 0 {
			builder.WriteByte(',')
		}
		keyJSON, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		builder.Write(keyJSON)
		builder.WriteByte(':')
		builder.Write(questions[id])
	}
	builder.WriteString("}}")
	return builder.Bytes(), nil
}

// ClassifierResult is the result of a classifier request.
type ClassifierResult struct {
	Api          ClassifierApi               `json:"api"`
	Provider     ProviderId                  `json:"provider"`
	Model        string                      `json:"model"`
	Answers      map[string]ClassifierAnswer `json:"answers"`
	Usage        *Usage                      `json:"usage,omitempty"`
	StopReason   ClassifierStopReason        `json:"stopReason"`
	ErrorMessage *string                     `json:"errorMessage,omitempty"`
	// Timestamp is a Unix timestamp in milliseconds.
	Timestamp float64 `json:"timestamp"`
}

// ProviderClassifier is the uniform contract implemented by classifier API
// modules: every classifier API module exports exactly classify().
type ProviderClassifier interface {
	Classify(model ClassifierModel, context ClassifierContext, options *ClassifierOptions) (ClassifierResult, error)
}

// ClassifierFunction is a classifier implementation bound to a provider.
type ClassifierFunction func(model ClassifierModel, context ClassifierContext, options *ClassifierOptions) (ClassifierResult, error)

// KnownClassifierApis returns the known classifier API identifiers in
// declaration order.
func KnownClassifierApis() []KnownClassifierApi {
	out := make([]KnownClassifierApi, len(classifierApis))
	copy(out, classifierApis)
	return out
}
