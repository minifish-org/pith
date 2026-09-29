package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	resources "github.com/minifish-org/pith/packages/agent/harness/resources"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized JSON result. It never reads expected results, golden files or TS
// sources and never reimplements SDK behavior.
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch envelope.Op {
	case "call":
		return runCoreResourcesCall(envelope.File, envelope.Fn, envelope.Args)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

func runCoreResourcesCall(file string, fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch {
	case strings.HasSuffix(file, "harness/prompt-templates.ts"):
		return runPromptTemplateCall(fn, args)
	case strings.HasSuffix(file, "harness/skills.ts"):
		return runSkillsCall(fn, args)
	default:
		return nil, fmt.Errorf("conformance: unsupported source %q", file)
	}
}

func runPromptTemplateCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "parseCommandArgs":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: parseCommandArgs requires an argument string")
		}
		var input string
		if err := json.Unmarshal(args[0], &input); err != nil {
			return nil, fmt.Errorf("conformance: invalid argument string: %w", err)
		}
		return json.Marshal(resources.ParseCommandArgs(input))
	case "substituteArgs":
		if len(args) < 2 {
			return nil, fmt.Errorf("conformance: substituteArgs requires content and args")
		}
		var content string
		if err := json.Unmarshal(args[0], &content); err != nil {
			return nil, fmt.Errorf("conformance: invalid content: %w", err)
		}
		var commandArgs []string
		if err := json.Unmarshal(args[1], &commandArgs); err != nil {
			return nil, fmt.Errorf("conformance: invalid args: %w", err)
		}
		return json.Marshal(resources.SubstituteArgs(content, commandArgs))
	default:
		return nil, fmt.Errorf("conformance: unsupported prompt-templates function %q", fn)
	}
}

func runSkillsCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "formatSkillInvocation":
		if len(args) < 1 {
			return nil, fmt.Errorf("conformance: formatSkillInvocation requires a skill")
		}
		var skill harnesstypes.Skill
		if err := json.Unmarshal(args[0], &skill); err != nil {
			return nil, fmt.Errorf("conformance: invalid skill: %w", err)
		}
		var additional *string
		if len(args) > 1 {
			var value string
			if err := json.Unmarshal(args[1], &value); err != nil {
				return nil, fmt.Errorf("conformance: invalid additional instructions: %w", err)
			}
			additional = &value
		}
		return json.Marshal(resources.FormatSkillInvocation(skill, additional))
	default:
		return nil, fmt.Errorf("conformance: unsupported skills function %q", fn)
	}
}
