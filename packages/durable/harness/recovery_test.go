package harness_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	chord "github.com/minifish-org/pith/packages/chord"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
	jsonl "github.com/minifish-org/pith/packages/durable/storage/jsonl"
	sqlite "github.com/minifish-org/pith/packages/durable/storage/sqlite"
)

// recoveryExternal is a small external service whose operations are idempotent
// by request key. It counts invocations separately from applied effects so a
// safe replay can run the tool again without repeating the effect.
type recoveryExternal struct {
	Calls   int            `json:"calls"`
	Effects map[string]int `json:"effects"`
}

func recoveryEffect(path, key string) (recoveryExternal, error) {
	state, err := recoveryReadEffects(path)
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
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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
	return state, file.Close()
}

func recoveryReadEffects(path string) (recoveryExternal, error) {
	state := recoveryExternal{Effects: map[string]int{}}
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

// recoveryStore opens one backend at path. SQLite uses the file path directly;
// JSONL treats the path as a directory.
func recoveryStore(ctx context.Context, backend, path string) (durable.Storage, error) {
	switch backend {
	case "jsonl":
		return jsonl.Open(ctx, path, jsonl.Options{Fsync: true})
	case "sqlite":
		return sqlite.Open(ctx, path, sqlite.Options{})
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
}

// TestRecoveryToolKillRestartChild is the child role of the killed-process
// recovery test. It is the same test binary with no candidate CLI involved. In
// the "first" role it acknowledges its durable effect and then blocks; the
// parent kills it. In the "recovered" role it reopens the store, reacquires the
// acknowledged submission, and finishes the run.
func TestRecoveryToolKillRestartChild(t *testing.T) {
	phase := os.Getenv("PITH_RECOVERY_PHASE")
	if phase == "" {
		return
	}
	ctx := testContext(t)
	directory := os.Getenv("PITH_RECOVERY_DIR")
	backend := os.Getenv("PITH_RECOVERY_BACKEND")
	policy := os.Getenv("PITH_RECOVERY_POLICY")
	store, err := recoveryStore(ctx, backend, filepath.Join(directory, "state"))
	if err != nil {
		t.Fatal(err)
	}
	registry := h.CreateRegistry()
	tool := h.ToolRegistration{
		Declaration: testToolDeclaration("effect"),
		Replay:      policy,
		Execute: func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
			key := fmt.Sprintf("tool-%d", api.TaskID())
			state, err := recoveryEffect(filepath.Join(directory, "external.json"), key)
			if err != nil {
				return h.ToolResult{}, err
			}
			if err := api.Output([]byte("external receipt\n")); err != nil {
				return h.ToolResult{}, err
			}
			if err := api.Details(ctx, testJSON(map[string]any{"key": key, "calls": state.Calls, "effects": len(state.Effects)})); err != nil {
				return h.ToolResult{}, err
			}
			if phase == "first" {
				fmt.Println("PITH_RECOVERY_EFFECT_ACK")
				<-ctx.Done()
				return h.ToolResult{}, ctx.Err()
			}
			return h.ToolResult{}, nil
		},
	}
	if err := registry.Install(h.Extension{Name: "effect", Tools: []h.ToolRegistration{tool}}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	models := &testModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if phase == "first" && requests.Add(1) == 1 {
			return testToolCall("effect"), nil
		}
		return testAnswer("recovered"), nil
	}}
	harness, err := h.Open(ctx, store, h.Options{Registry: registry, Models: models})
	if err != nil {
		t.Fatal(err)
	}
	root, err := harness.Root(ctx, h.RootOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if phase == "first" {
		if err := root.Configure(ctx, testJSON(map[string]any{"model": map[string]any{"provider": "fixture", "modelId": "fixture"}})); err != nil {
			t.Fatal(err)
		}
		if _, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("effect"), RequestID: "kill-restart"}); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}

	var record *durable.SubmissionRecord
	if err := harness.Commit(ctx, func(tx durable.Tx) error {
		var lookup error
		record, lookup = tx.SubmissionByRequest(ctx, root.ID(), "kill-restart")
		return lookup
	}); err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("acknowledged submission was lost across the kill")
	}
	submission, err := harness.Submission(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := submission.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "done" {
		t.Fatalf("submission settled as %q", settled.Status)
	}
	page, err := root.Entries(ctx, durable.EntryQuery{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, isError := testToolText(page.Items)
	if policy == "unsafe" {
		if !isError || !strings.Contains(text, "interrupted") || !strings.Contains(text, "external receipt") {
			t.Fatalf("unsafe recovery lost its interrupted receipt: text=%q error=%v", text, isError)
		}
	}
	if policy == "safe" && isError {
		t.Fatalf("safe replay produced an error result: %q", text)
	}
	if err := harness.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fmt.Println("PITH_RECOVERY_DONE")
}

// TestRecoveryToolKillRestartJSONLAndSQLite kills a real subprocess after a tool
// acknowledged its effect and before any outcome was written, reopens the same
// store, and checks the replay matrix: a safe invocation may run again (the
// external key deduplicates the effect) while an unsafe invocation must not.
func TestRecoveryToolKillRestartJSONLAndSQLite(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite"} {
		for _, policy := range []string{"safe", "unsafe"} {
			t.Run(backend+"-"+policy, func(t *testing.T) {
				ctx := testContext(t)
				directory := t.TempDir()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				environment := append(os.Environ(),
					"PITH_RECOVERY_DIR="+directory,
					"PITH_RECOVERY_BACKEND="+backend,
					"PITH_RECOVERY_POLICY="+policy,
				)

				child := exec.CommandContext(ctx, executable, "-test.run=^TestRecoveryToolKillRestartChild$")
				child.Env = append(environment, "PITH_RECOVERY_PHASE=first")
				stdout, err := child.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				child.Stderr = &stderr
				if err := child.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if child.Process != nil {
						_ = child.Process.Kill()
					}
				}()

				scanner := bufio.NewScanner(stdout)
				acknowledged := false
				for scanner.Scan() {
					if scanner.Text() == "PITH_RECOVERY_EFFECT_ACK" {
						acknowledged = true
						break
					}
				}
				if !acknowledged {
					_ = child.Wait()
					t.Fatalf("child never acknowledged its effect: %v stderr=%s", scanner.Err(), stderr.String())
				}
				if err := child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := child.Wait(); err == nil {
					t.Fatal("child exited normally; the kill path was not exercised")
				}

				before, err := recoveryReadEffects(filepath.Join(directory, "external.json"))
				if err != nil {
					t.Fatal(err)
				}
				if before.Calls != 1 || len(before.Effects) != 1 {
					t.Fatalf("external pre-kill state=%+v", before)
				}

				restarted := exec.CommandContext(ctx, executable, "-test.run=^TestRecoveryToolKillRestartChild$")
				restarted.Env = append(environment, "PITH_RECOVERY_PHASE=recovered")
				output, err := restarted.CombinedOutput()
				if err != nil {
					t.Fatalf("restart failed: %v output=%s", err, output)
				}
				if !bytes.Contains(output, []byte("PITH_RECOVERY_DONE")) {
					t.Fatalf("restart omitted its success barrier: %s", output)
				}
				after, err := recoveryReadEffects(filepath.Join(directory, "external.json"))
				if err != nil {
					t.Fatal(err)
				}
				wantCalls := 1
				if policy == "safe" {
					wantCalls = 2
				}
				if after.Calls != wantCalls || len(after.Effects) != 1 {
					t.Fatalf("policy=%s calls=%d want=%d effects=%v", policy, after.Calls, wantCalls, after.Effects)
				}
			})
		}
	}
}

