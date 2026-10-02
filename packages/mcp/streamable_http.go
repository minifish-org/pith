package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxErrorBodyBytes          = 8 * 1024
	errorMessageBodyChars      = 500
	defaultReconnectInitial    = time.Second
	defaultReconnectMaxDelay   = 30 * time.Second
	defaultReconnectMaxRetries = 5
)

// SseEvent is one server-sent event.
type SseEvent struct {
	Event string
	Data  string
	ID    string
}

// SSEStreamOptions configures ConsumeSSEStream.
type SSEStreamOptions struct {
	MaxEventBytes int
	OnEvent       func(SseEvent)
	OnID          func(string)
	OnRetry       func(int)
}

// ConsumeSSEStream parses a server-sent event stream, invoking the callbacks as
// events, ids and retry hints arrive.
func ConsumeSSEStream(reader io.Reader, options SSEStreamOptions) error {
	maxBytes := options.MaxEventBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	buffered := bufio.NewReader(reader)
	var eventName string
	var eventID string
	var dataLines []string
	dataBytes := 0

	dispatch := func() {
		if len(dataLines) == 0 {
			eventName = ""
			eventID = ""
			return
		}
		event := SseEvent{Data: strings.Join(dataLines, "\n")}
		if eventName != "" {
			event.Event = eventName
		}
		if eventID != "" {
			event.ID = eventID
		}
		if options.OnEvent != nil {
			options.OnEvent(event)
		}
		eventName = ""
		eventID = ""
		dataLines = nil
		dataBytes = 0
	}

	processLine := func(rawLine string) error {
		line := strings.TrimSuffix(rawLine, "\r")
		if line == "" {
			dispatch()
			return nil
		}
		if strings.HasPrefix(line, ":") {
			return nil
		}
		field := line
		value := ""
		if index := strings.Index(line, ":"); index >= 0 {
			field = line[:index]
			value = line[index+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		}
		switch field {
		case "data":
			if len(dataLines) > 0 {
				dataBytes++
			}
			dataBytes += len(value)
			if dataBytes > maxBytes {
				return fmt.Errorf("MCP SSE event exceeds %d bytes", maxBytes)
			}
			dataLines = append(dataLines, value)
		case "event":
			eventName = value
		case "id":
			if !strings.ContainsRune(value, 0) {
				eventID = value
				if options.OnID != nil {
					options.OnID(value)
				}
			}
		case "retry":
			if isDigits(value) {
				if delay, err := strconv.Atoi(value); err == nil && options.OnRetry != nil {
					options.OnRetry(delay)
				}
			}
		}
		return nil
	}

	for {
		line, err := buffered.ReadString('\n')
		if len(line) > 0 {
			text := strings.TrimSuffix(line, "\n")
			if processErr := processLine(text); processErr != nil {
				return processErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	dispatch()
	return nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ReconnectOptions controls SSE stream reconnection.
type ReconnectOptions struct {
	// InitialDelay is the delay before the first attempt. Default 1s.
	InitialDelay time.Duration
	// MaxDelay bounds the exponential backoff. Default 30s.
	MaxDelay time.Duration
	// MaxRetries is the number of consecutive failed attempts. Default 5.
	MaxRetries int
}

// HTTPOptions configures a StreamableHTTPTransport.
type HTTPOptions struct {
	// Client is the HTTP client to use. Defaults to http.DefaultClient.
	Client *http.Client
	// Headers are added to every request.
	Headers http.Header
	// OpenGetStream opens the server-to-client GET stream after initialization.
	// Defaults to true.
	OpenGetStream *bool
	// MaxMessageBytes bounds one SSE event. Defaults to DefaultMaxMessageBytes.
	MaxMessageBytes int
	// AuthProvider supplies bearer tokens and may refresh them after a 401.
	AuthProvider AuthProvider
	// Reconnect configures SSE reconnection.
	Reconnect *ReconnectOptions
}

// McpHTTPError reports a non-success MCP HTTP response.
type McpHTTPError struct {
	Status  int
	Message string
	Body    string
}

// Error implements the error interface.
func (e *McpHTTPError) Error() string { return e.Message }

// McpAuthRequiredError reports a 401 (or 403) that requires authentication.
type McpAuthRequiredError struct {
	Status          int
	Message         string
	Body            string
	WWWAuthenticate string
}

// Error implements the error interface.
func (e *McpAuthRequiredError) Error() string { return e.Message }

// McpSessionExpiredError reports a 404 for an established session.
type McpSessionExpiredError struct {
	Status  int
	Message string
	Body    string
}

// Error implements the error interface.
func (e *McpSessionExpiredError) Error() string { return e.Message }

var insufficientScopePattern = regexp.MustCompile(`(?i)(?:^|[\s,])error="?insufficient_scope"?`)

// StreamableHTTPTransport speaks MCP over the Streamable HTTP transport.
type StreamableHTTPTransport struct {
	TransportEvents

	url             *url.URL
	urlErr          error
	fetch           McpFetch
	headers         http.Header
	openGetStream   bool
	maxMessageBytes int
	authProvider    AuthProvider
	reconnect       ReconnectOptions

	ctx    context.Context
	cancel context.CancelFunc

	mu               sync.Mutex
	started          bool
	closed           bool
	sessionID        string
	protocolVersion  string
	getStreamStarted bool
}

// NewStreamableHTTPTransport builds a Streamable HTTP transport for a URL.
func NewStreamableHTTPTransport(rawURL string, options *HTTPOptions) Transport {
	transport := &StreamableHTTPTransport{
		fetch:   FetchFromClient(nil),
		headers: http.Header{},
	}
	transport.openGetStream = true
	transport.maxMessageBytes = DefaultMaxMessageBytes
	transport.reconnect = ReconnectOptions{
		InitialDelay: defaultReconnectInitial,
		MaxDelay:     defaultReconnectMaxDelay,
		MaxRetries:   defaultReconnectMaxRetries,
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		transport.urlErr = fmt.Errorf("invalid MCP server URL: %w", err)
		transport.url = &url.URL{}
	} else {
		transport.url = parsed
	}
	if options != nil {
		if options.Client != nil {
			transport.fetch = FetchFromClient(options.Client)
		}
		if options.Headers != nil {
			transport.headers = options.Headers.Clone()
		}
		if options.OpenGetStream != nil {
			transport.openGetStream = *options.OpenGetStream
		}
		if options.MaxMessageBytes > 0 {
			transport.maxMessageBytes = options.MaxMessageBytes
		}
		transport.authProvider = options.AuthProvider
		if options.Reconnect != nil {
			if options.Reconnect.InitialDelay > 0 {
				transport.reconnect.InitialDelay = options.Reconnect.InitialDelay
			}
			if options.Reconnect.MaxDelay > 0 {
				transport.reconnect.MaxDelay = options.Reconnect.MaxDelay
			}
			if options.Reconnect.MaxRetries > 0 {
				transport.reconnect.MaxRetries = options.Reconnect.MaxRetries
			}
		}
	}
	return transport
}

// SessionID returns the session id negotiated with the server.
func (t *StreamableHTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// Start marks the transport open.
func (t *StreamableHTTPTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.urlErr != nil {
		return t.urlErr
	}
	if t.started {
		return errors.New("MCP Streamable HTTP transport already started")
	}
	if t.closed {
		return NewConnectionClosedError("")
	}
	t.started = true
	t.ctx, t.cancel = context.WithCancel(context.Background())
	return nil
}

// SetProtocolVersion records the negotiated protocol version.
func (t *StreamableHTTPTransport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

// Send posts one message. Requests may receive a JSON or SSE response.
func (t *StreamableHTTPTransport) Send(message *Message) error {
	if err := t.ready(); err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	extra := http.Header{}
	extra.Set("Accept", "application/json, text/event-stream")
	extra.Set("Content-Type", "application/json")
	response, err := t.doFetch(http.MethodPost, t.url.String(), extra, data)
	if err != nil {
		return err
	}
	if err := t.checkResponse(response); err != nil {
		discardResponse(response)
		return err
	}
	t.captureSession(response)

	if !message.IsRequest() {
		discardResponse(response)
		if message.Method == "notifications/initialized" {
			t.startGetStream()
		}
		return nil
	}
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		discardResponse(response)
		return &McpHTTPError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("MCP server accepted request %s without a response", message.Method),
		}
	}
	kind := responseContentType(response)
	switch kind {
	case "application/json":
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		return t.emitJSONBody(body)
	case "text/event-stream":
		id := *message.ID
		body := response.Body
		go func() {
			defer body.Close()
			t.consumeResponseStream(body, id)
		}()
		return nil
	default:
		discardResponse(response)
		shown := kind
		if shown == "" {
			shown = "missing"
		}
		return &McpHTTPError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("Unsupported MCP response content type: %s", shown),
		}
	}
}

// Close aborts in-flight requests and deletes the session when established.
func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	started := t.started
	session := t.sessionID
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started && session != "" {
		ctx, timeoutCancel := context.WithTimeout(context.Background(), time.Second)
		headers, _, _, err := t.buildHeaders(ctx, nil)
		if err == nil {
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodDelete, t.url.String(), nil)
			if requestErr == nil {
				request.Header = headers
				if response, fetchErr := t.fetch(request); fetchErr == nil {
					discardResponse(response)
				}
			}
		}
		timeoutCancel()
	}
	t.EmitClose()
	return nil
}

func (t *StreamableHTTPTransport) ready() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started || t.closed {
		return NewConnectionClosedError("")
	}
	return nil
}

func (t *StreamableHTTPTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *StreamableHTTPTransport) doFetch(method, rawURL string, extra http.Header, body []byte) (*http.Response, error) {
	handler, _ := t.authProvider.(UnauthorizedHandler)
	for attempt := 0; ; attempt++ {
		headers, token, hasToken, err := t.buildHeaders(t.ctx, extra)
		if err != nil {
			return nil, err
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(t.ctx, method, rawURL, reader)
		if err != nil {
			return nil, err
		}
		request.Header = headers
		response, err := t.fetch(request)
		if err != nil {
			return nil, err
		}
		if attempt == 0 && handler != nil && needsAuthorization(response) {
			unauthorized := UnauthorizedContext{
				Response:  response,
				ServerURL: t.url,
				Fetch:     t.fetch,
				Token:     token,
				HasToken:  hasToken,
			}
			handlerErr := handler.OnUnauthorized(t.ctx, unauthorized)
			discardResponse(response)
			if handlerErr != nil {
				return nil, handlerErr
			}
			continue
		}
		return response, nil
	}
}

func (t *StreamableHTTPTransport) buildHeaders(ctx context.Context, extra http.Header) (http.Header, string, bool, error) {
	headers := http.Header{}
	t.mu.Lock()
	for name, values := range t.headers {
		for _, value := range values {
			headers.Add(name, value)
		}
	}
	session := t.sessionID
	version := t.protocolVersion
	provider := t.authProvider
	t.mu.Unlock()
	for name, values := range extra {
		for _, value := range values {
			headers.Set(name, value)
		}
	}
	if session != "" {
		headers.Set("Mcp-Session-Id", session)
	}
	if version != "" {
		headers.Set("MCP-Protocol-Version", version)
	}
	if provider == nil {
		return headers, "", false, nil
	}
	token, err := provider.Token(ctx)
	if err != nil {
		return nil, "", false, err
	}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	return headers, token, token != "", nil
}

func (t *StreamableHTTPTransport) captureSession(response *http.Response) {
	if response == nil {
		return
	}
	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		t.mu.Lock()
		t.sessionID = session
		t.mu.Unlock()
	}
}

func (t *StreamableHTTPTransport) currentSession() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

func (t *StreamableHTTPTransport) checkResponse(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body := readBodyLimited(response, maxErrorBodyBytes)
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return &McpAuthRequiredError{
			Status:          http.StatusUnauthorized,
			Message:         "MCP server requires authentication",
			Body:            body,
			WWWAuthenticate: response.Header.Get("Www-Authenticate"),
		}
	case response.StatusCode == http.StatusNotFound && t.currentSession() != "":
		return &McpSessionExpiredError{Status: http.StatusNotFound, Message: "MCP session expired", Body: body}
	default:
		return &McpHTTPError{
			Status:  response.StatusCode,
			Message: describeHTTPFailure(response.StatusCode, body),
			Body:    body,
		}
	}
}

