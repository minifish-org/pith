// This file carries mutation-line.ts: one serialized read-modify-write queue
// per Session.
package session

import "sync"

// MutationLine serializes complete read-modify-write jobs for one Session.
//
// The upstream Promise chain is modeled as a mutex plus condition variable. A
// job holds the line from acquire until release; sealing rejects every queued
// and future job while allowing the current holder to drain.
type MutationLine struct {
	mu     sync.Mutex
	cond   *sync.Cond
	held   bool
	sealed error
}

// NewMutationLine builds an empty line.
func NewMutationLine() *MutationLine {
	line := &MutationLine{}
	line.cond = sync.NewCond(&line.mu)
	return line
}

func (l *MutationLine) acquire() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.held && l.sealed == nil {
		l.cond.Wait()
	}
	if l.sealed != nil {
		return l.sealed
	}
	l.held = true
	return nil
}

func (l *MutationLine) release() {
	l.mu.Lock()
	l.held = false
	l.cond.Broadcast()
	l.mu.Unlock()
}

// seal records the first error and blocks until the current holder releases.
func (l *MutationLine) seal(err error) {
	l.mu.Lock()
	if l.sealed == nil {
		l.sealed = err
	}
	l.cond.Broadcast()
	for l.held {
		l.cond.Wait()
	}
	l.mu.Unlock()
}

// Run enqueues one operation and waits for its turn. Sealed lines reject both
// queued and future jobs with the sealing error.
func (l *MutationLine) Run(operation func() (any, error)) (any, error) {
	if err := l.acquire(); err != nil {
		return nil, err
	}
	defer l.release()
	return operation()
}
