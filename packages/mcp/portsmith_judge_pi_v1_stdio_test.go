package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/minifish-org/pith/packages/mcp"
	"os"
	"testing"
	"time"
)

// The Go test binary is the fixture server; no Node or external MCP server.
func TestPiV1MCPHelperProcess(t *testing.T) {
	if os.Getenv("PITH_V1_MCP_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	scan := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scan.Scan() {
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scan.Bytes(), &r) != nil {
			os.Exit(2)
		}
		if len(r.ID) == 0 {
			continue
		}
		var result any
		switch r.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "go-helper", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "from Go child"}}}
		case "ping":
			result = map[string]any{}
		default:
			os.Exit(3)
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result}) != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}

func TestPortsmithJudgePiV1MCPStdio(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	transport := mcp.NewStdioTransport(executable, []string{"-test.run=^TestPiV1MCPHelperProcess$"}, &mcp.StdioOptions{Env: append(os.Environ(), "PITH_V1_MCP_HELPER=1")})
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "judge", Version: "1", RequestTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("stdio protocol failed %+v %v", tools, err)
	}
	if _, err = client.CallTool(ctx, "echo", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err = client.Close(closeCtx); err != nil {
		t.Fatal("stdio close did not reap child", err)
	}
}