func (t *StreamableHTTPTransport) emitJSONBody(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		for _, item := range items {
			message, err := ParseMessage(item)
			if err != nil {
				return err
			}
			t.EmitMessage(message)
		}
		return nil
	}
	message, err := ParseMessage(trimmed)
	if err != nil {
		return err
	}
	t.EmitMessage(message)
	return nil
}

type streamCursor struct {
	lastEventID    string
	hasLastEventID bool
	retryMs        int
	hasRetry       bool
	received       bool
}

func (t *StreamableHTTPTransport) consumeSSE(reader io.Reader, cursor *streamCursor, onMessage func(*Message)) error {
	return ConsumeSSEStream(reader, SSEStreamOptions{
		MaxEventBytes: t.maxMessageBytes,
		OnID: func(id string) {
			cursor.lastEventID = id
			cursor.hasLastEventID = true
		},
		OnRetry: func(delayMs int) {
			cursor.retryMs = delayMs
			cursor.hasRetry = true
		},
		OnEvent: func(event SseEvent) {
			cursor.received = true
			// Events without data prime resumption; other event types are not
			// JSON-RPC.
			if strings.TrimSpace(event.Data) == "" || (event.Event != "" && event.Event != "message") {
				return
			}
			message, err := ParseMessage([]byte(event.Data))
			if err != nil {
				t.EmitError(err)
				return
			}
			if onMessage != nil {
				onMessage(message)
			}
			t.EmitMessage(message)
		},
	})
}

