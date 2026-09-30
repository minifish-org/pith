// Custom message types and transformers for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/messages.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. The accepted Pith
// packages/agent/harness/messages implementation already carries the exact
// message shapes and conversion rules, so this SDK surface re-exports and
// forwards to it instead of duplicating the union or its JSON handling. That
// keeps one owner for every shared type and avoids re-implementing the
// provider protocol.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	harnessmessages "github.com/minifish-org/pith/packages/agent/harness/messages"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// Rendered summary wrappers. They are literal prefixes/suffixes around a
// summary in the model-visible user message.
const (
	CompactionSummaryPrefix = harnessmessages.CompactionSummaryPrefix
	CompactionSummarySuffix = harnessmessages.CompactionSummarySuffix
	BranchSummaryPrefix     = harnessmessages.BranchSummaryPrefix
	BranchSummarySuffix     = harnessmessages.BranchSummarySuffix
)

// Message roles introduced by the coding agent on top of the provider union.
const (
	BashExecutionRole     = harnessmessages.BashExecutionRole
	CustomRole            = harnessmessages.CustomRole
	BranchSummaryRole     = harnessmessages.BranchSummaryRole
	CompactionSummaryRole = harnessmessages.CompactionSummaryRole
)

// BashExecutionMessage is the transcript record of one shell execution.
type BashExecutionMessage = harnessmessages.BashExecutionMessage

// CustomMessage is an application-defined message injected into the transcript.
type CustomMessage = harnessmessages.CustomMessage

// BranchSummaryMessage summarizes a branch the conversation returned from.
type BranchSummaryMessage = harnessmessages.BranchSummaryMessage

// CompactionSummaryMessage renders a compaction checkpoint for the model.
type CompactionSummaryMessage = harnessmessages.CompactionSummaryMessage

// MessageTimestamp accepts the upstream `number | string` timestamp input.
type MessageTimestamp = harnessmessages.MessageTimestamp

// TimestampFromNumber builds a timestamp from Unix milliseconds.
func TimestampFromNumber(value float64) MessageTimestamp {
	return harnessmessages.TimestampFromNumber(value)
}

// TimestampFromString builds a timestamp from an ISO-8601 date string.
func TimestampFromString(value string) MessageTimestamp {
	return harnessmessages.TimestampFromString(value)
}

// BashExecutionToText renders a bash execution message as model-visible text.
func BashExecutionToText(msg BashExecutionMessage) string {
	return harnessmessages.BashExecutionToText(msg)
}

// CreateBranchSummaryMessage builds a branch summary message.
func CreateBranchSummaryMessage(summary string, fromID *string, timestamp MessageTimestamp) BranchSummaryMessage {
	return harnessmessages.CreateBranchSummaryMessage(summary, fromID, timestamp)
}

// CreateCompactionSummaryMessage builds a compaction summary message.
func CreateCompactionSummaryMessage(
	summary string,
	tokensBefore float64,
	timestamp MessageTimestamp,
) CompactionSummaryMessage {
	return harnessmessages.CreateCompactionSummaryMessage(summary, tokensBefore, timestamp)
}

// CreateCustomMessage builds an application custom message.
func CreateCustomMessage(
	customType string,
	content aitypes.UserContent,
	display bool,
	details []byte,
	timestamp MessageTimestamp,
) CustomMessage {
	return harnessmessages.CreateCustomMessage(customType, content, display, details, timestamp)
}

// ConvertToLlm projects the agent transcript onto provider messages.
//
// Harness-only messages become user messages; bash executions flagged as
// excluded from context are dropped. It is the Go port of convertToLlm.
func ConvertToLlm(messages []agenttypes.AgentMessage) ([]aitypes.Message, error) {
	return harnessmessages.ConvertToLlm(messages)
}
