package mcp_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/mcp"
)

// ---------------------------------------------------------------------------
// In-memory test server
// ---------------------------------------------------------------------------

type selfServer struct {
	transport *mcp.InMemoryTransport
	mu        sync.Mutex
	messages  []*mcp.Message
	handlers  map[string]func(*mcp.Message) (any, error)
}

func newSelfServer(t *testing.T) (*mcp.InMemoryTransport, *selfServer) {
	t.Helper()
	clientTransport, serverTransport := mcp.CreateInMemoryTransportPair()
	server := &selfServer{transport: serverTransport, handlers: map[string]func(*mcp.Message) (any, error){}}
	serverTransport.OnMessage(func(message *mcp.Message) {
		server.mu.Lock()
		server.messages = append(server.messages, message)
		handler := server.handlers[message.Method]
		server.mu.Unlock()
		if !message.IsRequest() {
			return
		}
		go func() {
			if handler == nil {
				_ = serverTransport.Send(mcp.NewErrorMessage(*message.ID, &mcp.ErrorObject{
					Code:    mcp.JSONRPCCodeMethodNotFound,
					Message: "not found",
				}))
				return
			}
			result, err := handler(message)
			if err != nil {
				var mcpErr *mcp.McpError
				code := mcp.JSONRPCCodeInternalError
				messageText := err.Error()
				data := any(nil)
				if ok := asMcpError(err, &mcpErr); ok {
					code = mcpErr.Code
					messageText = mcpErr.Message
					data = mcpErr.Data
				}
				object := &mcp.ErrorObject{Code: code, Message: messageText}
				if data != nil {
					if encoded, marshalErr := json.Marshal(data); marshalErr == nil {
						object.Data = encoded
					}
				}
				_ = serverTransport.Send(mcp.NewErrorMessage(*message.ID, object))
				return
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				encoded = []byte("{}")
			}
			_ = serverTransport.Send(mcp.NewResultMessage(*message.ID, encoded))
		}()
	})
	server.setHandler("initialize", func(*mcp.Message) (any, error) {
		return map[string]any{
			"protocolVersion": mcp.LatestProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "test-server", "version": "1.0.0"},
			"instructions":    "Use test tools.",
		}, nil
	})
	if err := serverTransport.Start(); err != nil {
		t.Fatal(err)
	}
	return clientTransport, server
}

func asMcpError(err error, target **mcp.McpError) bool {
	if mcpErr, ok := err.(*mcp.McpError); ok {
		*target = mcpErr
		return true
	}
	return false
}

func (s *selfServer) setHandler(method string, handler func(*mcp.Message) (any, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

func (s *selfServer) snapshot() []*mcp.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*mcp.Message{}, s.messages...)
}

