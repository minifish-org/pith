package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/durable/env"
)

// blockingWriteEnv blocks the first write until the test releases it and
// records whether a later write of "second\n" started. Both are synchronization
// barriers, not timing guesses.
type blockingWriteEnv struct {
	*env.Local
	firstStarted  chan struct{}
	release       chan struct{}
	secondStarted atomic.Bool
}

func newBlockingWriteEnv(t *testing.T) *blockingWriteEnv {
	t.Helper()
	return &blockingWriteEnv{
		Local:        newEnv(t),
		firstStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (e *blockingWriteEnv) WriteFile(ctx context.Context, path string, content []byte) error {
	switch string(content) {
	case "first\n":
		close(e.firstStarted)
		<-e.release
	case "second\n":
		e.secondStarted.Store(true)
	}
	return e.Local.WriteFile(ctx, path, content)
}

// otherNamespaceEnv reports a different file system id while sharing the same
// files, so the mutation queue must treat it as a distinct namespace.
type otherNamespaceEnv struct {
	*blockingWriteEnv
}

func (e *otherNamespaceEnv) ID() string { return "other" }

func TestWriteQueueBlocksUntilAbortedWriteSettles(t *testing.T) {
	e := newBlockingWriteEnv(t)
	api := &fakeAPI{environment: e}
	tool := CreateWriteTool()

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, mustArgs(t, map[string]string{"path": "file.txt", "content": "first\n"}), api)
		firstDone <- err
	}()
	<-e.firstStarted
	cancel()

	secondDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "file.txt", "content": "second\n"}), api)
		secondDone <- err
	}()

	time.Sleep(50 * time.Millisecond)
	if e.secondStarted.Load() {
		t.Fatal("second write started before the first settled")
	}

	close(e.release)
	if err := <-firstDone; err == nil {
		t.Fatal("aborted write reported success")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := e.Local.ReadTextFile(context.Background(), "file.txt")
	if got != "second\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestWriteQueueSerializesAcrossEnvironmentsOfOneFileSystem(t *testing.T) {
	dir := t.TempDir()
	local1, err := env.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := &blockingWriteEnv{Local: local1, firstStarted: make(chan struct{}), release: make(chan struct{})}
	local2, err := env.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	second := &blockingWriteEnv{Local: local2, firstStarted: make(chan struct{}), release: make(chan struct{})}

	tool := CreateWriteTool()
	done1 := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "file.txt", "content": "first\n"}), &fakeAPI{environment: first})
		done1 <- err
	}()
	<-first.firstStarted

	done2 := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "file.txt", "content": "second\n"}), &fakeAPI{environment: second})
		done2 <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if second.secondStarted.Load() {
		t.Fatal("second environment of one file system was not serialized")
	}

	close(first.release)
	if err := <-done1; err != nil {
		t.Fatal(err)
	}
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	got, _ := local2.ReadTextFile(context.Background(), "file.txt")
	if got != "second\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestWriteQueueDoesNotSerializeDifferentFileSystems(t *testing.T) {
	first := newBlockingWriteEnv(t)
	otherLocal, err := env.NewLocal(first.CWD())
	if err != nil {
		t.Fatal(err)
	}
	other := &otherNamespaceEnv{&blockingWriteEnv{Local: otherLocal, firstStarted: make(chan struct{}), release: make(chan struct{})}}

	tool := CreateWriteTool()
	done1 := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "file.txt", "content": "first\n"}), &fakeAPI{environment: first})
		done1 <- err
	}()
	<-first.firstStarted

	done2 := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "file.txt", "content": "second\n"}), &fakeAPI{environment: other})
		done2 <- err
	}()

	deadline := time.After(2 * time.Second)
	for !other.secondStarted.Load() {
		select {
		case <-deadline:
			t.Fatal("different file system was serialized")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(first.release)
	if err := <-done1; err != nil {
		t.Fatal(err)
	}
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
}

func TestWriteQueueCanonicalizesNewFileThroughSymlinkedDirectory(t *testing.T) {
	e := newBlockingWriteEnv(t)
	if err := os.Mkdir(filepath.Join(e.CWD(), "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.CWD(), "real"), filepath.Join(e.CWD(), "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	api := &fakeAPI{environment: e}
	tool := CreateWriteTool()

	done1 := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "link/new.txt", "content": "first\n"}), api)
		done1 <- err
	}()
	<-e.firstStarted

	done2 := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"path": "real/new.txt", "content": "second\n"}), api)
		done2 <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if e.secondStarted.Load() {
		t.Fatal("symlinked path was not serialized with its canonical path")
	}

	close(e.release)
	if err := <-done1; err != nil {
		t.Fatal(err)
	}
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	got, _ := e.Local.ReadTextFile(context.Background(), "real/new.txt")
	if got != "second\n" {
		t.Fatalf("file = %q", got)
	}
}

