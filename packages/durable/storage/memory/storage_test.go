package memory_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
)

var ctx = context.Background()

// TestNewReservesRootAndMintsTwo covers the memory-specific half of upstream
// memory-storage.test.ts and the reserved root ID rule.
func TestNewReservesRootAndMintsTwo(t *testing.T) {
	storage := memory.New()
	t.Cleanup(func() { _ = storage.Close(context.Background()) })

	first, err := storage.MintID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 2 {
		t.Fatalf("first minted ID = %d want 2", first)
	}
}

// TestPreparedCommitDoesNotExposeRetainedState ports upstream
// memory-storage.test.ts "does not expose retained state through a prepared
// commit". Go cannot freeze the exposed writes, so the documented adaptation is
// that the exposed Writes snapshot is independent of the mutation Apply
// publishes.
func TestPreparedCommitDoesNotExposeRetainedState(t *testing.T) {
	storage := memory.New()
	t.Cleanup(func() { _ = storage.Close(context.Background()) })

	if _, err := storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}},
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := storage.PrepareCommit([]durable.StorageWrite{{
		Type:  durable.WriteEntry,
		Entry: &durable.EntryRecord{ID: 2, ConversationID: durable.RootConversationID, Kind: "test", Data: json.RawMessage(`{"nested":[1]}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Writes) != 1 || prepared.Writes[0].Entry == nil {
		t.Fatalf("prepared writes = %+v", prepared.Writes)
	}
	prepared.Writes[0].Entry.Data = json.RawMessage(`{"nested":[2]}`)

	if prepared.Apply() != 2 {
		t.Fatalf("first apply = %d", prepared.Apply())
	}
	if prepared.Apply() != 2 {
		t.Fatalf("second apply = %d", prepared.Apply())
	}
	stored, err := storage.Entry(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("prepared entry was not applied")
	}
	assertJSONEqual(t, stored.Entry.Data, `{"nested":[1]}`)
}

func TestClosedErrorsMatchSentinel(t *testing.T) {
	storage := memory.New()
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Commit(ctx, nil); !errors.Is(err, durable.ErrStorageClosed) {
		t.Fatalf("commit after close = %v", err)
	}
	if _, err := storage.MintID(ctx); !errors.Is(err, durable.ErrStorageClosed) {
		t.Fatalf("mint after close = %v", err)
	}
	if _, err := storage.Conversation(ctx, durable.RootConversationID); !errors.Is(err, durable.ErrStorageClosed) {
		t.Fatalf("read after close = %v", err)
	}
}

func TestUnknownConversationAndCursorErrors(t *testing.T) {
	storage := memory.New()
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	if _, err := storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.VisibleEntry(ctx, 999, 1); err == nil {
		t.Fatal("visible entry of unknown conversation was accepted")
	}
	if _, err := storage.FindLatestHeadMarker(ctx, 999, nil); err == nil {
		t.Fatal("head marker of unknown conversation was accepted")
	}
	if _, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: 999}, 10, nil); err == nil {
		t.Fatal("entry scan of unknown conversation was accepted")
	}
	if _, err := storage.ScanConversations(ctx, durable.ConversationQuery{}, 10, durable.Cursor(`{"after":"x"}`)); err == nil {
		t.Fatal("non-numeric cursor was accepted")
	}
}

func TestCommitCancellationBeforeAdmission(t *testing.T) {
	storage := memory.New()
	t.Cleanup(func() { _ = storage.Close(context.Background()) })
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.Commit(cancelled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled commit = %v", err)
	}
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		t.Fatalf("unmarshal %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &right); err != nil {
		t.Fatalf("unmarshal %s: %v", want, err)
	}
	leftBytes, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	if string(leftBytes) != string(rightBytes) {
		t.Fatalf("JSON got %s want %s", leftBytes, rightBytes)
	}
}
