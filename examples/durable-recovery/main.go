// Command durable-recovery demonstrates crash/restart and close/reopen recovery
// for the native Pith Durable SDK.
//
// The demo is fully offline: a scripted model runner stands in for a provider,
// so no credentials or network are required. Its scripted replies carry a
// synthetic usage block only so the transcript is well formed; it is not a real
// provider cost. A host that wants a real model can replace scriptedRunner with
// harness.NewPithModelRunner over a Pith provider.
//
// Default mode runs two demonstrations:
//
//		go run ./examples/durable-recovery
//		go run ./examples/durable-recovery -backend jsonl
//		go run ./examples/durable-recovery -replay unsafe
//
//	 1. crash/restart: the process re-execs itself as a child, lets a tool start
//	    an external effect and acknowledge it through Details(), then kills the
//	    child before an outcome can be written. A second process reopens the same
//	    store and finishes the run. With replay=safe the tool runs again and the
//	    external idempotency key deduplicates the effect; with replay=unsafe the
//	    interrupted receipt is returned unchanged. This is the intent/effect/
//	    outcome contract: the effect can happen before a crash, so a safe rerun
//	    still needs an external idempotency key and is never a generic
//	    exactly-once guarantee.
//
//	 2. close/reopen ticker: a durable task survives a Harness close in the
//	    middle of its life. Its checkpoint records the next step and a memo
//	    records that a step was already reported, so reopening continues without
//	    printing a repeated line.
//
// The -phase first|recovered modes are internal child roles used by the crash
// demo; running them directly is only useful for debugging.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
	jsonl "github.com/minifish-org/pith/packages/durable/storage/jsonl"
	sqlite "github.com/minifish-org/pith/packages/durable/storage/sqlite"
)

