package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// DefaultRequestTimeout is used when ClientOptions.RequestTimeout is zero.
const DefaultRequestTimeout = 30 * time.Second

// DefaultToolCallTimeout bounds inactivity during tools/call. Progress renews
// this window; an explicit client/request timeout or parent cancellation wins.
const DefaultToolCallTimeout = 10 * time.Minute

const maxListPages = 1000

// ClientStates.
const (
	ClientStateIdle       = "idle"
	ClientStateConnecting = "connecting"
	ClientStateConnected  = "connected"
	ClientStateClosed     = "closed"
)

// ClientOptions configures a Client.
type ClientOptions struct {
	// Name and Version identify the client to the server.
	Name    string
	Version string
	Title   string
	// Capabilities overrides the advertised client capabilities.
	Capabilities *ClientCapabilities
	// ProtocolVersion pins the initialize version. Defaults to the latest.
	ProtocolVersion string
	// RequestTimeout overrides all request windows. When unset, tools/call uses
	// DefaultToolCallTimeout; initialization/discovery use DefaultRequestTimeout.
	RequestTimeout time.Duration
	// Roots are advertised to the server and served from `roots/list`.
	Roots []Root
	// RootsFunc, when set, is called for each `roots/list` request.
	RootsFunc func(context.Context) ([]Root, error)
}

// RequestOptions customises a single request.
type RequestOptions struct {
	// Timeout overrides the client default for this request.
	Timeout time.Duration
	// OnProgress receives progress notifications. Its presence asks the server
	// to send them and renews the request timeout on each one.
	OnProgress func(ProgressNotification)
}

// RequestHandler answers a server-initiated request.
type RequestHandler func(ctx context.Context, params any) (any, error)

type rpcOutcome struct {
	result json.RawMessage
	err    error
}

type pendingRequest struct {
	id            JsonRpcId
	outcome       chan rpcOutcome
	timeout       time.Duration
	cancellable   bool
	onProgress    func(ProgressNotification)
	progressToken *JsonRpcId
	done          chan struct{}
	closeOnce     sync.Once

	mu       sync.Mutex
	timer    *time.Timer
	finished bool
}

func (p *pendingRequest) finish(out rpcOutcome) {
	select {
	case p.outcome <- out:
	default:
	}
}

func (p *pendingRequest) closeDone() {
	p.closeOnce.Do(func() { close(p.done) })
}

// Client is a protocol-level MCP client. It is safe for concurrent use.
type Client struct {
	options ClientOptions

	mu         sync.Mutex
	state      string
	transport  Transport
	nextID     uint64
	pending    map[JsonRpcId]*pendingRequest
	progress   map[JsonRpcId]JsonRpcId
	incoming   map[JsonRpcId]context.CancelFunc
	handlers   map[string]RequestHandler
	notifs     map[string]map[int64]func(any)
	errors     map[int64]func(error)
	closes     map[int64]func()
	listenerID int64
	disposers  []func()
	notified   bool

	serverInfo         *Implementation
	serverCapabilities *ServerCapabilities
	instructions       string
	protocolVersion    string
}

// NewClient builds a client for a transport. The transport is started by
// Connect.
func NewClient(transport Transport, options *ClientOptions) *Client {
	var resolved ClientOptions
	if options != nil {
		resolved = *options
	}
	client := &Client{
		options:   resolved,
		state:     ClientStateIdle,
		transport: transport,
		nextID:    1,
		pending:   map[JsonRpcId]*pendingRequest{},
		progress:  map[JsonRpcId]JsonRpcId{},
		incoming:  map[JsonRpcId]context.CancelFunc{},
		handlers:  map[string]RequestHandler{},
		notifs:    map[string]map[int64]func(any){},
		errors:    map[int64]func(error){},
		closes:    map[int64]func(){},
	}
	client.handlers["ping"] = func(context.Context, any) (any, error) { return map[string]any{}, nil }
	if len(resolved.Roots) > 0 || resolved.RootsFunc != nil {
		client.handlers["roots/list"] = client.handleRootsList
	}
	return client
}

func (c *Client) handleRootsList(ctx context.Context, _ any) (any, error) {
	roots := c.options.Roots
	if c.options.RootsFunc != nil {
		resolved, err := c.options.RootsFunc(ctx)
		if err != nil {
			return nil, err
		}
		roots = resolved
	}
	if roots == nil {
		roots = []Root{}
	}
	return RootsListResult{Roots: roots}, nil
}

