package durable

import "fmt"

// EntryKind is a typed entry-kind token. Upstream Pi's `defineEntry<D>(kind)`
// carries a compile-time payload type on the token; Go has no erased generic
// payload here, so the token records the kind and narrows EntryRecord values by
// that kind through Is. Typed payload helpers are added by the harness layer.
type EntryKind struct {
	Kind string
}

// DefineEntry creates a typed entry-kind token. The kind must be a non-empty
// string. It is the Go counterpart of upstream `defineEntry`.
func DefineEntry(kind string) (*EntryKind, error) {
	if kind == "" {
		return nil, fmt.Errorf("Entry kind must be a non-empty string")
	}
	return &EntryKind{Kind: kind}, nil
}

// Is reports whether the entry is present and has this token's kind. It is the
// Go counterpart of the upstream `Entry<D>.is` narrowing guard; callers that
// need payload typing should decode Data with their own JSON shape.
func (e *EntryKind) Is(entry *EntryRecord) bool {
	return entry != nil && e != nil && entry.Kind == e.Kind
}

// IsValue is the value-record form of Is for callers that already dereferenced
// an entry.
func (e *EntryKind) IsValue(entry EntryRecord) bool {
	return e != nil && entry.Kind == e.Kind
}

func mustEntry(kind string) *EntryKind {
	token, err := DefineEntry(kind)
	if err != nil {
		panic(err)
	}
	return token
}

// Built-in entry kinds. Their on-disk `kind` strings are the public protocol.
var (
	// UserEntry is user input; Model is [UserMessage]. Written by submissions.
	UserEntry = mustEntry("pi.user")
	// AssistantEntry is a provider result with any stop reason; Model is
	// [AssistantMessage]. Written by generation.
	AssistantEntry = mustEntry("pi.assistant")
	// SystemEntry is a positional prompt and tool change; Model is
	// [SystemMessage] with empty content.
	SystemEntry = mustEntry("pi.system")
	// ToolResultEntry is a tool result; Model is [ToolResultMessage] ending with
	// the rendered diagnostics block and Data holds the structured diagnostics.
	ToolResultEntry = mustEntry("pi.tool-result")
	// ResetEntry starts a new context: always HeadSelf, with Model absent for a
	// plain reset or [UserMessage] carrying handoff text.
	ResetEntry = mustEntry("pi.reset")
	// CompactionEntry is a compaction summary; Model is [UserMessage] with the
	// wrapped summary and Head the first kept entry.
	CompactionEntry = mustEntry("pi.compaction")
)
