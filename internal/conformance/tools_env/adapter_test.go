package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	env "github.com/minifish-org/pith/packages/agent/harness/env"
	tools "github.com/minifish-org/pith/packages/agent/harness/tools"
	outputcapture "github.com/minifish-org/pith/packages/agent/harness/utils/output_capture"
	shelloutput "github.com/minifish-org/pith/packages/agent/harness/utils/shell_output"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The tools-env batch defines image sniffing, base64 encoding, the local
// execution environment and the shell-output helpers as its call/env
// operations. Unknown operations and sources are reported as errors rather
// than silently succeeding.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op      string            `json:"op"`
		File    string            `json:"file"`
		Fn      string            `json:"fn"`
		Args    []json.RawMessage `json:"args"`
		Content string            `json:"content"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "call":
		return runToolsEnvCall(envelope.File, envelope.Fn, envelope.Args)
	case "env":
		return runEnvOperation(ctx, envelope.Content)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

func runToolsEnvCall(file string, fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch {
	case strings.HasSuffix(file, "harness/tools/image.ts"):
		return runImageCall(fn, args)
	case strings.HasSuffix(file, "harness/utils/output-capture.ts"):
		return runOutputCaptureCall(fn, args)
	case strings.HasSuffix(file, "harness/utils/shell-output.ts"):
		return runShellOutputCall(fn, args)
	default:
		return nil, fmt.Errorf("conformance: unsupported source %q", file)
	}
}

func runImageCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "detectSupportedImageMimeType":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: detectSupportedImageMimeType requires bytes")
		}
		buffer, err := decodeByteArray(args[0])
		if err != nil {
			return nil, err
		}
		mime := tools.DetectSupportedImageMimeType(buffer)
		if mime == nil {
			return undefinedValue(), nil
		}
		return json.Marshal(*mime)
	case "encodeBase64":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: encodeBase64 requires bytes")
		}
		buffer, err := decodeByteArray(args[0])
		if err != nil {
			return nil, err
		}
		return json.Marshal(tools.EncodeBase64(buffer))
	default:
		return nil, fmt.Errorf("conformance: unsupported image function %q", fn)
	}
}

func runOutputCaptureCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "sanitizeShellOutput":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: sanitizeShellOutput requires text")
		}
		var text string
		if err := json.Unmarshal(args[0], &text); err != nil {
			return nil, fmt.Errorf("conformance: invalid text: %w", err)
		}
		return json.Marshal(outputcapture.SanitizeShellOutput(text))
	default:
		return nil, fmt.Errorf("conformance: unsupported output-capture function %q", fn)
	}
}

func runShellOutputCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "sanitizeBinaryOutput":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: sanitizeBinaryOutput requires text")
		}
		var text string
		if err := json.Unmarshal(args[0], &text); err != nil {
			return nil, fmt.Errorf("conformance: invalid text: %w", err)
		}
		return json.Marshal(shelloutput.SanitizeBinaryOutput(text))
	default:
		return nil, fmt.Errorf("conformance: unsupported shell-output function %q", fn)
	}
}

func runEnvOperation(_ context.Context, content string) (json.RawMessage, error) {
	root, err := os.MkdirTemp("", "pith-conformance-")
	if err != nil {
		return nil, fmt.Errorf("conformance: create temp root: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	environment := env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: root})
	write := environment.WriteFile("nested/f.txt", []byte(content), harnesscontext.BackgroundContext)
	read := environment.ReadTextFile("nested/f.txt", harnesscontext.BackgroundContext)
	return json.Marshal(struct {
		WriteOK bool `json:"writeOK"`
		Read    any  `json:"read"`
	}{WriteOK: write.OK, Read: read})
}

func decodeByteArray(raw json.RawMessage) ([]byte, error) {
	var values []int
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("conformance: invalid byte array: %w", err)
	}
	buffer := make([]byte, len(values))
	for index, value := range values {
		buffer[index] = byte(value)
	}
	return buffer, nil
}

func undefinedValue() json.RawMessage { return json.RawMessage(`{"$undefined":true}`) }
