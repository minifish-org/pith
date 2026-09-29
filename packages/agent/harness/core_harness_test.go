package harness

import (
	"context"
	"encoding/json"
	"testing"

	harnessresult "github.com/minifish-org/pith/packages/agent/harness/result"
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// These self-tests translate the stable core-harness behavior from the pinned
// upstream sources. The experimental pico3 fixtures are excluded by the frozen
// ledger, so the tests cover the assembly surface: tagged results, the
// process-local lifecycle over a deterministic in-memory session and the
// snapshot/event DTO shapes. They use only injected fakes and never a concrete
// tool or provider.

func newSession(t *testing.T) (*harnesssession.MemorySessionRepo, harnesstypes.Session[harnesstypes.SessionMetadata]) {
	t.Helper()
	now := 1.0
	repo := harnesssession.NewMemorySessionRepo(&harnesssession.MemorySessionRepoOptions{
		Now: func() float64 { return now },
	})
	sessionID := "session"
	session, err := repo.Create(harnesstypes.SessionCreateOptions{ID: &sessionID}, context.Background())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return repo, session
}

func TestHarnessCreateRestoresEmptyLaneSet(t *testing.T) {
	repo, session := newSession(t)
	defer repo.Close(context.Background())
	thinking := agenttypes.ThinkingLevel("low")
	harness, open, err := CreateAgentHarness(harnesstypes.AgentHarnessOptions[any]{
		Session:         session,
		ThinkingLevel:   &thinking,
		ActiveToolNames: []string{},
	}, context.Background())
	if err != nil {
		t.Fatalf("create harness: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("fresh session restored %d operations", len(open))
	}
	if lanes := harness.Lanes(); len(lanes) != 0 {
		t.Fatalf("fresh harness exposed %d lanes", len(lanes))
	}

	first, err := harness.Lane("main", context.Background())
	if err != nil {
		t.Fatalf("acquire main lane: %v", err)
	}
	second, err := harness.Lane("main", context.Background())
	if err != nil {
		t.Fatalf("re-acquire main lane: %v", err)
	}
	if first != second {
		t.Fatal("lane acquisition must be process-local stable")
	}
	state := first.LaneState()
	if state.TipID != nil {
		t.Fatalf("new lane tip = %v, want nil", state.TipID)
	}
	if state.Configuration.ThinkingLevel != "low" {
		t.Fatalf("thinking level = %q, want low", state.Configuration.ThinkingLevel)
	}
	if len(state.Configuration.ActiveToolNames) != 0 {
		t.Fatalf("active tools = %v, want empty", state.Configuration.ActiveToolNames)
	}
	lanes := harness.Lanes()
	if len(lanes) != 1 || lanes[0].Name != "main" || lanes[0].TipID != nil {
		t.Fatalf("lane summary = %+v", lanes)
	}
	harness.Close(nil)
}

func TestHarnessTaggedErrorFlattensName(t *testing.T) {
	factory := harnessresult.TaggedError("LaneBusy")
	value, err := factory.New(map[string]any{
		"message":     "fixture error",
		"lane":        "main",
		"operationId": "r1",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	payload := value.ToJSON()
	if payload["_tag"] != "LaneBusy" || payload["name"] != "LaneBusy" {
		t.Fatalf("tag/name = %v/%v", payload["_tag"], payload["name"])
	}
	if payload["message"] != "fixture error" || payload["lane"] != "main" || payload["operationId"] != "r1" {
		t.Fatalf("payload = %#v", payload)
	}
	if !factory.Is(value) {
		t.Fatal("factory must recognize its own family")
	}
	if factory.Is(&Closed{Message: "x"}) {
		t.Fatal("factory must reject another family")
	}
}

func TestHarnessResultCarrierJSON(t *testing.T) {
	ok := harnessresult.Ok[any, error]("value")
	encoded, err := json.Marshal(ok)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"ok":true,"value":"value"}` {
		t.Fatalf("ok carrier = %s", encoded)
	}
	failure := harnessresult.Err[any, error](&NoActiveOperation{Lane: "main", Message: "none"})
	encoded, err = json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		OK    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK != false || decoded.Error.Message != "none" {
		t.Fatalf("error carrier = %s", encoded)
	}
}

func TestHarnessSessionSnapshotJSON(t *testing.T) {
	encoded, err := json.Marshal(SessionSnapshot{Lanes: []LaneInfo{}, Faulted: false})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"lanes":[],"faulted":false}` {
		t.Fatalf("snapshot = %s", encoded)
	}
	status := CurrentOperationInfo{
		ID:        "r1",
		Kind:      "run",
		StartedAt: 1,
		Status:    OperationStatusRunning,
	}
	if status.Status != "running" {
		t.Fatalf("status = %q", status.Status)
	}
}
