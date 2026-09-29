// Package output_capture is the Go port of
// packages/agent/src/harness/utils/output-capture.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// OutputCapture maintains and publishes one bounded shell-output view. Writes
// received while publication is rate-limited collapse into the latest view.
// Small changes remain responsive; complete window turnovers purchase a
// proportionally longer delay. The first update after idle and an explicit
// final flush are immediate.
package output_capture

import (
	"encoding/json"
	"strings"
	"sync"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	adaptive "github.com/minifish-org/pith/packages/agent/harness/utils/adaptive_publisher"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
)

// OutputMinEmitIntervalMs is the lower bound between output publications.
const OutputMinEmitIntervalMs = 100

// OutputTargetBytesPerSecond is the output publication throughput budget.
const OutputTargetBytesPerSecond = 100 * 1024

// OutputCapture maintains one bounded, sanitized view of a process's combined
// output and publishes adaptive incremental updates through the supplied
// callback.
type OutputCapture struct {
	maxBytes int
	maxLines int
	retain   harnesstypes.ShellOutputRetention
	context  harnesstypes.Context
	onUpdate func(harnesstypes.ShellOutputUpdate, harnesstypes.Context)

	decoder          utf8StreamDecoder
	mu               sync.Mutex
	buffer           string
	bufferBytes      int
	totalBytes       int
	newlines         int
	endsWithNewline  bool
	currentLineBytes int
	spillPath        *string
	disposed         bool
	publisher        *adaptive.AdaptivePublisher[harnesstypes.ShellOutputView, harnesstypes.ShellOutputUpdate]
}

// NewOutputCapture builds an output capture bound to a context whose
// cancellation does not itself stop the publisher.
func NewOutputCapture(
	options *harnesstypes.ShellOutputCaptureOptions,
	ctx harnesstypes.Context,
	onUpdate func(harnesstypes.ShellOutputUpdate, harnesstypes.Context),
	onError func(error),
) *OutputCapture {
	return NewOutputCaptureWithClock(options, ctx, onUpdate, onError, nil)
}

// NewOutputCaptureWithClock behaves like NewOutputCapture but schedules
// publications through the supplied clock. A nil clock selects RealClock.
func NewOutputCaptureWithClock(
	options *harnesstypes.ShellOutputCaptureOptions,
	ctx harnesstypes.Context,
	onUpdate func(harnesstypes.ShellOutputUpdate, harnesstypes.Context),
	onError func(error),
	clock adaptive.Clock,
) *OutputCapture {
	maxBytes := truncate.DefaultMaxBytes
	maxLines := truncate.DefaultMaxLines
	retain := harnesstypes.ShellOutputTail
	if options != nil {
		if options.Limits.MaxBytes > 0 {
			maxBytes = options.Limits.MaxBytes
		}
		if options.Limits.MaxLines > 0 {
			maxLines = options.Limits.MaxLines
		}
		if options.Limits.Retain != nil {
			retain = *options.Limits.Retain
		}
	}
	capture := &OutputCapture{
		maxBytes:        maxBytes,
		maxLines:        maxLines,
		retain:          retain,
		context:         ctx,
		onUpdate:        onUpdate,
		endsWithNewline: true,
	}
	capture.publisher = adaptive.NewAdaptivePublisher(adaptive.AdaptivePublisherOptions[harnesstypes.ShellOutputView, harnesstypes.ShellOutputUpdate]{
		Snapshot: capture.Snapshot,
		Update:   updateFrom,
		Measure: func(update harnesstypes.ShellOutputUpdate) int {
			encoded, err := json.Marshal(update)
			if err != nil {
				return 0
			}
			return len(encoded)
		},
		Publish: func(update harnesstypes.ShellOutputUpdate) {
			if capture.onUpdate != nil {
				capture.onUpdate(update, capture.context)
			}
		},
		OnError:              onError,
		MinIntervalMs:        intPointer(OutputMinEmitIntervalMs),
		TargetBytesPerSecond: intPointer(OutputTargetBytesPerSecond),
		Clock:                clock,
	})
	return capture
}

