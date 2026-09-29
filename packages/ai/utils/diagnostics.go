// This file is a Go port of packages/ai/src/utils/diagnostics.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

// DiagnosticErrorInfo is the extracted shape of a thrown diagnostic error.
type DiagnosticErrorInfo struct {
	Name    *string `json:"name,omitempty"`
	Message string  `json:"message"`
	Stack   *string `json:"stack,omitempty"`
	// Code is a string or number; it is kept as `any` so both numeric and
	// string error codes round-trip.
	Code any `json:"code,omitempty"`
}

// AssistantMessageDiagnostic is a redacted provider/runtime diagnostic attached
// to an assistant message. It is the utils-package type; the wire shape stored
// on the message is types.AssistantMessageDiagnostic.
type AssistantMessageDiagnostic struct {
	Type      string               `json:"type"`
	Timestamp float64              `json:"timestamp"`
	Error     *DiagnosticErrorInfo `json:"error,omitempty"`
	Details   types.JsonObject     `json:"details,omitempty"`
}

// FormatThrownValue renders a thrown value as a display string.
func FormatThrownValue(value any) string {
	if err, ok := value.(error); ok {
		if err.Error() != "" {
			return err.Error()
		}
		return "Error"
	}
	if text, ok := value.(string); ok {
		return text
	}
	return typesString(value)
}

// ExtractDiagnosticError normalizes a thrown value into DiagnosticErrorInfo.
func ExtractDiagnosticError(err any) DiagnosticErrorInfo {
	asError, ok := err.(error)
	if !ok {
		name := "ThrownValue"
		return DiagnosticErrorInfo{Name: &name, Message: FormatThrownValue(err)}
	}
	info := DiagnosticErrorInfo{Message: asError.Error()}
	if info.Message == "" {
		info.Message = "Error"
	}
	name := errorName(asError)
	if name != "" {
		info.Name = &name
	}
	if code := errorCode(asError); code != nil {
		info.Code = code
	}
	return info
}

// CreateAssistantMessageDiagnostic builds a diagnostic stamped with the current
// time.
func CreateAssistantMessageDiagnostic(kind string, err any, details types.JsonObject) AssistantMessageDiagnostic {
	return AssistantMessageDiagnostic{
		Type:      kind,
		Timestamp: float64(time.Now().UnixMilli()),
		Error:     ptrDiagnosticInfo(ExtractDiagnosticError(err)),
		Details:   details,
	}
}

// AppendAssistantMessageDiagnostic appends a diagnostic to a message's
// diagnostics slice in place.
func AppendAssistantMessageDiagnostic(diagnostics []AssistantMessageDiagnostic, diagnostic AssistantMessageDiagnostic) []AssistantMessageDiagnostic {
	return append(diagnostics, diagnostic)
}

func ptrDiagnosticInfo(info DiagnosticErrorInfo) *DiagnosticErrorInfo { return &info }

// errorName returns the error name. Go does not have a structured name field;
// callers can implement `interface{ Name() string }`.
func errorName(err error) string {
	if named, ok := err.(interface{ Name() string }); ok {
		return named.Name()
	}
	return ""
}

// errorCode returns a string or numeric error code, if the error exposes one.
func errorCode(err error) any {
	if coded, ok := err.(interface{ Code() string }); ok {
		return coded.Code()
	}
	return nil
}

func typesString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	default:
		return SafeJSONStringify(value)
	}
}