// TestRecoveryIntentMemosAcrossReopen drives an intent/effect/outcome task
// through a close and reopen. The intent checkpoint and its memo must survive,
// the effect must be idempotent by key, and the terminal receipt must remain.
func TestRecoveryIntentMemosAcrossReopen(t *testing.T) {
	ctx := testContext(t)
	path := filepath.Join(t.TempDir(), "state.sqlite")

	var mu sync.Mutex
	calls := 0
	applied := map[string]int{}
	apply := func(key string, amount int) int {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if _, ok := applied[key]; !ok {
			applied[key] = amount * 10
		}
		return applied[key]
	}
	callsMade := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	appliedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(applied)
	}

	reached := make(chan struct{})
	interrupt := true
	transfer := durable.TaskDefinition{
		Name:    "test.transfer",
		Version: 1,
		Initial: func(json.RawMessage) (json.RawMessage, error) {
			return testJSON(map[string]any{"phase": "prepare"}), nil
		},
		Phases: map[string]durable.PhaseHandler{
			"prepare": func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
				var input struct {
					Amount int `json:"amount"`
				}
				_ = json.Unmarshal(task.Input, &input)
				if _, err := r.Memo(ctx, "requested", testJSON(input.Amount)); err != nil {
					return err
				}
				return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
					current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: testJSON(map[string]any{"phase": "apply", "key": fmt.Sprintf("transfer-%d", task.ID)})}
					return nil
				})
			},
			"apply": func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
				var checkpoint struct {
					Key string `json:"key"`
				}
				_ = json.Unmarshal(task.State.Checkpoint, &checkpoint)
				var input struct {
					Amount int `json:"amount"`
				}
				_ = json.Unmarshal(task.Input, &input)
				receipt := apply(checkpoint.Key, input.Amount)
				if interrupt {
					interrupt = false
					close(reached)
					<-ctx.Done()
					return ctx.Err()
				}
				return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
					current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: testJSON(map[string]any{"receipt": receipt})}}
					return nil
				})
			},
		},
		Abort: func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
			return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
				current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeAborted}}
				return nil
			})
		},
	}

	open := func() *h.Harness {
		store, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		registry := h.CreateRegistry()
		if err := registry.Install(h.Extension{Name: "transfer", Tasks: []durable.TaskDefinition{transfer}}); err != nil {
			t.Fatal(err)
		}
		harness, err := h.Open(ctx, store, h.Options{Registry: registry, Models: &testModels{}})
		if err != nil {
			t.Fatal(err)
		}
		return harness
	}

	first := open()
	root, err := first.Root(ctx, h.RootOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var id durable.TaskID
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		var createErr error
		id, createErr = tx.CreateTask(ctx, transfer, testJSON(map[string]any{"amount": 7}), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "conversation"}})
		return createErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	second := open()
	defer second.Close(context.Background())
	record, err := second.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State.Status != durable.TaskStatusPending || record.AbortRequested || string(record.Memos["requested"]) != "7" || callsMade() != 1 {
		t.Fatalf("open dispatched or destroyed the intent: record=%+v calls=%d", record, callsMade())
	}
	receipt, err := second.WaitForTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Outcome == nil || receipt.State.Outcome.Status != durable.OutcomeCompleted || callsMade() != 2 || appliedCount() != 1 {
		t.Fatalf("receipt=%+v calls=%d applied=%d", receipt, callsMade(), appliedCount())
	}
	var outcome struct {
		Receipt int `json:"receipt"`
	}
	if err := json.Unmarshal(receipt.State.Outcome.Result, &outcome); err != nil || outcome.Receipt != 70 {
		t.Fatalf("outcome result=%s err=%v", receipt.State.Outcome.Result, err)
	}

	third := open()
	defer third.Close(context.Background())
	stored, err := third.GetTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State.Status != durable.TaskStatusTerminal || stored.State.Outcome == nil || stored.State.Outcome.Status != durable.OutcomeCompleted {
		t.Fatalf("terminal receipt did not survive reopen: %+v", stored.State)
	}
	if callsMade() != 2 {
		t.Fatalf("terminal work ran again: calls=%d", callsMade())
	}
}

