// Command durable is the minimal embedding example for the optional native Pith
// Durable SDK. It shows the full lifecycle an application uses:
//
//	open a storage adapter -> open the harness -> Root/CreateConversation ->
//	Submit -> Wait -> read the context -> Close
//
// It is offline by default: a scripted ModelRunner stands in for a provider and
// every printed reply is explicitly labelled OFFLINE. No provider is contacted
// and no cost is incurred. A host that has a Pith model/provider configuration
// can pass -offline=false with -provider/-model/-base-url/-api-key-env to run
// the same flow through the production harness.NewPithModelRunner adapter.
//
//	go run ./examples/durable                       # offline scripted, memory
//	go run ./examples/durable -backend sqlite -state ./durable.sqlite
//	go run ./examples/durable -offline=false -provider openai -model gpt-4o-mini \
//	    -api-key-env OPENAI_API_KEY
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
	h "github.com/minifish-org/pith/packages/durable/harness"
	"github.com/minifish-org/pith/packages/durable/storage/jsonl"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
	"github.com/minifish-org/pith/packages/durable/storage/sqlite"
	"github.com/minifish-org/pith/packages/durable/tools"
)

// config is the resolved command configuration.
type config struct {
	Offline   bool
	Backend   string
	State     string
	Provider  string
	ModelID   string
	BaseURL   string
	APIKeyEnv string
	Prompt    string
}

func main() {
	cfg := config{}
	flag.BoolVar(&cfg.Offline, "offline", true, "use the labelled offline scripted runner (no provider, no cost)")
	flag.StringVar(&cfg.Backend, "backend", "memory", "storage backend: memory, jsonl or sqlite")
	flag.StringVar(&cfg.State, "state", "", "state file/directory for jsonl or sqlite (default: a temp path)")
	flag.StringVar(&cfg.Provider, "provider", "openai-compatible", "provider id for live mode")
	flag.StringVar(&cfg.ModelID, "model", "gpt-4o-mini", "model id for live mode")
	flag.StringVar(&cfg.BaseURL, "base-url", "https://api.openai.com/v1", "provider base URL for live mode")
	flag.StringVar(&cfg.APIKeyEnv, "api-key-env", "OPENAI_API_KEY", "environment variable holding the API key for live mode")
	flag.StringVar(&cfg.Prompt, "prompt", "Summarize what the Durable SDK does in one sentence.", "user prompt")
	flag.Parse()

	// A context with a deadline is a hang detector for the example, not an
	// unattended run limit.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := run(ctx, cfg, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "durable example:", err)
		os.Exit(1)
	}
}

// run performs the example lifecycle and is reused by main_test.go.
func run(ctx context.Context, cfg config, out io.Writer) error {
	sink := &syncWriter{writer: out, mutex: &sync.Mutex{}}
	if cfg.Offline {
		fmt.Fprintln(sink, "[durable] OFFLINE mode: a scripted runner answers; no provider is contacted and no model usage or cost is real.")
	} else {
		fmt.Fprintf(sink, "[durable] LIVE mode: provider=%s model=%s\n", cfg.Provider, cfg.ModelID)
	}

	storage, closeStorage, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStorage()

	runner, err := modelRunner(cfg)
	if err != nil {
		return err
	}

	// A registry holds the built-in tasks plus explicitly installed extensions.
	// Coding tools are never installed implicitly; this example opts in.
	registry := h.CreateRegistry()
	if err := registry.Install(tools.CodingTools()); err != nil {
		return fmt.Errorf("install coding tools: %w", err)
	}

	harness, err := h.Open(ctx, storage, h.Options{
		Models:   runner,
		Registry: registry,
		Settings: func() h.Settings { return h.DefaultSettings() },
		Env: func(ctx context.Context, target h.EnvTarget) (env.ExecutionEnv, error) {
			cwd := target.CWD
			if cwd == "" {
				cwd = "."
			}
			return env.NewLocal(cwd)
		},
		OnReport: func(err error) { fmt.Fprintln(sink, "[durable] report:", err) },
	})
	if err != nil {
		return err
	}
	defer func() { _ = harness.Close(context.Background()) }()

	// SubscribeCommits observes committed storage publications. It never fires
	// for uncommitted drafts.
	unsubscribe := harness.SubscribeCommits(func(publication durable.CommitPublication) {
		fmt.Fprintf(sink, "[commit] seq=%d changes=%d\n", publication.Seq, len(publication.Changes))
	})
	defer unsubscribe()

	agent := json.RawMessage(fmt.Sprintf(`{"model":{"provider":%q,"modelId":%q}}`, cfg.Provider, cfg.ModelID))
	conversation, err := harness.Root(ctx, h.RootOptions{Agent: agent})
	if err != nil {
		return err
	}
	fmt.Fprintf(sink, "[durable] root conversation id=%d\n", conversation.ID())

	// A structural watch mirrors committed conversation state. Watch callbacks
	// serialize on a delivery line and never run inline.
	watch, err := conversation.Watch(ctx)
	if err != nil {
		return err
	}
	if err := watch.Start(func(ctx context.Context, view h.ConversationView, ops []chord.Op) error {
		fmt.Fprintf(sink, "[watch] entries=%d ops=%d\n", len(view.Entries), len(ops))
		return nil
	}); err != nil {
		return err
	}
	defer func() { _ = watch.Stop() }()

	draft := h.SubmissionDraft{Type: "input", Content: types.UserContentText(cfg.Prompt), RequestID: "example-request"}
	submission, err := conversation.Submit(ctx, draft)
	if err != nil {
		return err
	}
	record, err := submission.Wait(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(sink, "[durable] submission %d status=%s\n", submission.ID(), record.Status)

	// A committed request ID deduplicates a repeated submission in the same
	// conversation; the second Submit returns the same handle.
	again, err := conversation.Submit(ctx, draft)
	if err != nil {
		return err
	}
	if again.ID() != submission.ID() {
		return fmt.Errorf("request dedup: %d != %d", again.ID(), submission.ID())
	}
	fmt.Fprintf(sink, "[durable] deduplicated submission id=%d\n", again.ID())

	view, err := conversation.Context(ctx)
	if err != nil {
		return err
	}
	reply := lastAssistantText(view.Messages)
	if reply == "" {
		return fmt.Errorf("no assistant reply recorded")
	}
	fmt.Fprintf(sink, "[durable] assistant reply: %s\n", reply)
	fmt.Fprintf(sink, "[durable] active messages=%d entries=%d\n", len(view.Messages), len(view.Entries))
	return nil
}

func openStorage(ctx context.Context, cfg config) (durable.Storage, func(), error) {
	switch cfg.Backend {
	case "", "memory":
		storage := memory.New()
		return storage, func() { _ = storage.Close(context.Background()) }, nil
	case "jsonl":
		dir := cfg.State
		if dir == "" {
			var err error
			dir, err = os.MkdirTemp("", "durable-example-jsonl-")
			if err != nil {
				return nil, nil, err
			}
		}
		storage, err := jsonl.Open(ctx, dir, jsonl.Options{Fsync: true})
		if err != nil {
			return nil, nil, err
		}
		return storage, func() { _ = storage.Close(context.Background()) }, nil
	case "sqlite":
		path := cfg.State
		if path == "" {
			file, err := os.CreateTemp("", "durable-example-*.sqlite")
			if err != nil {
				return nil, nil, err
			}
			_ = file.Close()
			path = file.Name()
		}
		storage, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			return nil, nil, err
		}
		return storage, func() { _ = storage.Close(context.Background()) }, nil
	default:
		return nil, nil, fmt.Errorf("unknown backend %q", cfg.Backend)
	}
}

