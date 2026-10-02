// MCP runtime for the embedded SDK.
//
// This file ports the connection/tool/resource lifecycle of
// packages/coding-agent/src/extensions/mcp/index.ts, runtime.ts and tools.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe onto the accepted
// packages/mcp client. It owns one client per configured server, namespaces the
// server tools as `mcp__<server>__<tool>`, applies the configured exposure,
// forwards notifications and reports partial failures as diagnostics instead of
// failing the whole load.
//
// Configuration is caller-supplied. A caller may inject a transport factory
// (used by offline tests) or rely on the stdio/HTTP transports.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/mcp"
)

// MCPTransportFactory builds the transport for a config. It is the injection
// point for offline tests.
type MCPTransportFactory func(config MCPServerConfig) (mcp.Transport, error)

// MCPRuntimeOptions configures an MCPRuntime.
type MCPRuntimeOptions struct {
	ClientName     string
	ClientVersion  string
	RequestTimeout time.Duration
	// TransportFactory overrides transport construction. When nil the runtime
	// uses the stdio/HTTP transports.
	TransportFactory MCPTransportFactory
	// AuthProvider supplies a bearer token provider for an HTTP server.
	AuthProvider func(config MCPServerConfig) mcp.AuthProvider
	// OnNotification receives server notifications.
	OnNotification func(server, method string, params any)
	// OnError receives transport errors.
	OnError func(server string, err error)
}