func selfConnect(t *testing.T, options *mcp.ClientOptions) (*mcp.Client, *selfServer) {
	t.Helper()
	clientTransport, server := newSelfServer(t)
	if options == nil {
		options = &mcp.ClientOptions{Name: "test-client", Version: "2.0.0"}
	}
	client := mcp.NewClient(clientTransport, options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	return client, server
}

// ---------------------------------------------------------------------------
// Client behaviour
// ---------------------------------------------------------------------------

func TestSelfPiV1ClientInitializeAndPagination(t *testing.T) {
	client, server := selfConnect(t, nil)
	defer client.Close(context.Background())
	if client.ConnectionState() != mcp.ClientStateConnected {
		t.Fatalf("state = %s", client.ConnectionState())
	}
	if client.ProtocolVersion() != mcp.LatestProtocolVersion {
		t.Fatalf("protocol = %s", client.ProtocolVersion())
	}
	if client.ServerInfo() == nil || client.ServerInfo().Name != "test-server" {
		t.Fatalf("server info = %+v", client.ServerInfo())
	}
	server.setHandler("tools/list", func(message *mcp.Message) (any, error) {
		var params struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(message.Params, &params)
		if params.Cursor == "" {
			return map[string]any{
				"tools":      []any{map[string]any{"name": "search", "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": "page-2",
			}, nil
		}
		return map[string]any{
			"tools":      []any{map[string]any{"name": "read", "inputSchema": map[string]any{"type": "object"}}},
			"nextCursor": "",
		}, nil
	})
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 2 || tools[0].Name != "search" || tools[1].Name != "read" {
		t.Fatalf("tools = %+v err = %v", tools, err)
	}
}

func TestSelfPiV1ClientResources(t *testing.T) {
	client, server := selfConnect(t, nil)
	defer client.Close(context.Background())
	server.setHandler("resources/list", func(message *mcp.Message) (any, error) {
		return map[string]any{"resources": []any{map[string]any{"uri": "file:///a"}}}, nil
	})
	server.setHandler("resources/templates/list", func(*mcp.Message) (any, error) {
		return map[string]any{"resourceTemplates": []any{map[string]any{"uriTemplate": "repo://{x}"}}}, nil
	})
	server.setHandler("resources/read", func(message *mcp.Message) (any, error) {
		return map[string]any{"contents": []any{map[string]any{"uri": "file:///a", "text": "hello"}}}, nil
	})
	resources, err := client.ListResources(context.Background())
	if err != nil || len(resources) != 1 || resources[0].Name != "file:///a" {
		t.Fatalf("resources = %+v err = %v", resources, err)
	}
	templates, err := client.ListResourceTemplates(context.Background())
	if err != nil || len(templates) != 1 || templates[0].Name != "repo://{x}" {
		t.Fatalf("templates = %+v err = %v", templates, err)
	}
	result, err := client.ReadResource(context.Background(), "file:///a")
	if err != nil || len(result.Contents) != 1 || result.Contents[0].Text == nil || *result.Contents[0].Text != "hello" {
		t.Fatalf("read = %+v err = %v", result, err)
	}
	// Negative: contents without text or blob are rejected.
	server.setHandler("resources/read", func(*mcp.Message) (any, error) {
		return map[string]any{"contents": []any{map[string]any{"uri": "file:///a"}}}, nil
	})
	if _, err := client.ReadResource(context.Background(), "file:///a"); err == nil {
		t.Fatal("expected invalid contents error")
	}
}

func TestSelfPiV1ClientErrorsAndDefaults(t *testing.T) {
	client, server := selfConnect(t, nil)
	defer client.Close(context.Background())
	server.setHandler("tools/call", func(message *mcp.Message) (any, error) {
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(message.Params, &params)
		if params.Name == "fail" {
			return nil, mcp.NewMcpError(1234, "tool failed", map[string]any{"retryable": false})
		}
		if params.Name == "structured" {
			return map[string]any{"structuredContent": map[string]any{"ok": true}}, nil
		}
		if params.Name == "broken" {
			return map[string]any{"content": "not a list"}, nil
		}
		return map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": "ok"}},
			"structuredContent": map[string]any{"count": params.Arguments["count"]},
		}, nil
	})
	result, err := client.CallTool(context.Background(), "count", map[string]any{"count": 3})
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "ok" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if len(result.StructuredContent) == 0 {
		t.Fatal("structured content lost")
	}
	if _, err := client.CallTool(context.Background(), "fail", nil); err == nil {
		t.Fatal("expected error")
	} else if mcpErr, ok := err.(*mcp.McpError); !ok || mcpErr.Code != 1234 {
		t.Fatalf("error = %#v", err)
	}
	structured, err := client.CallTool(context.Background(), "structured", nil)
	if err != nil || structured.Content == nil || len(structured.Content) != 0 {
		t.Fatalf("structured-only result = %+v err = %v", structured, err)
	}
	if _, err := client.CallTool(context.Background(), "broken", nil); err == nil {
		t.Fatal("expected invalid result error")
	}
}

func TestSelfPiV1ClientProgressRenewsTimeout(t *testing.T) {
	client, server := selfConnect(t, nil)
	defer client.Close(context.Background())
	server.setHandler("tools/call", func(message *mcp.Message) (any, error) {
		var params struct {
			Meta map[string]any `json:"_meta"`
		}
		_ = json.Unmarshal(message.Params, &params)
		token := params.Meta["progressToken"]
		go func() {
			// Keep renewing the client timeout until the response is ready. The
			// 200ms cadence against a 1s timeout leaves ample slack under load.
			for i := 0; i < 40; i++ {
				time.Sleep(200 * time.Millisecond)
				encoded, _ := json.Marshal(map[string]any{"progressToken": token, "progress": i + 1, "total": 40})
				_ = server.transport.Send(mcp.NewNotificationMessage("notifications/progress", encoded))
			}
		}()
		time.Sleep(2500 * time.Millisecond)
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}, nil
	})
	progressed := make(chan mcp.ProgressNotification, 1)
	result, err := client.CallToolWithOptions(context.Background(), "slow", nil, &mcp.RequestOptions{
		Timeout:    1 * time.Second,
		OnProgress: func(p mcp.ProgressNotification) { progressed <- p },
	})
	if err != nil {
		t.Fatalf("progress renewal failed: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "done" {
		t.Fatalf("result = %+v", result)
	}
	select {
	case p := <-progressed:
		if p.Progress != 1 {
			t.Fatalf("progress = %+v", p)
		}
	default:
		t.Fatal("progress callback not invoked")
	}
}

func TestSelfPiV1ClientCancellation(t *testing.T) {
	client, server := selfConnect(t, nil)
	defer client.Close(context.Background())
	block := make(chan struct{})
	server.setHandler("tools/call", func(*mcp.Message) (any, error) {
		<-block
		return map[string]any{"content": []any{}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, "wait", nil)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected abort error")
	}
	time.Sleep(50 * time.Millisecond)
	var found bool
	for _, message := range server.snapshot() {
		if message.IsNotification() && message.Method == "notifications/cancelled" {
			found = true
		}
	}
	if !found {
		t.Fatal("cancelled notification not sent")
	}
	close(block)
}

func TestSelfPiV1ClientProtocolVersion(t *testing.T) {
	// A fresh client that rejects an unsupported version.
	clientTransport, unsupported := newSelfServer(t)
	unsupported.setHandler("initialize", func(*mcp.Message) (any, error) {
		return map[string]any{
			"protocolVersion": "1999-01-01",
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "ancient", "version": "1"},
		}, nil
	})
	rejected := mcp.NewClient(clientTransport, &mcp.ClientOptions{Name: "c", Version: "1"})
	if err := rejected.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("expected unsupported version error, got %v", err)
	}
	if rejected.ConnectionState() != mcp.ClientStateClosed {
		t.Fatalf("state = %s", rejected.ConnectionState())
	}
}

func TestSelfPiV1ClientCloseOnceAndRoots(t *testing.T) {
	clientTransport, server := newSelfServer(t)
	client := mcp.NewClient(clientTransport, &mcp.ClientOptions{
		Name:    "c",
		Version: "1",
		Roots:   []mcp.Root{{URI: "file:///workspace", Name: "workspace"}},
	})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := 0
	client.OnClose(func() { closed++ })
	changed := make(chan struct{}, 2)
	client.OnNotification("notifications/tools/list_changed", func(any) { changed <- struct{}{} })
	client.OnNotification("notifications/resources/list_changed", func(any) { changed <- struct{}{} })
	if err := server.transport.Send(mcp.NewRequestMessage(mcp.StringID("roots"), "roots/list", nil)); err != nil {
		t.Fatal(err)
	}
	if err := server.transport.Send(mcp.NewNotificationMessage("notifications/tools/list_changed", nil)); err != nil {
		t.Fatal(err)
	}
	if err := server.transport.Send(mcp.NewNotificationMessage("notifications/resources/list_changed", nil)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatal("notification not dispatched")
		}
	}
	time.Sleep(50 * time.Millisecond)
	var foundRoots bool
	for _, message := range server.snapshot() {
		if message.IsResponse() && message.ID != nil && *message.ID == mcp.StringID("roots") {
			foundRoots = true
		}
	}
	if !foundRoots {
		t.Fatal("roots/list was not answered")
	}
	_ = client.Close(context.Background())
	_ = client.Close(context.Background())
	if closed != 1 {
		t.Fatalf("close listeners fired %d times", closed)
	}
}