func (t *StreamableHTTPTransport) consumeResponseStream(body io.ReadCloser, requestID JsonRpcId) {
	cursor := &streamCursor{}
	answered := false
	onMessage := func(message *Message) {
		if message.IsResponse() && message.ID != nil && *message.ID == requestID {
			answered = true
		}
	}
	var stream io.ReadCloser = body
	var failure error
	for attempt := 0; ; {
		if stream != nil {
			failure = t.consumeSSE(stream, cursor, onMessage)
			_ = stream.Close()
			stream = nil
		}
		if answered || t.isClosed() {
			return
		}
		if failure != nil && !t.isRetryable(failure) {
			break
		}
		if !cursor.hasLastEventID || attempt >= t.maxRetries() {
			break
		}
		if cursor.received {
			attempt = 0
		}
		cursor.received = false
		if !t.sleep(t.reconnectDelay(attempt, cursor)) {
			return
		}
		attempt++
		next, err := t.openSseStream(cursor)
		if err != nil {
			failure = err
			if !t.isRetryable(err) {
				break
			}
			continue
		}
		if next == nil {
			break
		}
		stream = next
	}
	if t.isClosed() {
		return
	}
	reason := "stream ended without a response"
	if failure != nil {
		reason = failure.Error()
	}
	id := requestID
	t.EmitMessage(NewErrorMessage(id, &ErrorObject{
		Code:    JSONRPCCodeInternalError,
		Message: "MCP response stream failed: " + reason,
	}))
}