// ConnectionState reports the current client state.
func (c *Client) ConnectionState() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// ServerInfo returns the initialized server information, if connected.
func (c *Client) ServerInfo() *Implementation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}

// ServerCapabilities returns the initialized server capabilities, if connected.
func (c *Client) ServerCapabilities() *ServerCapabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverCapabilities
}

// Instructions returns the server's initialization instructions, if any.
func (c *Client) Instructions() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instructions
}

// ProtocolVersion returns the negotiated protocol version, if connected.
func (c *Client) ProtocolVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protocolVersion
}

// Connect starts the transport, negotiates the protocol version and sends the
// initialized notification.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.state != ClientStateIdle {
		state := c.state
		c.mu.Unlock()
		return fmt.Errorf("cannot connect MCP client in %s state", state)
	}
	transport := c.transport
	c.state = ClientStateConnecting
	c.mu.Unlock()
	if transport == nil {
		c.markClosed(NewConnectionClosedError("MCP client has no transport"))
		return errors.New("MCP client has no transport")
	}

	c.mu.Lock()
	c.disposers = []func(){
		transport.OnMessage(c.handleMessage),
		transport.OnError(func(err error) { c.emitError(err) }),
		transport.OnClose(func() { c.handleTransportClose() }),
	}
	c.mu.Unlock()

	if err := transport.Start(); err != nil {
		_ = c.Close(context.Background())
		return err
	}

	capabilities := ClientCapabilities{}
	if c.options.Capabilities != nil {
		capabilities = *c.options.Capabilities
	}
	if (len(c.options.Roots) > 0 || c.options.RootsFunc != nil) && capabilities.Roots == nil {
		capabilities.Roots = &RootsCapability{}
	}
	version := c.options.ProtocolVersion
	if version == "" {
		version = LatestProtocolVersion
	}
	clientInfo := map[string]any{"name": c.options.Name, "version": c.options.Version}
	if c.options.Title != "" {
		clientInfo["title"] = c.options.Title
	}
	raw, err := c.requestInternal(ctx, "initialize", map[string]any{
		"protocolVersion": version,
		"capabilities":    capabilities,
		"clientInfo":      clientInfo,
	}, nil, true)
	if err != nil {
		_ = c.Close(context.Background())
		return err
	}
	result, err := validateInitializeResult(raw)
	if err != nil {
		_ = c.Close(context.Background())
		return err
	}
	if !IsSupportedProtocolVersion(result.ProtocolVersion) {
		_ = c.Close(context.Background())
		return fmt.Errorf("MCP server selected unsupported protocol version %s", result.ProtocolVersion)
	}
	c.mu.Lock()
	c.protocolVersion = result.ProtocolVersion
	c.serverInfo = &result.ServerInfo
	c.serverCapabilities = &result.Capabilities
	c.instructions = result.Instructions
	c.mu.Unlock()
	transport.SetProtocolVersion(result.ProtocolVersion)

	if err := c.notifyInternal(ctx, "notifications/initialized", nil, true); err != nil {
		_ = c.Close(context.Background())
		return err
	}
	c.mu.Lock()
	c.state = ClientStateConnected
	c.mu.Unlock()
	return nil
}

// Close shuts the client and its transport down. It is idempotent.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	transport := c.transport
	c.transport = nil
	disposers := c.disposers
	c.disposers = nil
	c.mu.Unlock()
	for _, dispose := range disposers {
		dispose()
	}
	c.markClosed(NewConnectionClosedError(""))
	if transport == nil {
		return nil
	}
	return transport.Close()
}

// Ping sends a `ping` request.
func (c *Client) Ping(ctx context.Context) error {
	return c.PingWithOptions(ctx, nil)
}

// PingWithOptions sends a `ping` request with per-request options.
func (c *Client) PingWithOptions(ctx context.Context, options *RequestOptions) error {
	_, err := c.requestInternal(ctx, "ping", nil, options, false)
	return err
}

// Request sends an arbitrary request and returns the raw result.
func (c *Client) Request(ctx context.Context, method string, params map[string]any, options *RequestOptions) (json.RawMessage, error) {
	return c.requestInternal(ctx, method, params, options, false)
}

// Notify sends an arbitrary notification.
func (c *Client) Notify(ctx context.Context, method string, params map[string]any) error {
	return c.notifyInternal(ctx, method, params, false)
}