// ---------------------------------------------------------------------------
// Content conversion
// ---------------------------------------------------------------------------

func TestSelfPiV1ContentConversion(t *testing.T) {
	raw := `{"content":[{"type":"text","text":"hello"},{"type":"image","data":"aW1n","mimeType":"image/png"},{"type":"audio","data":"YXVk","mimeType":"audio/wav"},{"type":"resource_link","uri":"file:///a.txt","name":"a.txt"},{"type":"resource","resource":{"uri":"file:///b.txt","text":"inline"}},{"type":"resource","resource":{"uri":"file:///c.png","mimeType":"image/png","blob":"Yw=="}},{"type":"resource","resource":{"uri":"file:///d.bin","blob":"ZA=="}},{"type":"mystery","value":1}]}`
	var result mcp.CallToolResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	blocks := marshalBlocks(t, mcp.ToAIContent(result))
	if len(blocks) != 8 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	if blocks[0]["text"] != "hello" {
		t.Fatalf("text block = %+v", blocks[0])
	}
	if blocks[1]["data"] != "aW1n" || blocks[1]["mimeType"] != "image/png" {
		t.Fatalf("image block = %+v", blocks[1])
	}
	if blocks[2]["text"] != "[audio audio/wav omitted]" {
		t.Fatalf("audio block = %+v", blocks[2])
	}
	if blocks[3]["text"] != "a.txt: file:///a.txt" {
		t.Fatalf("link block = %+v", blocks[3])
	}
	if blocks[4]["text"] != "inline" {
		t.Fatalf("resource text = %+v", blocks[4])
	}
	if blocks[5]["data"] != "Yw==" || blocks[5]["mimeType"] != "image/png" {
		t.Fatalf("resource image = %+v", blocks[5])
	}
	if blocks[6]["text"] != "[binary resource file:///d.bin (unknown type) omitted]" {
		t.Fatalf("binary resource = %+v", blocks[6])
	}
	if blocks[7]["text"] != "[unsupported MCP content mystery]" {
		t.Fatalf("unsupported = %+v", blocks[7])
	}
}

func TestSelfPiV1ContentStructuredFallback(t *testing.T) {
	var result mcp.CallToolResult
	if err := json.Unmarshal([]byte(`{"content":[],"structuredContent":{"n":1}}`), &result); err != nil {
		t.Fatal(err)
	}
	blocks := marshalBlocks(t, mcp.ToAIContent(result))
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v", blocks)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(blocks[0]["text"])), &value); err != nil || value["n"] != float64(1) {
		t.Fatalf("structured fallback = %+v err = %v", blocks[0], err)
	}
	// With content present, structured content is not duplicated.
	if err := json.Unmarshal([]byte(`{"content":[{"type":"text","text":"n=1"}],"structuredContent":{"n":1}}`), &result); err != nil {
		t.Fatal(err)
	}
	blocks = marshalBlocks(t, mcp.ToAIContent(result))
	if len(blocks) != 1 || blocks[0]["text"] != "n=1" {
		t.Fatalf("structured with content = %+v", blocks)
	}
}