func modelRunner(cfg config) (h.ModelRunner, error) {
	if cfg.Offline {
		return &offlineRunner{model: &types.Model{
			Id:            "offline-scripted",
			Name:          "offline-scripted",
			Api:           types.ApiOpenAICompletions,
			Provider:      types.ProviderId(cfg.Provider),
			ContextWindow: 128000,
			MaxTokens:     4096,
			Input:         []types.ModelInputModality{types.ModelInputText},
		}}, nil
	}
	key := os.Getenv(cfg.APIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("live mode requires %s to be set (or use -offline=true)", cfg.APIKeyEnv)
	}
	return h.NewPithModelRunner(func(ctx context.Context, ref h.ModelRef) (*types.Model, error) {
		return &types.Model{
			Id:            cfg.ModelID,
			Name:          cfg.ModelID,
			Api:           types.ApiOpenAICompletions,
			Provider:      types.ProviderId(cfg.Provider),
			BaseUrl:       cfg.BaseURL,
			ContextWindow: 128000,
			MaxTokens:     4096,
			Input:         []types.ModelInputModality{types.ModelInputText},
		}, nil
	}, &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key}}}), nil
}

// offlineRunner is a scripted ModelRunner. It never contacts a provider.
type offlineRunner struct {
	model *types.Model
}

func (r *offlineRunner) Resolve(context.Context, h.ModelRef) (*types.Model, error) {
	return r.model, nil
}

func (r *offlineRunner) Run(_ context.Context, request h.ModelRequest, onUpdate func(types.AssistantMessage) error) (types.AssistantMessage, error) {
	message := types.NewAssistantMessage(r.model.Api, r.model.Provider, r.model.Id, float64(time.Now().UnixMilli()))
	message.Content = []types.ContentBlock{types.TextBlock(fmt.Sprintf(
		"(OFFLINE scripted reply; no provider contacted, no real usage or cost) I received %d context messages and %d tool declarations.",
		len(request.Messages), len(request.Tools),
	))}
	message.StopReason = types.StopReasonStop
	if onUpdate != nil {
		if err := onUpdate(message); err != nil {
			return message, err
		}
	}
	return message, nil
}

func lastAssistantText(messages []types.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		assistant := messages[index].Assistant
		if assistant == nil {
			continue
		}
		var builder strings.Builder
		for _, block := range assistant.Content {
			if block.Type == types.ContentTypeText && block.Text != nil {
				builder.WriteString(block.Text.Text)
			}
		}
		if builder.Len() > 0 {
			return builder.String()
		}
	}
	return ""
}

// syncWriter serializes writes from the watch callback and the main goroutine so
// the example is safe under the race detector.
type syncWriter struct {
	writer io.Writer
	mutex  *sync.Mutex
}

func (w *syncWriter) Write(data []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.writer.Write(data)
}
