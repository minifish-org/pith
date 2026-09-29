package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/agent/harness/compaction"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go compaction SDK and returns
// the normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
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
	switch {
	case strings.HasSuffix(file, "harness/compaction/compaction.ts"):
		return runCompactionCall(fn, args)
	case strings.HasSuffix(file, "harness/compaction/utils.ts"):
		return runUtilsCall(fn, args)
	case strings.HasSuffix(file, "harness/compaction/branch-summarization.ts"):
		return runBranchCall(fn, args)
	default:
		return nil, fmt.Errorf("conformance: unsupported source %q", file)
	}
}

func runUtilsCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "createFileOps":
		return marshalResult(compaction.CreateFileOps())
	case "computeFileLists":
		fileOps, err := decodeFileOperations(args, 0)
		if err != nil {
			return nil, err
		}
		readFiles, modifiedFiles := compaction.ComputeFileLists(fileOps)
		return marshalResult(map[string]any{"readFiles": readFiles, "modifiedFiles": modifiedFiles})
	case "extractFileOpsFromMessage":
		message, err := decodeAgentMessage(args, 0)
		if err != nil {
			return nil, err
		}
		fileOps, err := decodeFileOperations(args, 1)
		if err != nil {
			return nil, err
		}
		compaction.ExtractFileOpsFromMessage(message, &fileOps)
		return marshalResult(fileOps)
	case "formatFileOperations":
		readFiles, err := decodeStringSlice(args, 0)
		if err != nil {
			return nil, err
		}
		modifiedFiles, err := decodeStringSlice(args, 1)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.FormatFileOperations(readFiles, modifiedFiles))
	case "serializeConversation":
		messages, err := decodeLlmMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.SerializeConversation(messages))
	default:
		return nil, fmt.Errorf("conformance: unsupported utils function %q", fn)
	}
}

func runCompactionCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "calculateContextTokens":
		usage, err := decodeUsage(args, 0)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.CalculateContextTokens(usage))
	case "getLastAssistantUsage":
		entries, err := decodeEntries(args, 0)
		if err != nil {
			return nil, err
		}
		usage := compaction.GetLastAssistantUsage(entries)
		if usage == nil {
			return undefinedResult(), nil
		}
		return marshalResult(usage)
	case "estimateContextTokens":
		messages, err := decodeAgentMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.EstimateContextTokens(messages))
	case "shouldCompact":
		contextTokens, err := decodeFloat(args, 0)
		if err != nil {
			return nil, err
		}
		contextWindow, err := decodeFloat(args, 1)
		if err != nil {
			return nil, err
		}
		settings, err := decodeCompactionSettings(args, 2)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.ShouldCompact(contextTokens, contextWindow, settings))
	case "estimateTokens":
		message, err := decodeAgentMessage(args, 0)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.EstimateTokens(message))
	case "findTurnStartIndex":
		entries, err := decodeEntries(args, 0)
		if err != nil {
			return nil, err
		}
		entryIndex, err := decodeInt(args, 1)
		if err != nil {
			return nil, err
		}
		startIndex, err := decodeInt(args, 2)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.FindTurnStartIndex(entries, entryIndex, startIndex))
	case "findCutPoint":
		entries, err := decodeEntries(args, 0)
		if err != nil {
			return nil, err
		}
		startIndex, err := decodeInt(args, 1)
		if err != nil {
			return nil, err
		}
		endIndex, err := decodeInt(args, 2)
		if err != nil {
			return nil, err
		}
		keepRecentTokens, err := decodeInt(args, 3)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.FindCutPoint(entries, startIndex, endIndex, keepRecentTokens))
	case "prepareCompaction":
		entries, err := decodeEntries(args, 0)
		if err != nil {
			return nil, err
		}
		settings, err := decodeCompactionSettings(args, 1)
		if err != nil {
			return nil, err
		}
		result, err := compaction.PrepareCompaction(entries, settings)
		if err != nil {
			return nil, err
		}
		if !result.OK {
			return nil, result.Error
		}
		if result.Value == nil {
			return undefinedResult(), nil
		}
		return marshalResult(result.Value)
	case "serializeConversation":
		messages, err := decodeLlmMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return marshalResult(compaction.SerializeConversation(messages))
	default:
		return nil, fmt.Errorf("conformance: unsupported compaction function %q", fn)
	}
}

func runBranchCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "prepareBranchEntries":
		entries, err := decodeEntries(args, 0)
		if err != nil {
			return nil, err
		}
		tokenBudget := 0
		if len(args) > 1 && string(args[1]) != "null" {
			tokenBudget, err = decodeInt(args, 1)
			if err != nil {
				return nil, err
			}
		}
		return marshalResult(compaction.PrepareBranchEntries(entries, tokenBudget))
	default:
		return nil, fmt.Errorf("conformance: unsupported branch-summarization function %q", fn)
	}
}

// --- decoding helpers -------------------------------------------------------

func decodeAgentMessage(args []json.RawMessage, index int) (agenttypes.AgentMessage, error) {
	var message agenttypes.AgentMessage
	if err := unmarshalArg(args, index, &message); err != nil {
		return agenttypes.AgentMessage{}, err
	}
	return message, nil
}

func decodeAgentMessages(args []json.RawMessage, index int) ([]agenttypes.AgentMessage, error) {
	messages := []agenttypes.AgentMessage{}
	if err := unmarshalArg(args, index, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func decodeLlmMessages(args []json.RawMessage, index int) ([]aitypes.Message, error) {
	messages := []aitypes.Message{}
	if err := unmarshalArg(args, index, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func decodeUsage(args []json.RawMessage, index int) (aitypes.Usage, error) {
	var usage aitypes.Usage
	err := unmarshalArg(args, index, &usage)
	return usage, err
}

func decodeCompactionSettings(args []json.RawMessage, index int) (harnesstypes.CompactionSettings, error) {
	var settings harnesstypes.CompactionSettings
	err := unmarshalArg(args, index, &settings)
	return settings, err
}

func decodeFileOperations(args []json.RawMessage, index int) (harnesstypes.FileOperations, error) {
	var fileOps harnesstypes.FileOperations
	err := unmarshalArg(args, index, &fileOps)
	return fileOps, err
}

func decodeStringSlice(args []json.RawMessage, index int) ([]string, error) {
	values := []string{}
	err := unmarshalArg(args, index, &values)
	return values, err
}

func decodeFloat(args []json.RawMessage, index int) (float64, error) {
	var value float64
	err := unmarshalArg(args, index, &value)
	return value, err
}

func decodeInt(args []json.RawMessage, index int) (int, error) {
	var value int
	err := unmarshalArg(args, index, &value)
	return value, err
}

func unmarshalArg(args []json.RawMessage, index int, target any) error {
	if index >= len(args) {
		return fmt.Errorf("conformance: missing argument %d", index)
	}
	if string(args[index]) == "null" {
		return nil
	}
	if err := json.Unmarshal(args[index], target); err != nil {
		return fmt.Errorf("conformance: invalid argument %d: %w", index, err)
	}
	return nil
}

func decodeEntries(args []json.RawMessage, index int) ([]harnesstypes.Entry, error) {
	if index >= len(args) {
		return nil, fmt.Errorf("conformance: missing argument %d", index)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(args[index], &raws); err != nil {
		return nil, fmt.Errorf("conformance: invalid entry list: %w", err)
	}
	entries := make([]harnesstypes.Entry, 0, len(raws))
	for _, raw := range raws {
		entry, err := decodeEntry(raw)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func decodeEntry(raw json.RawMessage) (harnesstypes.Entry, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("conformance: invalid entry: %w", err)
	}
	switch probe.Type {
	case string(harnesstypes.EntryTypeMessage):
		var entry harnesstypes.MessageEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		return entry, nil
	case string(harnesstypes.EntryTypeCompaction):
		var entry harnesstypes.CompactionEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		return entry, nil
	case string(harnesstypes.EntryTypeBranchSummary):
		var entry harnesstypes.BranchSummaryEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		return entry, nil
	case string(harnesstypes.EntryTypeCustom):
		var entry harnesstypes.CustomEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		return entry, nil
	default:
		return nil, fmt.Errorf("conformance: unknown entry type %q", probe.Type)
	}
}

// --- encoding helpers -------------------------------------------------------

func marshalResult(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

// undefinedResult encodes an upstream undefined result distinctly from null, as
// required by the conformance protocol.
func undefinedResult() json.RawMessage {
	return json.RawMessage(`{"$undefined":true}`)
}
