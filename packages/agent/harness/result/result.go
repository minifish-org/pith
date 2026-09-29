// Package result is the Go port of packages/agent/src/harness/result.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Expected failures are values, not panics: Result carries either a success
// value or a typed error. Tagged errors keep the upstream `_tag` discriminator
// and the flat JSON payload produced by toJSON().
package result

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Result is a fallible operation outcome. Exactly one of Value or Error is
// meaningful, selected by OK.
type Result[TValue any, TError any] struct {
	OK    bool   `json:"ok"`
	Value TValue `json:"value"`
	Error TError `json:"error"`
}

// Ok creates a successful Result.
func Ok[TValue any, TError any](value TValue) Result[TValue, TError] {
	return Result[TValue, TError]{OK: true, Value: value}
}

// Err creates a failed Result.
func Err[TValue any, TError any](err TError) Result[TValue, TError] {
	return Result[TValue, TError]{OK: false, Error: err}
}

// IsOk reports whether the result is successful.
func (r Result[TValue, TError]) IsOk() bool { return r.OK }

// IsErr reports whether the result is failed.
func (r Result[TValue, TError]) IsErr() bool { return !r.OK }

// MarshalJSON writes only the payload selected by OK, matching the upstream
// discriminated union.
func (r Result[TValue, TError]) MarshalJSON() ([]byte, error) {
	if r.OK {
		return json.Marshal(struct {
			OK    bool   `json:"ok"`
			Value TValue `json:"value"`
		}{OK: true, Value: r.Value})
	}
	return json.Marshal(struct {
		OK    bool   `json:"ok"`
		Error TError `json:"error"`
	}{OK: false, Error: r.Error})
}

// UnmarshalJSON reads the discriminated union form.
func (r *Result[TValue, TError]) UnmarshalJSON(data []byte) error {
	var probe struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	r.OK = probe.OK
	if probe.OK {
		r.Error = *new(TError)
		if len(probe.Value) == 0 {
			r.Value = *new(TValue)
			return nil
		}
		return json.Unmarshal(probe.Value, &r.Value)
	}
	r.Value = *new(TValue)
	if len(probe.Error) == 0 {
		r.Error = *new(TError)
		return nil
	}
	return json.Unmarshal(probe.Error, &r.Error)
}

// TaggedErrorValue is the runtime shape produced by a TaggedError family.
type TaggedErrorValue interface {
	error
	// Tag is the stable discriminator, equivalent to upstream `_tag`.
	Tag() string
	// ToJSON returns the flat `_tag`/message/properties payload.
	ToJSON() map[string]any
}

// TaggedErrorFactory creates and identifies one tagged error family.
type TaggedErrorFactory interface {
	// Tag is the stable family discriminator.
	Tag() string
	// New builds one error carrying the supplied properties.
	New(props map[string]any) (TaggedErrorValue, error)
	// Is reports whether err belongs to this family.
	Is(err error) bool
}

// taggedError is the concrete factory implementation.
type taggedError struct {
	tag string
}

func (f taggedError) Tag() string { return f.tag }

func (f taggedError) New(props map[string]any) (TaggedErrorValue, error) {
	message, _ := props["message"].(string)
	return &genericTaggedError{tag: f.tag, message: message, fields: cloneProps(props)}, nil
}

func (f taggedError) Is(err error) bool {
	var tagged interface{ Tag() string }
	if !errors.As(err, &tagged) {
		return false
	}
	return tagged.Tag() == f.tag
}

// TaggedError builds a factory for the supplied tag.
func TaggedError(tag string) TaggedErrorFactory {
	return taggedError{tag: tag}
}

// genericTaggedError carries arbitrary properties for the factory-created form.
type genericTaggedError struct {
	tag     string
	message string
	fields  map[string]any
}

func (e *genericTaggedError) Error() string { return e.message }
func (e *genericTaggedError) Tag() string   { return e.tag }

// ToJSON returns the flat tag/message/properties payload.
func (e *genericTaggedError) ToJSON() map[string]any {
	return marshalTagged(e.tag, e.fields, e.message)
}