func marshalBlocks(t *testing.T, content any) []map[string]any {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(encoded, &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks
}

// ---------------------------------------------------------------------------
// SSE parser
// ---------------------------------------------------------------------------

func TestSelfPiV1SSEParser(t *testing.T) {
	stream := ": keepalive\r\nid: 7\r\ndata: {\"one\":\r\ndata: 1}\r\nretry: 25\r\n\r\n"
	var events []mcp.SseEvent
	var ids []string
	var retries []int
	err := mcp.ConsumeSSEStream(strings.NewReader(stream), mcp.SSEStreamOptions{
		OnEvent: func(event mcp.SseEvent) { events = append(events, event) },
		OnID:    func(id string) { ids = append(ids, id) },
		OnRetry: func(delay int) { retries = append(retries, delay) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "7" || events[0].Data != "{\"one\":\n1}" {
		t.Fatalf("events = %+v", events)
	}
	if len(ids) != 1 || ids[0] != "7" {
		t.Fatalf("ids = %+v", ids)
	}
	if len(retries) != 1 || retries[0] != 25 {
		t.Fatalf("retries = %+v", retries)
	}
}

func TestSelfPiV1SSEMaxEventBytes(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < 100; i++ {
		builder.WriteString("data: xxxxxxxxxxxxxxxx\n")
	}
	err := mcp.ConsumeSSEStream(strings.NewReader(builder.String()), mcp.SSEStreamOptions{MaxEventBytes: 256})
	if err == nil || !strings.Contains(err.Error(), "MCP SSE event exceeds 256 bytes") {
		t.Fatalf("expected size error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Streamable HTTP
// ---------------------------------------------------------------------------

type selfHTTPRequest struct {
	method  string
	headers http.Header
	message map[string]any
}

func selfProtocolHandler(requests *[]selfHTTPRequest, response http.ResponseWriter, request *http.Request, body map[string]any) {
	switch request.Method {
	case http.MethodGet:
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		response.WriteHeader(http.StatusOK)
		return
	}
	*requests = append(*requests, selfHTTPRequest{method: request.Method, headers: request.Header.Clone(), message: body})
	if body["id"] == nil {
		response.WriteHeader(http.StatusAccepted)
		return
	}
	if body["method"] == "initialize" {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Mcp-Session-Id", "session-1")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      body["id"],
			"result": map[string]any{
				"protocolVersion": mcp.LatestProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "http-fixture", "version": "1"},
			},
		})
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      body["id"],
		"result":  map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}},
	})
}

func TestSelfPiV1HTTPBasic(t *testing.T) {
	var mu sync.Mutex
	var requests []selfHTTPRequest
	var initialized bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if body["method"] != "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "session-1" || r.Header.Get("MCP-Protocol-Version") != mcp.LatestProtocolVersion {
				t.Error("negotiated headers missing")
			}
		}
		if body["method"] == "notifications/initialized" {
			initialized = true
		}
		if body["method"] == "tools/list" && !initialized {
			t.Error("tools/list before initialized")
		}
		mu.Unlock()
		selfProtocolHandler(&requests, w, r, body)
	}))
	defer server.Close()

	transport := mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{Client: server.Client()})
	httpTransport := transport.(*mcp.StreamableHTTPTransport)
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "c", Version: "1", RequestTimeout: time.Second})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if httpTransport.SessionID() != "session-1" {
		t.Fatalf("session = %s", httpTransport.SessionID())
	}
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools = %+v err = %v", tools, err)
	}
	_ = client.Close(context.Background())
}

func TestSelfPiV1HTTPAuthClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Www-Authenticate", `Bearer resource_metadata="https://example.com/meta"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("login required"))
	}))
	defer server.Close()
	client := mcp.NewClient(mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{Client: server.Client()}), &mcp.ClientOptions{Name: "c", Version: "1"})
	err := client.Connect(context.Background())
	authErr, ok := err.(*mcp.McpAuthRequiredError)
	if !ok {
		t.Fatalf("expected McpAuthRequiredError, got %T %v", err, err)
	}
	if authErr.Status != 401 || authErr.Body != "login required" {
		t.Fatalf("auth error = %+v", authErr)
	}
}

func TestSelfPiV1HTTPSessionExpired(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			if posts > 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		var requests []selfHTTPRequest
		selfProtocolHandler(&requests, w, r, body)
	}))
	defer server.Close()
	transport := mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{Client: server.Client(), OpenGetStream: boolPtr(false)})
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "c", Version: "1", RequestTimeout: time.Second})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := client.ListTools(context.Background())
	if _, ok := err.(*mcp.McpSessionExpiredError); !ok {
		t.Fatalf("expected session expired, got %T %v", err, err)
	}
	_ = client.Close(context.Background())
}

func TestSelfPiV1HTTPSSEResponseResume(t *testing.T) {
	var resumeHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Last-Event-ID") != "" {
			resumeHeaders = append(resumeHeaders, r.Header.Get("Last-Event-ID"))
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "id: 2\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}\n\n")
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		var requests []selfHTTPRequest
		switch body["method"] {
		case "tools/call":
			requests = append(requests, selfHTTPRequest{})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: 1\nretry: 5\ndata:\n\n")
		default:
			selfProtocolHandler(&requests, w, r, body)
		}
	}))
	defer server.Close()
	transport := mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{Client: server.Client(), OpenGetStream: boolPtr(false)})
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "c", Version: "1", RequestTimeout: 2 * time.Second})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := client.CallTool(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "resumed" {
		t.Fatalf("result = %+v", result)
	}
	if len(resumeHeaders) != 1 || resumeHeaders[0] != "1" {
		t.Fatalf("resume headers = %+v", resumeHeaders)
	}
	_ = client.Close(context.Background())
}

func TestSelfPiV1HTTPStreamWithoutAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["method"] == "tools/call" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": nothing here\n\n")
			return
		}
		var requests []selfHTTPRequest
		selfProtocolHandler(&requests, w, r, body)
	}))
	defer server.Close()
	transport := mcp.NewStreamableHTTPTransport(server.URL, &mcp.HTTPOptions{Client: server.Client(), OpenGetStream: boolPtr(false)})
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "c", Version: "1", RequestTimeout: 5 * time.Second})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := client.CallTool(context.Background(), "echo", nil)
	if err == nil || !strings.Contains(err.Error(), "MCP response stream failed") {
		t.Fatalf("expected stream failure, got %v", err)
	}
	_ = client.Close(context.Background())
}

