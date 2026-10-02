package mcp

import (
	"sync"
)

// DefaultMaxMessageBytes bounds a single transport message when no limit is
// configured.
const DefaultMaxMessageBytes = 16 * 1024 * 1024

// Transport carries JSON-RPC messages for a client.
type Transport interface {
	// Start opens the transport.
	Start() error
	// Send writes one message. It returns after the message has been handed to
	// the underlying transport, which for streaming HTTP responses may be
	// before the response arrives.
	Send(message *Message) error
	// Close shuts the transport down. It is idempotent.
	Close() error
	// OnMessage registers a message listener and returns a disposer.
	OnMessage(listener func(*Message)) func()
	// OnError registers an error listener and returns a disposer.
	OnError(listener func(error)) func()
	// OnClose registers a close listener and returns a disposer. The listener
	// fires at most once per transport.
	OnClose(listener func()) func()
	// SetProtocolVersion records the version negotiated by initialize.
	SetProtocolVersion(version string)
}

// TransportEvents is the listener bookkeeping shared by transports.
// EmitClose fires the close listeners at most once.
type TransportEvents struct {
	mu               sync.Mutex
	nextID           int64
	messageListeners map[int64]func(*Message)
	errorListeners   map[int64]func(error)
	closeListeners   map[int64]func()
	closeEmitted     bool
}

func (t *TransportEvents) addMessage(listener func(*Message)) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.messageListeners == nil {
		t.messageListeners = map[int64]func(*Message){}
	}
	t.nextID++
	id := t.nextID
	t.messageListeners[id] = listener
	return id
}

// OnMessage registers a message listener and returns a disposer.
func (t *TransportEvents) OnMessage(listener func(*Message)) func() {
	id := t.addMessage(listener)
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.messageListeners, id)
	}
}

// OnError registers an error listener and returns a disposer.
func (t *TransportEvents) OnError(listener func(error)) func() {
	t.mu.Lock()
	if t.errorListeners == nil {
		t.errorListeners = map[int64]func(error){}
	}
	t.nextID++
	id := t.nextID
	t.errorListeners[id] = listener
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.errorListeners, id)
	}
}

// OnClose registers a close listener and returns a disposer.
func (t *TransportEvents) OnClose(listener func()) func() {
	t.mu.Lock()
	if t.closeListeners == nil {
		t.closeListeners = map[int64]func(){}
	}
	t.nextID++
	id := t.nextID
	t.closeListeners[id] = listener
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		delete(t.closeListeners, id)
	}
}

// SetProtocolVersion is a no-op for transports that do not use the version.
func (t *TransportEvents) SetProtocolVersion(string) {}

// EmitMessage delivers a message to every listener.
func (t *TransportEvents) EmitMessage(message *Message) {
	t.mu.Lock()
	listeners := make([]func(*Message), 0, len(t.messageListeners))
	for _, listener := range t.messageListeners {
		listeners = append(listeners, listener)
	}
	t.mu.Unlock()
	for _, listener := range listeners {
		listener(message)
	}
}

// EmitError delivers an error to every listener. A nil error is ignored.
func (t *TransportEvents) EmitError(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	listeners := make([]func(error), 0, len(t.errorListeners))
	for _, listener := range t.errorListeners {
		listeners = append(listeners, listener)
	}
	t.mu.Unlock()
	for _, listener := range listeners {
		listener(err)
	}
}

// EmitClose delivers the close event once.
func (t *TransportEvents) EmitClose() {
	t.mu.Lock()
	if t.closeEmitted {
		t.mu.Unlock()
		return
	}
	t.closeEmitted = true
	listeners := make([]func(), 0, len(t.closeListeners))
	for _, listener := range t.closeListeners {
		listeners = append(listeners, listener)
	}
	t.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}