func intPointer(value int) *int { return &value }

// Truncated reports whether the complete output exceeds the retained view.
func (c *OutputCapture) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncatedLocked()
}

func (c *OutputCapture) truncatedLocked() bool {
	return c.totalBytes > c.maxBytes || c.totalLines() > c.maxLines
}

// PushText appends decoded text, flushing any pending partial rune first.
func (c *OutputCapture) PushText(text string) {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return
	}
	flushed := c.decoder.Decode(nil, false)
	c.appendTextLocked(flushed)
	c.appendTextLocked(text)
	c.mu.Unlock()
	if flushed != "" || text != "" {
		c.publisher.MarkDirty()
	}
}

// Push appends raw process bytes, decoding UTF-8 across chunk boundaries.
func (c *OutputCapture) Push(chunk []byte) {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return
	}
	text := c.decoder.Decode(chunk, true)
	c.appendTextLocked(text)
	c.mu.Unlock()
	if text != "" {
		c.publisher.MarkDirty()
	}
}

// Finish flushes any partial rune from the stream decoder.
func (c *OutputCapture) Finish() {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return
	}
	text := c.decoder.Decode(nil, false)
	c.appendTextLocked(text)
	c.mu.Unlock()
	if text != "" {
		c.publisher.MarkDirty()
	}
}

// SetSpillPath records the full-output file and publishes the metadata.
func (c *OutputCapture) SetSpillPath(path string) {
	c.mu.Lock()
	if c.disposed || (c.spillPath != nil && *c.spillPath == path) {
		c.mu.Unlock()
		return
	}
	c.spillPath = &path
	c.mu.Unlock()
	c.publisher.MarkDirty()
	c.Flush()
}

// Snapshot returns the current bounded view.
func (c *OutputCapture) Snapshot() harnesstypes.ShellOutputView {
	c.mu.Lock()
	defer c.mu.Unlock()
	var retained truncate.TruncationResult
	if c.retain == harnesstypes.ShellOutputHead {
		retained = truncate.TruncateHead(c.buffer, truncate.TruncationOptions{MaxBytes: intPointer(c.maxBytes), MaxLines: intPointer(c.maxLines)})
	} else {
		retained = truncate.TruncateTail(c.buffer, truncate.TruncationOptions{MaxBytes: intPointer(c.maxBytes), MaxLines: intPointer(c.maxLines)})
	}
	totalLines := c.totalLines()
	truncated := c.truncatedLocked()
	truncation := harnesstypes.ShellOutputTruncationFrom(retained)
	truncation.Truncated = truncated
	if truncated {
		if totalLines > c.maxLines {
			truncation.TruncatedBy = stringPointer("lines")
		} else {
			truncation.TruncatedBy = stringPointer("bytes")
		}
	} else {
		truncation.TruncatedBy = nil
	}
	truncation.TotalBytes = c.totalBytes
	truncation.TotalLines = totalLines
	metadata := harnesstypes.ShellOutputMetadata{Truncation: truncation}
	if c.spillPath != nil {
		metadata.SpillPath = c.spillPath
	}
	if retained.LastLinePartial {
		bytes := c.currentLineBytes
		metadata.LastLineBytes = &bytes
	}
	return harnesstypes.ShellOutputView{
		ShellOutputMetadata: metadata,
		Text:                SanitizeShellOutput(retained.Content),
	}
}

func stringPointer(value string) *string { return &value }

// Flush publishes the pending view immediately.
func (c *OutputCapture) Flush() {
	c.mu.Lock()
	disposed := c.disposed
	c.mu.Unlock()
	if disposed {
		return
	}
	c.publisher.Flush(true)
}

// Dispose stops publication permanently.
func (c *OutputCapture) Dispose() {
	c.publisher.Dispose()
	c.mu.Lock()
	c.disposed = true
	c.mu.Unlock()
}

