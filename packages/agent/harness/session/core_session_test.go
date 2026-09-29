package session

import (
	"context"
	"testing"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

var testContext = harnesscontext.BackgroundContext

func TestMutationLineSerializes(t *testing.T) {
	line := NewMutationLine()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = line.Run(func() (any, error) {
			close(started)
			<-release
			return "first", nil
		})
		close(done)
	}()
	<-started
	second := make(chan string, 1)
	go func() {
		value, _ := line.Run(func() (any, error) { return "second", nil })
		second <- value.(string)
	}()
	select {
	case <-second:
		t.Fatal("second job ran before the first released")
	default:
	}
	close(release)
	if value := <-second; value != "second" {
		t.Fatalf("second = %q", value)
	}
	<-done
}

func TestMutationLineSealRejectsQueuedAndFuture(t *testing.T) {
	line := NewMutationLine()
	started := make(chan struct{})
	release := make(chan struct{})
	running := make(chan struct{})
	go func() {
		_, _ = line.Run(func() (any, error) {
			close(started)
			<-release
			return nil, nil
		})
		close(running)
	}()
	<-started
	sealed := make(chan struct{})
	go func() { line.seal(context.Canceled); close(sealed) }()
	for i := 0; i < 50; i++ {
		if _, err := line.Run(func() (any, error) { return nil, nil }); err == nil {
			t.Fatal("expected sealed line to reject")
		}
	}
	close(release)
	<-running
	<-sealed
}

func TestPrepareStorageCommitStampsInOrder(t *testing.T) {
	entry := harnesstypes.CustomEntry{EntryBase: harnesstypes.EntryBase{ID: "a", Type: harnesstypes.EntryTypeCustom}, CustomType: "note"}
	prepared := PrepareStorageCommit([]harnesstypes.Write{
		InsertEntry(entry),
		SetValue(SessionName, "name"),
	}, 5, 100)
	if prepared.Result.FirstSeq != 5 || len(prepared.Result.Seqs) != 2 {
		t.Fatalf("unexpected result %+v", prepared.Result)
	}
	if prepared.Writes[0].Kind != "entry" || prepared.Writes[1].Kind != "value" {
		t.Fatalf("unexpected kinds %+v", prepared.Writes)
	}
	if err := ValidateCommittedWrites(prepared.Writes, 5, emptyValidationState{}); err != nil {
		t.Fatal(err)
	}
}

type emptyValidationState struct{}

func (emptyValidationState) HasEntryOrUsageID(string) bool { return false }
func (emptyValidationState) HasEntryID(string) bool        { return false }

func TestMemorySessionRepoLifecycle(t *testing.T) {
	repo := NewMemorySessionRepo(&MemorySessionRepoOptions{Now: func() float64 { return 1 }})
	s, err := repo.Create(harnesstypes.SessionCreateOptions{ID: stringPointer("session")}, testContext)
	if err != nil {
		t.Fatal(err)
	}
	name := "demo"
	if err := s.SetName(&name, testContext); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBranch("main", nil, testContext); err != nil {
		t.Fatal(err)
	}
	gotName, err := s.GetName(testContext)
	if err != nil || gotName == nil || *gotName != "demo" {
		t.Fatalf("name = %v %v", gotName, err)
	}
	branchValue, ok, err := s.Branch("main", testContext)
	if err != nil || !ok || branchValue.Name() != "main" {
		t.Fatalf("branch = %v %v %v", branchValue, ok, err)
	}
	label := "label"
	if err := s.SetLabel("e1", &label, testContext); err != nil {
		t.Fatal(err)
	}
	gotLabel, err := s.GetLabel("e1", testContext)
	if err != nil || gotLabel == nil || *gotLabel != "label" {
		t.Fatalf("label = %v %v", gotLabel, err)
	}
	metadata := s.Metadata()
	if err := s.Close(testContext); err != nil {
		t.Fatal(err)
	}
	reopened, err := repo.Open(metadata, testContext)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.GetName(testContext)
	if err != nil || again == nil || *again != "demo" {
		t.Fatalf("again = %v %v", again, err)
	}
	if err := reopened.Close(testContext); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(metadata, testContext); err != nil {
		t.Fatal(err)
	}
	remaining, err := repo.List(nil, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining = %v", remaining)
	}
	if err := repo.Close(testContext); err != nil {
		t.Fatal(err)
	}
}

func TestMemorySessionRepoRejectsDuplicate(t *testing.T) {
	repo := NewMemorySessionRepo(&MemorySessionRepoOptions{Now: func() float64 { return 1 }})
	first, err := repo.Create(harnesstypes.SessionCreateOptions{ID: stringPointer("session")}, testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(testContext)
	if _, err := repo.Create(harnesstypes.SessionCreateOptions{ID: stringPointer("session")}, testContext); err == nil {
		t.Fatal("expected duplicate id to reject")
	}
}

func TestBuildContextEntriesTrimsToLastCompaction(t *testing.T) {
	entries := []harnesstypes.Entry{
		harnesstypes.CustomEntry{EntryBase: harnesstypes.EntryBase{ID: "a", Type: harnesstypes.EntryTypeCustom}},
		harnesstypes.CompactionEntry{EntryBase: harnesstypes.EntryBase{ID: "b", Type: harnesstypes.EntryTypeCompaction}},
		harnesstypes.CustomEntry{EntryBase: harnesstypes.EntryBase{ID: "c", Type: harnesstypes.EntryTypeCustom}},
	}
	trimmed := BuildContextEntries(entries)
	if len(trimmed) != 2 || EntryID(trimmed[0]) != "b" || EntryID(trimmed[1]) != "c" {
		t.Fatalf("trimmed = %v", entryIDs(trimmed))
	}
}

func TestBuildSessionContextProjectsCustomEntries(t *testing.T) {
	projector := func(entry harnesstypes.CustomEntry, ctx harnesstypes.Context) ([]agenttypes.AgentMessage, error) {
		return []agenttypes.AgentMessage{agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("projected", 1)))}, nil
	}
	entries := []harnesstypes.Entry{
		harnesstypes.CustomEntry{EntryBase: harnesstypes.EntryBase{ID: "custom", Type: harnesstypes.EntryTypeCustom}, CustomType: "note"},
	}
	messages, err := BuildSessionContext(entries, &SessionContextBuildOptions{EntryProjectors: map[string]harnesstypes.EntryProjector{"note": projector}}, testContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Message == nil || messages[0].Message.User == nil {
		t.Fatalf("messages = %v", messages)
	}
}

func TestProjectForkCurrentStateWriteExcludesOperationState(t *testing.T) {
	plan := ForkCurrentStatePlan{Scope: "tree"}
	projected, err := ProjectForkCurrentStateWrite(CommittedWrite{Kind: "value", Op: "set", Namespace: NamespaceOperation, Key: "op", Value: true}, plan, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if projected != nil {
		t.Fatal("operation state must be excluded")
	}
	if _, err := ProjectForkCurrentStateWrite(CommittedWrite{Kind: "value", Op: "set", Namespace: "pi", Key: ""}, plan, func(string) bool { return true }); err == nil {
		t.Fatal("expected unknown reserved namespace to reject")
	}
}

func stringPointer(value string) *string { return &value }

func entryIDs(entries []harnesstypes.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, EntryID(entry))
	}
	return out
}
