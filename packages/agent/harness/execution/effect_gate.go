// Package harnessexecution is the Go port of
// packages/agent/src/harness/execution/effect-gate.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// An effect gate separates the procedure-facing admission check from the
// owner-facing lifecycle: a drive pass admits effects while open, rejects new
// effects once cancellation is requested, and surfaces the terminal error after
// the gate closes. The gate never invokes external callbacks while holding its
// internal lock.
package harnessexecution

import (
	"errors"
	"sync"
)

// AbortRequested is the expected internal control flow when cancellation wins
// effect admission. It carries the cancellation channel the owner registered.
type AbortRequested struct {
	cancellation <-chan struct{}
}

// Cancellation is the channel that settles when the abort completes.
func (e *AbortRequested) Cancellation() <-chan struct{} { return e.cancellation }

func (e *AbortRequested) Error() string { return "Abort requested" }

// Gate is the procedure-facing synchronous admission capability for one drive
// pass. Admit runs invoke only while the gate is open.
type Gate interface {
	// Signal is closed once the gate aborts or closes.
	Signal() <-chan struct{}
	// Admit checks admission and then runs invoke.
	Admit(invoke func() error) error
}

// GateControl is the owner-facing lifecycle control for one drive pass.
type GateControl interface {
	// BeginAbort requests cancellation with the channel that settles the abort.
	BeginAbort(cancellation <-chan struct{})
	// SignalAbort closes the public signal for a gate already aborting.
	SignalAbort()
	// Close seals the gate with its terminal error.
	Close(err error)
}

type gateStatus int

const (
	gateOpen gateStatus = iota
	gateAborting
	gateClosed
)

type gate struct {
	mu           sync.Mutex
	status       gateStatus
	cancellation <-chan struct{}
	closedErr    error
	done         chan struct{}
}

// CreateGate returns separate procedure-facing and owner-facing views of one
// effect gate.
func CreateGate() (Gate, GateControl) {
	g := &gate{status: gateOpen, done: make(chan struct{})}
	return g, g
}

func (g *gate) Signal() <-chan struct{} { return g.done }

func (g *gate) admitErrorLocked() error {
	switch g.status {
	case gateAborting:
		return &AbortRequested{cancellation: g.cancellation}
	case gateClosed:
		if g.closedErr != nil {
			return g.closedErr
		}
		return errors.New("effect gate is closed")
	default:
		return nil
	}
}

// Admit checks admission and then runs invoke. invoke is never called while
// the internal lock is held.
func (g *gate) Admit(invoke func() error) error {
	g.mu.Lock()
	admitErr := g.admitErrorLocked()
	g.mu.Unlock()
	if admitErr != nil {
		return admitErr
	}
	return invoke()
}

// BeginAbort moves an open gate to aborting. It is a no-op once the gate
// already left the open state.
func (g *gate) BeginAbort(cancellation <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status != gateOpen {
		return
	}
	g.status = gateAborting
	g.cancellation = cancellation
}

// SignalAbort closes the public signal for a gate that is aborting.
func (g *gate) SignalAbort() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status != gateAborting {
		return
	}
	select {
	case <-g.done:
		return
	default:
		close(g.done)
	}
}

// Close seals the gate with its terminal error and settles the public signal.
func (g *gate) Close(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status == gateClosed {
		return
	}
	g.status = gateClosed
	g.closedErr = err
	select {
	case <-g.done:
	default:
		close(g.done)
	}
}
