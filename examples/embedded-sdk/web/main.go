// Command web embeds the SDK in an HTTP server with Server-Sent Events.
//
// It demonstrates per-user session isolation (one AgentSession per X-User
// header) and request cancellation (the http.Request context is passed to
// Prompt, so a disconnected client stops the provider call). It is an embedding
// sample, NOT an authentication or authorization system: the X-User header is
// trusted as-is and must be replaced by the caller's own identity layer.
//
// By default the agent uses an offline fake stream, so running the example
// makes no paid model call. Set PITH_SDK_LIVE=1 with OPENAI_API_KEY to use the
// native provider.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	codingagent "github.com/minifish-org/pith/packages/coding-agent"
)

type server struct {
	cwd string

	mu       sync.Mutex
	sessions map[string]*codingagent.AgentSession
}

func main() {
	addr := os.Getenv("PITH_SDK_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	cwd, _ := os.Getwd()
	srv := &server{cwd: cwd, sessions: map[string]*codingagent.AgentSession{}}
	defer srv.close()

	mux := http.NewServeMux()
	mux.HandleFunc("/chat", srv.handleChat)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "POST/GET /chat?message=... with an X-User header. Streams SSE.")
	})
	log.Printf("embedded SDK sample listening on http://%s", addr)
	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func (s *server) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.sessions {
		_ = session.Close()
	}
}

// sessionFor returns the caller's isolated session, creating it on first use.
func (s *server) sessionFor(user string) (*codingagent.AgentSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[user]; ok {
		return session, nil
	}
	session, err := codingagent.CreateAgentSession(s.sessionOptions())
	if err != nil {
		return nil, err
	}
	s.sessions[user] = session
	return session, nil
}

func (s *server) sessionOptions() codingagent.SessionOptions {
	options := codingagent.SessionOptions{
		Cwd:   s.cwd,
		Model: codingagent.ModelOptions{Model: fakeModel(), StreamFn: fakeStream("embedded reply")},
	}
	if os.Getenv("PITH_SDK_LIVE") == "1" {
		options.Model = codingagent.ModelOptions{
			Model: liveModel(),
			APIKey: func(context.Context, string) (string, error) {
				return os.Getenv("OPENAI_API_KEY"), nil
			},
		}
	}
	return options
}

func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	// Demo identity only. Replace with real authentication in production.
	user := strings.TrimSpace(r.Header.Get("X-User"))
	if user == "" {
		user = "anonymous"
	}
	message := r.URL.Query().Get("message")
	if message == "" {
		http.Error(w, "missing message", http.StatusBadRequest)
		return
	}

	session, err := s.sessionFor(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Serialize writes because the listener runs on the agent goroutine.
	var writeMu sync.Mutex
	writeEvent := func(event codingagent.SessionEvent) {
		writeMu.Lock()
		defer writeMu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: {\"type\":%q,\"tool\":%q}\n\n", event.Type, event.Type, event.ToolName)
		flusher.Flush()
	}
	unsubscribe := session.Subscribe(writeEvent)
	defer unsubscribe()

	// r.Context() is cancelled when the client disconnects; Prompt propagates
	// that to the provider request, tools and retry backoff.
	result, err := session.Prompt(r.Context(), message)
	if err != nil {
		writeMu.Lock()
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		flusher.Flush()
		writeMu.Unlock()
		return
	}

	writeMu.Lock()
	fmt.Fprintf(w, "event: done\ndata: {\"stopReason\":%q,\"turns\":%d}\n\n", result.StopReason, result.Turns)
	flusher.Flush()
	writeMu.Unlock()
}

func fakeModel() *aitypes.Model {
	return &aitypes.Model{
		Id: "fake-model", Name: "fake-model",
		Api: aitypes.ApiOpenAICompletions, Provider: aitypes.ProviderOpenAI,
		BaseUrl: "http://localhost.invalid/v1", ContextWindow: 128000, MaxTokens: 4096,
	}
}

func liveModel() *aitypes.Model {
	id := os.Getenv("PITH_SDK_MODEL")
	if id == "" {
		id = "gpt-4o-mini"
	}
	model := fakeModel()
	model.Id = id
	model.Name = id
	model.BaseUrl = "https://api.openai.com/v1"
	return model
}

func fakeStream(text string) agenttypes.StreamFn {
	return func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
		message.Content = []aitypes.ContentBlock{aitypes.TextBlock(text)}
		message.StopReason = aitypes.StopReasonStop
		message.Usage = aitypes.Usage{Input: 8, Output: 4, TotalTokens: 12}
		stream.Push(aitypes.NewDoneEvent(aitypes.StopReasonStop, message))
		return stream
	}
}
