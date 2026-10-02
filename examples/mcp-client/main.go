// Command mcp-client exercises the native Go Model Context Protocol client
// end to end against an in-process MCP server.
//
// The client and server speak real JSON-RPC over an in-memory transport: no
// Node.js helper, no subprocess and no network are required, so the example
// builds and runs with CGO_ENABLED=0. It demonstrates initialize negotiation,
// tool discovery, a tool call and conversion of the MCP result into AI content
// blocks that a model provider can consume.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/minifish-org/pith/packages/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-client example:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clientTransport, serverTransport := mcp.CreateInMemoryTransportPair()
	if err := serverTransport.Start(); err != nil {
		return err
	}
	serverTransport.OnMessage(func(message *mcp.Message) {
		if !message.IsRequest() {
			return
		}
		// Answer on another goroutine, matching a real server.
		go answer(serverTransport, message)
	})

	client := mcp.NewClient(clientTransport, &mcp.ClientOptions{
		Name:           "pith-example",
		Version:        "1.0.0",
		RequestTimeout: 5 * time.Second,
	})
	defer client.Close(context.Background())

	if err := client.Connect(ctx); err != nil {
		return err
	}
	server := client.ServerInfo()
	fmt.Printf("connected to %s %s (protocol %s)\n", server.Name, server.Version, client.ProtocolVersion())

	tools, err := client.ListTools(ctx)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		fmt.Printf("tool %s: %s\n", tool.Name, tool.Description)
	}

	result, err := client.CallTool(ctx, "echo", map[string]any{"text": "hello from Go"})
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("tool reported an error result")
	}
	fmt.Println("structured content:", string(result.StructuredContent))
	fmt.Println("ai content:")
	for _, block := range mcp.ToAIContent(result) {
		if block.Text != nil {
			fmt.Println("  text:", block.Text.Text)
		}
	}
	return nil
}

// answer implements the server half of the protocol for the example.
func answer(transport *mcp.InMemoryTransport, message *mcp.Message) {
	var result any
	switch message.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": mcp.LatestProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "example-server", "version": "1.0.0"},
		}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{
			"name":        "echo",
			"description": "Return the supplied text.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		}}}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(message.Params, &params)
		text := fmt.Sprint(params.Arguments["text"])
		result = map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": text}},
			"structuredContent": map[string]any{"echo": text, "length": len(text)},
		}
	default:
		_ = transport.Send(mcp.NewErrorMessage(*message.ID, &mcp.ErrorObject{
			Code:    mcp.JSONRPCCodeMethodNotFound,
			Message: "method not found: " + message.Method,
		}))
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		encoded = []byte("{}")
	}
	_ = transport.Send(mcp.NewResultMessage(*message.ID, encoded))
}