// SetRequestHandler registers a handler for a server-initiated request.
func (c *Client) SetRequestHandler(method string, handler RequestHandler) func() {
	c.mu.Lock()
	c.handlers[method] = handler
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.handlers[method]; ok {
			delete(c.handlers, method)
		}
	}
}

// OnNotification registers a listener for a notification method.
func (c *Client) OnNotification(method string, listener func(any)) func() {
	c.mu.Lock()
	if c.notifs[method] == nil {
		c.notifs[method] = map[int64]func(any){}
	}
	c.listenerID++
	id := c.listenerID
	c.notifs[method][id] = listener
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if listeners := c.notifs[method]; listeners != nil {
			delete(listeners, id)
			if len(listeners) == 0 {
				delete(c.notifs, method)
			}
		}
	}
}

// OnError registers an error listener.
func (c *Client) OnError(listener func(error)) func() {
	c.mu.Lock()
	c.listenerID++
	id := c.listenerID
	c.errors[id] = listener
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.errors, id)
	}
}

// OnClose registers a listener fired at most once when the connection closes.
func (c *Client) OnClose(listener func()) func() {
	c.mu.Lock()
	c.listenerID++
	id := c.listenerID
	c.closes[id] = listener
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.closes, id)
	}
}

// ListTools fetches every page of `tools/list`.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	return c.ListToolsWithOptions(ctx, nil)
}