func (c *OutputCapture) appendTextLocked(text string) {
	if text == "" {
		return
	}
	textBytes := truncate.Utf8ByteLength(text)
	c.totalBytes += textBytes
	c.newlines += countNewlines(text)
	c.endsWithNewline = strings.HasSuffix(text, "\n")
	lastNewline := strings.LastIndex(text, "\n")
	if lastNewline == -1 {
		c.currentLineBytes += textBytes
	} else {
		c.currentLineBytes = truncate.Utf8ByteLength(text[lastNewline+1:])
	}
	c.buffer += text
	c.bufferBytes += textBytes
	guard := c.maxBytes * 2
	if c.bufferBytes > guard*2 {
		if c.retain == harnesstypes.ShellOutputTail {
			c.buffer = trimToLastUtf8Bytes(c.buffer, guard)
		} else {
			c.buffer = trimToFirstUtf8Bytes(c.buffer, guard)
		}
		c.bufferBytes = truncate.Utf8ByteLength(c.buffer)
	}
}

func (c *OutputCapture) totalLines() int {
	if c.endsWithNewline || c.totalBytes == 0 {
		return c.newlines
	}
	return c.newlines + 1
}

// ApplyShellOutputUpdate folds one incremental update into the running view.
func ApplyShellOutputUpdate(current *harnesstypes.ShellOutputView, update harnesstypes.ShellOutputUpdate) harnesstypes.ShellOutputView {
	switch update.Kind {
	case harnesstypes.ShellOutputUpdateReplace:
		if update.Output != nil {
			return *update.Output
		}
		return harnesstypes.ShellOutputView{}
	case harnesstypes.ShellOutputUpdateAppend:
		text := ""
		if current != nil {
			text = current.Text
		}
		if update.Text != nil {
			text += *update.Text
		}
		view := harnesstypes.ShellOutputView{Text: text}
		if update.Metadata != nil {
			view.ShellOutputMetadata = *update.Metadata
		}
		return view
	case harnesstypes.ShellOutputUpdateSlide:
		text := ""
		if current != nil {
			drop := 0
			if update.Drop != nil {
				drop = *update.Drop
			}
			if drop >= 0 && drop <= len(current.Text) {
				text = current.Text[drop:]
			}
		}
		if update.Text != nil {
			text += *update.Text
		}
		view := harnesstypes.ShellOutputView{Text: text}
		if update.Metadata != nil {
			view.ShellOutputMetadata = *update.Metadata
		}
		return view
	case harnesstypes.ShellOutputUpdateMetadata:
		text := ""
		if current != nil {
			text = current.Text
		}
		view := harnesstypes.ShellOutputView{Text: text}
		if update.Metadata != nil {
			view.ShellOutputMetadata = *update.Metadata
		}
		return view
	default:
		if current != nil {
			return *current
		}
		return harnesstypes.ShellOutputView{}
	}
}

func updateFrom(previous *harnesstypes.ShellOutputView, current harnesstypes.ShellOutputView) *harnesstypes.ShellOutputUpdate {
	if previous == nil {
		return &harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateReplace, Output: &current}
	}
	metadata := harnesstypes.ShellOutputMetadata{Truncation: current.Truncation}
	if current.SpillPath != nil {
		metadata.SpillPath = current.SpillPath
	}
	if current.LastLineBytes != nil {
		metadata.LastLineBytes = current.LastLineBytes
	}
	if current.Text == previous.Text {
		return &harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateMetadata, Metadata: &metadata}
	}
	if len(current.Text) > len(previous.Text) && strings.HasPrefix(current.Text, previous.Text) {
		text := current.Text[len(previous.Text):]
		return &harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateAppend, Text: &text, Metadata: &metadata}
	}
	scan := current.Truncation.MaxBytes * 2
	if len(previous.Text) < scan {
		scan = len(previous.Text)
	}
	if len(current.Text) < scan {
		scan = len(current.Text)
	}
	shared := suffixPrefixOverlap(previous.Text, current.Text, scan)
	if shared > 0 {
		drop := len(previous.Text) - shared
		text := current.Text[shared:]
		return &harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateSlide, Drop: &drop, Text: &text, Metadata: &metadata}
	}
	return &harnesstypes.ShellOutputUpdate{Kind: harnesstypes.ShellOutputUpdateReplace, Output: &current}
}

