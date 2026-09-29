package output_capture

import (
	"context"
	"strings"
	"sync"
	"testing"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	adaptive "github.com/minifish-org/pith/packages/agent/harness/utils/adaptive_publisher"
)

type fakeClock struct {
	mu     sync.Mutex
	now    int64
	timers []*fakeTimer
}

type fakeTimer struct {
	at      int64
	fn      func()
	stopped bool
}

func (c *fakeClock) NowMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(ms int64, fn func()) adaptive.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{at: c.now + ms, fn: fn}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *fakeTimer) Stop() bool {
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (c *fakeClock) advance(ms int64) {
	c.mu.Lock()
	target := c.now + ms
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, timer := range c.timers {
			if timer.stopped || timer.at > target {
				continue
			}
			if next == nil || timer.at < next.at {
				next = timer
			}
		}
		if next != nil {
			next.stopped = true
			if next.at > c.now {
				c.now = next.at
			}
		}
		c.mu.Unlock()
		if next == nil {
			break
		}
		next.fn()
	}
	c.mu.Lock()
	c.now = target
	c.mu.Unlock()
}

type captureHarness struct {
	capture *OutputCapture
	clock   *fakeClock
	updates []harnesstypes.ShellOutputUpdate
}

func newCaptureHarness(maxBytes int, maxLines int, retain harnesstypes.ShellOutputRetention) *captureHarness {
	harness := &captureHarness{clock: &fakeClock{}}
	capture := NewOutputCaptureWithClock(
		&harnesstypes.ShellOutputCaptureOptions{Limits: harnesstypes.ShellOutputLimits{MaxBytes: maxBytes, MaxLines: maxLines, Retain: &retain}},
		context.Background(),
		func(update harnesstypes.ShellOutputUpdate, _ harnesstypes.Context) {
			harness.updates = append(harness.updates, update)
		},
		func(error) {},
		harness.clock,
	)
	harness.capture = capture
	return harness
}

func fold(updates []harnesstypes.ShellOutputUpdate) *harnesstypes.ShellOutputView {
	var output *harnesstypes.ShellOutputView
	for _, update := range updates {
		view := ApplyShellOutputUpdate(output, update)
		output = &view
	}
	return output
}

func TestSanitizeShellOutputRemovesInvalidControls(t *testing.T) {
	input := "a\x00b\tc\nd\re\u0007f\ufff9g\ufffbh😀"
	expected := "ab\tc\ndefgh😀"
	if got := SanitizeShellOutput(input); got != expected {
		t.Fatalf("SanitizeShellOutput = %q, want %q", got, expected)
	}
}

func TestOutputCaptureDecodesUTF8SplitAcrossChunks(t *testing.T) {
	harness := newCaptureHarness(50, 100, harnesstypes.ShellOutputTail)
	bytes := []byte("😀")
	harness.capture.Push(bytes[:2])
	if got := harness.capture.Snapshot().Text; got != "" {
		t.Fatalf("partial rune should not render: %q", got)
	}
	harness.capture.Push(bytes[2:])
	harness.capture.Finish()
	if got := harness.capture.Snapshot().Text; got != "😀" {
		t.Fatalf("split rune did not decode: %q", got)
	}
}

func TestOutputCaptureFirstPublishAndTrickle(t *testing.T) {
	harness := newCaptureHarness(50, 100, harnesstypes.ShellOutputTail)
	harness.capture.Push([]byte("one"))
	if len(harness.updates) != 1 || harness.updates[0].Kind != harnesstypes.ShellOutputUpdateReplace {
		t.Fatalf("first view should publish replace immediately: %#v", harness.updates)
	}
	harness.clock.advance(150)
	harness.capture.Push([]byte(" two"))
	if len(harness.updates) != 2 || harness.updates[1].Kind != harnesstypes.ShellOutputUpdateAppend {
		t.Fatalf("trickle should append: %#v", harness.updates)
	}
	if text := fold(harness.updates); text == nil || text.Text != "one two" {
		t.Fatalf("folded output = %#v", text)
	}
}

func TestOutputCaptureCollapsesBurst(t *testing.T) {
	harness := newCaptureHarness(50, 100, harnesstypes.ShellOutputTail)
	harness.capture.Push([]byte("a"))
	harness.capture.Push([]byte("b"))
	harness.capture.Push([]byte("c"))
	if len(harness.updates) != 1 {
		t.Fatalf("burst should collapse into the first view: %#v", harness.updates)
	}
	harness.clock.advance(100)
	if len(harness.updates) != 2 || harness.updates[1].Kind != harnesstypes.ShellOutputUpdateAppend || *harness.updates[1].Text != "bc" {
		t.Fatalf("trailing view should append the collapsed suffix: %#v", harness.updates)
	}
}