// ListToolsWithOptions fetches every page of `tools/list` with per-request
// options.
func (c *Client) ListToolsWithOptions(ctx context.Context, options *RequestOptions) ([]Tool, error) {
	items, err := c.listAll(ctx, "tools/list", "tools", isToolItem, options)
	if err != nil {
		return nil, err
	}
	tools := make([]Tool, 0, len(items))
	for _, item := range items {
		var tool Tool
		if err := json.Unmarshal(item, &tool); err != nil {
			return nil, invalid("Invalid entry in MCP tools/list result")
		}
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{}
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// ListResources fetches every page of `resources/list`, filling a missing name
// from the URI.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	return c.ListResourcesWithOptions(ctx, nil)
}

// ListResourcesWithOptions fetches every page of `resources/list` with
// per-request options.
func (c *Client) ListResourcesWithOptions(ctx context.Context, options *RequestOptions) ([]Resource, error) {
	items, err := c.listAll(ctx, "resources/list", "resources", isResourceItem, options)
	if err != nil {
		return nil, err
	}
	resources := make([]Resource, 0, len(items))
	for _, item := range items {
		var resource Resource
		if err := json.Unmarshal(item, &resource); err != nil {
			return nil, invalid("Invalid entry in MCP resources/list result")
		}
		if resource.Name == "" {
			resource.Name = resource.URI
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

// ListResourceTemplates fetches every page of `resources/templates/list`.
// ListResourceTemplates fetches every page of `resources/templates/list`.
func (c *Client) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	return c.ListResourceTemplatesWithOptions(ctx, nil)
}

// ListResourceTemplatesWithOptions fetches every page of
// `resources/templates/list` with per-request options.
func (c *Client) ListResourceTemplatesWithOptions(ctx context.Context, options *RequestOptions) ([]ResourceTemplate, error) {
	items, err := c.listAll(ctx, "resources/templates/list", "resourceTemplates", isResourceTemplateItem, options)
	if err != nil {
		return nil, err
	}
	templates := make([]ResourceTemplate, 0, len(items))
	for _, item := range items {
		var template ResourceTemplate
		if err := json.Unmarshal(item, &template); err != nil {
			return nil, invalid("Invalid entry in MCP resources/templates/list result")
		}
		if template.Name == "" {
			template.Name = template.URITemplate
		}
		templates = append(templates, template)
	}
	return templates, nil
}

// ReadResource reads one resource.
func (c *Client) ReadResource(ctx context.Context, uri string) (ReadResourceResult, error) {
	return c.ReadResourceWithOptions(ctx, uri, nil)
}

// ReadResourceWithOptions reads one resource with per-request options.
func (c *Client) ReadResourceWithOptions(ctx context.Context, uri string, options *RequestOptions) (ReadResourceResult, error) {
	raw, err := c.requestInternal(ctx, "resources/read", map[string]any{"uri": uri}, options, false)
	if err != nil {
		return ReadResourceResult{}, err
	}
	var result ReadResourceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ReadResourceResult{}, invalid("Invalid MCP resources/read result")
	}
	for _, contents := range result.Contents {
		if contents.URI == "" || (contents.Text == nil && contents.Blob == nil) {
			return ReadResourceResult{}, invalid("Invalid contents in MCP resources/read result")
		}
	}
	return result, nil
}

// CallTool invokes a tool and validates the result.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	return c.CallToolWithOptions(ctx, name, args, nil)
}

// CallToolWithOptions invokes a tool with per-request options.
func (c *Client) CallToolWithOptions(ctx context.Context, name string, args map[string]any, options *RequestOptions) (CallToolResult, error) {
	resolved := RequestOptions{}
	if options != nil {
		resolved = *options
	}
	if resolved.OnProgress == nil {
		resolved.OnProgress = func(ProgressNotification) {}
	}
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	raw, err := c.requestInternal(ctx, "tools/call", params, &resolved, false)
	if err != nil {
		return CallToolResult{}, err
	}
	return validateCallToolResult(raw)
}

func (c *Client) listAll(ctx context.Context, method, key string, isItem func(map[string]any) bool, options *RequestOptions) ([]json.RawMessage, error) {
	var items []json.RawMessage
	seen := map[string]bool{}
	var cursor *string
	for page := 0; page < maxListPages; page++ {
		var params map[string]any
		if cursor != nil {
			params = map[string]any{"cursor": *cursor}
		}
		raw, err := c.requestInternal(ctx, method, params, options, false)
		if err != nil {
			return nil, err
		}
		pageItems, next, err := validateListPage(method, key, raw, isItem)
		if err != nil {
			return nil, err
		}
		items = append(items, pageItems...)
		if next == nil {
			return items, nil
		}
		if seen[*next] {
			return nil, fmt.Errorf("MCP %s returned duplicate cursor: %s", method, *next)
		}
		seen[*next] = true
		cursor = next
	}
	return nil, fmt.Errorf("MCP %s exceeded %d pages", method, maxListPages)
}

func (c *Client) requestInternal(ctx context.Context, method string, params map[string]any, options *RequestOptions, allowConnecting bool) (json.RawMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	transport := c.requireTransport(allowConnecting)
	if transport == nil {
		return nil, NewConnectionClosedError("MCP client is not connected")
	}
	if err := ctx.Err(); err != nil {
		return nil, NewAbortError("")
	}
	c.mu.Lock()
	id := NumberID(float64(c.nextID))
	c.nextID++
	c.mu.Unlock()

	var progressToken *JsonRpcId
	if options != nil && options.OnProgress != nil {
		token := id
		progressToken = &token
		params = withProgressToken(params, token)
	}
	var encodedParams json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		encodedParams = encoded
	}

	timeout := DefaultRequestTimeout
	if method == "tools/call" {
		timeout = DefaultToolCallTimeout
	}
	if c.options.RequestTimeout > 0 {
		timeout = c.options.RequestTimeout
	}
	if options != nil && options.Timeout > 0 {
		timeout = options.Timeout
	}

	entry := &pendingRequest{
		id:            id,
		outcome:       make(chan rpcOutcome, 1),
		timeout:       timeout,
		cancellable:   method != "initialize",
		progressToken: progressToken,
		done:          make(chan struct{}),
	}
	if options != nil {
		entry.onProgress = options.OnProgress
	}

	c.mu.Lock()
	c.pending[id] = entry
	if progressToken != nil {
		c.progress[*progressToken] = id
	}
	c.mu.Unlock()

	c.armTimeout(id, entry)

	go func() {
		select {
		case <-ctx.Done():
			c.cancelPending(id, NewAbortError(""), entry.cancellable, abortReason(ctx))
		case <-entry.done:
		}
	}()

	if err := transport.Send(NewRequestMessage(id, method, encodedParams)); err != nil {
		c.cancelPending(id, err, false, "")
	}

	outcome := <-entry.outcome
	return outcome.result, outcome.err
}

func (c *Client) notifyInternal(ctx context.Context, method string, params map[string]any, allowConnecting bool) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return NewAbortError("")
		}
	}
	transport := c.requireTransport(allowConnecting)
	if transport == nil {
		return NewConnectionClosedError("MCP client is not connected")
	}
	var encodedParams json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return err
		}
		encodedParams = encoded
	}
	return transport.Send(NewNotificationMessage(method, encodedParams))
}