func (t *StreamableHTTPTransport) startGetStream() {
	t.mu.Lock()
	if !t.openGetStream || t.getStreamStarted || t.closed {
		t.mu.Unlock()
		return
	}
	t.getStreamStarted = true
	t.mu.Unlock()
	go t.runGetStream()
}

func (t *StreamableHTTPTransport) runGetStream() {
	cursor := &streamCursor{}
	for attempt := 0; !t.isClosed(); {
		var failure error
		noStream := false
		stream, err := t.openSseStream(cursor)
		switch {
		case err != nil:
			failure = err
		case stream == nil:
			noStream = true
		default:
			openedAt := time.Now()
			if consumeErr := t.consumeSSE(stream, cursor, nil); consumeErr != nil {
				failure = consumeErr
			} else if cursor.received || time.Since(openedAt) > t.maxDelay() {
				attempt = 0
			}
			_ = stream.Close()
		}
		if noStream {
			return
		}
		if t.isClosed() {
			return
		}
		if failure != nil && !t.isRetryable(failure) {
			t.EmitError(failure)
			return
		}
		cursor.received = false
		if attempt >= t.maxRetries() {
			t.EmitError(errors.New("MCP server-to-client stream dropped and could not be reopened"))
			return
		}
		if !t.sleep(t.reconnectDelay(attempt, cursor)) {
			return
		}
		attempt++
	}
}

