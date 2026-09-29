// This file carries testing/gating-storage.ts: a deterministic commit-parking
// decorator for failure injection and concurrency tests.
package testing

import (
	"sync"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// CommitDiscarded is returned for every commit rejected after simulated
// storage loss.
type CommitDiscarded struct{ Message string }

func (e *CommitDiscarded) Error() string {
	if e.Message == "" {
		return "commit discarded"
	}
	return e.Message
}

type parkedCommit struct {
	release chan struct{}
	landed  chan struct{}
}

type pendingWaiter struct {
	count int
	done  chan struct{}
}

// GatingStorage deterministically parks admitted commits.
type GatingStorage struct {
	StorageDecorator
	mu        sync.Mutex
	cond      *sync.Cond
	armed     bool
	discarded bool
	queue     []*parkedCommit
	waiters   []*pendingWaiter
}

// NewGatingStorage wraps a delegate.
func NewGatingStorage(delegate harnesstypes.Storage) *GatingStorage {
	storage := &GatingStorage{StorageDecorator: NewStorageDecorator(delegate)}
	storage.cond = sync.NewCond(&storage.mu)
	return storage
}

// Arm enables gating; fixture setup bypasses it until then.
func (s *GatingStorage) Arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

// Pending returns the number of parked commits.
func (s *GatingStorage) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// WaitPending waits until at least count commits are parked.
func (s *GatingStorage) WaitPending(count int) error {
	if count < 1 {
		return &CommitDiscarded{Message: "Pending commit count must be a positive safe integer"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.discarded {
			return &CommitDiscarded{Message: "storage discarded"}
		}
		if len(s.queue) >= count {
			return nil
		}
		s.cond.Wait()
	}
}

// Next releases count parked commits in FIFO order and waits for each write to
// land.
func (s *GatingStorage) Next(count int) error {
	if count < 1 {
		return &CommitDiscarded{Message: "Released commit count must be a positive safe integer"}
	}
	for index := 0; index < count; index++ {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.discarded {
			s.cond.Wait()
		}
		if s.discarded {
			s.mu.Unlock()
			return &CommitDiscarded{Message: "commit discarded"}
		}
		parked := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		close(parked.release)
		<-parked.landed
	}
	return nil
}

// Discard drops parked commits and permanently rejects every later commit.
func (s *GatingStorage) Discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.discarded {
		return
	}
	s.discarded = true
	for _, parked := range s.queue {
		close(parked.release)
	}
	s.queue = nil
	s.cond.Broadcast()
}

// Commit parks the write when armed, otherwise forwards it.
func (s *GatingStorage) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	s.mu.Lock()
	if s.discarded {
		s.mu.Unlock()
		return harnesstypes.CommitResult{}, &CommitDiscarded{Message: "commit rejected: storage discarded"}
	}
	if !s.armed {
		s.mu.Unlock()
		return s.Delegate.Commit(writes, ctx)
	}
	parked := &parkedCommit{release: make(chan struct{}), landed: make(chan struct{})}
	s.queue = append(s.queue, parked)
	s.cond.Broadcast()
	s.mu.Unlock()

	<-parked.release
	s.mu.Lock()
	discarded := s.discarded
	s.mu.Unlock()
	if discarded {
		close(parked.landed)
		return harnesstypes.CommitResult{}, &CommitDiscarded{Message: "commit rejected: storage discarded"}
	}
	result, err := s.Delegate.Commit(writes, ctx)
	close(parked.landed)
	return result, err
}