func (c *Client) requireTransport(allowConnecting bool) Transport {
	c.mu.Lock()
	defer c.mu.Unlock()
	transport := c.transport
	if transport != nil && (c.state == ClientStateConnected || (allowConnecting && c.state == ClientStateConnecting)) {
		return transport
	}
	return nil
}

func (c *Client) armTimeout(id JsonRpcId, entry *pendingRequest) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.finished {
		return
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	if entry.timeout <= 0 {
		return
	}
	entry.timer = time.AfterFunc(entry.timeout, func() {
		c.cancelPending(id, &McpTimeoutError{Timeout: entry.timeout}, entry.cancellable, "Request timed out")
	})
}

func (c *Client) cancelPending(id JsonRpcId, err error, notifyServer bool, reason string) {
	entry := c.removePending(id)
	if entry == nil {
		return
	}
	entry.finish(rpcOutcome{err: err})
	if !notifyServer {
		return
	}
	transport := c.requireTransport(false)
	if transport == nil {
		return
	}
	params := map[string]any{"requestId": id}
	if reason != "" {
		params["reason"] = reason
	}
	encoded, marshalErr := json.Marshal(params)
	if marshalErr != nil {
		return
	}
	if sendErr := transport.Send(NewNotificationMessage("notifications/cancelled", encoded)); sendErr != nil {
		c.emitError(sendErr)
	}
}

func (c *Client) removePending(id JsonRpcId) *pendingRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.pending[id]
	if entry == nil {
		return nil
	}
	delete(c.pending, id)
	if entry.progressToken != nil {
		delete(c.progress, *entry.progressToken)
	}
	entry.mu.Lock()
	entry.finished = true
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	entry.mu.Unlock()
	entry.closeDone()
	return entry
}

func (c *Client) failAllPending(err error) {
	c.mu.Lock()
	entries := make([]*pendingRequest, 0, len(c.pending))
	for id, entry := range c.pending {
		delete(c.pending, id)
		if entry.progressToken != nil {
			delete(c.progress, *entry.progressToken)
		}
		entry.mu.Lock()
		entry.finished = true
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		entry.mu.Unlock()
		entry.closeDone()
		entries = append(entries, entry)
	}
	c.mu.Unlock()
	for _, entry := range entries {
		entry.finish(rpcOutcome{err: err})
	}
}

func (c *Client) handleMessage(message *Message) {
	switch {
	case message.IsResponse():
		c.handleResponse(message)
	case message.IsRequest():
		go c.handleIncomingRequest(message)
	case message.IsNotification():
		c.handleNotification(message)
	default:
		c.emitError(NewMcpError(JSONRPCCodeInvalidRequest, "Received invalid JSON-RPC message", nil))
	}
}

func (c *Client) handleResponse(message *Message) {
	entry := c.removePending(*message.ID)
	if entry == nil {
		c.emitError(fmt.Errorf("received response for unknown MCP request %s", message.ID.String()))
		return
	}
	if message.Error != nil {
		var data any
		if len(message.Error.Data) > 0 {
			_ = json.Unmarshal(message.Error.Data, &data)
		}
		entry.finish(rpcOutcome{err: NewMcpError(message.Error.Code, message.Error.Message, data)})
		return
	}
	var result json.RawMessage
	if message.Result != nil {
		result = *message.Result
	}
	entry.finish(rpcOutcome{result: result})
}

