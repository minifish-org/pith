// MCP resources for the embedded SDK.
//
// This file ports packages/coding-agent/src/extensions/mcp/resources.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. It lists and reads the
// resources of connected servers and converts their contents to the provider
// content blocks the model accepts. Listing is best-effort: a server that does
// not implement resources is skipped rather than failing the connection.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"fmt"
	"sort"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/mcp"
)

// MCPResource is one resource of a connected server, namespaced by server.
type MCPResource struct {
	Server      string `json:"server"`
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Size        *int64 `json:"size,omitempty"`
}

// ListAllResources lists the resources of every connected server. A server that
// does not support resources is skipped.
func (r *MCPRuntime) ListAllResources(ctx context.Context) []MCPResource {
	r.mu.RLock()
	order := append([]string(nil), r.order...)
	r.mu.RUnlock()
	out := []MCPResource{}
	for _, server := range order {
		resources, err := r.ListResources(ctx, server)
		if err != nil {
			continue
		}
		for _, resource := range resources {
			out = append(out, MCPResource{
				Server:      server,
				URI:         resource.URI,
				Name:        resource.Name,
				Title:       resource.Title,
				Description: resource.Description,
				MimeType:    resource.MimeType,
				Size:        resource.Size,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Server != out[j].Server {
			return out[i].Server < out[j].Server
		}
		return out[i].URI < out[j].URI
	})
	return out
}

// ReadResource reads one resource from a connected server and returns the
// provider content blocks. Text and image resources keep their payload; binary
// blobs become a short placeholder.
func (r *MCPRuntime) ReadResource(ctx context.Context, server, uri string) ([]aitypes.ContentBlock, error) {
	client := r.clientFor(server)
	if client == nil {
		return nil, fmt.Errorf("server %s is not connected", server)
	}
	result, err := client.ReadResource(ctx, uri)
	if err != nil {
		return nil, err
	}
	return MCPResourceContentsToContent(result.Contents), nil
}

// MCPResourceContentsToContent converts resource contents to content blocks.
func MCPResourceContentsToContent(contents []mcp.ResourceContents) []aitypes.ContentBlock {
	blocks := make([]aitypes.ContentBlock, 0, len(contents))
	for _, content := range contents {
		switch {
		case content.Text != nil:
			blocks = append(blocks, aitypes.TextBlock(*content.Text))
		case content.Blob != nil:
			mimeType := content.MimeType
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			blocks = append(blocks, aitypes.TextBlock("["+mimeType+" resource omitted]"))
		default:
			blocks = append(blocks, aitypes.TextBlock(content.URI))
		}
	}
	return blocks
}