func suffixPrefixOverlap(before string, after string, scan int) int {
	if len(before) == 0 || len(after) == 0 || scan == 0 {
		return 0
	}
	tail := before
	if len(before) > scan {
		tail = before[len(before)-scan:]
	}
	probeLengths := []int{64}
	if len(after) < 64 {
		probeLengths[0] = len(after)
	}
	probeLengths = append(probeLengths, 1)
	for _, probeLength := range probeLengths {
		if probeLength <= 0 {
			continue
		}
		probe := after[:probeLength]
		candidates := 0
		for offset := 0; offset <= len(tail); {
			rel := strings.Index(tail[offset:], probe)
			if rel == -1 {
				break
			}
			index := offset + rel
			candidates++
			if candidates > 8 {
				break
			}
			overlapLength := len(tail) - index
			if overlapLength <= len(after) && tail[index:] == after[:overlapLength] {
				return overlapLength
			}
			offset = index + 1
		}
		if probeLength == 1 {
			break
		}
	}
	return 0
}

// SanitizeShellOutput removes invalid control characters without changing
// text or line boundaries. Tab and newline are preserved; other C0 controls
// and the interlinear annotation controls are dropped.
func SanitizeShellOutput(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		invalid := r <= 0x08 || (r >= 0x0b && r <= 0x1f) || (r >= 0xfff9 && r <= 0xfffb)
		if invalid {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func countNewlines(text string) int {
	return strings.Count(text, "\n")
}

func trimToLastUtf8Bytes(text string, maxBytes int) string {
	bytes := []byte(text)
	if len(bytes) <= maxBytes {
		return text
	}
	start := len(bytes) - maxBytes
	for start < len(bytes) && bytes[start]&0xc0 == 0x80 {
		start++
	}
	return string(bytes[start:])
}

func trimToFirstUtf8Bytes(text string, maxBytes int) string {
	bytes := []byte(text)
	if len(bytes) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && bytes[end]&0xc0 == 0x80 {
		end--
	}
	return string(bytes[:end])
}

func decodeUTF8(data []byte) string {
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

// utf8StreamDecoder mirrors the streaming TextDecoder used upstream.
type utf8StreamDecoder struct {
	pending []byte
}

// Decode decodes a chunk. With stream set, an incomplete trailing rune is
// retained until the next chunk; otherwise it is replaced.
func (d *utf8StreamDecoder) Decode(chunk []byte, stream bool) string {
	data := chunk
	if len(d.pending) > 0 {
		combined := make([]byte, 0, len(d.pending)+len(chunk))
		combined = append(combined, d.pending...)
		combined = append(combined, chunk...)
		d.pending = nil
		data = combined
	}
	if !stream {
		return decodeUTF8(data)
	}
	if incomplete := incompleteSuffixLen(data); incomplete > 0 {
		cut := len(data) - incomplete
		d.pending = append([]byte{}, data[cut:]...)
		return decodeUTF8(data[:cut])
	}
	return decodeUTF8(data)
}

// incompleteSuffixLen reports the byte length of an incomplete trailing UTF-8
// sequence, or 0 when the data ends on a complete rune.
func incompleteSuffixLen(data []byte) int {
	for i := 1; i <= 3 && i <= len(data); i++ {
		b := data[len(data)-i]
		if b&0x80 == 0 {
			return 0
		}
		if b&0xc0 == 0xc0 {
			need := 1
			switch {
			case b&0xf8 == 0xf0:
				need = 4
			case b&0xf0 == 0xe0:
				need = 3
			case b&0xe0 == 0xc0:
				need = 2
			}
			if i < need {
				return i
			}
			return 0
		}
	}
	return 0
}