func main() {
	backend := flag.String("backend", "sqlite", "storage backend: sqlite or jsonl")
	replay := flag.String("replay", "safe", "tool replay policy: safe or unsafe")
	phase := flag.String("phase", "demo", "internal child phase: demo, first or recovered")
	dir := flag.String("dir", "", "state directory (internal child role)")
	flag.Parse()

	switch *phase {
	case "first", "recovered":
		if *dir == "" {
			fmt.Fprintln(os.Stderr, "child phase requires -dir")
			os.Exit(2)
		}
		if *replay != "safe" && *replay != "unsafe" {
			fmt.Fprintf(os.Stderr, "-replay must be safe or unsafe, got %q\n", *replay)
			os.Exit(2)
		}
		if err := child(*phase, *backend, *dir, *replay); err != nil {
			fmt.Fprintln(os.Stderr, "child:", err)
			os.Exit(1)
		}
	case "demo":
		if err := crashDemo(*backend, *replay); err != nil {
			fmt.Fprintln(os.Stderr, "crash demo:", err)
			os.Exit(1)
		}
		if err := tickerDemo(*backend); err != nil {
			fmt.Fprintln(os.Stderr, "ticker demo:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase %q\n", *phase)
		os.Exit(2)
	}
}

// openStore opens one durable backend at path. The path is a SQLite file, or a
// JSONL directory.
func openStore(ctx context.Context, backend, path string) (durable.Storage, error) {
	switch backend {
	case "sqlite":
		return sqlite.Open(ctx, path, sqlite.Options{})
	case "jsonl":
		return jsonl.Open(ctx, path, jsonl.Options{Fsync: true})
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// scriptedRunner is an offline stand-in for a provider. In the "first" child
// role it requests one tool call; everywhere else it returns a plain answer. A
// host replaces it with a runner backed by the Pith AI providers.
type scriptedRunner struct {
	phase string
	mu    sync.Mutex
	calls int
}

func fixtureModel() *aitypes.Model {
	var model aitypes.Model
	_ = json.Unmarshal([]byte(`{"id":"fixture","name":"fixture","api":"openai-completions","provider":"fixture","contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`), &model)
	return &model
}

func (r *scriptedRunner) Resolve(context.Context, h.ModelRef) (*aitypes.Model, error) {
	return fixtureModel(), nil
}

func (r *scriptedRunner) Run(context.Context, h.ModelRequest, func(aitypes.AssistantMessage) error) (aitypes.AssistantMessage, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if r.phase == "first" && call == 1 {
		return toolCallMessage("charge"), nil
	}
	return textMessage("recovered and finished"), nil
}

func textMessage(text string) aitypes.AssistantMessage {
	var message aitypes.AssistantMessage
	_ = json.Unmarshal(mustJSON(map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
		"api": "openai-completions", "provider": "fixture", "model": "fixture",
		"stopReason": "stop", "timestamp": 1,
		"usage": map[string]any{"input": 2, "output": 1, "totalTokens": 3, "cost": map[string]any{}},
	}), &message)
	return message
}

func toolCallMessage(name string) aitypes.AssistantMessage {
	var message aitypes.AssistantMessage
	_ = json.Unmarshal(mustJSON(map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": "call-1", "name": name, "arguments": map[string]any{}}},
		"api": "openai-completions", "provider": "fixture", "model": "fixture",
		"stopReason": "toolUse", "timestamp": 1,
	}), &message)
	return message
}

// effectState is the external service's state: it counts invocations and applies
// each idempotency key at most once. The key is what makes a retried effect safe.
type effectState struct {
	Calls   int            `json:"calls"`
	Effects map[string]int `json:"effects"`
}

func readEffectState(path string) (effectState, error) {
	state := effectState{Effects: map[string]int{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Effects == nil {
		state.Effects = map[string]int{}
	}
	return state, nil
}

// applyEffect performs the external effect. A second call with the same key
// records another invocation but does not repeat the effect.
func applyEffect(path, key string) (effectState, error) {
	state, err := readEffectState(path)
	if err != nil {
		return state, err
	}
	state.Calls++
	if _, ok := state.Effects[key]; !ok {
		state.Effects[key] = 1
	}
	data, err := json.Marshal(state)
	if err != nil {
		return state, err
	}
	temp := path + ".tmp"
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return state, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return state, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return state, err
	}
	if err := file.Close(); err != nil {
		return state, err
	}
	return state, os.Rename(temp, path)
}

// chargeTool is the simulated effectful tool. Its declaration and the committed
// checkpoint are what recovery replays; the closure only runs when the stored
// and current replay policies both allow it.
func chargeTool(phase, dir, replay string) h.ToolRegistration {
	return h.ToolRegistration{
		Declaration: aitypes.NewTool("charge", "Record an idempotent external charge",
			json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)),
		Replay: replay,
		Execute: func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
			key := fmt.Sprintf("charge-%d", api.TaskID())
			state, err := applyEffect(filepath.Join(dir, "effects.json"), key)
			if err != nil {
				return h.ToolResult{}, err
			}
			if err := api.Output([]byte("charge accepted\n")); err != nil {
				return h.ToolResult{}, err
			}
			if err := api.Details(ctx, mustJSON(map[string]any{"key": key, "calls": state.Calls, "effects": len(state.Effects)})); err != nil {
				return h.ToolResult{}, err
			}
			if phase == "first" {
				// Acknowledge after the effect and its intent are durable, then
				// block so the parent can kill the process at this boundary.
				fmt.Println("RECOVERY_EFFECT_ACK")
				<-time.After(10 * time.Minute)
			}
			return h.ToolResult{}, nil
		},
	}
}

func requestID() string { return "crash-restart" }

