// Package utils provides AI-level conversions, validation helpers and the
// assistant message event stream.
//
// This file is a Go port of packages/ai/src/utils/event-stream.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// event-stream.ts owns the generic FifoQueue/EventStream (ported to
// packages/ai/utils/eventstream) plus the assistant specialization whose event
// and message types live in types.ts. Because those types are owned by
// packages/ai/types and the AI types package must stay free of a dependency on
// this package, the concrete assistant stream is declared in packages/ai/types
// and re-exported here. The dependency therefore runs one way only:
// packages/ai/utils -> packages/ai/types -> packages/ai/utils/eventstream.
package utils

import (
	"github.com/minifish-org/pith/packages/ai/types"
)

// AssistantMessageEventStream is the assistant message event stream. It is the
// same type as types.AssistantMessageEventStream; this alias keeps the
// upstream utils/event-stream.ts export surface without introducing a second,
// incompatible stream type.
type AssistantMessageEventStream = types.AssistantMessageEventStream

// NewAssistantMessageEventStream creates an assistant message event stream.
func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return types.NewAssistantMessageEventStream()
}

// CreateAssistantMessageEventStream is the factory function for
// AssistantMessageEventStream, for use by extensions.
func CreateAssistantMessageEventStream() *AssistantMessageEventStream {
	return types.CreateAssistantMessageEventStream()
}