// TestRecoveryToolReplayPolicyAcrossReopen covers the full stored/current replay
// matrix over a durable reopen. A tool reruns only when both the stored
// checkpoint policy and the currently installed declaration say safe.
func TestRecoveryToolReplayPolicyAcrossReopen(t *testing.T) {
	cases := []struct {
		stored  string
		current string
		runs    int32
	}{
		{"safe", "safe", 2},
		{"safe", "unsafe", 1},
		{"unsafe", "safe", 1},
		{"unsafe", "unsafe", 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.stored+"-then-"+testCase.current, func(t *testing.T) {
			ctx := testContext(t)
			path := filepath.Join(t.TempDir(), "state.sqlite")
			var runs atomic.Int32
			var requests atomic.Int32
			started := make(chan struct{})
			var once sync.Once

			tool := func(replay string) h.ToolRegistration {
				return h.ToolRegistration{
					Declaration: testToolDeclaration("work"),
					Replay:      replay,
					Execute: func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
						run := runs.Add(1)
						if err := api.Output([]byte("run\n")); err != nil {
							return h.ToolResult{}, err
						}
						if err := api.Details(ctx, testJSON(map[string]any{"run": run})); err != nil {
							return h.ToolResult{}, err
						}
						if run == 1 {
							once.Do(func() { close(started) })
							<-ctx.Done()
							return h.ToolResult{}, ctx.Err()
						}
						return h.ToolResult{}, nil
					},
				}
			}
			models := &testModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
				if requests.Add(1) == 1 {
					return testToolCall("work"), nil
				}
				return testAnswer("done"), nil
			}}
			open := func(replay string) *h.Harness {
				store, err := sqlite.Open(ctx, path, sqlite.Options{})
				if err != nil {
					t.Fatal(err)
				}
				registry := h.CreateRegistry()
				if err := registry.Install(h.Extension{Name: "tools", Tools: []h.ToolRegistration{tool(replay)}}); err != nil {
					t.Fatal(err)
				}
				harness, err := h.Open(ctx, store, h.Options{Registry: registry, Models: models})
				if err != nil {
					t.Fatal(err)
				}
				return harness
			}

			first := open(testCase.stored)
			root, err := first.Root(ctx, h.RootOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := root.Configure(ctx, testJSON(map[string]any{"model": map[string]any{"provider": "fixture", "modelId": "fixture"}})); err != nil {
				t.Fatal(err)
			}
			submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("work"), RequestID: "work"})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := first.Close(ctx); err != nil {
				t.Fatal(err)
			}

			second := open(testCase.current)
			defer second.Close(context.Background())
			reacquired, err := second.Submission(ctx, submission.ID())
			if err != nil {
				t.Fatal(err)
			}
			settled, err := reacquired.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if settled.Status != "done" {
				t.Fatalf("submission settled as %q", settled.Status)
			}
			if runs.Load() != testCase.runs {
				t.Fatalf("runs=%d want=%d", runs.Load(), testCase.runs)
			}
		})
	}
}