// child runs one process role of the crash demo.
func child(phase, backend, dir, replay string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store, err := openStore(ctx, backend, filepath.Join(dir, "state"))
	if err != nil {
		return err
	}
	registry := h.CreateRegistry()
	if err := registry.Install(h.Extension{Name: "charge", Tools: []h.ToolRegistration{chargeTool(phase, dir, replay)}}); err != nil {
		return err
	}
	harness, err := h.Open(ctx, store, h.Options{Registry: registry, Models: &scriptedRunner{phase: phase}})
	if err != nil {
		return err
	}
	defer harness.Close(context.Background())

	root, err := harness.Root(ctx, h.RootOptions{})
	if err != nil {
		return err
	}

	if phase == "first" {
		if err := root.Configure(ctx, mustJSON(map[string]any{"model": map[string]any{"provider": "fixture", "modelId": "fixture"}})); err != nil {
			return err
		}
		if _, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: aitypes.UserContentText("charge the account"), RequestID: requestID()}); err != nil {
			return err
		}
		<-time.After(10 * time.Minute)
		return nil
	}

	// Recovered: reacquire the acknowledged submission and wait for it to settle.
	// Waiting for an unfinished submission enables scheduling.
	var record *durable.SubmissionRecord
	if err := harness.Commit(ctx, func(tx durable.Tx) error {
		var lookup error
		record, lookup = tx.SubmissionByRequest(ctx, root.ID(), requestID())
		return lookup
	}); err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("acknowledged submission %q is missing", requestID())
	}
	submission, err := harness.Submission(ctx, record.ID)
	if err != nil {
		return err
	}
	settled, err := submission.Wait(ctx)
	if err != nil {
		return err
	}
	if settled.Status != "done" {
		return fmt.Errorf("submission settled as %q", settled.Status)
	}
	state, err := readEffectState(filepath.Join(dir, "effects.json"))
	if err != nil {
		return err
	}
	fmt.Printf("RECOVERY_DONE calls=%d effects=%d\n", state.Calls, len(state.Effects))
	return nil
}

