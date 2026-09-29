package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The core-contracts batch defines the pure truncation helpers as its
// call-operations. Other operation kinds belong to later harness slices and are
// reported as errors rather than silently succeeding.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op   string            `json:"op"`
		File string            `json:"file"`
		Fn   string            `json:"fn"`
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "call":
		return runCall(envelope.File, envelope.Fn, envelope.Args)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

func runCall(file string, fn string, args []json.RawMessage) (json.RawMessage, error) {
	if strings.HasSuffix(file, "harness/utils/truncate.ts") {
		return runTruncateCall(fn, args)
	}
	return nil, fmt.Errorf("conformance: unsupported source %q", file)
}

func runTruncateCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "truncateHead", "truncateTail":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: %s requires content", fn)
		}
		var content string
		if err := json.Unmarshal(args[0], &content); err != nil {
			return nil, fmt.Errorf("conformance: invalid content: %w", err)
		}
		options, err := decodeTruncationOptions(args)
		if err != nil {
			return nil, err
		}
		var result truncate.TruncationResult
		if fn == "truncateHead" {
			result = truncate.TruncateHead(content, options)
		} else {
			result = truncate.TruncateTail(content, options)
		}
		return json.Marshal(result)
	case "truncateLine":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: truncateLine requires a line")
		}
		var line string
		if err := json.Unmarshal(args[0], &line); err != nil {
			return nil, fmt.Errorf("conformance: invalid line: %w", err)
		}
		maxChars := truncate.GrepMaxLineLength
		if len(args) > 1 {
			if err := json.Unmarshal(args[1], &maxChars); err != nil {
				return nil, fmt.Errorf("conformance: invalid maxChars: %w", err)
			}
		}
		text, wasTruncated := truncate.TruncateLine(line, maxChars)
		return json.Marshal(map[string]any{"text": text, "wasTruncated": wasTruncated})
	case "formatSize":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: formatSize requires a byte count")
		}
		var bytes int
		if err := json.Unmarshal(args[0], &bytes); err != nil {
			return nil, fmt.Errorf("conformance: invalid byte count: %w", err)
		}
		return json.Marshal(truncate.FormatSize(bytes))
	case "utf8ByteLength":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: utf8ByteLength requires content")
		}
		var content string
		if err := json.Unmarshal(args[0], &content); err != nil {
			return nil, fmt.Errorf("conformance: invalid content: %w", err)
		}
		return json.Marshal(truncate.Utf8ByteLength(content))
	default:
		return nil, fmt.Errorf("conformance: unsupported truncate function %q", fn)
	}
}

func decodeTruncationOptions(args []json.RawMessage) (truncate.TruncationOptions, error) {
	var options truncate.TruncationOptions
	if len(args) < 2 || string(args[1]) == "null" {
		return options, nil
	}
	if err := json.Unmarshal(args[1], &options); err != nil {
		return options, fmt.Errorf("conformance: invalid truncation options: %w", err)
	}
	return options, nil
}