// openSseStream opens a GET SSE stream. It returns (nil, nil) for a 405.
func (t *StreamableHTTPTransport) openSseStream(cursor *streamCursor) (io.ReadCloser, error) {
	extra := http.Header{}
	extra.Set("Accept", "text/event-stream")
	if cursor.hasLastEventID {
		extra.Set("Last-Event-ID", cursor.lastEventID)
	}
	response, err := t.doFetch(http.MethodGet, t.url.String(), extra, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusMethodNotAllowed {
		discardResponse(response)
		return nil, nil
	}
	if err := t.checkResponse(response); err != nil {
		discardResponse(response)
		return nil, err
	}
	t.captureSession(response)
	if responseContentType(response) != "text/event-stream" {
		kind := responseContentType(response)
		if kind == "" {
			kind = "missing"
		}
		discardResponse(response)
		return nil, &McpHTTPError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("Unsupported MCP GET response content type: %s", kind),
		}
	}
	return response.Body, nil
}

func (t *StreamableHTTPTransport) isRetryable(err error) bool {
	var httpErr *McpHTTPError
	if errors.As(err, &httpErr) {
		return isTransientStatus(httpErr.Status)
	}
	var authErr *McpAuthRequiredError
	if errors.As(err, &authErr) {
		return false
	}
	var sessionErr *McpSessionExpiredError
	if errors.As(err, &sessionErr) {
		return false
	}
	return true
}

func (t *StreamableHTTPTransport) reconnectDelay(attempt int, cursor *streamCursor) time.Duration {
	if cursor != nil && cursor.hasRetry {
		return time.Duration(cursor.retryMs) * time.Millisecond
	}
	initial := t.reconnect.InitialDelay
	if initial <= 0 {
		initial = defaultReconnectInitial
	}
	delay := initial
	for i := 0; i < attempt; i++ {
		if delay >= t.maxDelay()/2 {
			return t.maxDelay()
		}
		delay *= 2
	}
	if delay > t.maxDelay() {
		return t.maxDelay()
	}
	return delay
}

func (t *StreamableHTTPTransport) maxDelay() time.Duration {
	if t.reconnect.MaxDelay > 0 {
		return t.reconnect.MaxDelay
	}
	return defaultReconnectMaxDelay
}

func (t *StreamableHTTPTransport) maxRetries() int {
	if t.reconnect.MaxRetries > 0 {
		return t.reconnect.MaxRetries
	}
	return defaultReconnectMaxRetries
}

// sleep waits for the delay, returning false if the transport closed first.
func (t *StreamableHTTPTransport) sleep(delay time.Duration) bool {
	t.mu.Lock()
	ctx := t.ctx
	t.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func needsAuthorization(response *http.Response) bool {
	if response.StatusCode == http.StatusUnauthorized {
		return true
	}
	if response.StatusCode != http.StatusForbidden {
		return false
	}
	return insufficientScopePattern.MatchString(response.Header.Get("Www-Authenticate"))
}

func isTransientStatus(status int) bool {
	return status == 408 || status == 429 || status >= 500
}

func describeHTTPFailure(status int, body string) string {
	text := strings.TrimSpace(body)
	snippet := text
	if len(snippet) > errorMessageBodyChars {
		snippet = snippet[:errorMessageBodyChars-3] + "..."
	}
	if snippet == "" {
		return fmt.Sprintf("MCP HTTP request failed with status %d", status)
	}
	return fmt.Sprintf("MCP HTTP request failed with status %d: %s", status, snippet)
}

func responseContentType(response *http.Response) string {
	value := response.Header.Get("Content-Type")
	if index := strings.Index(value, ";"); index >= 0 {
		value = value[:index]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func readBodyLimited(response *http.Response, limit int64) string {
	if response.Body == nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(response.Body, limit))
	return string(data)
}

func discardResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
}