func cloneProps(props map[string]any) map[string]any {
	copied := make(map[string]any, len(props))
	for key, value := range props {
		copied[key] = value
	}
	return copied
}

// marshalTagged flattens a struct (or explicit field map) into the upstream
// JSON shape: every own property plus `_tag` and `message`.
func marshalTagged(tag string, value any, message string) map[string]any {
	payload := map[string]any{}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			payload[key] = item
		}
	default:
		encoded, err := json.Marshal(value)
		if err == nil {
			var decoded map[string]any
			if json.Unmarshal(encoded, &decoded) == nil {
				payload = decoded
			}
		}
	}
	if message != "" {
		payload["message"] = message
	}
	if _, present := payload["message"]; !present {
		payload["message"] = message
	}
	// The upstream TaggedError constructor sets an own enumerable `name` to the
	// tag, then assigns the supplied properties over it. A property named
	// `name` therefore wins; otherwise the tag is the error name and must stay
	// in the flattened payload.
	if _, present := payload["name"]; !present {
		payload["name"] = tag
	}
	payload["_tag"] = tag
	return payload
}

// LaneBusy reports that a lane already owns a different operation.
type LaneBusy struct {
	Lane          string `json:"lane"`
	OperationId   string `json:"operationId"`
	OperationKind string `json:"operationKind"`
	Message       string `json:"message"`
}

func (e *LaneBusy) Error() string          { return e.Message }
func (e *LaneBusy) Tag() string            { return "LaneBusy" }
func (e *LaneBusy) ToJSON() map[string]any { return marshalTagged("LaneBusy", e, e.Message) }

// OperationMismatch reports that an operation id does not match the lane.
type OperationMismatch struct {
	Lane                string  `json:"lane"`
	ExpectedOperationId string  `json:"expectedOperationId"`
	CurrentOperationId  *string `json:"currentOperationId,omitempty"`
	LastOperationId     *string `json:"lastOperationId,omitempty"`
	Message             string  `json:"message"`
}

func (e *OperationMismatch) Error() string { return e.Message }
func (e *OperationMismatch) Tag() string   { return "OperationMismatch" }
func (e *OperationMismatch) ToJSON() map[string]any {
	return marshalTagged("OperationMismatch", e, e.Message)
}

// NoActiveRun reports that no run is active on the lane.
type NoActiveRun struct {
	Lane    string `json:"lane"`
	Message string `json:"message"`
}

func (e *NoActiveRun) Error() string          { return e.Message }
func (e *NoActiveRun) Tag() string            { return "NoActiveRun" }
func (e *NoActiveRun) ToJSON() map[string]any { return marshalTagged("NoActiveRun", e, e.Message) }

// NoActiveOperation reports that no operation is active on the lane.
type NoActiveOperation struct {
	Lane    string `json:"lane"`
	Message string `json:"message"`
}

func (e *NoActiveOperation) Error() string { return e.Message }
func (e *NoActiveOperation) Tag() string   { return "NoActiveOperation" }
func (e *NoActiveOperation) ToJSON() map[string]any {
	return marshalTagged("NoActiveOperation", e, e.Message)
}

// NothingToResume reports that the lane has nothing to resume.
type NothingToResume struct {
	Lane    string `json:"lane"`
	Message string `json:"message"`
}

func (e *NothingToResume) Error() string { return e.Message }
func (e *NothingToResume) Tag() string   { return "NothingToResume" }
func (e *NothingToResume) ToJSON() map[string]any {
	return marshalTagged("NothingToResume", e, e.Message)
}

// NothingToCompact reports that the lane has nothing to compact.
type NothingToCompact struct {
	Lane    string `json:"lane"`
	Message string `json:"message"`
}

func (e *NothingToCompact) Error() string { return e.Message }
func (e *NothingToCompact) Tag() string   { return "NothingToCompact" }
func (e *NothingToCompact) ToJSON() map[string]any {
	return marshalTagged("NothingToCompact", e, e.Message)
}

