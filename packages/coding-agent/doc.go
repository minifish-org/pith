// Package codingagent is the embedded, headless Go SDK for the Pith coding
// agent. It is a Go port of the Pi coding-agent core (revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe) and reuses the accepted Pith AI,
// agent-core, harness, session/compaction and tool packages as dependencies.
//
// # What this package is
//
// The package embeds an agent loop directly in a Go application. There is no
// subprocess, no Node/Pi binary and no IPC boundary between the caller and the
// agent: CreateAgentSession returns an *AgentSession whose Prompt method runs
// the provider request and the tool loop in-process.
//
// The public entry point is:
//
//	session, err := codingagent.CreateAgentSession(codingagent.SessionOptions{
//	    Cwd:   "/path/to/project",
//	    Model: codingagent.ModelOptions{Model: model, APIKey: keyCallback},
//	})
//	defer session.Close()
//	result, err := session.Prompt(ctx, "fix the failing test")
//
// All inputs are explicit. The SDK never discovers an agent directory, a
// credentials file or a model from a process-global location unless the caller
// supplies the path. There is no global model registration and no process-wide
// default stream function, so two sessions in the same process cannot
// interfere through package state.
//
// # Supported providers
//
// A ModelOptions.Model is an *aitypes.Model from the accepted Pith AI catalog.
// When ModelOptions.StreamFn is nil the SDK resolves the native provider for
// the model's Api through the existing Pith AI API registry (OpenAI Chat
// Completions/Responses, Anthropic Messages, Google Generative AI/Vertex,
// Mistral Conversations, Bedrock Converse, Azure/Codex Responses and Pi
// Messages). A caller that needs a custom transport can set StreamFn
// explicitly; a caller that needs a custom endpoint can set the model's
// BaseUrl. Credentials are supplied through ModelOptions.APIKey, which runs on
// every request and never comes from a global cache.
//
// # Lifecycle and ownership
//
// CreateAgentSession owns the resources it creates. When SessionOptions.Manager
// is nil the session opens an in-memory manager and closes it in Close. When
// the caller supplies a Manager the caller owns it and must close it. The
// caller also owns the context passed to Prompt, any credential callback, and
// any custom tool implementation.
//
// Close is idempotent with respect to the owned manager and releases the
// session's subscriptions. A session is single-run: Prompt returns
// ErrAgentSessionBusy if a run is already active. Mutation methods such as
// SetModel and SetActiveTools are rejected while a run is in flight.
//
// # Concurrency
//
// A single AgentSession serializes Prompt. Subscribe, Steer, FollowUp, Abort,
// Messages and Stats are safe to call from other goroutines. Steer and FollowUp
// enqueue messages for the active run; Abort cancels the active run's provider
// request, tool execution and retry backoff. Subscriber callbacks run outside
// the session lock, so a listener may unsubscribe or inspect a snapshot without
// deadlocking.
//
// # Cancellation
//
// Cancellation is context based and propagates to the provider HTTP request,
// running tools and child processes started by the bash tool. Cancel the
// context passed to Prompt to stop a run; call Abort to stop without a
// context. A run that is cancelled returns a result whose StopReason is
// aborted (or an error), and the session remains usable for later prompts.
//
// # Limits
//
// The SDK imposes no implicit per-run turn, time or file-size cap. RunPolicy
// lets the caller bound turns, retry attempts, retry delay and automatic
// compaction explicitly. Summary generation is caller-provided through
// RunPolicy.Summarize. Length-truncated assistant responses are continued
// rather than treated as complete, and an incomplete tool-argument payload is
// never executed.
//
// # Privacy
//
// The SDK reads only the files named by ResourceOptions and the project
// working directory needed by the enabled tools. It does not upload files
// anywhere by itself. Credential callbacks and prompts are the caller's
// responsibility; errors surfaced by the SDK are sanitized diagnostics and do
// not include credential values. The web example demonstrates embedding, not
// an authentication or authorization system.
//
// # Failures
//
// Provider and tool failures are returned as Go errors or encoded in the
// assistant message's stop reason. Tool calls and tool results are always
// paired in the persisted transcript, and turn boundaries are durable. A
// transient provider error can be retried according to RunPolicy; retry
// exhaustion is reported to the caller.
//
// # Exclusions
//
// This increment is headless. TS extension execution, JS execution plugins,
// TUI widgets and themes, npm/Git extension package installation, remote
// service management, RPC host mode and browser login UI are out of scope.
// Plain-text skills, project context files and prompt templates remain
// resources. Custom Go tools and ToolHooks replace JS execution plugins.
// Credential callbacks and provider selection are in scope. See
// docs/sdk/compatibility.md for the frozen compatibility table.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent
