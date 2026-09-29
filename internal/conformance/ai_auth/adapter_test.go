package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"

	"github.com/minifish-org/pith/packages/ai/auth"
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op      string `json:"op"`
		File    string `json:"file"`
		Aborted bool   `json:"aborted"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "credentials":
		return runCredentialsCase(ctx, envelope.Aborted)
	case "pkce":
		return runPKCECase()
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

// runCredentialsCase drives the real InMemoryCredentialStore through the
// serialized read-modify-write, list, no-op modify and delete sequence.
func runCredentialsCase(ctx context.Context, aborted bool) (json.RawMessage, error) {
	store := auth.NewInMemoryCredentialStore()

	if aborted {
		abortedCtx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := store.Read(abortedCtx, "p", nil); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("conformance: aborted read unexpectedly succeeded")
	}

	if _, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
		key := "first"
		return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &key}, nil
	}, nil); err != nil {
		return nil, err
	}

	// Two concurrent appends. The first append is started and observed running
	// before the second is issued, which reproduces the deterministic enqueue
	// order of the upstream Promise.all fixture.
	firstStarted := make(chan struct{})
	var startedOnce sync.Once
	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, 2)

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		_, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
			startedOnce.Do(func() { close(firstStarted) })
			credential, ok := current.(*authtypes.ApiKeyCredential)
			if !ok || credential.Key == nil {
				return nil, fmt.Errorf("conformance: unexpected credential type")
			}
			key := *credential.Key + "-second"
			return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &key}, nil
		}, nil)
		errorsCh <- err
	}()
	<-firstStarted

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		_, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
			credential, ok := current.(*authtypes.ApiKeyCredential)
			if !ok || credential.Key == nil {
				return nil, fmt.Errorf("conformance: unexpected credential type")
			}
			key := *credential.Key + "-third"
			return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: &key}, nil
		}, nil)
		errorsCh <- err
	}()

	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			return nil, err
		}
	}

	read, err := store.Read(ctx, "p", nil)
	if err != nil {
		return nil, err
	}
	list, err := store.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := store.Modify(ctx, "p", func(current authtypes.Credential) (authtypes.Credential, error) {
		return nil, nil
	}, nil); err != nil {
		return nil, err
	}
	retained, err := store.Read(ctx, "p", nil)
	if err != nil {
		return nil, err
	}
	if err := store.Delete(ctx, "p", nil); err != nil {
		return nil, err
	}
	deleted, err := store.Read(ctx, "p", nil)
	if err != nil {
		return nil, err
	}

	encodedList, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	output := struct {
		Read     json.RawMessage `json:"read"`
		List     json.RawMessage `json:"list"`
		Retained json.RawMessage `json:"retained"`
		Deleted  json.RawMessage `json:"deleted"`
	}{
		Read:     encodeCredential(read),
		List:     encodedList,
		Retained: encodeCredential(retained),
		Deleted:  encodeCredential(deleted),
	}
	return json.Marshal(output)
}

func encodeCredential(credential authtypes.Credential) json.RawMessage {
	if credential == nil {
		return json.RawMessage(`{"$undefined":true}`)
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return json.RawMessage(`{"$undefined":true}`)
	}
	return encoded
}

var urlSafePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// runPKCECase generates two verifiers through the real PKCE implementation and
// reports the shape the evaluator checks.
func runPKCECase() (json.RawMessage, error) {
	first, err := oauth.GeneratePKCE()
	if err != nil {
		return nil, err
	}
	second, err := oauth.GeneratePKCE()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(first.Verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	output := struct {
		Length           int  `json:"length"`
		URLSafe          bool `json:"urlSafe"`
		ChallengeMatches bool `json:"challengeMatches"`
		Distinct         bool `json:"distinct"`
	}{
		Length:           len(first.Verifier),
		URLSafe:          urlSafePattern.MatchString(first.Verifier),
		ChallengeMatches: expectedChallenge == first.Challenge,
		Distinct:         first.Verifier != second.Verifier,
	}
	return json.Marshal(output)
}
