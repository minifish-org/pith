// Package harnessmessages is the Go port of
// packages/agent/src/harness/messages.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package models the harness-specific transcript messages that extend the
// provider message union. Application custom messages stay lossless: their raw
// JSON is preserved verbatim, and only the fields needed to project a message
// into the provider view are decoded.
package harnessmessages

import (
	"encoding/json"
	"fmt"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// CompactionSummaryPrefix opens a rendered compaction summary.
const CompactionSummaryPrefix = `The conversation history before this point was compacted into the following summary:

<summary>
`

// CompactionSummarySuffix closes a rendered compaction summary.
const CompactionSummarySuffix = `
</summary>`

// BranchSummaryPrefix opens a rendered branch summary.
const BranchSummaryPrefix = `The following is a summary of a branch that this conversation came back from:

<summary>
`

// BranchSummarySuffix closes a rendered branch summary.
const BranchSummarySuffix = `</summary>`

// Message roles introduced by the harness.
const (
	BashExecutionRole     = "bashExecution"
	CustomRole            = "custom"
	BranchSummaryRole     = "branchSummary"
	CompactionSummaryRole = "compactionSummary"
)

// BashExecutionMessage records one shell command execution in the transcript.
type BashExecutionMessage struct {
	Role               string  `json:"role"`
	Command            string  `json:"command"`
	Output             string  `json:"output"`
	ExitCode           *int    `json:"exitCode,omitempty"`
	Cancelled          bool    `json:"cancelled"`
	Truncated          bool    `json:"truncated"`
	FullOutputPath     *string `json:"fullOutputPath,omitempty"`
	Timestamp          float64 `json:"timestamp"`
	ExcludeFromContext *bool   `json:"excludeFromContext,omitempty"`
}

// CustomMessage records an application-defined message.
type CustomMessage struct {
	Role       string              `json:"role"`
	CustomType string              `json:"customType"`
	Content    aitypes.UserContent `json:"content"`
	Display    bool                `json:"display"`
	Details    json.RawMessage     `json:"details,omitempty"`
	Timestamp  float64             `json:"timestamp"`
}

// BranchSummaryMessage records a summary of a branch the conversation returned
// from.
type BranchSummaryMessage struct {
	Role      string  `json:"role"`
	Summary   string  `json:"summary"`
	FromID    *string `json:"fromId"`
	Timestamp float64 `json:"timestamp"`
}

// CompactionSummaryMessage records a rendered compaction summary.
type CompactionSummaryMessage struct {
	Role         string  `json:"role"`
	Summary      string  `json:"summary"`
	TokensBefore float64 `json:"tokensBefore"`
	Timestamp    float64 `json:"timestamp"`
}

// MessageTimestamp is a Unix-millisecond number or an ISO-8601 string, matching
// the upstream `number | string` timestamp input.
type MessageTimestamp struct {
	number *float64
	text   *string
}

// TimestampFromNumber builds a timestamp from Unix milliseconds.
func TimestampFromNumber(value float64) MessageTimestamp {
	return MessageTimestamp{number: &value}
}

// TimestampFromString builds a timestamp from a date string.
func TimestampFromString(value string) MessageTimestamp {
	return MessageTimestamp{text: &value}
}

// Milliseconds resolves the timestamp to Unix milliseconds.
func (t MessageTimestamp) Milliseconds() float64 {
	if t.number != nil {
		return *t.number
	}
	if t.text == nil {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, time.ANSIC, time.UnixDate} {
		if parsed, err := time.Parse(layout, *t.text); err == nil {
			return float64(parsed.UnixMilli())
		}
	}
	return 0
}

// BashExecutionToText renders a bash execution message as model-visible text.
func BashExecutionToText(msg BashExecutionMessage) string {
	text := "Ran `" + msg.Command + "`\n"
	if msg.Output != "" {
		text += "```\n" + msg.Output + "\n```"
	} else {
		text += "(no output)"
	}
	if msg.Cancelled {
		text += "\n\n(command cancelled)"
	} else if msg.ExitCode != nil && *msg.ExitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", *msg.ExitCode)
	}
	if msg.Truncated && msg.FullOutputPath != nil {
		text += "\n\n[Output truncated. Full output: " + *msg.FullOutputPath + "]"
	}
	return text
}