func (c *Client) handleIncomingRequest(message *Message) {
	c.mu.Lock()
	transport := c.transport
	handler := c.handlers[message.Method]
	c.mu.Unlock()
	if transport == nil {
		return
	}
	if handler == nil {
		_ = transport.Send(NewErrorMessage(*message.ID, &ErrorObject{
			Code:    JSONRPCCodeMethodNotFound,
			Message: "Method not found: " + message.Method,
		}))
		return
	}
	params, _ := message.decodeParams()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.incoming[*message.ID] = cancel
	c.mu.Unlock()
	result, err := handler(ctx, params)
	c.mu.Lock()
	delete(c.incoming, *message.ID)
	c.mu.Unlock()
	cancel()
	if err != nil {
		var mcpErr *McpError
		errObject := &ErrorObject{Code: JSONRPCCodeInternalError, Message: err.Error()}
		if errors.As(err, &mcpErr) {
			errObject.Code = mcpErr.Code
			errObject.Message = mcpErr.Message
			if mcpErr.Data != nil {
				if encoded, marshalErr := json.Marshal(mcpErr.Data); marshalErr == nil {
					errObject.Data = encoded
				}
			}
		}
		if sendErr := transport.Send(NewErrorMessage(*message.ID, errObject)); sendErr != nil {
			c.emitError(sendErr)
		}
		return
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil || result == nil {
		encoded = []byte("{}")
	}
	if sendErr := transport.Send(NewResultMessage(*message.ID, encoded)); sendErr != nil {
		c.emitError(sendErr)
	}
}

func (c *Client) handleNotification(message *Message) {
	switch message.Method {
	case "notifications/progress":
		c.handleProgress(message)
	case "notifications/cancelled":
		c.handleCancelled(message)
	}
	params, _ := message.decodeParams()
	c.mu.Lock()
	listeners := make([]func(any), 0, len(c.notifs[message.Method]))
	for _, listener := range c.notifs[message.Method] {
		listeners = append(listeners, listener)
	}
	c.mu.Unlock()
	for _, listener := range listeners {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					c.emitError(fmt.Errorf("MCP notification listener panicked: %v", recovered))
				}
			}()
			listener(params)
		}()
	}
}

func (c *Client) handleProgress(message *Message) {
	var params struct {
		ProgressToken *JsonRpcId `json:"progressToken"`
		Progress      *float64   `json:"progress"`
		Total         *float64   `json:"total"`
		Message       string     `json:"message"`
	}
	if len(message.Params) > 0 {
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return
		}
	}
	if params.ProgressToken == nil || params.Progress == nil {
		return
	}
	c.mu.Lock()
	requestID, ok := c.progress[*params.ProgressToken]
	var entry *pendingRequest
	if ok {
		entry = c.pending[requestID]
	}
	c.mu.Unlock()
	if !ok || entry == nil {
		return
	}
	c.armTimeout(requestID, entry)
	if entry.onProgress != nil {
		notification := ProgressNotification{ProgressToken: *params.ProgressToken, Progress: *params.Progress}
		notification.Total = params.Total
		notification.Message = params.Message
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					c.emitError(fmt.Errorf("MCP progress listener panicked: %v", recovered))
				}
			}()
			entry.onProgress(notification)
		}()
	}
}

func (c *Client) handleCancelled(message *Message) {
	var params struct {
		RequestID *JsonRpcId `json:"requestId"`
		Reason    string     `json:"reason"`
	}
	if len(message.Params) > 0 {
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return
		}
	}
	if params.RequestID == nil {
		return
	}
	c.mu.Lock()
	cancel := c.incoming[*params.RequestID]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Client) handleTransportClose() {
	c.markClosed(NewConnectionClosedError(""))
}

func (c *Client) markClosed(err error) {
	c.mu.Lock()
	wasClosed := c.state == ClientStateClosed
	c.state = ClientStateClosed
	incoming := c.incoming
	c.incoming = map[JsonRpcId]context.CancelFunc{}
	listeners := make([]func(), 0, len(c.closes))
	if !wasClosed {
		for _, listener := range c.closes {
			listeners = append(listeners, listener)
		}
	}
	notify := !wasClosed
	c.mu.Unlock()
	c.failAllPending(err)
	for _, cancel := range incoming {
		cancel()
	}
	if !notify {
		return
	}
	for _, listener := range listeners {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					c.emitError(fmt.Errorf("MCP close listener panicked: %v", recovered))
				}
			}()
			listener()
		}()
	}
}

func (c *Client) emitError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	listeners := make([]func(error), 0, len(c.errors))
	for _, listener := range c.errors {
		listeners = append(listeners, listener)
	}
	c.mu.Unlock()
	for _, listener := range listeners {
		listener(err)
	}
}

func abortReason(ctx context.Context) string {
	if cause := context.Cause(ctx); cause != nil {
		return cause.Error()
	}
	return "Aborted"
}

func withProgressToken(params map[string]any, token JsonRpcId) map[string]any {
	next := map[string]any{}
	for key, value := range params {
		next[key] = value
	}
	meta := map[string]any{}
	if existing, ok := next["_meta"].(map[string]any); ok {
		for key, value := range existing {
			meta[key] = value
		}
	}
	meta["progressToken"] = token
	next["_meta"] = meta
	return next
}

func invalid(message string) *McpError {
	return NewMcpError(JSONRPCCodeInvalidRequest, message, nil)
}

