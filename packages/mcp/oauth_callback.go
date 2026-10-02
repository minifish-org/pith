package mcp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OAuthCallback is an authorization response delivered to the loopback server.
type OAuthCallback struct {
	Code   string
	State  string
	ISS    string
	HasISS bool
}

// OAuthCallbackPage is the browser page shown after the redirect.
type OAuthCallbackPage struct {
	OK      bool
	Message string
	Details string
}

// OAuthCallbackServerOptions configures the loopback callback server.
type OAuthCallbackServerOptions struct {
	// Host is the listen address. Defaults to 127.0.0.1.
	Host string
	// RedirectHost is the host name placed in RedirectURL. Defaults to Host.
	RedirectHost string
	// Port is the listen port. Defaults to an ephemeral port.
	Port int
	// Path is the callback path. Defaults to /callback.
	Path string
	// Timeout bounds WaitForCallback. Defaults to 5 minutes.
	Timeout time.Duration
	// RenderPage renders the browser page as HTML instead of plain text.
	RenderPage func(OAuthCallbackPage) string
}

type callbackPending struct {
	ch    chan callbackResult
	timer *time.Timer
}

type callbackResult struct {
	callback OAuthCallback
	err      error
}

// OAuthCallbackServer receives an OAuth redirect on loopback.
type OAuthCallbackServer struct {
	RedirectURL string

	server     *http.Server
	listener   net.Listener
	path       string
	timeout    time.Duration
	renderPage func(OAuthCallbackPage) string

	mu      sync.Mutex
	pending map[string]*callbackPending
	closed  bool
}

func plainTextCallbackPage(page OAuthCallbackPage) string {
	if page.OK {
		return "Authorization complete. You may close this window."
	}
	if page.Details != "" {
		return page.Message + "\n\n" + page.Details
	}
	return page.Message
}

// NewOAuthCallbackServer starts a loopback callback server.
func NewOAuthCallbackServer(options *OAuthCallbackServerOptions) (*OAuthCallbackServer, error) {
	resolved := OAuthCallbackServerOptions{}
	if options != nil {
		resolved = *options
	}
	host := resolved.Host
	if host == "" {
		host = "127.0.0.1"
	}
	redirectHost := resolved.RedirectHost
	if redirectHost == "" {
		redirectHost = host
	}
	path := resolved.Path
	if path == "" {
		path = "/callback"
	}
	timeout := resolved.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(resolved.Port)))
	if err != nil {
		return nil, err
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("OAuth callback server did not bind to TCP")
	}
	displayHost := redirectHost
	if strings.Contains(displayHost, ":") && !strings.HasPrefix(displayHost, "[") {
		displayHost = "[" + displayHost + "]"
	}
	server := &OAuthCallbackServer{
		RedirectURL: "http://" + displayHost + ":" + strconv.Itoa(tcpAddress.Port) + path,
		listener:    listener,
		path:        path,
		timeout:     timeout,
		renderPage:  resolved.RenderPage,
		pending:     map[string]*callbackPending{},
	}
	server.server = &http.Server{Handler: http.HandlerFunc(server.handle)}
	go func() { _ = server.server.Serve(listener) }()
	return server, nil
}

// WaitForCallback waits for the authorization response for a state value.
func (s *OAuthCallbackServer) WaitForCallback(state string) (OAuthCallback, error) {
	s.mu.Lock()
	if _, exists := s.pending[state]; exists {
		s.mu.Unlock()
		return OAuthCallback{}, errors.New("OAuth state is already pending")
	}
	pending := &callbackPending{ch: make(chan callbackResult, 1)}
	pending.timer = time.AfterFunc(s.timeout, func() {
		s.mu.Lock()
		if current, ok := s.pending[state]; ok && current == pending {
			delete(s.pending, state)
		}
		s.mu.Unlock()
		pending.ch <- callbackResult{err: errors.New("OAuth callback timed out")}
	})
	s.pending[state] = pending
	s.mu.Unlock()
	result := <-pending.ch
	return result.callback, result.err
}

// Close stops the server and rejects every pending wait.
func (s *OAuthCallbackServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pending := s.pending
	s.pending = map[string]*callbackPending{}
	s.mu.Unlock()
	for _, entry := range pending {
		entry.timer.Stop()
		entry.ch <- callbackResult{err: errors.New("OAuth callback server closed")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *OAuthCallbackServer) reply(response http.ResponseWriter, status int, page OAuthCallbackPage) {
	if s.renderPage != nil {
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(status)
		_, _ = response.Write([]byte(s.renderPage(page)))
		return
	}
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(status)
	_, _ = response.Write([]byte(plainTextCallbackPage(page)))
}

func (s *OAuthCallbackServer) handle(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != s.path {
		s.reply(response, http.StatusNotFound, OAuthCallbackPage{Message: "Not found"})
		return
	}
	state := request.URL.Query().Get("state")
	s.mu.Lock()
	pending := s.pending[state]
	if pending != nil {
		delete(s.pending, state)
	}
	s.mu.Unlock()
	if state == "" || pending == nil {
		s.reply(response, http.StatusBadRequest, OAuthCallbackPage{Message: "Invalid or expired OAuth state"})
		return
	}
	pending.timer.Stop()
	if errorCode := request.URL.Query().Get("error"); errorCode != "" {
		description := request.URL.Query().Get("error_description")
		if description == "" {
			description = errorCode
		}
		pending.ch <- callbackResult{err: errors.New(description)}
		s.reply(response, http.StatusOK, OAuthCallbackPage{
			Message: "Authorization failed. You may close this window.",
			Details: description,
		})
		return
	}
	code := request.URL.Query().Get("code")
	if code == "" {
		pending.ch <- callbackResult{err: errors.New("OAuth callback did not include an authorization code")}
		s.reply(response, http.StatusBadRequest, OAuthCallbackPage{Message: "Missing authorization code"})
		return
	}
	callback := OAuthCallback{Code: code, State: state}
	if iss := request.URL.Query().Get("iss"); iss != "" {
		callback.ISS = iss
		callback.HasISS = true
	}
	pending.ch <- callbackResult{callback: callback}
	s.reply(response, http.StatusOK, OAuthCallbackPage{OK: true})
}
