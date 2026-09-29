// This file is a Go port of packages/ai/src/auth/credential-store.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package auth

import (
	"context"
	"sync"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// InMemoryCredentialStore is the default in-memory credential store. Apps
// inject persistent stores. It is keyed by Provider id, one credential per
// provider; writes are serialized per provider.
//
// Ports `InMemoryCredentialStore` from
// packages/ai/src/auth/credential-store.ts.
type InMemoryCredentialStore struct {
	mu          sync.Mutex
	credentials map[string]authtypes.Credential
	locks       map[string]*sync.Mutex
}

// NewInMemoryCredentialStore builds an empty store.
func NewInMemoryCredentialStore() *InMemoryCredentialStore {
	return &InMemoryCredentialStore{
		credentials: map[string]authtypes.Credential{},
		locks:       map[string]*sync.Mutex{},
	}
}

// lockFor returns the per-provider mutex, creating it on demand.
func (s *InMemoryCredentialStore) lockFor(providerID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credentials == nil {
		s.credentials = map[string]authtypes.Credential{}
	}
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	lock, ok := s.locks[providerID]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[providerID] = lock
	}
	return lock
}

func operationSignal(ctx context.Context, options *authtypes.AuthOperationOptions) context.Context {
	if options != nil && options.Signal != nil {
		if ctx == nil {
			return options.Signal
		}
		select {
		case <-ctx.Done():
			return ctx
		case <-options.Signal.Done():
			return options.Signal
		default:
			return ctx
		}
	}
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func throwIfAborted(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Read returns the stored credential, or nil when the entry is missing.
func (s *InMemoryCredentialStore) Read(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	signal := operationSignal(ctx, options)
	if err := throwIfAborted(signal); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credentials[providerID], nil
}

// List returns stored credential metadata without exposing secrets.
func (s *InMemoryCredentialStore) List(ctx context.Context, options *authtypes.AuthOperationOptions) ([]authtypes.CredentialInfo, error) {
	signal := operationSignal(ctx, options)
	if err := throwIfAborted(signal); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	infos := make([]authtypes.CredentialInfo, 0, len(s.credentials))
	for providerID, credential := range s.credentials {
		infos = append(infos, authtypes.CredentialInfo{ProviderID: providerID, Type: credential.CredentialType()})
	}
	return infos, nil
}

// Modify serializes a read-modify-write through the only write path. fn sees
// the current credential because correct writes (refresh, login-during-refresh)
// depend on it. Returning nil leaves the entry unchanged. The post-write
// credential is returned.
func (s *InMemoryCredentialStore) Modify(ctx context.Context, providerID string, fn func(current authtypes.Credential) (authtypes.Credential, error), options *authtypes.AuthOperationOptions) (authtypes.Credential, error) {
	signal := operationSignal(ctx, options)
	if err := throwIfAborted(signal); err != nil {
		return nil, err
	}
	lock := s.lockFor(providerID)
	lock.Lock()
	defer lock.Unlock()
	if err := throwIfAborted(signal); err != nil {
		return nil, err
	}

	s.mu.Lock()
	current := s.credentials[providerID]
	s.mu.Unlock()

	next, err := fn(current)
	if err != nil {
		return nil, err
	}
	if err := throwIfAborted(signal); err != nil {
		return nil, err
	}
	if next != nil {
		s.mu.Lock()
		s.credentials[providerID] = next
		s.mu.Unlock()
		return next, nil
	}
	return current, nil
}

// Delete removes a credential (logout), serialized against Modify.
func (s *InMemoryCredentialStore) Delete(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) error {
	signal := operationSignal(ctx, options)
	if err := throwIfAborted(signal); err != nil {
		return err
	}
	lock := s.lockFor(providerID)
	lock.Lock()
	defer lock.Unlock()
	if err := throwIfAborted(signal); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.credentials, providerID)
	s.mu.Unlock()
	return nil
}
