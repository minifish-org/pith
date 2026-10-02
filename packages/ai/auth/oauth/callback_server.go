// This file is a Go port of packages/ai/src/auth/oauth/callback-server.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Loopback OAuth redirect handler shared by the browser sign-in flows. The
// upstream module uses node:http; the Go port binds a loopback TCP listener
// with net/http.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// OAuthCallbackServerOptions configures a loopback OAuth redirect server.
type OAuthCallbackServerOptions[T any] struct {
	// ProviderName is shown on the browser page, for example "OpenAI".
	ProviderName string
	// Host is the address to listen on.
	Host string
	// Port is the port to listen on; 0 picks a free port.
	Port int
	// Path is the callback path.
	Path string
	// RedirectHost is the host in redirectUri when it differs from Host, for
	// example "localhost". Empty means Host.
	RedirectHost string
	// State is the expected state parameter. nil when the provider sends none.
	State *string
	// Complete finishes the sign-in with the received code before the browser
	// page is sent, so the page can show exchange failures.
	Complete func(ctx context.Context, code string) (T, error)
	// Signal cancels the wait; nil means no cancellation.
	Signal context.Context
	// Timeout bounds how long to wait; 0 means no timeout.
	Timeout time.Duration
}

// OAuthCallbackServer is a running loopback redirect handler.
type OAuthCallbackServer[T any] struct {
	// RedirectURI is the browser redirect target.
	RedirectURI string

	mu       sync.Mutex
	claimed  bool
	settled  bool
	value    *T
	waitErr  error
	done     chan struct{}
	stopFunc []func() bool
	server   *http.Server
	listener net.Listener
}

// Wait resolves with the result of Complete, or (nil, nil) after Cancel. It
// returns an error when the provider redirects with an error, Complete fails,
// the signal aborts, or the timeout elapses.
func (s *OAuthCallbackServer[T]) Wait() (*T, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, s.waitErr
}

// Cancel stops waiting for the browser unless a callback is already being
// completed.
func (s *OAuthCallbackServer[T]) Cancel() {
	s.mu.Lock()
	claimed := s.claimed
	settled := s.settled
	s.mu.Unlock()
	if !claimed && !settled {
		s.finish(nil, nil)
	}
}

// Close stops the server and fails a still-pending wait.
func (s *OAuthCallbackServer[T]) Close() {
	s.finish(nil, errors.New("OAuth callback server closed"))
	if s.server != nil {
		_ = s.server.Close()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
}

func (s *OAuthCallbackServer[T]) finish(value *T, err error) {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.value = value
	s.waitErr = err
	stops := s.stopFunc
	s.stopFunc = nil
	s.mu.Unlock()
	for _, stop := range stops {
		if stop != nil {
			stop()
		}
	}
	close(s.done)
}

func sendCallbackPage(w http.ResponseWriter, status int, html string) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, html)
}

