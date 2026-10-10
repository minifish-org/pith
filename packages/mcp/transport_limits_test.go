package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/mcp"
)

type countedReader struct {
	io.Reader
	read int
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestMCPRejectsUnterminatedOversizeSSEBeforeReadingItAll(t *testing.T) {
	r := &countedReader{Reader: strings.NewReader("data: " + strings.Repeat("x", 1<<20))}
	err := mcp.ConsumeSSEStream(r, mcp.SSEStreamOptions{MaxEventBytes: 1024})
	if err == nil || r.read > 8192 {
		t.Fatalf("oversize SSE read %d bytes before rejecting: %v", r.read, err)
	}
}

func TestMCPTransportAcceptsMessagesAboveFormerLimitAndHonorsOverride(t *testing.T) {
	if mcp.DefaultMaxMessageBytes != 128<<20 {
		t.Fatal("unexpected transport default")
	}
	text := strings.Repeat("x", 17<<20)
	seen := false
	err := mcp.ConsumeSSEStream(strings.NewReader("data: "+text+"\n\n"), mcp.SSEStreamOptions{OnEvent: func(event mcp.SseEvent) { seen = event.Data == text }})
	if err != nil || !seen {
		t.Fatalf("large SSE event = %v, delivered=%v", err, seen)
	}
	if err := mcp.ConsumeSSEStream(strings.NewReader("data: "+strings.Repeat("x", 4096)+"\n\n"), mcp.SSEStreamOptions{MaxEventBytes: 1024}); err == nil {
		t.Fatal("host message limit ignored")
	}
}

func TestMCPCallToolAutomaticallyRequestsProgressAndRenews(t *testing.T) {
	transport, server := newSelfServer(t)
	server.setHandler("initialize", func(*mcp.Message) (any, error) {
		return map[string]any{"protocolVersion": mcp.LatestProtocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}, nil
	})
	server.setHandler("tools/call", func(message *mcp.Message) (any, error) {
		var params struct {
			Meta map[string]any `json:"_meta"`
		}
		_ = json.Unmarshal(message.Params, &params)
		if params.Meta["progressToken"] == nil {
			t.Error("plain CallTool did not request progress")
		}
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Millisecond)
			encoded, _ := json.Marshal(map[string]any{"progressToken": params.Meta["progressToken"], "progress": i + 1})
			_ = server.transport.Send(mcp.NewNotificationMessage("notifications/progress", encoded))
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}, nil
	})
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "fixture", Version: "1", RequestTimeout: 500 * time.Millisecond})
	defer client.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := client.CallTool(ctx, "slow", nil)
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "done" {
		t.Fatalf("automatic progress renewal = %+v %v", result, err)
	}
}

func TestMCPHTTPJSONHonorsMessageBudgetBeforeDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request mcp.Message
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.ID == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		result := any(map[string]any{"protocolVersion": mcp.LatestProtocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}})
		if request.Method == "tools/call" {
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 4096)}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	getStream := false
	client := mcp.NewClient(mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{MaxMessageBytes: 1024, OpenGetStream: &getStream}), nil)
	defer client.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "large", nil); err == nil || !strings.Contains(err.Error(), "MCP HTTP JSON response exceeds 1024 bytes") {
		t.Fatalf("JSON response cap = %v", err)
	}
}