// InvalidMessage reports a malformed queued message.
type InvalidMessage struct {
	Lane    string `json:"lane"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *InvalidMessage) Error() string { return e.Message }
func (e *InvalidMessage) Tag() string   { return "InvalidMessage" }
func (e *InvalidMessage) ToJSON() map[string]any {
	return marshalTagged("InvalidMessage", e, e.Message)
}

// InvalidNavigation reports an invalid navigation target.
type InvalidNavigation struct {
	Lane    string `json:"lane"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *InvalidNavigation) Error() string { return e.Message }
func (e *InvalidNavigation) Tag() string   { return "InvalidNavigation" }
func (e *InvalidNavigation) ToJSON() map[string]any {
	return marshalTagged("InvalidNavigation", e, e.Message)
}

// UnknownSkill reports an unknown skill name.
type UnknownSkill struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

func (e *UnknownSkill) Error() string          { return e.Message }
func (e *UnknownSkill) Tag() string            { return "UnknownSkill" }
func (e *UnknownSkill) ToJSON() map[string]any { return marshalTagged("UnknownSkill", e, e.Message) }

// UnknownTemplate reports an unknown prompt template name.
type UnknownTemplate struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

func (e *UnknownTemplate) Error() string { return e.Message }
func (e *UnknownTemplate) Tag() string   { return "UnknownTemplate" }
func (e *UnknownTemplate) ToJSON() map[string]any {
	return marshalTagged("UnknownTemplate", e, e.Message)
}

// UnknownTarget reports an unknown navigation target.
type UnknownTarget struct {
	TargetId string `json:"targetId"`
	Message  string `json:"message"`
}

func (e *UnknownTarget) Error() string          { return e.Message }
func (e *UnknownTarget) Tag() string            { return "UnknownTarget" }
func (e *UnknownTarget) ToJSON() map[string]any { return marshalTagged("UnknownTarget", e, e.Message) }

// InvalidLane reports an unknown lane name.
type InvalidLane struct {
	Lane    string `json:"lane"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *InvalidLane) Error() string          { return e.Message }
func (e *InvalidLane) Tag() string            { return "InvalidLane" }
func (e *InvalidLane) ToJSON() map[string]any { return marshalTagged("InvalidLane", e, e.Message) }

// Closed reports an operation against a closed harness.
type Closed struct {
	Message string `json:"message"`
}

func (e *Closed) Error() string          { return e.Message }
func (e *Closed) Tag() string            { return "Closed" }
func (e *Closed) ToJSON() map[string]any { return marshalTagged("Closed", e, e.Message) }

// HarnessFault wraps an unexpected harness failure with its cause.
type HarnessFault struct {
	Message string
	Cause   error
}

func (e *HarnessFault) Error() string { return e.Message }
func (e *HarnessFault) Unwrap() error { return e.Cause }

// NewHarnessFault builds a harness fault.
func NewHarnessFault(message string, cause error) *HarnessFault {
	return &HarnessFault{Message: message, Cause: cause}
}

// HarnessClosed reports that the harness was closed while an operation was
// active.
type HarnessClosed struct{}

func (HarnessClosed) Error() string {
	return "AgentHarness was closed while the operation was active"
}

// ErrorMatchers maps a tagged error discriminator to the value produced for
// that family.
type ErrorMatchers[TValue any] map[string]func(error) TValue

// MatchError dispatches on the tagged discriminator, mirroring upstream
// matchError. A missing matcher is a programming error and panics with the
// upstream-style TypeError text.
func MatchError[TValue any](err error, matchers ErrorMatchers[TValue]) TValue {
	var tagged interface{ Tag() string }
	if !errors.As(err, &tagged) {
		panic(fmt.Sprintf("matchError: %v is not a tagged error", err))
	}
	matcher, ok := matchers[tagged.Tag()]
	if !ok {
		panic(fmt.Sprintf("matchError: no matcher for tag %q", tagged.Tag()))
	}
	return matcher(err)
}
