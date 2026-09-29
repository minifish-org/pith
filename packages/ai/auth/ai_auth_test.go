package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

type fakeAuthContext struct {
	values map[string]string
	files  map[string]bool
}

func (f fakeAuthContext) Env(name string) (*string, error) {
	if value, ok := f.values[name]; ok {
		return &value, nil
	}
	return nil, nil
}

func (f fakeAuthContext) FileExists(path string) (bool, error) {
	return f.files[path], nil
}

// Writes are serialized per provider; a no-op modify leaves the entry
// unchanged, and delete removes it.
func TestInMemoryCredentialStoreModifyListDelete(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCredentialStore()

	if _, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
		return authtypes.NewApiKeyCredential("first"), nil
	}, nil); err != nil {
		t.Fatal(err)
	}

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	for _, suffix := range []string{"-second", "-third"} {
		suffix := suffix
		go func() {
			defer waitGroup.Done()
			_, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
				credential := current.(*authtypes.ApiKeyCredential)
				key := *credential.Key + suffix
				return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &key}, nil
			}, nil)
			if err != nil {
				t.Errorf("modify %s: %v", suffix, err)
			}
		}()
	}
	waitGroup.Wait()

	read, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	key := read.(*authtypes.ApiKeyCredential).Key
	if key == nil || !strings.HasPrefix(*key, "first") || strings.Count(*key, "-") != 2 {
		t.Fatalf("serialized read = %v", key)
	}

	list, err := store.List(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ProviderID != "p" || list[0].Type != authtypes.CredentialTypeAPIKey {
		t.Fatalf("list = %v", list)
	}

	if _, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
		return nil, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if retained == nil {
		t.Fatal("no-op modify removed the credential")
	}

	if err := store.Delete(ctx, "p", nil); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Read(ctx, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("deleted credential still present: %v", deleted)
	}
}

// An already-aborted operation rejects instead of reading or writing.
func TestInMemoryCredentialStoreAborted(t *testing.T) {
	aborted, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewInMemoryCredentialStore()
	if _, err := store.Read(aborted, "p", nil); err == nil {
		t.Fatal("aborted read succeeded")
	}
	if _, err := store.Modify(aborted, "p", func(authtypes.Credential) (authtypes.Credential, error) {
		return authtypes.NewApiKeyCredential("x"), nil
	}, nil); err == nil {
		t.Fatal("aborted modify succeeded")
	}
}

// EnvApiKeyAuth: a stored key wins, otherwise the first set env var resolves.
func TestEnvApiKeyAuthResolution(t *testing.T) {
	auth := EnvApiKeyAuth("Anthropic API key", []string{"FIRST_KEY", "SECOND_KEY"})
	ctx := fakeAuthContext{values: map[string]string{"SECOND_KEY": "from-env"}}

	storedKey := "stored"
	result, err := auth.Resolve(context.Background(), authtypes.ApiKeyResolveInput{
		Ctx:        ctx,
		Credential: &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &storedKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "stored" || result.Source == nil || *result.Source != "stored credential" {
		t.Fatalf("stored resolution = %#v", result)
	}

	result, err = auth.Resolve(context.Background(), authtypes.ApiKeyResolveInput{Ctx: ctx})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "from-env" || result.Source == nil || *result.Source != "SECOND_KEY" {
		t.Fatalf("env resolution = %#v", result)
	}

	empty := fakeAuthContext{}
	result, err = auth.Resolve(context.Background(), authtypes.ApiKeyResolveInput{Ctx: empty})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("unconfigured provider resolved: %#v", result)
	}
}

// LazyOAuth loads the flow once and forwards every operation.
func TestLazyOAuthLoadsOnce(t *testing.T) {
	var loads int32
	flow := &authtypes.OAuthAuth{
		Name: "Deferred",
		ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
			return authtypes.ModelAuth{APIKey: &credential.Access}, nil
		},
	}
	lazy := LazyOAuth(LazyOAuthInput{
		Name: "Deferred",
		Load: func() (*authtypes.OAuthAuth, error) {
			atomic.AddInt32(&loads, 1)
			return flow, nil
		},
	})
	for i := 0; i < 3; i++ {
		result, err := lazy.ToAuth(context.Background(), &authtypes.OAuthCredential{
			OAuthCredentials: authtypes.OAuthCredentials{Access: "token"},
			Type:             authtypes.CredentialTypeOAuth,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.APIKey == nil || *result.APIKey != "token" {
			t.Fatalf("unexpected lazy result: %#v", result)
		}
	}
	if atomic.LoadInt32(&loads) != 1 {
		t.Fatalf("load count = %d; want 1", loads)
	}
}

// A stored credential owns the provider, so ambient env is only consulted when
// nothing is stored.
func TestResolveProviderAuthStoredCredentialWins(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCredentialStore()
	storedKey := "stored-key"
	if _, err := store.Modify(ctx, "anthropic", func(authtypes.Credential) (authtypes.Credential, error) {
		return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &storedKey}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	provider := AuthProviderSpec{
		ID: "anthropic",
		Auth: authtypes.ProviderAuth{
			APIKey: apiKeyAuthPtr(EnvApiKeyAuth("Anthropic API key", []string{"ANTHROPIC_API_KEY"})),
		},
	}
	authContext := fakeAuthContext{values: map[string]string{"ANTHROPIC_API_KEY": "env-key"}}

	result, err := ResolveProviderAuth(ctx, provider, store, authContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "stored-key" {
		t.Fatalf("stored credential did not win: %#v", result)
	}

	if err := store.Delete(ctx, "anthropic", nil); err != nil {
		t.Fatal(err)
	}
	result, err = ResolveProviderAuth(ctx, provider, store, authContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "env-key" {
		t.Fatalf("ambient env not used after delete: %#v", result)
	}
}

// Concurrent resolution of an expired OAuth credential refreshes exactly once.
func TestResolveProviderAuthConcurrentRefreshOnce(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCredentialStore()
	expired := authtypes.NewOAuthCredential("old-refresh", "old-access", 0)
	if _, err := store.Modify(ctx, "oauth-provider", func(authtypes.Credential) (authtypes.Credential, error) {
		return expired, nil
	}, nil); err != nil {
		t.Fatal(err)
	}

	var refreshCount int32
	flow := &authtypes.OAuthAuth{
		Name: "OAuth",
		Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
			atomic.AddInt32(&refreshCount, 1)
			time.Sleep(20 * time.Millisecond)
			return authtypes.NewOAuthCredential("new-refresh", "new-access", float64(time.Now().UnixMilli())+10*60*1000), nil
		},
		ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
			return authtypes.ModelAuth{APIKey: &credential.Access}, nil
		},
	}
	provider := AuthProviderSpec{ID: "oauth-provider", Auth: authtypes.ProviderAuth{OAuth: flow}}

	var waitGroup sync.WaitGroup
	results := make([]*authtypes.AuthResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		i := i
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results[i], errs[i] = ResolveProviderAuth(ctx, provider, store, fakeAuthContext{}, nil)
		}()
	}
	waitGroup.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("resolve %d: %v", i, errs[i])
		}
		if results[i] == nil || results[i].Auth.APIKey == nil || *results[i].Auth.APIKey != "new-access" {
			t.Fatalf("resolve %d = %#v", i, results[i])
		}
	}
	if count := atomic.LoadInt32(&refreshCount); count != 1 {
		t.Fatalf("refresh count = %d; want 1", count)
	}
}