func TestSelfPiV1HTTPNoSessionHeaderCrossOrigin(t *testing.T) {
	var leaked bool
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Header.Get("Mcp-Session-Id") != "" {
			leaked = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"` + r.Host + `/mcp","authorization_servers":[]}`))
	}))
	defer other.Close()
	// Discovery against another origin must never carry the session header.
	fetch := mcp.FetchFromClient(other.Client())
	_, _ = mcp.DiscoverProtectedResourceMetadata(context.Background(), other.URL+"/mcp", mcp.DiscoverProtectedResourceOptions{Fetch: fetch})
	mu.Lock()
	defer mu.Unlock()
	if leaked {
		t.Fatal("session header leaked to another origin")
	}
}

func boolPtr(value bool) *bool { return &value }

// ---------------------------------------------------------------------------
// stdio helpers and tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// stdio helpers and tests
// ---------------------------------------------------------------------------

// selfMCPStdioHelperSource is a small standalone MCP stdio server used as the
// child process in the stdio self-tests. Compiling it separately keeps the
// helper out of the test binary while staying fully offline.
const selfMCPStdioHelperSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "-grandchild":
		signal.Ignore(syscall.SIGTERM)
		for {
			time.Sleep(time.Hour)
		}
	case "-stubborn":
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(os.Args[0], "-grandchild")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		fmt.Fprintf(os.Stderr, "grandchild %d\n", child.Process.Pid)
		serve(true)
	default:
		serve(false)
	}
}

func serve(ignoreEOF bool) {
	fmt.Fprintln(os.Stderr, "stdio fixture ready")
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var message map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		id, ok := message["id"]
		if !ok || len(id) == 0 {
			continue
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		var result any
		switch method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "stdio-fixture", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			var params struct {
				Arguments map[string]any
			}
			_ = json.Unmarshal(message["params"], &params)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint(params.Arguments["text"])}}}
		case "ping":
			result = map[string]any{}
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "not found"}})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	if ignoreEOF {
		for {
			time.Sleep(time.Hour)
		}
	}
}
`

// buildSelfMCPHelper compiles selfMCPStdioHelperSource into a temporary binary
// and returns its path.
func buildSelfMCPHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "helper.go")
	if err := os.WriteFile(source, []byte(selfMCPStdioHelperSource), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "mcp-helper")
	build := exec.Command("go", "build", "-o", binary, source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build MCP stdio helper: %v\n%s", err, output)
	}
	return binary
}

func TestSelfPiV1Stdio(t *testing.T) {
	helper := buildSelfMCPHelper(t)
	var stderrMu sync.Mutex
	var stderrText strings.Builder
	transport := mcp.NewStdioTransport(helper, nil, &mcp.StdioOptions{
		OnStderr: func(chunk string) {
			stderrMu.Lock()
			stderrText.WriteString(chunk)
			stderrMu.Unlock()
		},
	})
	stdio := transport.(*mcp.StdioTransport)
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "c", Version: "1", RequestTimeout: time.Second})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v err = %v", tools, err)
	}
	result, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if stdio.PID() == 0 {
		t.Fatal("child pid lost")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdio.Stderr(), "stdio fixture ready") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stdio.Stderr(), "stdio fixture ready") {
		t.Fatalf("stderr = %q", stdio.Stderr())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if client.ConnectionState() != mcp.ClientStateClosed {
		t.Fatalf("state = %s", client.ConnectionState())
	}
}

func TestSelfPiV1StdioStubbornChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX only")
	}
	helper := buildSelfMCPHelper(t)
	transport := mcp.NewStdioTransport(helper, []string{"-stubborn"}, &mcp.StdioOptions{
		CloseTimeout: 100 * time.Millisecond,
	})
	stdio := transport.(*mcp.StdioTransport)
	client := mcp.NewClient(transport, &mcp.ClientOptions{Name: "c", Version: "1", RequestTimeout: time.Second})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	var grandchild int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && grandchild == 0 {
		matches := grandchildPattern.FindStringSubmatch(stdio.Stderr())
		if len(matches) == 2 {
			grandchild, _ = strconv.Atoi(matches[1])
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatalf("grandchild pid not found in %q", stdio.Stderr())
	}
	start := time.Now()
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("close took %s", elapsed)
	}
	alive := true
	for i := 0; i < 100 && alive; i++ {
		if err := syscall.Kill(grandchild, 0); err != nil {
			alive = false
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if alive {
		t.Fatalf("grandchild %d survived shutdown", grandchild)
	}
}

var grandchildPattern = regexp.MustCompile(`grandchild (\d+)`)

// ---------------------------------------------------------------------------
// OAuth
// ---------------------------------------------------------------------------

type selfOAuthProvider struct {
	mu               sync.Mutex
	redirectURL      string
	clientMetadata   mcp.OAuthClientMetadata
	client           *mcp.OAuthClientInformation
	tokens           *mcp.OAuthTokens
	verifier         string
	discovery        *mcp.OAuthDiscoveryState
	authorizationURL string
}

func newSelfOAuthProvider(redirectURL string) *selfOAuthProvider {
	return &selfOAuthProvider{
		redirectURL: redirectURL,
		clientMetadata: mcp.OAuthClientMetadata{
			RedirectURIs:            []string{redirectURL},
			ClientName:              "pi-mcp-test",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		},
	}
}

func (p *selfOAuthProvider) RedirectURL() string { return p.redirectURL }

func (p *selfOAuthProvider) ClientMetadata() mcp.OAuthClientMetadata { return p.clientMetadata }

func (p *selfOAuthProvider) ClientMetadataURL() string { return "" }

func (p *selfOAuthProvider) ClientInformation() (*mcp.OAuthClientInformation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client, nil
}

func (p *selfOAuthProvider) SaveClientInformation(information *mcp.OAuthClientInformation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client = information
	return nil
}

func (p *selfOAuthProvider) Tokens() (*mcp.OAuthTokens, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokens, nil
}

func (p *selfOAuthProvider) SaveTokens(tokens *mcp.OAuthTokens) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens = tokens
	return nil
}

