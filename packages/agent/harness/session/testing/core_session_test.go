package testing

import (
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

var testContext = harnesstypes.Context(nil)

func TestInstrumentedStorageRecordsAttempts(t *testing.T) {
	delegate := session.NewMemoryStorage(&session.MemoryStorageOptions{Now: func() float64 { return 100 }})
	storage := NewInstrumentedStorage(delegate)
	first := []harnesstypes.Write{session.SetValue(session.SessionName, "first")}
	second := []harnesstypes.Write{session.SetValue(session.SessionName, "second")}
	if _, err := storage.Commit(first, testContext); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Commit(second, testContext); err != nil {
		t.Fatal(err)
	}
	attempts := storage.GetCommitAttempts()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	storage.ClearCommitAttempts()
	if len(storage.GetCommitAttempts()) != 0 {
		t.Fatal("expected cleared attempts")
	}
	stored, _, err := storage.GetValue(session.SessionName, testContext)
	if err != nil || stored.Value != "second" {
		t.Fatalf("value = %v %v", stored.Value, err)
	}
}

func TestGatingStorageParksAndReleases(t *testing.T) {
	storage := NewGatingStorage(session.NewMemoryStorage(&session.MemoryStorageOptions{Now: func() float64 { return 10 }}))
	if _, err := storage.Commit([]harnesstypes.Write{session.SetValue(session.SessionName, "setup")}, testContext); err != nil {
		t.Fatal(err)
	}
	if storage.Pending() != 0 {
		t.Fatalf("setup must bypass gating")
	}
	storage.Arm()
	waited := make(chan error, 1)
	go func() { waited <- storage.WaitPending(1) }()
	select {
	case <-waited:
		t.Fatal("WaitPending resolved before a commit parked")
	case <-time.After(50 * time.Millisecond):
	}
	commitDone := make(chan error, 1)
	go func() {
		_, err := storage.Commit([]harnesstypes.Write{session.SetValue(session.SessionName, "parked")}, testContext)
		commitDone <- err
	}()
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	if storage.Pending() != 1 {
		t.Fatalf("pending = %d", storage.Pending())
	}
	stored, _, err := storage.GetValue(session.SessionName, testContext)
	if err != nil || stored.Value != "setup" {
		t.Fatalf("value before release = %v", stored.Value)
	}
	if err := storage.Next(1); err != nil {
		t.Fatal(err)
	}
	if err := <-commitDone; err != nil {
		t.Fatal(err)
	}
	stored, _, err = storage.GetValue(session.SessionName, testContext)
	if err != nil || stored.Value != "parked" {
		t.Fatalf("value after release = %v", stored.Value)
	}
}

func TestGatingStorageDiscardRejectsEverything(t *testing.T) {
	storage := NewGatingStorage(session.NewMemoryStorage(nil))
	storage.Arm()
	commitDone := make(chan error, 1)
	go func() {
		_, err := storage.Commit([]harnesstypes.Write{session.SetValue(session.SessionName, "lost")}, testContext)
		commitDone <- err
	}()
	if err := storage.WaitPending(1); err != nil {
		t.Fatal(err)
	}
	storage.Discard()
	if err := <-commitDone; err == nil {
		t.Fatal("expected parked commit to reject after discard")
	}
	if _, err := storage.Commit(nil, testContext); err == nil {
		t.Fatal("expected later commit to reject")
	}
	if err := storage.Next(1); err == nil {
		t.Fatal("expected next to reject")
	}
}