// TestRecoveryCommittedGenerationPartial reopens after an assistant partial was
// already committed. The partial is retained as a raw aborted entry, is excluded
// from future model context, and the run resends the same messages.
func TestRecoveryCommittedGenerationPartial(t *testing.T) {
	ctx := testContext(t)
	path := filepath.Join(t.TempDir(), "state.sqlite")
	var calls atomic.Int32
	committed := make(chan struct{})
	var once sync.Once
	models := &testModels{run: func(ctx context.Context, _ h.ModelRequest, emit func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if calls.Add(1) == 1 {
			if err := emit(testAnswer("committed partial")); err != nil {
				return ai.AssistantMessage{}, err
			}
			<-ctx.Done()
			return ai.AssistantMessage{}, ctx.Err()
		}
		return testAnswer("complete"), nil
	}}
	open := func() *h.Harness {
		store, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		registry := h.CreateRegistry()
		harness, err := h.Open(ctx, store, h.Options{Registry: registry, Models: models})
		if err != nil {
			t.Fatal(err)
		}
		return harness
	}

	first := open()
	root, err := first.Root(ctx, h.RootOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Configure(ctx, testJSON(map[string]any{"model": map[string]any{"provider": "fixture", "modelId": "fixture"}})); err != nil {
		t.Fatal(err)
	}
	watch, err := root.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.Start(func(_ context.Context, view h.ConversationView, _ []chord.Op) error {
		encoded, _ := json.Marshal(view.Docs["pi.live"])
		if strings.Contains(string(encoded), "committed partial") {
			once.Do(func() { close(committed) })
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("partial"), RequestID: "partial"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}

	second := open()
	defer second.Close(context.Background())
	root, err = second.Root(ctx, h.RootOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reacquired, err := second.Submission(ctx, submission.ID())
	if err != nil {
		t.Fatal(err)
	}
	settled, err := reacquired.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "done" {
		t.Fatalf("submission settled as %q", settled.Status)
	}
	if calls.Load() != 2 {
		t.Fatalf("recovery repeated preparation: calls=%d", calls.Load())
	}

	page, err := root.Entries(ctx, durable.EntryQuery{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	sawAborted := false
	for _, entry := range page.Items {
		for _, message := range entry.Model {
			if message.Assistant != nil && message.Assistant.StopReason == ai.StopReasonAborted {
				sawAborted = true
			}
		}
	}
	if !sawAborted {
		t.Fatal("committed partial was not retained as an aborted raw entry")
	}
	view, err := root.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range view.Messages {
		if message.Assistant != nil && message.Assistant.StopReason == ai.StopReasonAborted {
			t.Fatal("aborted partial entered future model context")
		}
	}
}
