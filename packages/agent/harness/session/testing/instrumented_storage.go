// This file carries testing/instrumented-storage.ts: a transparent decorator
// that records commit admission.
package testing

import (
	"sync"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// InstrumentedStorage records every commit transaction in admission order.
type InstrumentedStorage struct {
	StorageDecorator
	mu             sync.Mutex
	commitAttempts [][]harnesstypes.Write
}

// NewInstrumentedStorage wraps a delegate.
func NewInstrumentedStorage(delegate harnesstypes.Storage) *InstrumentedStorage {
	return &InstrumentedStorage{StorageDecorator: NewStorageDecorator(delegate)}
}

// Commit records the transaction reference before delegating.
func (s *InstrumentedStorage) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	s.mu.Lock()
	s.commitAttempts = append(s.commitAttempts, writes)
	s.mu.Unlock()
	return s.Delegate.Commit(writes, ctx)
}

// GetCommitAttempts returns a copy of the recorded transactions.
func (s *InstrumentedStorage) GetCommitAttempts() [][]harnesstypes.Write {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]harnesstypes.Write, len(s.commitAttempts))
	copy(out, s.commitAttempts)
	return out
}

// ClearCommitAttempts clears the recorded transactions.
func (s *InstrumentedStorage) ClearCommitAttempts() {
	s.mu.Lock()
	s.commitAttempts = nil
	s.mu.Unlock()
}
