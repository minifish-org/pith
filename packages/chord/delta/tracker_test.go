package delta

import "testing"

func TestTrackerLifecycleAndIsolation(t *testing.T) {
	tracker, err := Track(map[string]any{"nested": map[string]any{"value": 1}})
	if err != nil {
		t.Fatal(err)
	}
	change, err := tracker.BeginChange()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := change.Value()
	if err != nil {
		t.Fatal(err)
	}
	draft.(map[string]any)["nested"].(map[string]any)["value"] = 2
	prepared, err := change.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if prepared.BaseRevision() != 0 || tracker.Revision() != 0 {
		t.Fatal("preparation advanced authority")
	}
	deltaJSONEqual(t, prepared.Base(), map[string]any{"nested": map[string]any{"value": 1}})
	deltaJSONEqual(t, prepared.Value(), map[string]any{"nested": map[string]any{"value": 2}})
	deltaJSONEqual(t, tracker.Value(), map[string]any{"nested": map[string]any{"value": 1}})
	replayed, err := ApplyImmutable(prepared.Base(), prepared.Ops())
	if err != nil {
		t.Fatal(err)
	}
	deltaJSONEqual(t, replayed, prepared.Value())
	draft.(map[string]any)["nested"].(map[string]any)["value"] = 999
	deltaJSONEqual(t, prepared.Value(), map[string]any{"nested": map[string]any{"value": 2}})
	if _, err := change.Value(); err == nil {
		t.Fatal("settled change handle remained usable")
	}
	if err := tracker.Adopt(prepared); err != nil {
		t.Fatal(err)
	}
	if tracker.Revision() != 1 {
		t.Fatal("adoption did not advance the revision exactly once")
	}
	deltaJSONEqual(t, tracker.Value(), map[string]any{"nested": map[string]any{"value": 2}})
	if err := tracker.Adopt(prepared); err == nil {
		t.Fatal("prepared candidate was adopted twice")
	}
	public := tracker.Value().(map[string]any)
	public["nested"].(map[string]any)["value"] = 500
	deltaJSONEqual(t, tracker.Value(), map[string]any{"nested": map[string]any{"value": 2}})
}

func TestTrackerRejectsForeignStaleAndAborted(t *testing.T) {
	newTracker := func() *Tracker {
		t.Helper()
		tracker, err := Track(map[string]any{"value": 0})
		if err != nil {
			t.Fatal(err)
		}
		return tracker
	}
	a, b := newTracker(), newTracker()
	foreign, err := a.PrepareReplace(map[string]any{"value": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Adopt(foreign); err == nil {
		t.Fatal("foreign owner accepted")
	}
	first, err := a.PrepareReplace(map[string]any{"value": 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.PrepareReplace(map[string]any{"value": 3})
	if err != nil {
		t.Fatal(err)
	}
	open, err := a.BeginChange()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Adopt(first); err != nil {
		t.Fatal(err)
	}
	if err := a.Adopt(second); err == nil {
		t.Fatal("stale prepared revision accepted")
	}
	if _, err := open.Prepare(); err == nil {
		t.Fatal("stale open draft prepared")
	}
	aborted, err := a.PrepareReplace(map[string]any{"value": 9})
	if err != nil {
		t.Fatal(err)
	}
	aborted.Abort()
	aborted.Abort()
	if err := a.Adopt(aborted); err == nil {
		t.Fatal("aborted candidate adopted")
	}
	change, err := a.BeginChange()
	if err != nil {
		t.Fatal(err)
	}
	change.Abort()
	change.Abort()
	if _, err := change.Prepare(); err == nil {
		t.Fatal("aborted draft prepared")
	}
	deltaJSONEqual(t, a.Value(), map[string]any{"value": 2})
	noop, err := a.BeginChange()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := noop.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Ops()) != 0 {
		t.Fatal("unchanged draft emitted operations")
	}
	if err := a.Adopt(prepared); err != nil {
		t.Fatal(err)
	}
	if a.Revision() != 2 {
		t.Fatal("accepted no-op candidate did not advance the revision")
	}
}

func TestTrackerPrepareReplaceNoopEmitsNoOps(t *testing.T) {
	tracker, err := Track(map[string]any{"value": 0})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := tracker.PrepareReplace(map[string]any{"value": 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Ops()) != 0 {
		t.Fatal("equal replacement emitted operations")
	}
	if err := tracker.Adopt(prepared); err != nil {
		t.Fatal(err)
	}
	if tracker.Revision() != 1 {
		t.Fatal("no-op replacement did not advance the revision")
	}
}

func TestTrackRejectsNonContainerRoots(t *testing.T) {
	for _, root := range []any{nil, "scalar", true, 2} {
		if _, err := Track(root); err == nil {
			t.Fatalf("non-container tracker root accepted: %v", root)
		}
	}
}