// crashDemo orchestrates the kill/restart boundary using real subprocesses. The
// parent kills the child only after the child acknowledged its durable effect.
func crashDemo(backend, replay string) error {
	dir, err := os.MkdirTemp("", "durable-recovery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fmt.Printf("durable-recovery demo: backend=%s replay=%s\n", backend, replay)

	first := exec.CommandContext(ctx, executable, "-phase", "first", "-backend", backend, "-dir", dir, "-replay", replay)
	stdout, err := first.StdoutPipe()
	if err != nil {
		return err
	}
	first.Stderr = os.Stderr
	if err := first.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	acknowledged := false
	for scanner.Scan() {
		if scanner.Text() == "RECOVERY_EFFECT_ACK" {
			acknowledged = true
			break
		}
	}
	if !acknowledged {
		_ = first.Process.Kill()
		_ = first.Wait()
		return fmt.Errorf("child never acknowledged its durable effect: %v", scanner.Err())
	}
	if err := first.Process.Kill(); err != nil {
		return err
	}
	if err := first.Wait(); err == nil {
		return fmt.Errorf("child exited normally instead of being killed")
	}

	before, err := readEffectState(filepath.Join(dir, "effects.json"))
	if err != nil {
		return err
	}
	fmt.Printf("pre-kill: calls=%d effects=%d\n", before.Calls, len(before.Effects))
	if before.Calls != 1 || len(before.Effects) != 1 {
		return fmt.Errorf("external pre-kill state=%+v", before)
	}

	recovered := exec.CommandContext(ctx, executable, "-phase", "recovered", "-backend", backend, "-dir", dir, "-replay", replay)
	recovered.Stdout = os.Stdout
	recovered.Stderr = os.Stderr
	if err := recovered.Run(); err != nil {
		return fmt.Errorf("recovered child: %w", err)
	}

	after, err := readEffectState(filepath.Join(dir, "effects.json"))
	if err != nil {
		return err
	}
	fmt.Printf("recovered: calls=%d effects=%d\n", after.Calls, len(after.Effects))
	if len(after.Effects) != 1 {
		return fmt.Errorf("replay repeated an external effect: %v", after.Effects)
	}
	switch replay {
	case "safe":
		if after.Calls != 2 {
			return fmt.Errorf("safe replay did not rerun the invocation: calls=%d", after.Calls)
		}
	case "unsafe":
		if after.Calls != 1 {
			return fmt.Errorf("unsafe replay reran the invocation: calls=%d", after.Calls)
		}
	}
	fmt.Println("crash recovery complete")
	return nil
}

// tickerDemo closes a Harness in the middle of a durable task's life and reopens
// the same store, showing that the checkpoint and memo carry the work forward.
func tickerDemo(backend string) error {
	dir, err := os.MkdirTemp("", "durable-ticker-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ticksPath := filepath.Join(dir, "ticks.log")
	reached := make(chan int, 16)

	ticker := durable.TaskDefinition{
		Name:    "example.ticker",
		Version: 1,
		Initial: func(json.RawMessage) (json.RawMessage, error) {
			return mustJSON(map[string]any{"phase": "tick", "n": 1}), nil
		},
		Phases: map[string]durable.PhaseHandler{
			"tick": func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
				var checkpoint struct {
					N int `json:"n"`
				}
				_ = json.Unmarshal(task.State.Checkpoint, &checkpoint)
				var input struct {
					To int `json:"to"`
				}
				_ = json.Unmarshal(task.Input, &input)
				n := checkpoint.N

				// The memo is first-writer-wins: a step reported before a crash
				// is not reported again when the checkpoint is replayed.
				existing, err := r.Memo(ctx, fmt.Sprintf("printed-%d", n), nil)
				if err != nil {
					return err
				}
				if len(existing) == 0 {
					if _, err := r.Memo(ctx, fmt.Sprintf("printed-%d", n), mustJSON(true)); err != nil {
						return err
					}
					if err := appendLine(ticksPath, fmt.Sprintf("tick %d", n)); err != nil {
						return err
					}
				}
				select {
				case reached <- n:
				default:
				}

				if n >= input.To {
					return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
						current.State = durable.TaskState{
							Status:  durable.TaskStatusTerminal,
							Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(fmt.Sprintf("counted to %d", n))},
						}
						return nil
					})
				}
				if err := r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
					current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(map[string]any{"phase": "tick", "n": n + 1})}
					return nil
				}); err != nil {
					return err
				}
				return r.Sleep(ctx, r.Now()+2)
			},
		},
		Abort: func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
			return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
				current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeAborted}}
				return nil
			})
		},
	}

	open := func() (*h.Harness, error) {
		store, err := openStore(context.Background(), backend, filepath.Join(dir, "state"))
		if err != nil {
			return nil, err
		}
		registry := h.CreateRegistry()
		if err := registry.Install(h.Extension{Name: "ticker", Tasks: []durable.TaskDefinition{ticker}}); err != nil {
			return nil, err
		}
		return h.Open(context.Background(), store, h.Options{Registry: registry})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first, err := open()
	if err != nil {
		return err
	}
	root, err := first.Root(ctx, h.RootOptions{})
	if err != nil {
		return err
	}
	var id durable.TaskID
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		var createErr error
		id, createErr = tx.CreateTask(ctx, ticker, mustJSON(map[string]any{"to": 3}), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "conversation"}})
		return createErr
	}); err != nil {
		return err
	}
	if err := first.Resume(); err != nil {
		return err
	}

	// Close as soon as the first step is reported; the checkpoint from that step
	// may or may not have committed, and the memo makes either outcome safe.
	select {
	case <-reached:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := first.Close(ctx); err != nil {
		return err
	}
	fmt.Println("ticker: closed after the first reported step")

	second, err := open()
	if err != nil {
		return err
	}
	defer second.Close(context.Background())
	receipt, err := second.WaitForTask(ctx, id)
	if err != nil {
		return err
	}
	if receipt.State.Outcome == nil || receipt.State.Outcome.Status != durable.OutcomeCompleted {
		return fmt.Errorf("unexpected ticker outcome: %+v", receipt.State)
	}

	lines, err := readLines(ticksPath)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, line := range lines {
		if seen[line] {
			return fmt.Errorf("ticker repeated a step after reopen: %q", line)
		}
		seen[line] = true
	}
	fmt.Printf("ticker: reopened and completed (%d distinct steps)\n", len(lines))
	return nil
}

func appendLine(path, line string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line + "\n"); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, string(data[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines, nil
}
