package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// Content block type discriminators (MCP wire values).
const (
	ContentBlockText         = "text"
	ContentBlockImage        = "image"
	ContentBlockAudio        = "audio"
	ContentBlockResourceLink = "resource_link"
	ContentBlockResource     = "resource"
)

// ContentAnnotations carries optional content metadata.
type ContentAnnotations struct {
	Audience     []string `json:"audience,omitempty"`
	Priority     *float64 `json:"priority,omitempty"`
	LastModified string   `json:"lastModified,omitempty"`
}

// EmbeddedResource is the `resource` member of an embedded resource block.
//
// Text and Blob are pointers so a resource that carries an empty string is
// still distinguishable from one that carries the other representation.
type EmbeddedResource struct {
	URI      string         `json:"uri"`
	MimeType string         `json:"mimeType,omitempty"`
	Text     *string        `json:"text,omitempty"`
	Blob     *string        `json:"blob,omitempty"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

// ResourceContents is one entry of a `resources/read` result.
type ResourceContents struct {
	URI      string         `json:"uri"`
	MimeType string         `json:"mimeType,omitempty"`
	Text     *string        `json:"text,omitempty"`
	Blob     *string        `json:"blob,omitempty"`
	Meta     map[string]any `json:"_meta,omitempty"`
}

// ContentBlock is a single tool-result content block. Only the fields relevant
// to Type are populated.
type ContentBlock struct {
	Type        string              `json:"type"`
	Text        string              `json:"text,omitempty"`
	Data        string              `json:"data,omitempty"`
	MimeType    string              `json:"mimeType,omitempty"`
	URI         string              `json:"uri,omitempty"`
	Name        string              `json:"name,omitempty"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Size        *int64              `json:"size,omitempty"`
	Resource    *EmbeddedResource   `json:"resource,omitempty"`
	Annotations *ContentAnnotations `json:"annotations,omitempty"`
	Meta        map[string]any      `json:"_meta,omitempty"`
}

// CallToolResult is the `tools/call` response payload.
//
// StructuredContent keeps the raw JSON so a Codemode consumer can read the
// structured value while ToAIContent renders a text fallback for models.
type CallToolResult struct {
	Content           []ContentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	Meta              map[string]any  `json:"_meta,omitempty"`
}

// ToAIContent converts a tool result to the text and image blocks a model API
// accepts.
//
// Text and images pass through; embedded text resources become text and
// embedded image resources become images. Audio, resource links and binary
// resources become a short, readable placeholder. A result without content
// blocks but with structured content becomes its indented JSON, because
// servers should mirror structured results as text but do not always do so.
func ToAIContent(result CallToolResult) []aitypes.ContentBlock {
	content := make([]aitypes.ContentBlock, 0, len(result.Content))
	for _, block := range result.Content {
		content = append(content, blockToAIContent(block))
	}
	if len(content) == 0 && len(result.StructuredContent) > 0 && string(result.StructuredContent) != "null" {
		content = append(content, aitypes.TextBlock(indentJSON(result.StructuredContent)))
	}
	return content
}

func blockToAIContent(block ContentBlock) aitypes.ContentBlock {
	switch block.Type {
	case ContentBlockText:
		return aitypes.TextBlock(block.Text)
	case ContentBlockImage:
		return aitypes.ImageBlock(block.Data, block.MimeType)
	case ContentBlockAudio:
		return aitypes.TextBlock(fmt.Sprintf("[audio %s omitted]", block.MimeType))
	case ContentBlockResourceLink:
		return aitypes.TextBlock(fmt.Sprintf("%s: %s", block.Name, block.URI))
	case ContentBlockResource:
		resource := block.Resource
		if resource == nil {
			return aitypes.TextBlock("[binary resource  (unknown type) omitted]")
		}
		if resource.Text != nil {
			return aitypes.TextBlock(*resource.Text)
		}
		if strings.HasPrefix(resource.MimeType, "image/") && resource.Blob != nil {
			return aitypes.ImageBlock(*resource.Blob, resource.MimeType)
		}
		mime := resource.MimeType
		if mime == "" {
			mime = "unknown type"
		}
		return aitypes.TextBlock(fmt.Sprintf("[binary resource %s (%s) omitted]", resource.URI, mime))
	default:
		return aitypes.TextBlock(fmt.Sprintf("[unsupported MCP content %s]", block.Type))
	}
}

// indentJSON renders raw JSON with two-space indentation, matching the upstream
// `JSON.stringify(value, null, 2)` fallback while preserving numbers exactly.
func indentJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}
