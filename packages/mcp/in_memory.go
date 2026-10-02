package mcp

import (
	"encoding/json"
	"sync"
)

// InMemoryTransport is a transport whose peer is another InMemoryTransport in
// the same process. It is used for tests and for embedding an MCP server.
type InMemoryTransport struct {
	TransportEvents

	mu      sync.Mutex
	peer    *InMemoryTransport
	started bool
	closed  bool
}

// NewInMemoryTransport builds an unconnected transport.
func NewInMemoryTransport() *InMemoryTransport { return &InMemoryTransport{} }

// ConnectPeer pairs two transports. Each transport may have one peer.
func (t *InMemoryTransport) ConnectPeer(peer *InMemoryTransport) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.peer != nil {
		return NewMcpError(JSONRPCCodeInternalError, "In-memory MCP transport already has a peer", nil)
	}
	t.peer = peer
	return nil
}

// Start marks the transport open.
func (t *InMemoryTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return NewConnectionClosedError("")
	}
	t.started = true
	return nil
}

// Send clones the message and delivers it to the peer asynchronously so a
// handler that answers on the same goroutine cannot deadlock.
func (t *InMemoryTransport) Send(message *Message) error {
	t.mu.Lock()
	started := t.started
	closed := t.closed
	peer := t.peer
	t.mu.Unlock()
	if !started || closed {
		return NewConnectionClosedError("")
	}
	if peer == nil {
		return NewConnectionClosedError("In-memory MCP peer is not connected")
	}
	peer.mu.Lock()
	peerReady := peer.started && !peer.closed
	peer.mu.Unlock()
	if !peerReady {
		return NewConnectionClosedError("In-memory MCP peer is not connected")
	}
	copy, err := cloneMessage(message)
	if err != nil {
		return err
	}
	go peer.deliver(copy)
	return nil
}

// Close closes this transport and, transitively, its peer.
func (t *InMemoryTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	peer := t.peer
	t.mu.Unlock()
	t.EmitClose()
	if peer != nil {
		return peer.Close()
	}
	return nil
}

// EmitError exposes error emission so tests can simulate transport failures.
func (t *InMemoryTransport) EmitError(err error) {
	t.TransportEvents.EmitError(err)
}

func (t *InMemoryTransport) deliver(message *Message) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return
	}
	t.EmitMessage(message)
}

func cloneMessage(message *Message) (*Message, error) {
	if message == nil {
		return nil, NewMcpError(JSONRPCCodeInternalError, "nil MCP message", nil)
	}
	data, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	var clone Message
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

// CreateInMemoryTransportPair returns two connected transports.
func CreateInMemoryTransportPair() (client, server *InMemoryTransport) {
	client = NewInMemoryTransport()
	server = NewInMemoryTransport()
	_ = client.ConnectPeer(server)
	_ = server.ConnectPeer(client)
	return client, server
}
