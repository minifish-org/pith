// This file is a Go port of packages/ai/src/utils/text.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

// sectionNames returns the section names in render order.
//
// Upstream iterates `Object.entries(message.sections ?? {})` in insertion
// order, which is semantic for Durable prompt replay: a section can be removed
// and re-added in a different position. The Go model is a map, so a decoded
// system message records the wire order in SectionOrder; that recorded order is
// honored first. Duplicate or stale order entries are ignored and any remaining
// map keys are appended in sorted order, so legacy map-only callers keep the
// deterministic sorted fallback.
func sectionNames(sections types.SystemSections, order []string) []string {
	return types.OrderSectionNames(sections, order)
}

// ContentText extracts and joins text blocks from message content.
//
// Content is `string | readonly Content[]` upstream. The Go signature accepts
// any of the in-memory content shapes the SDK uses: a plain string, a
// types.SystemContent/UserContent carrier, or a slice of content blocks. The
// optional separator defaults to "\n".
func ContentText(content any, separator ...string) string {
	sep := "\n"
	if len(separator) > 0 {
		sep = separator[0]
	}

	switch value := content.(type) {
	case nil:
		return ""
	case string:
		return value
	case types.SystemContent:
		if value.Structured {
			return joinTextContents(value.Blocks, sep)
		}
		return value.Text
	case types.UserContent:
		if value.Structured {
			return joinTextBlocks(value.Blocks, sep)
		}
		return value.Text
	case []types.TextContent:
		parts := make([]string, 0, len(value))
		for _, block := range value {
			parts = append(parts, block.Text)
		}
		return strings.Join(parts, sep)
	case []types.ContentBlock:
		return joinTextBlocks(value, sep)
	default:
		return ""
	}
}

func joinTextBlocks(blocks []types.ContentBlock, sep string) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == types.ContentTypeText && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, sep)
}

func joinTextContents(blocks []types.TextContent, sep string) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, sep)
}

// GetSystemMessageText renders a system message as a complete prompt: its
// content followed by its sections.
func GetSystemMessageText(message types.SystemMessage) string {
	parts := []string{ContentText(message.Content)}
	for _, name := range sectionNames(message.Sections, message.SectionOrder) {
		text := message.Sections[name]
		if text != nil {
			parts = append(parts, *text)
		}
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n\n")
}

// RenderSystemMessageUpdate renders a later system message for APIs that accept
// system messages mid-conversation. Section changes are framed by name so the
// model can relate them to the leading prompt. This framing is request-time
// only and may change between versions.
func RenderSystemMessageUpdate(message types.SystemMessage) string {
	parts := []string{}
	if text := ContentText(message.Content); len(text) > 0 {
		parts = append(parts, text)
	}
	for _, name := range sectionNames(message.Sections, message.SectionOrder) {
		value := message.Sections[name]
		if value == nil {
			parts = append(parts, `Removed system prompt section "`+name+`".`)
		} else {
			parts = append(parts, `Updated system prompt section "`+name+`":`+"\n\n"+*value)
		}
	}
	return strings.Join(parts, "\n\n")
}
