// This file is a Go port of packages/ai/src/utils/models-error.ts from Pi at the
// frozen target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"fmt"
	"strings"
)

// ModelsErrorCode is a stable error discriminator, mirroring the upstream
// `ModelsErrorCode` union.
type ModelsErrorCode string

// ModelsErrorCode values.
const (
	ModelsErrorModelSource     ModelsErrorCode = "model_source"
	ModelsErrorModelValidation ModelsErrorCode = "model_validation"
	ModelsErrorProvider        ModelsErrorCode = "provider"
	ModelsErrorStream          ModelsErrorCode = "stream"
	ModelsErrorAuth            ModelsErrorCode = "auth"
	ModelsErrorOAuth           ModelsErrorCode = "oauth"
)

// ModelsError is a model-resolution error carrying a stable code.
//
// Callers surface only Error(), so the underlying cause is appended to the
// message (upstream `withCauseDetail`).
type ModelsError struct {
	Code ModelsErrorCode
	msg  string
	// Cause is the wrapped underlying error, if any. It is exposed via Unwrap so
	// callers can inspect it without changing the message contract.
	Cause error
}

// NewModelsError builds a ModelsError. A non-nil cause is appended to message
// unless the message already contains its detail.
func NewModelsError(code ModelsErrorCode, message string, cause error) *ModelsError {
	return &ModelsError{Code: code, msg: withCauseDetail(message, cause), Cause: cause}
}

// Error implements error.
func (e *ModelsError) Error() string { return e.msg }

// Unwrap exposes the underlying cause for errors.Is/errors.As.
func (e *ModelsError) Unwrap() error { return e.Cause }

// Name reports the stable error name, matching upstream `error.name`.
func (e *ModelsError) Name() string { return "ModelsError" }

// withCauseDetail appends the rendered cause to message unless it is empty or
// already present.
func withCauseDetail(message string, cause error) string {
	if cause == nil {
		return message
	}
	detail := strings.TrimSpace(FormatThrownValue(cause))
	if detail == "" || strings.Contains(message, detail) {
		return message
	}
	return fmt.Sprintf("%s: %s", message, detail)
}
