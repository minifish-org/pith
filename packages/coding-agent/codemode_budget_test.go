package codingagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/codemode"
)

func TestCodemodeModelOutputBudget(t *testing.T) {
	registry, err := NewToolRegistry(t.TempDir(), nil, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewCodemodeTool(registry, &codemode.SandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tool.close()
	for _, test := range []struct {
		name, code, prefix, structured string
		failure                        bool
	}{
		{"logs and return share budget", "// @options: {\"max_output_tokens\":2}\ntext('1234'); return {key:'value'};", "1234{\"ke", `{"key":"value"}`, false},
		{"zero suppresses returned text", "// @options: {\"max_output_tokens\":0}\nreturn {key:'value'};", "", `{"key":"value"}`, false},
		{"Unicode prefix does not skip to return", "// @options: {\"max_output_tokens\":2}\ntext('你好🙂'); return {key:'value'};", "你好", `{"key":"value"}`, false},
		{"failure stays visible", "// @options: {\"max_output_tokens\":0}\ntext('hidden'); throw new Error('visible failure');", "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"code": test.code})
			result, err := tool.Execute(context.Background(), args)
			if err != nil || result.IsError != test.failure {
				t.Fatalf("execution: %+v %v", result, err)
			}
			if string(result.StructuredContent) != test.structured {
				t.Fatalf("structured result was cut: %q", result.StructuredContent)
			}
			var text strings.Builder
			for _, block := range result.Content {
				if block.Text != nil {
					text.WriteString(block.Text.Text)
				}
			}
			visible := text.String()
			if !strings.HasPrefix(visible, test.prefix+"[Text output truncated by max_output_tokens;") {
				t.Fatalf("model-visible budget: %q", visible)
			}
			if test.failure && (!strings.Contains(visible, "visible failure") || strings.Contains(visible, "hidden")) {
				t.Fatalf("error hidden by budget: %q", visible)
			}
		})
	}
}
