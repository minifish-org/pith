// Package codemode implements the native, CGO-free Codemode sandbox: JavaScript
// scripts run as async function bodies inside an embedded quickjs-wasi VM driven
// by wazero, with tool calls bridged over a JSON boundary.
package codemode

import (
	"context"
	"encoding/json"
	"time"
)

// Tool is one callable function exposed to scripts. Tools are called as
// tools.<identifier>(args); globals are called as top-level identifiers.
type Tool struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	// Spread is globals-only: the script's arguments are passed as a JSON array
	// instead of the first argument.
	Spread bool
	// Signature is globals-only: a TypeScript parameter list and return type used
	// for declaration rendering instead of the schemas.
	Signature string
	// Execute receives the script's argument after a JSON round trip and returns
	// a JSON-serializable result. A returned nil result crosses as undefined.
	//
	// Callbacks can run concurrently. The complete blocking callback runs in its
	// own goroutine, not behind a JavaScript-style ordered host handler, so
	// independent callbacks — overlapping globals or tool calls, and calls from
	// concurrent Sandbox.Execute invocations — may start and finish in either
	// order. Hosts must synchronize any shared state a callback touches.
	// Sequential await expresses dependency order and Promise.all permits overlap;
	// the sandbox schedules callbacks accordingly but never serializes them.
	//
	// The context is cancelled when the script finishes (including unawaited
	// calls), the execution times out, the caller aborts or the sandbox closes.
	// Returning from Execute cancels outstanding callback contexts, but it cannot
	// force a callback that ignores its context to stop. Do not rely on the
	// completion or side effects of an unawaited callback before Execute returns.
	Execute func(context.Context, json.RawMessage) (json.RawMessage, error)
}

// SandboxOptions configures a Sandbox.
type SandboxOptions struct {
	Tools []Tool
	// Globals are functions exposed as top-level identifiers instead of on
	// tools. They are not recorded in Result.Calls.
	Globals []Tool
	// Timeout is the default per-execution deadline. Zero uses the 300s default;
	// a negative value disables the deadline.
	Timeout time.Duration
	// MemoryLimitBytes caps QuickJS heap allocations. Zero leaves it unbounded
	// beyond the WASM address space.
	MemoryLimitBytes uint64
	// MaxStackBytes caps the QuickJS native stack. Zero uses 512 KiB.
	MaxStackBytes uint64
}

// ExecuteOptions overrides per-execution behavior.
type ExecuteOptions struct {
	// Store is the snapshot scripts read with load(). It is JSON-copied; the
	// caller's map is never mutated.
	Store map[string]json.RawMessage
	// Timeout overrides the sandbox default. Zero uses the sandbox default; a
	// negative value disables the deadline.
	Timeout time.Duration
}

// OutputItem is one item of script output, in the order produced. Text items
// carry Text; image items carry Data (base64) and MimeType.
type OutputItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// MarshalJSON emits the frozen union wire shape.
func (o OutputItem) MarshalJSON() ([]byte, error) {
	if o.Type == "image" {
		return json.Marshal(struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}{o.Type, o.Data, o.MimeType})
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{o.Type, o.Text})
}

// Call records one tool invocation. Globals are not recorded.
type Call struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	DurationMs float64 `json:"durationMs"`
}

// StoreWrites holds the keys a successful script changed with store(). Only
// successful executions report writes; failed runs leave it nil.
type StoreWrites struct {
	Set    map[string]json.RawMessage `json:"set"`
	Delete []string                   `json:"delete"`
}

// ExecutionError describes why an execution failed.
type ExecutionError struct {
	// Kind is one of "script", "timeout", "aborted" or "sandbox".
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

// Result is the outcome of Sandbox.Execute. Undefined script values leave Value nil.
type Result struct {
	OK          bool            `json:"ok"`
	Value       json.RawMessage `json:"value,omitempty"`
	Output      []OutputItem    `json:"output"`
	Calls       []Call          `json:"calls"`
	StoreWrites *StoreWrites    `json:"storeWrites,omitempty"`
	Error       *ExecutionError `json:"error,omitempty"`
}