// MCPDiagnostic is one partial failure or setup warning.
type MCPDiagnostic struct {
	Server  string `json:"server,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Error implements the error interface so diagnostics can be logged as errors.
func (d MCPDiagnostic) Error() string {
	if d.Server == "" {
		return d.Type + ": " + d.Message
	}
	return d.Server + ": " + d.Type + ": " + d.Message
}

// MCPToolDefinition is one resolved server tool.
type MCPToolDefinition struct {
	Server         string
	Name           string
	NamespacedName string
	Description    string
	Exposure       string
	InputSchema    json.RawMessage
	OutputSchema   json.RawMessage
}

// MCPServerStatus is the connection state of one server.
type MCPServerStatus struct {
	Name       string `json:"name"`
	Connected  bool   `json:"connected"`
	ToolCount  int    `json:"toolCount"`
	ResourceCt int    `json:"resourceCount"`
	Error      string `json:"error,omitempty"`
}

// MCPRuntime owns the MCP clients of one session.
type MCPRuntime struct {
	mu      sync.RWMutex
	options MCPRuntimeOptions

	configs  map[string]MCPServerConfig
	order    []string
	clients  map[string]*mcp.Client
	statuses map[string]MCPServerStatus
	tools    []MCPToolDefinition
	unsubs   map[string][]func()
	closed   bool
}

// NewMCPRuntime builds an empty runtime.
func NewMCPRuntime(options MCPRuntimeOptions) *MCPRuntime {
	return &MCPRuntime{
		options:  options,
		configs:  map[string]MCPServerConfig{},
		clients:  map[string]*mcp.Client{},
		statuses: map[string]MCPServerStatus{},
		unsubs:   map[string][]func(){},
	}
}

// Load validates and connects the supplied servers, replacing any previous
// configuration. Partial failures are reported as diagnostics and do not abort
// the remaining servers.
func (r *MCPRuntime) Load(ctx context.Context, configs []MCPServerConfig) []MCPDiagnostic {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return []MCPDiagnostic{{Type: "runtime", Message: "MCP runtime is closed"}}
	}
	r.mu.Unlock()

	r.Close(context.Background())

	diagnostics := make([]MCPDiagnostic, 0)
	for _, config := range configs {
		if err := ValidateMCPServerConfig(config); err != nil {
			diagnostics = append(diagnostics, MCPDiagnostic{Server: config.Name, Type: "config", Message: err.Error()})
			continue
		}
		if !config.MCPEnabled() {
			continue
		}
		if err := r.connect(ctx, config); err != nil {
			diagnostics = append(diagnostics, MCPDiagnostic{Server: config.Name, Type: "connect", Message: err.Error()})
		}
	}
	return diagnostics
}

func (r *MCPRuntime) connect(ctx context.Context, config MCPServerConfig) error {
	transport, err := r.transportFor(config)
	if err != nil {
		return err
	}
	options := &mcp.ClientOptions{
		Name:           firstNonEmpty(r.options.ClientName, "pith"),
		Version:        firstNonEmpty(r.options.ClientVersion, "1"),
		RequestTimeout: r.options.RequestTimeout,
	}
	client := mcp.NewClient(transport, options)

	r.mu.Lock()
	r.configs[config.Name] = config
	r.order = append(r.order, config.Name)
	r.clients[config.Name] = client
	r.statuses[config.Name] = MCPServerStatus{Name: config.Name}
	serverName := config.Name
	if r.options.OnNotification != nil {
		for _, method := range mcpNotificationMethods {
			method := method
			r.unsubs[serverName] = append(r.unsubs[serverName], client.OnNotification(method, func(params any) {
				r.Notify(serverName, method, params)
			}))
		}
	}
	if r.options.OnError != nil {
		r.unsubs[serverName] = append(r.unsubs[serverName], client.OnError(func(err error) {
			r.options.OnError(serverName, err)
		}))
	}
	r.mu.Unlock()

	if err := client.Connect(ctx); err != nil {
		r.markStatus(serverName, func(status *MCPServerStatus) { status.Error = err.Error() })
		return err
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		r.markStatus(serverName, func(status *MCPServerStatus) { status.Error = err.Error() })
		return err
	}

	resourceCount := 0
	if resources, err := client.ListResources(ctx); err == nil {
		resourceCount = len(resources)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = client.Close(context.Background())
		return errors.New("MCP runtime is closed")
	}
	for _, tool := range tools {
		exposure := MCPExposureForTool(config, tool.Name)
		if exposure == MCPExposureHidden {
			continue
		}
		input, _ := json.Marshal(tool.InputSchema)
		var output json.RawMessage
		if tool.OutputSchema != nil {
			output, _ = json.Marshal(tool.OutputSchema)
		}
		r.tools = append(r.tools, MCPToolDefinition{
			Server:         config.Name,
			Name:           tool.Name,
			NamespacedName: MCPNamespace(config.Name) + "__" + tool.Name,
			Description:    tool.Description,
			Exposure:       exposure,
			InputSchema:    input,
			OutputSchema:   output,
		})
	}
	r.statuses[serverName] = MCPServerStatus{Name: serverName, Connected: true, ToolCount: len(tools), ResourceCt: resourceCount}
	r.mu.Unlock()
	return nil
}

func (r *MCPRuntime) transportFor(config MCPServerConfig) (mcp.Transport, error) {
	if r.options.TransportFactory != nil {
		return r.options.TransportFactory(config)
	}
	switch config.Type {
	case "", "stdio":
		env := []string{}
		keys := make([]string, 0, len(config.Env))
		for key := range config.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			env = append(env, key+"="+config.Env[key])
		}
		return mcp.NewStdioTransport(config.Command, config.Args, &mcp.StdioOptions{Env: env, Cwd: config.Cwd}), nil
	case "http":
		headers := map[string][]string{}
		for key, value := range config.Headers {
			headers[key] = []string{value}
		}
		httpOptions := &mcp.HTTPOptions{Headers: headers}
		if r.options.AuthProvider != nil {
			httpOptions.AuthProvider = r.options.AuthProvider(config)
		}
		return mcp.NewStreamableHTTPTransport(config.URL, httpOptions), nil
	default:
		return nil, fmt.Errorf("unsupported transport type %q", config.Type)
	}
}

func (r *MCPRuntime) markStatus(server string, mutate func(*MCPServerStatus)) {
	r.mu.Lock()
	status := r.statuses[server]
	mutate(&status)
	status.Name = server
	r.statuses[server] = status
	r.mu.Unlock()
}

// Tools returns a copy of the resolved server tools in load order.
func (r *MCPRuntime) Tools() []MCPToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]MCPToolDefinition(nil), r.tools...)
}

// Statuses returns the connection state of every configured server.
func (r *MCPRuntime) Statuses() []MCPServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MCPServerStatus, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.statuses[name])
	}
	return out
}

// ToolDefinitions adapts the server tools to the SDK tool surface, honouring
// the configured exposure. A tool from a server that is not connected is
// omitted.
func (r *MCPRuntime) ToolDefinitions() []ToolDefinition {
	exported := r.Tools()
	definitions := make([]ToolDefinition, 0, len(exported))
	for _, tool := range exported {
		tool := tool
		client := r.clientFor(tool.Server)
		if client == nil {
			continue
		}
		definitions = append(definitions, ToolDefinition{
			Name:         tool.NamespacedName,
			Description:  tool.Description,
			Parameters:   tool.InputSchema,
			OutputSchema: tool.OutputSchema,
			Exposure:     tool.Exposure,
			Execute: func(ctx context.Context, arguments json.RawMessage) (ToolResult, error) {
				return callMCPTool(ctx, client, tool, arguments)
			},
		})
	}
	return definitions
}

func (r *MCPRuntime) clientFor(server string) *mcp.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clients[server]
}

// callMCPTool invokes one server tool and converts its result. Structured
// content is preserved separately from the model-facing content.
func callMCPTool(ctx context.Context, client *mcp.Client, tool MCPToolDefinition, arguments json.RawMessage) (ToolResult, error) {
	if client == nil {
		return ToolResult{}, fmt.Errorf("server %s is not connected", tool.Server)
	}
	var args map[string]any
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return ToolResult{}, fmt.Errorf("mcp tool arguments: %w", err)
		}
	}
	result, err := client.CallTool(ctx, tool.Name, args)
	if err != nil {
		return ToolResult{}, err
	}
	content := mcp.ToAIContent(result)
	details, _ := json.Marshal(map[string]any{"server": tool.Server, "tool": tool.Name})
	converted := ToolResult{
		Content:           content,
		Details:           details,
		StructuredContent: result.StructuredContent,
		IsError:           result.IsError,
	}
	return converted, nil
}

func detailsFromMCP(server, tool string) map[string]any {
	return map[string]any{"server": server, "tool": tool}
}

// ListResources lists the resources of one connected server.
func (r *MCPRuntime) ListResources(ctx context.Context, server string) ([]mcp.Resource, error) {
	client := r.clientFor(server)
	if client == nil {
		return nil, fmt.Errorf("server %s is not connected", server)
	}
	return client.ListResources(ctx)
}

// Restart reconnects a single server with its stored configuration. It returns
// the diagnostics for that server.
func (r *MCPRuntime) Restart(ctx context.Context, server string) []MCPDiagnostic {
	r.mu.Lock()
	config, ok := r.configs[server]
	client := r.clients[server]
	unsubs := r.unsubs[server]
	r.mu.Unlock()
	if !ok {
		return []MCPDiagnostic{{Server: server, Type: "restart", Message: "server is not configured"}}
	}
	for _, unsubscribe := range unsubs {
		unsubscribe()
	}
	if client != nil {
		_ = client.Close(context.Background())
	}
	r.mu.Lock()
	delete(r.clients, server)
	delete(r.statuses, server)
	delete(r.unsubs, server)
	filtered := r.tools[:0]
	for _, tool := range r.tools {
		if tool.Server != server {
			filtered = append(filtered, tool)
		}
	}
	r.tools = filtered
	r.mu.Unlock()

	if err := r.connect(ctx, config); err != nil {
		return []MCPDiagnostic{{Server: server, Type: "connect", Message: err.Error()}}
	}
	return nil
}

// Notify forwards a server notification. It is wired by the client's
// notification subscriptions; this helper exists for callers that own the
// callback.
func (r *MCPRuntime) Notify(server, method string, params any) {
	if r.options.OnNotification != nil {
		r.options.OnNotification(server, method, params)
	}
}

// Close closes every client and cancels in-flight requests. It is idempotent.
func (r *MCPRuntime) Close(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	clients := make([]*mcp.Client, 0, len(r.clients))
	for _, client := range r.clients {
		clients = append(clients, client)
	}
	unsubs := r.unsubs
	r.clients = map[string]*mcp.Client{}
	r.unsubs = map[string][]func(){}
	r.tools = nil
	r.configs = map[string]MCPServerConfig{}
	r.order = nil
	r.statuses = map[string]MCPServerStatus{}
	r.mu.Unlock()

	for _, list := range unsubs {
		for _, unsubscribe := range list {
			unsubscribe()
		}
	}
	for _, client := range clients {
		_ = client.Close(ctx)
	}
}

// MarkClosed prevents further loads. It does not close clients; call Close.
func (r *MCPRuntime) MarkClosed() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// mcpNotificationMethods are the server notification methods the runtime
// forwards to OnNotification.
var mcpNotificationMethods = []string{
	"notifications/message",
	"notifications/resources/list_changed",
	"notifications/resources/updated",
	"notifications/tools/list_changed",
	"notifications/prompts/list_changed",
}

// MCPContentText renders the text blocks of an MCP result for diagnostics.
func MCPContentText(result mcp.CallToolResult) string {
	parts := []string{}
	for _, block := range mcp.ToAIContent(result) {
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
