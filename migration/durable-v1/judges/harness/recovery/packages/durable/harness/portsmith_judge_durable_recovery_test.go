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
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
	jsonl "github.com/minifish-org/pith/packages/durable/storage/jsonl"
	sqlite "github.com/minifish-org/pith/packages/durable/storage/sqlite"
)

type djExternal struct {
	Calls   int            `json:"calls"`
	Effects map[string]int `json:"effects"`
}

func djEffect(path, key string) (djExternal, error) {
	state := djExternal{Effects: map[string]int{}}
	if b, e := os.ReadFile(path); e == nil {
		if e = json.Unmarshal(b, &state); e != nil {
			return state, e
		}
	} else if !os.IsNotExist(e) {
		return state, e
	}
	state.Calls++
	if _, ok := state.Effects[key]; !ok {
		state.Effects[key] = 1
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if e != nil {
		return state, e
	}
	b := djJSON(state)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	return state, e
}
func djRecoveryStorage(ctx context.Context, backend, path string) (durable.Storage, error) {
	if backend == "jsonl" {
		return jsonl.Open(ctx, path, jsonl.Options{Fsync: true})
	}
	return sqlite.Open(ctx, path, sqlite.Options{})
}

// This child is the same independent test binary, with no candidate CLI or
// generated helper involved. It acknowledges only after both effect persistence
// and Harness Details() commit. Parent kills it before an outcome can be written.
func TestPortsmithJudgeDurableRecoveryChild(t *testing.T) {
	phase := os.Getenv("PITH_JUDGE_DURABLE_PHASE")
	if phase == "" {
		return
	}
	ctx := djContext(t)
	directory := os.Getenv("PITH_JUDGE_DURABLE_DIR")
	backend := os.Getenv("PITH_JUDGE_DURABLE_BACKEND")
	policy := os.Getenv("PITH_JUDGE_DURABLE_POLICY")
	store, e := djRecoveryStorage(ctx, backend, filepath.Join(directory, "state"))
	djCheck(t, e)
	reg := h.CreateRegistry()
	tool := h.ToolRegistration{Declaration: djDeclaration("effect"), Replay: policy, Execute: func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
		key := fmt.Sprintf("tool-%d", api.TaskID())
		state, e := djEffect(filepath.Join(directory, "external.json"), key)
		if e != nil {
			return h.ToolResult{}, e
		}
		if e = api.Output([]byte("durable external receipt\n")); e != nil {
			return h.ToolResult{}, e
		}
		if e = api.Details(ctx, djJSON(map[string]any{"key": key, "effects": len(state.Effects)})); e != nil {
			return h.ToolResult{}, e
		}
		if phase == "first" {
			fmt.Println("PITH_JUDGE_EFFECT_ACK")
			select {
			case <-ctx.Done():
				return h.ToolResult{}, ctx.Err()
			}
		}
		return h.ToolResult{}, nil
	}}
	djCheck(t, reg.Install(h.Extension{Name: "effect", Tools: []h.ToolRegistration{tool}}))
	var requests atomic.Int32
	models := &djModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if phase == "first" && requests.Add(1) == 1 {
			return djCall("effect"), nil
		}
		return djAnswer("recovered"), nil
	}}
	hh, e := h.Open(ctx, store, h.Options{Registry: reg, Models: models})
	djCheck(t, e)
	root, e := hh.Root(ctx, h.RootOptions{})
	djCheck(t, e)
	if phase == "first" {
		djConfigure(t, ctx, root)
		s := djSubmit(t, ctx, root, djInput("effect", "kill-restart"))
		_, e = s.Wait(ctx)
		djCheck(t, e)
		t.Fatal("first process settled before parent kill")
	}
	var record *durable.SubmissionRecord
	djCheck(t, hh.Commit(ctx, func(tx durable.Tx) error {
		var e error
		record, e = tx.SubmissionByRequest(ctx, root.ID(), "kill-restart")
		return e
	}))
	if record == nil {
		t.Fatal("killed process lost acknowledged submission")
	}
	pending, e := hh.Submission(ctx, record.ID)
	djCheck(t, e)
	djSettled(t, ctx, pending, "done")
	history := djHistory(t, ctx, root)
	text, isError := djToolText(history)
	if policy == "unsafe" && (!isError || !strings.Contains(text, "interrupted") || !strings.Contains(text, "durable external receipt")) {
		t.Fatalf("unsafe recovery failed to retain interrupted receipt: text=%q error=%v", text, isError)
	}
	if policy == "safe" && isError {
		t.Fatalf("safe replay failed: %q", text)
	}
	djCheck(t, hh.Close(ctx))
	fmt.Println("PITH_JUDGE_RECOVERY_DONE")
}