// CreateBranchSummaryMessage builds a branch summary message.
func CreateBranchSummaryMessage(summary string, fromID *string, timestamp MessageTimestamp) BranchSummaryMessage {
	return BranchSummaryMessage{
		Role:      BranchSummaryRole,
		Summary:   summary,
		FromID:    fromID,
		Timestamp: timestamp.Milliseconds(),
	}
}

// CreateCompactionSummaryMessage builds a compaction summary message.
func CreateCompactionSummaryMessage(summary string, tokensBefore float64, timestamp MessageTimestamp) CompactionSummaryMessage {
	return CompactionSummaryMessage{
		Role:         CompactionSummaryRole,
		Summary:      summary,
		TokensBefore: tokensBefore,
		Timestamp:    timestamp.Milliseconds(),
	}
}

// CreateCustomMessage builds an application custom message.
func CreateCustomMessage(
	customType string,
	content aitypes.UserContent,
	display bool,
	details json.RawMessage,
	timestamp MessageTimestamp,
) CustomMessage {
	return CustomMessage{
		Role:       CustomRole,
		CustomType: customType,
		Content:    content,
		Display:    display,
		Details:    details,
		Timestamp:  timestamp.Milliseconds(),
	}
}

// ConvertToLlm projects the agent transcript onto provider messages.
//
// Harness-only messages are rendered as user messages; bash executions that are
// excluded from context are dropped. It is the Go port of convertToLlm.
func ConvertToLlm(messages []agenttypes.AgentMessage) ([]aitypes.Message, error) {
	out := []aitypes.Message{}
	for _, message := range messages {
		if message.Message != nil {
			switch message.Message.Role {
			case aitypes.SystemMessageRole, aitypes.UserMessageRole, aitypes.AssistantMessageRole, aitypes.ToolResultMessageRole:
				out = append(out, *message.Message)
			}
			continue
		}
		if message.Custom == nil {
			continue
		}
		converted, ok, err := convertCustomMessage(message.Custom)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, converted)
		}
	}
	return out, nil
}

func convertCustomMessage(custom *agenttypes.CustomMessage) (aitypes.Message, bool, error) {
	switch custom.Role {
	case BashExecutionRole:
		var message BashExecutionMessage
		if err := json.Unmarshal(custom.Raw, &message); err != nil {
			return aitypes.Message{}, false, err
		}
		if message.ExcludeFromContext != nil && *message.ExcludeFromContext {
			return aitypes.Message{}, false, nil
		}
		return userMessageFromBlocks([]aitypes.ContentBlock{aitypes.TextBlock(BashExecutionToText(message))}, message.Timestamp), true, nil
	case CustomRole:
		var message CustomMessage
		if err := json.Unmarshal(custom.Raw, &message); err != nil {
			return aitypes.Message{}, false, err
		}
		blocks := contentBlocks(message.Content)
		return userMessageFromBlocks(blocks, message.Timestamp), true, nil
	case BranchSummaryRole:
		var message BranchSummaryMessage
		if err := json.Unmarshal(custom.Raw, &message); err != nil {
			return aitypes.Message{}, false, err
		}
		text := BranchSummaryPrefix + message.Summary + BranchSummarySuffix
		return userMessageFromBlocks([]aitypes.ContentBlock{aitypes.TextBlock(text)}, message.Timestamp), true, nil
	case CompactionSummaryRole:
		var message CompactionSummaryMessage
		if err := json.Unmarshal(custom.Raw, &message); err != nil {
			return aitypes.Message{}, false, err
		}
		text := CompactionSummaryPrefix + message.Summary + CompactionSummarySuffix
		return userMessageFromBlocks([]aitypes.ContentBlock{aitypes.TextBlock(text)}, message.Timestamp), true, nil
	default:
		return aitypes.Message{}, false, nil
	}
}

func contentBlocks(content aitypes.UserContent) []aitypes.ContentBlock {
	if content.Structured {
		if content.Blocks == nil {
			return []aitypes.ContentBlock{}
		}
		return content.Blocks
	}
	return []aitypes.ContentBlock{aitypes.TextBlock(content.Text)}
}

func userMessageFromBlocks(blocks []aitypes.ContentBlock, timestamp float64) aitypes.Message {
	message := aitypes.NewUserMessageBlocks(blocks, timestamp)
	return aitypes.NewUserMessageVariant(message)
}
