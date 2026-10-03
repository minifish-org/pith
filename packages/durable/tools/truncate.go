// Truncation utilities shared by the Durable coding tools.
//
// This is a Go port of packages/durable/src/truncate.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// Truncation is based on two independent limits - whichever is hit first wins:
// a line limit (default 2000) and a byte limit (default 50 KiO). The head path
// never returns partial lines. Tool output streams are bounded separately by
// the harness output buffer, whose tail path may return a partial line.
package tools

import (
	"fmt"
	"strings"
)

// DefaultMaxLines is the default line limit.
const DefaultMaxLines = 2000

// DefaultMaxBytes is the default byte limit (50 KiB).
const DefaultMaxBytes = 50 * 1024

// TruncationResult describes one truncation decision and its output. The
// Content field is deliberately retained for callers and is omitted from the
// read tool's details payload.
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
	MaxLines *int
	MaxBytes *int
}

// Utf8ByteLength returns the UTF-8 encoded byte length of content.
func Utf8ByteLength(content string) int {
	return len(content)
}

// FormatSize renders bytes as a human-readable size exactly like the upstream
// formatSize helper.
func FormatSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

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

// TruncateHead keeps the first N lines/bytes. It never returns partial lines;
// when the first line alone exceeds the byte limit it returns empty content
// with FirstLineExceedsLimit set.
func TruncateHead(content string, options ...TruncationOptions) TruncationResult {
	maxLines := DefaultMaxLines
	maxBytes := DefaultMaxBytes
	if len(options) > 0 {
		if options[0].MaxLines != nil {
			maxLines = *options[0].MaxLines
		}
		if options[0].MaxBytes != nil {
			maxBytes = *options[0].MaxBytes
		}
	}

	totalBytes := Utf8ByteLength(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	firstLineBytes := 0
	if len(lines) > 0 {
		firstLineBytes = Utf8ByteLength(lines[0])
	}
	if firstLineBytes > maxBytes {
		truncatedBy := "bytes"
		return TruncationResult{
			Content:               "",
			Truncated:             true,
			TruncatedBy:           &truncatedBy,
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           0,
			OutputBytes:           0,
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
	// Without a byte break, only omitted lines prove the line limit was
	// reached; otherwise a trailing newline exceeded the byte limit.
	if truncatedBy != "bytes" {
		if len(outputLinesArr) < totalLines {
			truncatedBy = "lines"
		} else {
			truncatedBy = "bytes"
		}
	}

	outputContent := strings.Join(outputLinesArr, "\n")
	return TruncationResult{
		Content:     outputContent,
		Truncated:   true,
		TruncatedBy: &truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(outputLinesArr),
		OutputBytes: Utf8ByteLength(outputContent),
		MaxLines:    maxLines,
		MaxBytes:    maxBytes,
	}
}