func TestPortsmithJudgeDurableKilledProcessToolRecovery(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite"} {
		for _, policy := range []string{"safe", "unsafe"} {
			t.Run(backend+"-"+policy, func(t *testing.T) {
				ctx := djContext(t)
				directory := t.TempDir()
				exe, e := os.Executable()
				djCheck(t, e)
				environment := append(os.Environ(), "PITH_JUDGE_DURABLE_DIR="+directory, "PITH_JUDGE_DURABLE_BACKEND="+backend, "PITH_JUDGE_DURABLE_POLICY="+policy)
				child := exec.CommandContext(ctx, exe, "-test.run=^TestPortsmithJudgeDurableRecoveryChild$")
				child.Env = append(environment, "PITH_JUDGE_DURABLE_PHASE=first")
				stdout, e := child.StdoutPipe()
				djCheck(t, e)
				var stderr bytes.Buffer
				child.Stderr = &stderr
				djCheck(t, child.Start())
				defer func() {
					if child.Process != nil {
						_ = child.Process.Kill()
					}
				}()
				scanner := bufio.NewScanner(stdout)
				ack := false
				for scanner.Scan() {
					if scanner.Text() == "PITH_JUDGE_EFFECT_ACK" {
						ack = true
						break
					}
				}
				if !ack {
					_ = child.Wait()
					t.Fatalf("effect barrier never acknowledged: %v stderr=%s", scanner.Err(), stderr.String())
				}
				djCheck(t, child.Process.Kill())
				if e := child.Wait(); e == nil {
					t.Fatal("child exited normally; kill path not exercised")
				}
				encoded, e := os.ReadFile(filepath.Join(directory, "external.json"))
				djCheck(t, e)
				var before djExternal
				djCheck(t, json.Unmarshal(encoded, &before))
				if before.Calls != 1 || len(before.Effects) != 1 {
					t.Fatalf("external pre-kill state=%+v", before)
				}
				restarted := exec.CommandContext(ctx, exe, "-test.run=^TestPortsmithJudgeDurableRecoveryChild$")
				restarted.Env = append(environment, "PITH_JUDGE_DURABLE_PHASE=recovered")
				output, e := restarted.CombinedOutput()
				if e != nil {
					t.Fatalf("restart failed: %v output=%s", e, output)
				}
				if !bytes.Contains(output, []byte("PITH_JUDGE_RECOVERY_DONE")) {
					t.Fatalf("restart omitted success barrier: %s", output)
				}
				encoded, e = os.ReadFile(filepath.Join(directory, "external.json"))
				djCheck(t, e)
				var after djExternal
				djCheck(t, json.Unmarshal(encoded, &after))
				want := 1
				if policy == "safe" {
					want = 2
				}
				if after.Calls != want || len(after.Effects) != 1 {
					t.Fatalf("recovery policy=%s calls=%d want=%d effects=%v; invocation repetition and external idempotency are separate", policy, after.Calls, want, after.Effects)
				}
			})
		}
	}
}

func TestPortsmithJudgeDurableCloseReopenKeepsIntentMemos(t *testing.T) {
	ctx := djContext(t)
	path := filepath.Join(t.TempDir(), "state.sqlite")
	reg := h.CreateRegistry()
	reached := make(chan struct{})
	var runs atomic.Int32
	task := djTask("judge.memo-recovery", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		run := runs.Add(1)
		winner, e := r.Memo(ctx, "idempotency-key", djJSON(fmt.Sprintf("candidate-%d", run)))
		if e != nil {
			return e
		}
		if string(winner) != `"candidate-1"` {
			t.Errorf("memo changed across retry: %s", winner)
		}
		if run == 1 {
			close(reached)
			<-ctx.Done()
			return ctx.Err()
		}
		return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error {
			c.State = djTerminal("completed", string(winner))
			return nil
		})
	})
	djCheck(t, reg.Install(h.Extension{Name: "memo", Tasks: []durable.TaskDefinition{task}}))
	hh, root := djFileOpen(t, ctx, path, reg, &djModels{})
	id := djCreate(t, ctx, root, task, nil, false)
	djCheck(t, hh.Resume())
	djAwait(t, ctx, reached)
	djCheck(t, hh.Close(ctx))
	hh, _ = djFileOpen(t, ctx, path, reg, &djModels{})
	defer hh.Close(context.Background())
	record, e := hh.GetTask(ctx, id)
	djCheck(t, e)
	if record.State.Status != "pending" || record.AbortRequested || string(record.Memos["idempotency-key"]) != `"candidate-1"` || runs.Load() != 1 {
		t.Fatalf("open dispatched or destroyed intent: record=%+v runs=%d", record, runs.Load())
	}
	receipt := djReceipt(t, ctx, hh, id, "completed")
	if len(receipt.Memos) != 0 {
		t.Fatal("terminal retained live-only memos")
	}
	if runs.Load() != 2 {
		t.Fatal("intent not resumed exactly once on reopen")
	}
}
