package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func TestPortsmithJudgeDurablePersistedJoinPolicies(t *testing.T) {
	for _, policy := range []string{"allSettled", "failFast"} {
		t.Run(policy, func(t *testing.T) {
			ctx := djContext(t)
			reg := h.CreateRegistry()
			firstStarted := make(chan struct{})
			secondStarted := make(chan struct{})
			failRelease := make(chan struct{})
			successRelease := make(chan struct{})
			waiting := make(chan struct{})
			var firstID, secondID durable.TaskID
			failing := djTask("judge.join-fail", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
				close(firstStarted)
				select {
				case <-failRelease:
				case <-ctx.Done():
					return ctx.Err()
				}
				return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error {
					c.State = durable.TaskState{Status: "terminal", Outcome: &durable.TaskOutcome{Status: "failed", Error: &durable.TaskOutcomeError{Message: "independent failure"}}}
					return nil
				})
			})
			succeeding := djTask("judge.join-sibling", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
				close(secondStarted)
				select {
				case <-successRelease:
				case <-ctx.Done():
					return ctx.Err()
				}
				return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error { c.State = djTerminal("completed", nil); return nil })
			})
			parent := djTask("judge.join-parent", func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
				e := r.Commit(ctx, func(tx durable.Tx, c *durable.TaskRecord) error {
					var e error
					firstID, e = tx.CreateTask(ctx, failing, djJSON(nil), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "task", TaskID: task.ID}})
					if e != nil {
						return e
					}
					secondID, e = tx.CreateTask(ctx, succeeding, djJSON(nil), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "task", TaskID: task.ID}})
					if e != nil {
						return e
					}
					c.State = durable.TaskState{Status: "waiting", Checkpoint: djJSON(map[string]any{"phase": "finish", "on": []durable.TaskID{firstID, secondID}}), On: []durable.TaskID{firstID, secondID}, Policy: policy}
					return nil
				})
				close(waiting)
				return e
			})
			parent.Phases["finish"] = func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
				var checkpoint struct {
					On []durable.TaskID `json:"on"`
				}
				if e := json.Unmarshal(task.State.Checkpoint, &checkpoint); e != nil {
					return e
				}
				outcomes, e := r.Outcomes(ctx, checkpoint.On)
				if e != nil {
					return e
				}
				statuses := []string{}
				for _, o := range outcomes {
					statuses = append(statuses, o.Status)
				}
				return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error {
					c.State = djTerminal("completed", statuses)
					return nil
				})
			}
			djCheck(t, reg.Install(h.Extension{Name: "joins", Tasks: []durable.TaskDefinition{parent, failing, succeeding}}))
			hh, root := djOpen(t, ctx, reg, nil)
			id := djCreate(t, ctx, root, parent, nil, false)
			djCheck(t, hh.Resume())
			djAwait(t, ctx, waiting)
			djAwait(t, ctx, firstStarted)
			djAwait(t, ctx, secondStarted)
			record, e := hh.GetTask(ctx, id)
			djCheck(t, e)
			if record.State.Status != "waiting" || record.State.Policy != policy || len(record.State.On) != 2 {
				t.Fatalf("wait not durable: %+v", record)
			}
			close(failRelease)
			djReceipt(t, ctx, hh, firstID, "failed")
			want := `["failed","aborted"]`
			if policy == "allSettled" {
				close(successRelease)
				djReceipt(t, ctx, hh, secondID, "completed")
				want = `["failed","completed"]`
			} else {
				djReceipt(t, ctx, hh, secondID, "aborted")
			}
			receipt := djReceipt(t, ctx, hh, id, "completed")
			if string(receipt.State.Outcome.Result) != want {
				t.Fatalf("join outcome order=%s want %s", receipt.State.Outcome.Result, want)
			}
		})
	}
}
