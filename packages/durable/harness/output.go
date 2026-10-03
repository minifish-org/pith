package harness

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Default output limits from the pinned truncate.ts.
const (
	DefaultMaxBytes = 50 * 1024
	DefaultMaxLines = 2000
)

// BoundedOutput is retained output and what the limits dropped.
type BoundedOutput struct {
	Text         string
	DroppedBytes int
	DroppedLines int
}

// OutputSlice is one exact slice of the input within the limits.
type OutputSlice struct {
	Text         string
	Bytes        int
	DroppedBytes int
	DroppedLines int
}

// SanitizeOutput removes control characters that break display and transcripts;
// tabs and newlines stay.
func SanitizeOutput(text string) string {
	var builder strings.Builder
	for _, r := range text {
		if r == '\t' || r == '\n' {
			builder.WriteRune(r)
			continue
		}
		if r < 0x20 {
			continue
		}
		if r >= 0xfff9 && r <= 0xfffb {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// BoundOutput bounds text to whole lines within the limits.
func BoundOutput(text string, limits OutputLimits) OutputSlice {
	bytes := []byte(text)
	var from, to int
	if limits.Retain == "tail" {
		from, to = tailRange(bytes, limits)
	} else {
		from, to = headRange(bytes, limits)
	}
	kept := bytes[from:to]
	result := OutputSlice{
		Bytes:        len(kept),
		DroppedBytes: len(bytes) - len(kept),
		DroppedLines: countLines(bytes) - countLines(kept),
	}
	if len(kept) == len(bytes) {
		result.Text = text
	} else {
		result.Text = string(kept)
	}
	return result
}

func headRange(bytes []byte, limits OutputLimits) (int, int) {
	if limits.MaxLines == 0 || limits.MaxBytes == 0 {
		return 0, 0
	}
	end := len(bytes)
	lines := 0
	for index := indexByte(bytes, '\n', 0); index != -1; index = indexByte(bytes, '\n', index+1) {
		lines++
		if lines == limits.MaxLines {
			end = index + 1
			break
		}
	}
	if end > limits.MaxBytes {
		newline := lastIndexByte(bytes, '\n', limits.MaxBytes-1)
		if newline == -1 {
			end = characterEnd(bytes, limits.MaxBytes)
		} else {
			end = newline + 1
		}
	}
	return 0, end
}

func tailRange(bytes []byte, limits OutputLimits) (int, int) {
	length := len(bytes)
	if limits.MaxLines == 0 || limits.MaxBytes == 0 {
		return length, length
	}
	last := length - 1
	if length > 0 && bytes[length-1] == '\n' {
		last = length - 2
	}
	start := 0
	lines := 1
	index := -1
	if last >= 0 {
		index = lastIndexByte(bytes, '\n', last)
	}
	for index != -1 {
		if lines == limits.MaxLines {
			start = index + 1
			break
		}
		lines++
		if index == 0 {
			index = -1
		} else {
			index = lastIndexByte(bytes, '\n', index-1)
		}
	}
	if length-start > limits.MaxBytes {
		from := length - limits.MaxBytes
		newline := indexByte(bytes, '\n', from-1)
		if newline != -1 && newline+1 < length {
			start = newline + 1
		} else {
			start = characterStart(bytes, from)
		}
	}
	return start, length
}

// CharacterEnd returns the last character boundary at or before index.
func CharacterEnd(bytes []byte, index int) int {
	return characterEnd(bytes, index)
}

func characterEnd(bytes []byte, index int) int {
	end := index
	for end > 0 && end < len(bytes) && bytes[end]&0xc0 == 0x80 {
		end--
	}
	return end
}

func characterStart(bytes []byte, index int) int {
	start := index
	for start < len(bytes) && bytes[start]&0xc0 == 0x80 {
		start++
	}
	return start
}

func countLines(bytes []byte) int {
	if len(bytes) == 0 {
		return 0
	}
	newlines := 0
	for index := indexByte(bytes, '\n', 0); index != -1; index = indexByte(bytes, '\n', index+1) {
		newlines++
	}
	if bytes[len(bytes)-1] == '\n' {
		return newlines
	}
	return newlines + 1
}

func indexByte(bytes []byte, b byte, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(bytes); i++ {
		if bytes[i] == b {
			return i
		}
	}
	return -1
}

func lastIndexByte(bytes []byte, b byte, from int) int {
	if from >= len(bytes) {
		from = len(bytes) - 1
	}
	for i := from; i >= 0; i-- {
		if bytes[i] == b {
			return i
		}
	}
	return -1
}

// OutputBuffer is the bounded running output of one tool call. It accepts
// chunks that may split a UTF-8 sequence.
type OutputBuffer struct {
	mu      sync.Mutex
	limits  OutputLimits
	pending []byte
	stored  []byte
	full    bool
	// counts over the whole stream
	totalBytes    int
	totalNewlines int
	endsWithNL    bool
}

// NewOutputBuffer creates a bounded buffer.
func NewOutputBuffer(limits OutputLimits) *OutputBuffer {
	return &OutputBuffer{limits: limits, endsWithNL: true}
}

// Push accepts a chunk; it returns whether anything was accepted.
func (b *OutputBuffer) Push(chunk []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	data := append(append([]byte(nil), b.pending...), chunk...)
	valid := validPrefixLen(data)
	b.pending = append([]byte(nil), data[valid:]...)
	if valid == 0 {
		return false
	}
	return b.accept(string(data[:valid]))
}

// validPrefixLen returns the length of the longest valid UTF-8 prefix that is
// safe to emit, holding back an incomplete trailing sequence.
func validPrefixLen(data []byte) int {
	i := 0
	for i < len(data) {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			// Either invalid bytes or an incomplete trailing sequence: stop here.
			return i
		}
		i += size
	}
	return i
}

// End flushes an incomplete trailing character as a replacement character.
func (b *OutputBuffer) End() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) > 0 {
		b.accept(strings.ToValidUTF8(string(b.pending), "\ufffd"))
		b.pending = nil
	}
}