// A failed refresh is an error and never falls back to ambient env credentials.
func TestResolveProviderAuthFailedRefreshDoesNotFallBack(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCredentialStore()
	if _, err := store.Modify(ctx, "anthropic", func(authtypes.Credential) (authtypes.Credential, error) {
		return authtypes.NewOAuthCredential("dead-refresh", "dead-access", 0), nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	flow := &authtypes.OAuthAuth{
		Name: "OAuth",
		Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
			return nil, errors.New("invalid_grant")
		},
		ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
			return authtypes.ModelAuth{APIKey: &credential.Access}, nil
		},
	}
	provider := AuthProviderSpec{
		ID: "anthropic",
		Auth: authtypes.ProviderAuth{
			APIKey: apiKeyAuthPtr(EnvApiKeyAuth("Anthropic API key", []string{"ANTHROPIC_API_KEY"})),
			OAuth:  flow,
		},
	}
	authContext := fakeAuthContext{values: map[string]string{"ANTHROPIC_API_KEY": "env-key"}}

	result, err := ResolveProviderAuth(ctx, provider, store, authContext, nil)
	if err == nil {
		t.Fatalf("failed refresh resolved: %#v", result)
	}
	var modelsErr *ModelsError
	if !errors.As(err, &modelsErr) || modelsErr.Code != ModelsErrorOAuth {
		t.Fatalf("error = %v; want ModelsError code oauth", err)
	}
}

// Explicit apiKey overrides skip stored credentials.
func TestResolveProviderAuthExplicitOverride(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryCredentialStore()
	override := "explicit-key"
	provider := AuthProviderSpec{
		ID:   "anthropic",
		Auth: authtypes.ProviderAuth{APIKey: apiKeyAuthPtr(EnvApiKeyAuth("Anthropic API key", []string{"ANTHROPIC_API_KEY"}))},
	}
	authContext := fakeAuthContext{values: map[string]string{"ANTHROPIC_API_KEY": "env-key"}}
	result, err := ResolveProviderAuth(ctx, provider, store, authContext, &AuthResolutionOverrides{APIKey: &override})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "explicit-key" {
		t.Fatalf("override result = %#v", result)
	}
}

func TestDefaultProviderAuthContext(t *testing.T) {
	t.Setenv("PITH_AUTH_TEST", "value")
	authContext := DefaultProviderAuthContext()
	value, err := authContext.Env("PITH_AUTH_TEST")
	if err != nil {
		t.Fatal(err)
	}
	if value == nil || *value != "value" {
		t.Fatalf("env = %v", value)
	}
	t.Setenv("PITH_AUTH_EMPTY", "   ")
	empty, err := authContext.Env("PITH_AUTH_EMPTY")
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Fatalf("whitespace env = %q; want nil", *empty)
	}
	missing, err := authContext.Env("PITH_AUTH_MISSING")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("missing env = %q; want nil", *missing)
	}
	exists, err := authContext.FileExists("ai_auth_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("ai_auth_test.go should exist relative to the package test")
	}
}

func apiKeyAuthPtr(auth authtypes.ApiKeyAuth) *authtypes.ApiKeyAuth { return &auth }