func TestOutputCaptureSlidesPostCapTrickle(t *testing.T) {
	harness := newCaptureHarness(10, 100, harnesstypes.ShellOutputTail)
	harness.capture.Push([]byte("abcdefghij"))
	harness.clock.advance(150)
	harness.capture.Push([]byte("k"))
	if len(harness.updates) < 2 {
		t.Fatalf("expected a post-cap update: %#v", harness.updates)
	}
	update := harness.updates[1]
	if update.Kind != harnesstypes.ShellOutputUpdateSlide || update.Drop == nil || *update.Drop != 1 || *update.Text != "k" {
		t.Fatalf("expected slide drop=1 text=k: %#v", update)
	}
	view := fold(harness.updates)
	if view == nil || view.Text != "bcdefghijk" || view.Truncation.TotalBytes != 11 {
		t.Fatalf("unexpected folded view: %#v", view)
	}
}

func TestOutputCaptureSingleLineByteCount(t *testing.T) {
	harness := newCaptureHarness(10, 100, harnesstypes.ShellOutputTail)
	harness.capture.Push([]byte(strings.Repeat("x", 100)))
	snapshot := harness.capture.Snapshot()
	if snapshot.Text != strings.Repeat("x", 10) {
		t.Fatalf("retained text length = %d", len(snapshot.Text))
	}
	if snapshot.LastLineBytes == nil || *snapshot.LastLineBytes != 100 {
		t.Fatalf("lastLineBytes = %#v", snapshot.LastLineBytes)
	}
	if !snapshot.Truncation.LastLinePartial {
		t.Fatalf("expected a partial line: %#v", snapshot.Truncation)
	}
}

func TestOutputCaptureReplacesAfterCompleteTurnover(t *testing.T) {
	harness := newCaptureHarness(10, 100, harnesstypes.ShellOutputTail)
	harness.capture.Push([]byte("abcdefghij"))
	harness.capture.Push([]byte(strings.Repeat("x", 100)))
	harness.clock.advance(100)
	if len(harness.updates) < 2 || harness.updates[1].Kind != harnesstypes.ShellOutputUpdateReplace {
		t.Fatalf("complete turnover should replace: %#v", harness.updates)
	}
	view := fold(harness.updates)
	if view == nil || len(view.Text) != 10 || view.Truncation.TotalBytes != 110 {
		t.Fatalf("unexpected folded view: %#v", view)
	}
}

func TestOutputCapturePreservesHeadAfterGuard(t *testing.T) {
	harness := newCaptureHarness(100, 2, harnesstypes.ShellOutputHead)
	harness.capture.Push([]byte("first\nsecond\n" + strings.Repeat("tail", 100)))
	if got := harness.capture.Snapshot().Text; got != "first\nsecond" {
		t.Fatalf("head retention = %q", got)
	}
}

func TestOutputCapturePublishesSpillMetadataWithoutText(t *testing.T) {
	harness := newCaptureHarness(50, 100, harnesstypes.ShellOutputTail)
	harness.capture.Push([]byte("output"))
	harness.capture.SetSpillPath("/tmp/output.log")
	last := harness.updates[len(harness.updates)-1]
	if last.Kind != harnesstypes.ShellOutputUpdateMetadata || last.Metadata == nil || last.Metadata.SpillPath == nil || *last.Metadata.SpillPath != "/tmp/output.log" {
		t.Fatalf("expected metadata-only spill update: %#v", last)
	}
	view := fold(harness.updates)
	if view == nil || view.SpillPath == nil || *view.SpillPath != "/tmp/output.log" {
		t.Fatalf("folded spill path missing: %#v", view)
	}
}

func TestApplyShellOutputUpdateAllKinds(t *testing.T) {
	metadata := harnesstypes.ShellOutputMetadata{Truncation: harnesstypes.ShellOutputTruncation{TotalBytes: 5}}
	appendText := " world"
	slideText := "world"
	drop := 6
	replaceView := harnesstypes.ShellOutputView{Text: "fresh"}

	current := &harnesstypes.ShellOutputView{Text: "hello"}
	if got := ApplyShellOutputUpdate(current, harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateAppend, Text: &appendText, Metadata: &metadata}); got.Text != "hello world" || got.Truncation.TotalBytes != 5 {
		t.Fatalf("append = %#v", got)
	}
	if got := ApplyShellOutputUpdate(current, harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateSlide, Text: &slideText, Drop: &drop}); got.Text != "world" {
		t.Fatalf("slide = %#v", got)
	}
	if got := ApplyShellOutputUpdate(current, harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateMetadata, Metadata: &metadata}); got.Text != "hello" {
		t.Fatalf("metadata = %#v", got)
	}
	if got := ApplyShellOutputUpdate(current, harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateReplace, Output: &replaceView}); got.Text != "fresh" {
		t.Fatalf("replace = %#v", got)
	}
	if fold(nil) != nil {
		t.Fatalf("empty fold should be nil")
	}
}