// StartOAuthCallbackServer binds a loopback listener and starts serving the
// browser callback.
func StartOAuthCallbackServer[T any](options OAuthCallbackServerOptions[T]) (*OAuthCallbackServer[T], error) {
	if options.Signal != nil && options.Signal.Err() != nil {
		return nil, errors.New("Login cancelled")
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(options.Host, strconv.Itoa(options.Port)))
	if err != nil {
		return nil, err
	}

	provider := options.ProviderName
	server := &OAuthCallbackServer[T]{
		done:     make(chan struct{}),
		listener: listener,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURL := r.URL
		if r.Method != http.MethodGet || requestURL.Path != options.Path {
			sendCallbackPage(w, http.StatusNotFound, utils.OAuthErrorHTML("Callback route not found.", nil))
			return
		}
		query := requestURL.Query()
		if options.State != nil && query.Get("state") != *options.State {
			sendCallbackPage(w, http.StatusBadRequest, utils.OAuthErrorHTML("State mismatch.", nil))
			return
		}
		server.mu.Lock()
		duplicate := server.claimed || server.settled
		server.mu.Unlock()
		if duplicate {
			sendCallbackPage(w, http.StatusConflict, utils.OAuthErrorHTML("This sign-in has already been handled.", nil))
			return
		}
		if providerError := query.Get("error"); providerError != "" {
			description := query.Get("error_description")
			if description == "" {
				description = providerError
			}
			sendCallbackPage(w, http.StatusBadRequest, utils.OAuthErrorHTML(provider+" authorization failed.", &description))
			server.finish(nil, fmt.Errorf("%s authorization failed: %s", provider, description))
			return
		}
		code := query.Get("code")
		if code == "" {
			sendCallbackPage(w, http.StatusBadRequest, utils.OAuthErrorHTML("Missing authorization code.", nil))
			return
		}
		server.mu.Lock()
		server.claimed = true
		server.mu.Unlock()

		completeCtx := options.Signal
		if completeCtx == nil {
			completeCtx = context.Background()
		}
		value, completeErr := options.Complete(completeCtx, code)
		if completeErr != nil {
			message := completeErr.Error()
			sendCallbackPage(w, http.StatusBadGateway, utils.OAuthErrorHTML(provider+" sign-in failed.", &message))
			server.finish(nil, completeErr)
			return
		}
		sendCallbackPage(w, http.StatusOK, utils.OAuthSuccessHTML("Signed in to "+provider+". You may now close this page."))
		server.finish(&value, nil)
	})

	server.server = &http.Server{Handler: handler}
	go func() {
		_ = server.server.Serve(listener)
	}()

	if options.Signal != nil {
		signal := options.Signal
		server.stopFunc = append(server.stopFunc, context.AfterFunc(signal, func() {
			server.finish(nil, errors.New("Login cancelled"))
		}))
	}
	if options.Timeout > 0 {
		timer := time.AfterFunc(options.Timeout, func() {
			server.finish(nil, fmt.Errorf("%s sign-in timed out", provider))
		})
		server.stopFunc = append(server.stopFunc, timer.Stop)
	}

	redirectHost := options.RedirectHost
	if redirectHost == "" {
		redirectHost = options.Host
	}
	host := redirectHost
	if strings.Contains(redirectHost, ":") {
		host = "[" + redirectHost + "]"
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		server.Close()
		return nil, errors.New("OAuth callback server did not bind to TCP")
	}
	server.RedirectURI = fmt.Sprintf("http://%s:%d%s", host, address.Port, options.Path)
	return server, nil
}

// OAuthManualPrompt is the fallback prompt shown when the loopback callback
// cannot be reached, for example over SSH.
type OAuthManualPrompt struct {
	Message     string
	Placeholder string
}

// Callback or manual result discriminators.
const (
	OAuthResultCallback = "callback"
	OAuthResultManual   = "manual"
)

// CallbackOrManualResult is the outcome of WaitForCallbackOrManualInput.
type CallbackOrManualResult[T any] struct {
	Type  string
	Value *T
	Input string
}

// WaitForCallbackOrManualInput waits for the browser callback, or for the user
// to paste the code or redirect URL when the browser cannot reach the loopback
// server. Without a callback server only the manual prompt is used.
func WaitForCallbackOrManualInput[T any](
	ctx context.Context,
	interaction authtypes.ProviderAuthInteraction,
	callback *OAuthCallbackServer[T],
	prompt OAuthManualPrompt,
) (CallbackOrManualResult[T], error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()

	type manualOutcome struct {
		input string
		err   error
	}
	manualCh := make(chan manualOutcome, 1)
	placeholder := prompt.Placeholder
	go func() {
		input, err := interaction.Prompt(manualCtx, authtypes.AuthPrompt{
			Type:        authtypes.AuthPromptManualCode,
			Message:     prompt.Message,
			Placeholder: &placeholder,
		})
		if callback != nil {
			callback.Cancel()
		}
		manualCh <- manualOutcome{input: input, err: err}
	}()

	if callback != nil {
		value, err := callback.Wait()
		if err != nil {
			return CallbackOrManualResult[T]{}, err
		}
		if value != nil {
			return CallbackOrManualResult[T]{Type: OAuthResultCallback, Value: value}, nil
		}
		outcome := <-manualCh
		if outcome.err != nil {
			return CallbackOrManualResult[T]{}, outcome.err
		}
		return CallbackOrManualResult[T]{Type: OAuthResultManual, Input: outcome.input}, nil
	}

	outcome := <-manualCh
	if outcome.err != nil {
		return CallbackOrManualResult[T]{}, outcome.err
	}
	return CallbackOrManualResult[T]{Type: OAuthResultManual, Input: outcome.input}, nil
}
