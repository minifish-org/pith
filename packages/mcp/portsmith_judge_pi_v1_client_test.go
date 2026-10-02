package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/minifish-org/pith/packages/mcp"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPortsmithJudgePiV1MCPHTTP(t *testing.T) {
	var mu sync.Mutex
	initialized := false
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  map[string]any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if request.JSONRPC != "2.0" {
			t.Error("invalid JSON-RPC version")
		}
		if request.Method != "initialize" {
			if r.Header.Get("MCP-Session-Id") != "session-fixture" || r.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
				t.Error("negotiated session headers lost")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch request.Method {
		case "initialize":
			w.Header().Set("MCP-Session-Id", "session-fixture")
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "notifications/initialized":
			mu.Lock()
			initialized = true
			mu.Unlock()
			w.WriteHeader(202)
			return
		case "tools/list":
			mu.Lock()
			ready := initialized
			mu.Unlock()
			if !ready {
				t.Error("tools requested before initialized notification")
			}
			if request.Params["cursor"] == "next" {
				result = map[string]any{"tools": []any{map[string]any{"name": "second", "inputSchema": map[string]any{"type": "object"}}}}
			} else {
				result = map[string]any{"nextCursor": "next", "tools": []any{map[string]any{"name": "first", "inputSchema": map[string]any{"type": "object"}}}}
			}
		case "tools/call":
			mu.Lock()
			calls++
			mu.Unlock()
			value := request.Params["arguments"]
			result = map[string]any{"content": []any{}, "structuredContent": value}
		default:
			t.Error("unexpected method", request.Method)
			w.WriteHeader(400)
			return
		}
		if len(request.ID) == 0 {
			t.Error("request missing correlation id")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{Client: server.Client()}), &mcp.ClientOptions{Name: "judge", Version: "1", RequestTimeout: time.Second})
	defer client.Close(context.Background())
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil || len(tools) != 2 || tools[0].Name != "first" || tools[1].Name != "second" {
		t.Fatalf("pagination failed: %+v %v", tools, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := client.CallTool(ctx, "first", map[string]any{"n": i})
			if err != nil {
				t.Error(err)
				return
			}
			var object map[string]any
			if err = json.Unmarshal(r.StructuredContent, &object); err != nil || object["n"] != float64(i) {
				t.Errorf("request correlation/structured content lost: %s %v", r.StructuredContent, err)
			}
		}(i)
	}
	wg.Wait()
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 12 {
		t.Errorf("unexpected request replay count %d", count)
	}
}

func TestPortsmithJudgePiV1MCPContent(t *testing.T) {
	for _, raw := range []string{
		`{"content":[],"structuredContent":{"answer":42}}`,
		`{"content":[{"type":"text","text":"hello"},{"type":"image","data":"aGVsbG8=","mimeType":"image/png"},{"type":"resource","resource":{"uri":"file:///x","text":"resource text"}},{"type":"audio","data":"YQ==","mimeType":"audio/wav"}]}`,
	} {
		var result mcp.CallToolResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		content := mcp.ToAIContent(result)
		encoded, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		var blocks []map[string]any
		if err = json.Unmarshal(encoded, &blocks); err != nil {
			t.Fatal(err)
		}
		if len(result.StructuredContent) > 0 {
			if len(blocks) != 1 {
				t.Fatalf("structured-only result missing fallback: %s", encoded)
			}
			var value map[string]any
			if err = json.Unmarshal([]byte(fmt.Sprint(blocks[0]["text"])), &value); err != nil || !reflect.DeepEqual(value, map[string]any{"answer": float64(42)}) {
				t.Fatal("structured output fallback not JSON", string(encoded))
			}
		}
		if len(result.StructuredContent) == 0 {
			if len(blocks) != 4 || blocks[0]["text"] != "hello" || blocks[1]["data"] != "aGVsbG8=" || blocks[2]["text"] != "resource text" || blocks[3]["text"] != "[audio audio/wav omitted]" {
				t.Fatal("content conversion differs from upstream", string(encoded))
			}
		}
	}
}