func validateInitializeResult(raw json.RawMessage) (InitializeResult, error) {
	var probe struct {
		ProtocolVersion *string          `json:"protocolVersion"`
		Capabilities    *json.RawMessage `json:"capabilities"`
		ServerInfo      *struct {
			Name    *string `json:"name"`
			Version *string `json:"version"`
		} `json:"serverInfo"`
		Instructions *string `json:"instructions"`
	}
	var result InitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return InitializeResult{}, invalid("Invalid MCP initialize result")
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return InitializeResult{}, invalid("Invalid MCP initialize result")
	}
	if probe.ProtocolVersion == nil || *probe.ProtocolVersion == "" {
		return InitializeResult{}, invalid("Invalid MCP initialize result")
	}
	if probe.Capabilities == nil || !isJSONObject(*probe.Capabilities) {
		return InitializeResult{}, invalid("Invalid MCP initialize result")
	}
	if probe.ServerInfo == nil || probe.ServerInfo.Name == nil || probe.ServerInfo.Version == nil {
		return InitializeResult{}, invalid("Invalid MCP initialize result")
	}
	return result, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe map[string]any
	return json.Unmarshal(trimmed, &probe) == nil
}

func isToolItem(item map[string]any) bool {
	if _, ok := item["name"].(string); !ok {
		return false
	}
	schema, ok := item["inputSchema"].(map[string]any)
	return ok && schema != nil
}

func isResourceItem(item map[string]any) bool {
	if _, ok := item["uri"].(string); !ok {
		return false
	}
	if name, present := item["name"]; present && name != nil {
		_, ok := name.(string)
		return ok
	}
	return true
}

func isResourceTemplateItem(item map[string]any) bool {
	if _, ok := item["uriTemplate"].(string); !ok {
		return false
	}
	if name, present := item["name"]; present && name != nil {
		_, ok := name.(string)
		return ok
	}
	return true
}

func validateListPage(method, key string, raw json.RawMessage, isItem func(map[string]any) bool) ([]json.RawMessage, *string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, nil, invalid("Invalid MCP " + method + " result")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return nil, nil, invalid("Invalid MCP " + method + " result")
	}
	itemsRaw, ok := envelope[key]
	if !ok {
		return nil, nil, invalid("Invalid MCP " + method + " result")
	}
	itemsRaw = bytes.TrimSpace(itemsRaw)
	if len(itemsRaw) == 0 || itemsRaw[0] != '[' {
		return nil, nil, invalid("Invalid MCP " + method + " result")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, nil, invalid("Invalid MCP " + method + " result")
	}
	for _, item := range items {
		var object map[string]any
		if err := json.Unmarshal(item, &object); err != nil || object == nil {
			return nil, nil, invalid("Invalid entry in MCP " + method + " result")
		}
		if !isItem(object) {
			return nil, nil, invalid("Invalid entry in MCP " + method + " result")
		}
	}
	var next *string
	if cursorRaw, present := envelope["nextCursor"]; present {
		cursorRaw = bytes.TrimSpace(cursorRaw)
		if len(cursorRaw) > 0 && string(cursorRaw) != "null" {
			var cursor string
			if err := json.Unmarshal(cursorRaw, &cursor); err != nil {
				return nil, nil, invalid("Invalid MCP " + method + " cursor")
			}
			if cursor != "" {
				value := cursor
				next = &value
			}
		}
	}
	return items, next, nil
}

func validateCallToolResult(raw json.RawMessage) (CallToolResult, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return CallToolResult{}, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid MCP tools/call result", nil)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return CallToolResult{}, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid MCP tools/call result", nil)
	}
	if content, present := envelope["content"]; present {
		content = bytes.TrimSpace(content)
		if len(content) == 0 || content[0] != '[' {
			return CallToolResult{}, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid MCP tools/call result", nil)
		}
	}
	if structured, present := envelope["structuredContent"]; present {
		structured = bytes.TrimSpace(structured)
		if string(structured) != "null" && !isJSONObject(structured) {
			return CallToolResult{}, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid MCP tools/call structured content", nil)
		}
	}
	var result CallToolResult
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return CallToolResult{}, NewMcpError(JSONRPCCodeInvalidRequest, "Invalid MCP tools/call result", nil)
	}
	if result.Content == nil {
		result.Content = []ContentBlock{}
	}
	return result, nil
}
