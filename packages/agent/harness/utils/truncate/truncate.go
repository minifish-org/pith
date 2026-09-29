// Package truncate is the Go port of
// packages/agent/src/harness/utils/truncate.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Truncation is based on two independent limits: whichever is hit first wins.
// Complete lines are never cut in the head path; the tail path may return a
// partial first line when a single line exceeds the byte limit.
package truncate

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// DefaultMaxLines is the default line limit.
const DefaultMaxLines = 2000

// DefaultMaxBytes is the default byte limit (50KB).
const DefaultMaxBytes = 50 * 1024

// GrepMaxLineLength is the maximum character count of one grep match line.
const GrepMaxLineLength = 500

// TruncationResult describes one truncation decision and its output.
type TruncationResult struct {
	Content               string  `json:"content"`
	Truncated             bool    `json:"truncated"`
	TruncatedBy           *string `json:"truncatedBy"`
	TotalLines            int     `json:"totalLines"`
	TotalBytes            int     `json:"totalBytes"`
	OutputLines           int     `json:"outputLines"`
	OutputBytes           int     `json:"outputBytes"`
	LastLinePartial       bool    `json:"lastLinePartial"`
	FirstLineExceedsLimit bool    `json:"firstLineExceedsLimit"`
	MaxLines              int     `json:"maxLines"`
	MaxBytes              int     `json:"maxBytes"`
}

// TruncationOptions overrides the default limits.
type TruncationOptions struct {
	MaxLines *int `json:"maxLines,omitempty"`
	MaxBytes *int `json:"maxBytes,omitempty"`
}

// Utf8ByteLength returns the UTF-8 encoded byte length of content.
func Utf8ByteLength(content string) int {
	return len(content)
}

// FormatSize renders bytes as a human-readable size.
func FormatSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

func stringPointer(value string) *string { return &value }

func splitLinesForCounting(content string) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func firstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// TruncateHead keeps the first N lines/bytes. Suitable for file reads. Never
// returns partial lines.
func TruncateHead(content string, options TruncationOptions) TruncationResult {
	maxLines := DefaultMaxLines
	if options.MaxLines != nil {
		maxLines = *options.MaxLines
	}
	maxBytes := DefaultMaxBytes
	if options.MaxBytes != nil {
		maxBytes = *options.MaxBytes
	}

	totalBytes := Utf8ByteLength(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:               content,
			Truncated:             false,
			TruncatedBy:           nil,
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           totalLines,
			OutputBytes:           totalBytes,
			LastLinePartial:       false,
			FirstLineExceedsLimit: false,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	firstLineBytes := Utf8ByteLength(firstLine(lines))
	if firstLineBytes > maxBytes {
		return TruncationResult{
			Content:               "",
			Truncated:             true,
			TruncatedBy:           stringPointer("bytes"),
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           0,
			OutputBytes:           0,
			LastLinePartial:       false,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	outputLinesArr := make([]string, 0, len(lines))
	outputBytesCount := 0
	truncatedBy := "lines"
	for i := 0; i < len(lines) && i < maxLines; i++ {
		line := lines[i]
		lineBytes := Utf8ByteLength(line)
		if i > 0 {
			lineBytes++
		}
		if outputBytesCount+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}
		outputLinesArr = append(outputLinesArr, line)
		outputBytesCount += lineBytes
	}
	if len(outputLinesArr) >= maxLines && outputBytesCount <= maxBytes {
		truncatedBy = "lines"
	}

	outputContent := strings.Join(outputLinesArr, "\n")
	return TruncationResult{
		Content:               outputContent,
		Truncated:             true,
		TruncatedBy:           stringPointer(truncatedBy),
		TotalLines:            totalLines,
		TotalBytes:            totalBytes,
		OutputLines:           len(outputLinesArr),
		OutputBytes:           Utf8ByteLength(outputContent),
		LastLinePartial:       false,
		FirstLineExceedsLimit: false,
		MaxLines:              maxLines,
		MaxBytes:              maxBytes,
	}
}

// TruncateTail keeps the last N lines/bytes. Suitable for command output. May
// return a partial first line when the last line exceeds the byte limit.
func TruncateTail(content string, options TruncationOptions) TruncationResult {
	maxLines := DefaultMaxLines
	if options.MaxLines != nil {
		maxLines = *options.MaxLines
	}
	maxBytes := DefaultMaxBytes
	if options.MaxBytes != nil {
		maxBytes = *options.MaxBytes
	}

	totalBytes := Utf8ByteLength(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:               content,
			Truncated:             false,
			TruncatedBy:           nil,
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           totalLines,
			OutputBytes:           totalBytes,
			LastLinePartial:       false,
			FirstLineExceedsLimit: false,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	outputLinesArr := make([]string, 0, len(lines))
	outputBytesCount := 0
	truncatedBy := "lines"
	lastLinePartial := false
	for i := len(lines) - 1; i >= 0 && len(outputLinesArr) < maxLines; i-- {
		line := lines[i]
		lineBytes := Utf8ByteLength(line)
		if len(outputLinesArr) > 0 {
			lineBytes++
		}
		if outputBytesCount+lineBytes > maxBytes {
			truncatedBy = "bytes"
			if len(outputLinesArr) == 0 {
				truncatedLine := truncateStringToBytesFromEnd(line, maxBytes)
				outputLinesArr = append([]string{truncatedLine}, outputLinesArr...)
				outputBytesCount = Utf8ByteLength(truncatedLine)
				lastLinePartial = true
			}
			break
		}
		outputLinesArr = append([]string{line}, outputLinesArr...)
		outputBytesCount += lineBytes
	}
	if len(outputLinesArr) >= maxLines && outputBytesCount <= maxBytes {
		truncatedBy = "lines"
	}

	outputContent := strings.Join(outputLinesArr, "\n")
	return TruncationResult{
		Content:               outputContent,
		Truncated:             true,
		TruncatedBy:           stringPointer(truncatedBy),
		TotalLines:            totalLines,
		TotalBytes:            totalBytes,
		OutputLines:           len(outputLinesArr),
		OutputBytes:           Utf8ByteLength(outputContent),
		LastLinePartial:       lastLinePartial,
		FirstLineExceedsLimit: false,
		MaxLines:              maxLines,
		MaxBytes:              maxBytes,
	}
}

// truncateStringToBytesFromEnd keeps the trailing bytes of one line without
// splitting a multi-byte rune.
func truncateStringToBytesFromEnd(str string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	bytesUsed := 0
	start := len(str)
	for i := len(str); i > 0; {
		_, size := utf8.DecodeLastRuneInString(str[:i])
		if size <= 0 {
			size = 1
		}
		if bytesUsed+size > maxBytes {
			break
		}
		bytesUsed += size
		i -= size
		start = i
	}
	return str[start:]
}

// TruncateLine truncates a single line to maxChars runes, appending the
// upstream "[truncated]" suffix. The upstream counter is JS string length
// (UTF-16 code units); the Go adaptation counts runes, which is identical for
// all BMP text and only differs for surrogate pairs.
func TruncateLine(line string, maxChars int) (string, bool) {
	runes := []rune(line)
	if len(runes) <= maxChars {
		return line, false
	}
	return string(runes[:maxChars]) + "... [truncated]", true
}