func (p *selfOAuthProvider) RedirectToAuthorization(authorizationURL string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authorizationURL = authorizationURL
	return nil
}

func (p *selfOAuthProvider) SaveCodeVerifier(verifier string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.verifier = verifier
	return nil
}

func (p *selfOAuthProvider) CodeVerifier() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifier == "" {
		return "", fmt.Errorf("Missing code verifier")
	}
	return p.verifier, nil
}

func (p *selfOAuthProvider) State() (string, error) { return "expected-state", nil }

func (p *selfOAuthProvider) InvalidateCredentials(kind string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if kind == "all" || kind == "client" {
		p.client = nil
	}
	if kind == "all" || kind == "tokens" {
		p.tokens = nil
	}
	if kind == "all" || kind == "verifier" {
		p.verifier = ""
	}
	if kind == "all" || kind == "discovery" {
		p.discovery = nil
	}
	return nil
}

func (p *selfOAuthProvider) SaveDiscoveryState(state mcp.OAuthDiscoveryState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discovery = &state
	return nil
}

func (p *selfOAuthProvider) DiscoveryState() (*mcp.OAuthDiscoveryState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discovery, nil
}

func TestSelfPiV1OAuthDiscoverPKCEAndExchange(t *testing.T) {
	var expectedChallenge string
	origin := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}, "scopes_supported": []string{"org:read"}})
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, map[string]any{
				"issuer":                                base,
				"authorization_endpoint":                base + "/authorize",
				"token_endpoint":                        base + "/token",
				"registration_endpoint":                 base + "/register",
				"response_types_supported":              []string{"code"},
				"token_endpoint_auth_methods_supported": []string{"none"},
				"code_challenge_methods_supported":      []string{"S256"},
			})
		case "/register":
			var metadata map[string]any
			_ = json.NewDecoder(r.Body).Decode(&metadata)
			metadata["client_id"] = "test-client"
			metadata["client_secret"] = ""
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, metadata)
		case "/authorize":
			expectedChallenge = r.URL.Query().Get("code_challenge")
			redirect, _ := urlParse(r.URL.Query().Get("redirect_uri"))
			query := redirect.Query()
			query.Set("code", "test-code")
			query.Set("state", r.URL.Query().Get("state"))
			redirect.RawQuery = query.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			_ = r.ParseForm()
			challenge := sha256Base64URL(r.Form.Get("code_verifier"))
			if r.Form.Get("code") != "test-code" || challenge != expectedChallenge {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": "invalid_grant"})
				return
			}
			writeJSON(w, map[string]any{"access_token": "first-token", "refresh_token": "refresh-token", "token_type": "Bearer", "scope": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	origin = server.URL

	provider := newSelfOAuthProvider("http://127.0.0.1/callback")
	result, err := mcp.AuthorizeMcp(context.Background(), provider, mcp.OAuthFlowOptions{ServerURL: origin + "/mcp", Fetch: mcp.FetchFromClient(server.Client())})
	if err != nil {
		t.Fatal(err)
	}
	if result != mcp.OAuthFlowRedirect {
		t.Fatalf("result = %s", result)
	}
	authURL, err := urlParse(provider.authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("scope") != "org:read" || authURL.Query().Get("resource") != origin+"/mcp" {
		t.Fatalf("authorization URL = %s", provider.authorizationURL)
	}
	// Simulate the browser following the redirect.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(provider.authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	location, err := urlParse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	code := location.Query().Get("code")
	authorized, err := mcp.AuthorizeMcp(context.Background(), provider, mcp.OAuthFlowOptions{
		ServerURL:         origin + "/mcp",
		AuthorizationCode: code,
		Fetch:             mcp.FetchFromClient(server.Client()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized != mcp.OAuthFlowAuthorized {
		t.Fatalf("result = %s", authorized)
	}
	tokens, _ := provider.Tokens()
	if tokens == nil || tokens.AccessToken != "first-token" || tokens.Scope != "org:read" {
		t.Fatalf("tokens = %+v", tokens)
	}
}

func TestSelfPiV1OAuthConcurrentRefreshCoalescing(t *testing.T) {
	var mu sync.Mutex
	var grants []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server":
			writeJSON(w, map[string]any{
				"issuer":                   base,
				"authorization_endpoint":   base + "/authorize",
				"token_endpoint":           base + "/token",
				"response_types_supported": []string{"code"},
			})
		case "/token":
			_ = r.ParseForm()
			refreshToken := r.Form.Get("refresh_token")
			mu.Lock()
			grants = append(grants, refreshToken)
			mu.Unlock()
			if refreshToken != "r1" {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": "invalid_grant"})
				return
			}
			time.Sleep(20 * time.Millisecond)
			writeJSON(w, map[string]any{"access_token": "a2", "refresh_token": "r2", "token_type": "Bearer", "expires_in": 3600})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &mcp.MemoryOAuthStateStore{}
	provider := mcp.NewMcpOAuthProvider(mcp.McpOAuthProviderOptions{
		ServerURL:      server.URL + "/mcp",
		RedirectURL:    "http://127.0.0.1/callback",
		ClientMetadata: mcp.OAuthClientMetadata{ClientName: "test"},
		ClientID:       "client",
		Store:          store,
		OnRedirect:     func(string) error { return nil },
	})
	if err := provider.SaveTokens(&mcp.OAuthTokens{AccessToken: "a1", RefreshToken: "r1", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	auth := mcp.AdaptOAuthProvider(provider)
	handler := auth.(mcp.UnauthorizedHandler)
	unauthorized := func() mcp.UnauthorizedContext {
		return mcp.UnauthorizedContext{
			Response:  &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": []string{"Bearer"}}},
			ServerURL: mustParseURL(t, server.URL+"/mcp"),
			Fetch:     mcp.FetchFromClient(server.Client()),
			Token:     "a1",
			HasToken:  true,
		}
	}
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = handler.OnUnauthorized(context.Background(), unauthorized())
		}()
	}
	wait.Wait()
	_ = handler.OnUnauthorized(context.Background(), unauthorized())
	mu.Lock()
	defer mu.Unlock()
	if len(grants) != 1 || grants[0] != "r1" {
		t.Fatalf("grants = %+v", grants)
	}
	token, err := auth.Token(context.Background())
	if err != nil || token != "a2" {
		t.Fatalf("token = %q err = %v", token, err)
	}
	state, _ := store.Load()
	if state == nil || state.Tokens == nil || state.Tokens.RefreshToken != "r2" {
		t.Fatalf("state = %+v", state)
	}
	if state.TokensExpireAt == nil || *state.TokensExpireAt <= time.Now().UnixMilli()+3_500_000 {
		t.Fatalf("expiry = %+v", state.TokensExpireAt)
	}
}

func TestSelfPiV1OAuthStepUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			writeJSON(w, map[string]any{
				"issuer":                   base,
				"authorization_endpoint":   base + "/authorize",
				"token_endpoint":           base + "/token",
				"response_types_supported": []string{"code"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	provider := newSelfOAuthProvider("http://127.0.0.1/callback")
	provider.client = &mcp.OAuthClientInformation{ClientID: "client"}
	provider.tokens = &mcp.OAuthTokens{AccessToken: "a1", RefreshToken: "r1", TokenType: "Bearer", Scope: "repo read:org"}
	auth := mcp.AdaptOAuthProvider(provider)
	handler := auth.(mcp.UnauthorizedHandler)
	err := handler.OnUnauthorized(context.Background(), mcp.UnauthorizedContext{
		Response: &http.Response{StatusCode: 403, Header: http.Header{
			"Www-Authenticate": []string{`Bearer error="insufficient_scope", scope="repo admin"`},
		}},
		ServerURL: mustParseURL(t, server.URL+"/mcp"),
		Fetch:     mcp.FetchFromClient(server.Client()),
		Token:     "a1",
		HasToken:  true,
	})
	if _, ok := err.(*mcp.McpOAuthAuthorizationRequiredError); !ok {
		t.Fatalf("expected authorization required, got %T %v", err, err)
	}
	authURL, err := urlParse(provider.authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("scope") != "repo read:org admin" {
		t.Fatalf("scope = %s", authURL.Query().Get("scope"))
	}
	if provider.tokens.AccessToken != "a1" {
		t.Fatalf("tokens overwritten: %+v", provider.tokens)
	}
}

func TestSelfPiV1OAuthStoreScoping(t *testing.T) {
	store := &mcp.MemoryOAuthStateStore{}
	first := mcp.NewMcpOAuthProvider(mcp.McpOAuthProviderOptions{
		ServerURL:      "https://one.example/mcp",
		RedirectURL:    "http://127.0.0.1/callback",
		ClientMetadata: mcp.OAuthClientMetadata{ClientName: "test"},
		Store:          store,
		OnRedirect:     func(string) error { return nil },
	})
	if err := first.SaveTokens(&mcp.OAuthTokens{AccessToken: "secret", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if tokens, _ := first.Tokens(); tokens == nil || tokens.AccessToken != "secret" {
		t.Fatalf("tokens = %+v", tokens)
	}
	second := mcp.NewMcpOAuthProvider(mcp.McpOAuthProviderOptions{
		ServerURL:      "https://two.example/mcp",
		RedirectURL:    "http://127.0.0.1/callback",
		ClientMetadata: mcp.OAuthClientMetadata{ClientName: "test"},
		Store:          store,
		OnRedirect:     func(string) error { return nil },
	})
	if tokens, _ := second.Tokens(); tokens != nil {
		t.Fatalf("credentials leaked across servers: %+v", tokens)
	}
}

func TestSelfPiV1OAuthIssuerMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			writeJSON(w, map[string]any{
				"issuer":                   "https://attacker.example",
				"authorization_endpoint":   "http://" + r.Host + "/authorize",
				"token_endpoint":           "http://" + r.Host + "/token",
				"response_types_supported": []string{"code"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	_, err := mcp.DiscoverAuthorizationServerMetadata(context.Background(), server.URL, mcp.DiscoverAuthorizationServerOptions{
		Fetch: mcp.FetchFromClient(server.Client()),
	})
	if _, ok := err.(*mcp.OAuthIssuerMismatchError); !ok {
		t.Fatalf("expected issuer mismatch, got %T %v", err, err)
	}
}

func TestSelfPiV1OAuthConfiguredMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			writeJSON(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/idp/metadata.json":
			writeJSON(w, map[string]any{
				"issuer":                   "https://idp.example",
				"authorization_endpoint":   base + "/idp/authorize",
				"token_endpoint":           base + "/idp/token",
				"response_types_supported": []string{"code"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := newSelfOAuthProvider("http://127.0.0.1/callback")
	provider.client = &mcp.OAuthClientInformation{ClientID: "client"}
	metadataURL := mustParseURL(t, server.URL+"/idp/metadata.json")
	result, err := mcp.AuthorizeMcp(context.Background(), provider, mcp.OAuthFlowOptions{
		ServerURL:                      server.URL + "/mcp",
		AuthorizationServerMetadataURL: metadataURL,
		Fetch:                          mcp.FetchFromClient(server.Client()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != mcp.OAuthFlowRedirect {
		t.Fatalf("result = %s", result)
	}
	authURL, err := urlParse(provider.authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Path != "/idp/authorize" {
		t.Fatalf("authorization endpoint = %s", authURL.Path)
	}
	insecure := mustParseURL(t, "http://idp.example/metadata.json")
	_, err = mcp.AuthorizeMcp(context.Background(), provider, mcp.OAuthFlowOptions{
		ServerURL:                      server.URL + "/mcp",
		AuthorizationServerMetadataURL: insecure,
	})
	if _, ok := err.(*mcp.OAuthInsecureEndpointError); !ok {
		t.Fatalf("expected insecure endpoint error, got %T %v", err, err)
	}
}

func TestSelfPiV1OAuthISSValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/token":
			writeJSON(w, map[string]any{"access_token": "token", "token_type": "Bearer"})
		default:
			_ = base
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	origin := server.URL

	exchange := func(code string, iss string, hasISS bool, issSupported bool) (string, error) {
		provider := newSelfOAuthProvider("http://127.0.0.1/callback")
		provider.client = &mcp.OAuthClientInformation{ClientID: "client"}
		provider.verifier = "verifier"
		provider.discovery = &mcp.OAuthDiscoveryState{
			AuthorizationServerURL: origin,
			AuthorizationServerMetadata: &mcp.AuthorizationServerMetadata{
				Issuer:                 origin,
				AuthorizationEndpoint:  origin + "/authorize",
				TokenEndpoint:          origin + "/token",
				ResponseTypesSupported: []string{"code"},
				AuthorizationResponseISSParameterSupported: issSupported,
			},
		}
		return mcp.AuthorizeMcp(context.Background(), provider, mcp.OAuthFlowOptions{
			ServerURL:         origin + "/mcp",
			AuthorizationCode: code,
			ISS:               iss,
			HasISS:            hasISS,
			Fetch:             mcp.FetchFromClient(server.Client()),
		})
	}
	if _, err := exchange("other", "https://attacker.example", true, false); err == nil {
		t.Fatal("expected issuer mismatch for other issuer")
	}
	if _, err := exchange("missing", "", false, true); err == nil {
		t.Fatal("expected issuer mismatch for missing iss")
	}
	if result, err := exchange("matching", origin, true, true); err != nil || result != mcp.OAuthFlowAuthorized {
		t.Fatalf("matching = %s err = %v", result, err)
	}
	if result, err := exchange("omitted", "", false, false); err != nil || result != mcp.OAuthFlowAuthorized {
		t.Fatalf("omitted = %s err = %v", result, err)
	}
}

func TestSelfPiV1OAuthCallbackPages(t *testing.T) {
	callback, err := mcp.NewOAuthCallbackServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	pending := make(chan mcp.OAuthCallback, 1)
	failure := make(chan error, 1)
	go func() {
		result, err := callback.WaitForCallback("s1")
		if err != nil {
			failure <- err
			return
		}
		pending <- result
	}()
	response, err := http.Get(callback.RedirectURL + "?code=abc&state=s1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), "Authorization complete") {
		t.Fatalf("body = %s", body)
	}
	select {
	case result := <-pending:
		if result.Code != "abc" {
			t.Fatalf("callback = %+v", result)
		}
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("callback timed out")
	}

	// Custom renderPage and error responses.
	var mu sync.Mutex
	var pages []mcp.OAuthCallbackPage
	callback2, err := mcp.NewOAuthCallbackServer(&mcp.OAuthCallbackServerOptions{
		RenderPage: func(page mcp.OAuthCallbackPage) string {
			mu.Lock()
			pages = append(pages, page)
			mu.Unlock()
			return "<p>page</p>"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer callback2.Close()
	deniedDone := make(chan error, 1)
	go func() {
		_, err := callback2.WaitForCallback("s2")
		deniedDone <- err
	}()
	response, err = http.Get(callback2.RedirectURL + "?error=access_denied&error_description=Denied&state=s2")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if err := <-deniedDone; err == nil || !strings.Contains(err.Error(), "Denied") {
		t.Fatalf("expected denial, got %v", err)
	}
	mu.Lock()
	last := pages[len(pages)-1]
	mu.Unlock()
	if last.OK || last.Details != "Denied" {
		t.Fatalf("page = %+v", last)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func sha256Base64URL(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func urlParse(value string) (*url.URL, error) {
	return url.Parse(value)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