// blockingEditEnv blocks the first edit write of "ALPHA\nbeta\n" until release.
type blockingEditEnv struct {
	*env.Local
	firstEditWriteStarted  chan struct{}
	release                chan struct{}
	firstEditWriteSettled  atomic.Bool
	secondEditWriteStarted atomic.Bool
}

func newBlockingEditEnv(t *testing.T) *blockingEditEnv {
	t.Helper()
	return &blockingEditEnv{Local: newEnv(t), firstEditWriteStarted: make(chan struct{}), release: make(chan struct{})}
}

func (e *blockingEditEnv) WriteFile(ctx context.Context, path string, content []byte) error {
	switch string(content) {
	case "ALPHA\nbeta\n":
		close(e.firstEditWriteStarted)
		<-e.release
		// Match the source test: the environment settles the write under a
		// background context so the cancellation is observed by the tool.
		err := e.Local.WriteFile(context.Background(), path, content)
		e.firstEditWriteSettled.Store(true)
		return err
	case "ALPHA\nBETA\n", "alpha\nBETA\n":
		e.secondEditWriteStarted.Store(true)
	}
	return e.Local.WriteFile(ctx, path, content)
}

func TestEditQueueBlocksUntilAbortedEditSettles(t *testing.T) {
	e := newBlockingEditEnv(t)
	if err := e.Local.WriteFile(context.Background(), "file.txt", []byte("alpha\nbeta\n")); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{environment: e}
	tool := CreateEditTool()
	ctx, cancel := context.WithCancel(context.Background())

	firstDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, mustArgs(t, map[string]any{"path": "file.txt", "edits": []any{map[string]string{"oldText": "alpha", "newText": "ALPHA"}}}), api)
		firstDone <- err
	}()
	<-e.firstEditWriteStarted
	cancel()

	secondDone := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": "file.txt", "edits": []any{map[string]string{"oldText": "beta", "newText": "BETA"}}}), api)
		secondDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if e.secondEditWriteStarted.Load() {
		t.Fatal("second edit started before the first settled")
	}

	close(e.release)
	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "Operation aborted") {
		t.Fatalf("first edit err = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second edit: %v", err)
	}
	if !e.firstEditWriteSettled.Load() {
		t.Fatal("first edit write never settled")
	}
	got, _ := e.Local.ReadTextFile(context.Background(), "file.txt")
	if got != "ALPHA\nBETA\n" {
		t.Fatalf("file = %q", got)
	}
}

// slowReadEnv slows reads so two concurrent edits overlap; the queue must still
// serialize them.
type slowReadEnv struct {
	*env.Local
	delay time.Duration
}

func (e *slowReadEnv) ReadTextFile(ctx context.Context, path string) (string, error) {
	time.Sleep(e.delay)
	return e.Local.ReadTextFile(ctx, path)
}

func TestEditQueueSerializesCanonicalAndSymlinkPaths(t *testing.T) {
	dir := t.TempDir()
	local, err := env.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &slowReadEnv{Local: local, delay: 20 * time.Millisecond}
	if err := e.Local.WriteFile(context.Background(), "target.txt", []byte("alpha\nbeta\ngamma\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	api := &fakeAPI{environment: e}
	tool := CreateEditTool()

	done := make(chan error, 2)
	for _, edit := range []map[string]any{
		{"path": "target.txt", "edits": []any{map[string]string{"oldText": "alpha", "newText": "ALPHA"}}},
		{"path": "link.txt", "edits": []any{map[string]string{"oldText": "beta", "newText": "BETA"}}},
	} {
		go func(edit map[string]any) {
			_, err := tool.Execute(context.Background(), mustArgs(t, edit), api)
			done <- err
		}(edit)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.Local.ReadTextFile(context.Background(), "target.txt")
	if got != "ALPHA\nBETA\ngamma\n" {
		t.Fatalf("file = %q", got)
	}
}