func (b *OutputBuffer) accept(text string) bool {
	if text == "" {
		return false
	}
	bytes := []byte(text)
	b.totalBytes += len(bytes)
	b.totalNewlines += countNewlines(text)
	b.endsWithNL = strings.HasSuffix(text, "\n")
	if b.full {
		return true
	}
	b.stored = append(b.stored, bytes...)
	if b.limits.Retain == "tail" {
		return true
	}
	if b.limits.MaxBytes > 0 && len(b.stored) > b.limits.MaxBytes {
		b.full = true
		return true
	}
	if b.limits.MaxLines > 0 && countNewlines(string(b.stored)) >= b.limits.MaxLines {
		b.full = true
	}
	return true
}

// Snapshot returns the retained, sanitized output and what the limits dropped.
func (b *OutputBuffer) Snapshot() BoundedOutput {
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := BoundOutput(string(b.stored), b.limits)
	totalLines := lineCount(b.totalNewlines, b.endsWithNL)
	keptLines := countLines([]byte(kept.Text))
	return BoundedOutput{
		Text:         SanitizeOutput(kept.Text),
		DroppedBytes: b.totalBytes - kept.Bytes,
		DroppedLines: totalLines - keptLines,
	}
}

func countNewlines(text string) int {
	return strings.Count(text, "\n")
}

func lineCount(newlines int, terminated bool) int {
	if terminated {
		return newlines
	}
	return newlines + 1
}

// Progress commits what a running tool reported. This Go adaptation commits
// synchronously so a committed slot always holds the output and details a tool
// has published before it is interrupted.
type Progress struct {
	write   func() (int, error)
	onError func(error)
	stopped bool
}

// NewProgress builds a progress committer.
func NewProgress(write func() (int, error), onError func(error)) *Progress {
	return &Progress{write: write, onError: onError}
}

// Mark schedules a commit.
func (p *Progress) Mark() {
	if p == nil || p.stopped {
		return
	}
	if _, err := p.write(); err != nil && p.onError != nil {
		p.onError(err)
	}
}

// MarkAndWait schedules a commit and waits for it.
func (p *Progress) MarkAndWait() error {
	if p == nil || p.stopped {
		return nil
	}
	if _, err := p.write(); err != nil {
		if p.onError != nil {
			p.onError(err)
		}
		return err
	}
	return nil
}

// Stop stops committing.
func (p *Progress) Stop() { p.stopped = true }
