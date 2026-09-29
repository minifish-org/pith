// Package agentruntime is the Go port of the durable AgentHarness runtime in
// packages/agent/src/harness/runtime/.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package owns the process-local drive machine, the durable lane state
// reducers and the transcript/restore projections. Storage, model streaming
// and tool execution are injected; the runtime never reaches for a global
// default.
package agentruntime

import (
	"encoding/json"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// LaneSnapshotReduction reports an observable snapshot rebase request. The
// upstream reducer returns undefined for ordinary events and "rebase" when a
// navigation completes. Go models the JS undefined with the empty value so a
// caller can encode it as the evaluator's {"$undefined":true} marker.
type LaneSnapshotReduction string

// LaneSnapshotReductionRebase requests a fresh snapshot after navigation.
const LaneSnapshotReductionRebase LaneSnapshotReduction = "rebase"

// LaneOperationRetry is the retry projection of an open operation.
type LaneOperationRetry struct {
	Attempt       int     `json:"attempt"`
	MaxAttempts   int     `json:"maxAttempts"`
	NextAttemptAt float64 `json:"nextAttemptAt"`
}

// LaneOperationDeferred is the deferred projection of a suspended operation.
type LaneOperationDeferred struct {
	Handle json.RawMessage `json:"handle"`
	Poll   json.RawMessage `json:"poll"`
}

// LaneSnapshotTool is one tool call observed on an open operation.
type LaneSnapshotTool struct {
	Status     string          `json:"status"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result,omitempty"`
	IsError    *bool           `json:"isError,omitempty"`
}

// LaneSnapshotOperation is the mutable operation projection of a lane.
type LaneSnapshotOperation struct {
	ID               string                 `json:"id"`
	Kind             string                 `json:"kind"`
	StartedAt        float64                `json:"startedAt"`
	FromTipID        *string                `json:"fromTipId"`
	Status           string                 `json:"status"`
	Retry            *LaneOperationRetry    `json:"retry,omitempty"`
	Deferred         *LaneOperationDeferred `json:"deferred,omitempty"`
	StreamingMessage json.RawMessage        `json:"streamingMessage,omitempty"`
	RunningTools     []LaneSnapshotTool     `json:"runningTools"`
}

// LaneStats is the reducer-owned session statistics projection. Usage is kept
// as strict JSON so the exact provider accounting survives a round trip.
type LaneStats struct {
	MessageCount int             `json:"messageCount"`
	Usage        json.RawMessage `json:"usage"`
}

// LaneSnapshot is the reader-facing projection of one lane.
type LaneSnapshot struct {
	Lane          string                              `json:"lane"`
	Transcript    []json.RawMessage                   `json:"transcript"`
	TipID         *string                             `json:"tipId"`
	LastResult    *harnesstypes.OperationResultRecord `json:"lastResult,omitempty"`
	Configuration harnesstypes.LaneConfiguration      `json:"configuration"`
	Stats         LaneStats                           `json:"stats"`
	Operation     *LaneSnapshotOperation              `json:"operation"`
	Queues        json.RawMessage                     `json:"queues"`
	Faulted       *bool                               `json:"faulted,omitempty"`
}

// LaneEvent is the reducer-relevant projection of one harness event. The
// upstream union is spread across the harness assembly; the runtime keeps the
// fields the reducer observes and preserves unknown payloads as strict JSON.
type LaneEvent struct {
	Type string
	Lane *string

	RunID    string
	HasRunID bool

	OperationID string
	StartedAt   float64
	EndedAt     float64
	Status      string
	Error       *harnesstypes.OperationError
	FromTipID   *string
	TipID       *string

	Message json.RawMessage
	Entry   json.RawMessage

	ToolCallID    string
	ToolName      string
	Args          json.RawMessage
	Result        json.RawMessage
	PartialResult json.RawMessage
	IsError       bool

	Attempt     int
	MaxAttempts int
	NotBefore   float64

	Deferred json.RawMessage
	Poll     json.RawMessage
	Queues   json.RawMessage
	Totals   json.RawMessage

	Property string
	Value    json.RawMessage
}

func rawHas(raw json.RawMessage) bool {
	if raw == nil {
		return false
	}
	trimmed := raw
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	return len(trimmed) > 0
}

// UnmarshalJSON decodes the reducer-relevant subset of a harness event.
func (e *LaneEvent) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decode := func(key string, target any) error {
		raw, ok := fields[key]
		if !ok || !rawHas(raw) {
			return nil
		}
		return json.Unmarshal(raw, target)
	}
	if err := decode("type", &e.Type); err != nil {
		return err
	}
	if err := decode("lane", &e.Lane); err != nil {
		return err
	}
	if raw, ok := fields["runId"]; ok && rawHas(raw) {
		e.HasRunID = true
		if err := json.Unmarshal(raw, &e.RunID); err != nil {
			return err
		}
	}
	if err := decode("operationId", &e.OperationID); err != nil {
		return err
	}
	if err := decode("startedAt", &e.StartedAt); err != nil {
		return err
	}
	if err := decode("endedAt", &e.EndedAt); err != nil {
		return err
	}
	if err := decode("status", &e.Status); err != nil {
		return err
	}
	if err := decode("error", &e.Error); err != nil {
		return err
	}
	if err := decode("fromTipId", &e.FromTipID); err != nil {
		return err
	}
	if err := decode("tipId", &e.TipID); err != nil {
		return err
	}
	e.Message = fields["message"]
	e.Entry = fields["entry"]
	if err := decode("toolCallId", &e.ToolCallID); err != nil {
		return err
	}
	if err := decode("toolName", &e.ToolName); err != nil {
		return err
	}
	e.Args = fields["args"]
	e.Result = fields["result"]
	e.PartialResult = fields["partialResult"]
	if err := decode("isError", &e.IsError); err != nil {
		return err
	}
	if err := decode("attempt", &e.Attempt); err != nil {
		return err
	}
	if err := decode("maxAttempts", &e.MaxAttempts); err != nil {
		return err
	}
	if err := decode("notBefore", &e.NotBefore); err != nil {
		return err
	}
	e.Deferred = fields["deferred"]
	e.Poll = fields["poll"]
	e.Queues = fields["queues"]
	e.Totals = fields["totals"]
	if err := decode("property", &e.Property); err != nil {
		return err
	}
	e.Value = fields["value"]
	return nil
}

func upsertTool(operation *LaneSnapshotOperation, tool LaneSnapshotTool) {
	for index := range operation.RunningTools {
		if operation.RunningTools[index].ToolCallID == tool.ToolCallID {
			operation.RunningTools[index] = tool
			return
		}
	}
	operation.RunningTools = append(operation.RunningTools, tool)
}

func matchingOperation(snapshot *LaneSnapshot, operationID string) *LaneSnapshotOperation {
	if snapshot.Operation != nil && snapshot.Operation.ID == operationID {
		return snapshot.Operation
	}
	return nil
}

type minimalMessage struct {
	Role       string `json:"role"`
	StopReason string `json:"stopReason"`
	ToolCallID string `json:"toolCallId"`
}

func parseMinimalMessage(raw json.RawMessage) minimalMessage {
	var message minimalMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &message)
	}
	return message
}

type minimalEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message struct {
		Role       string `json:"role"`
		ToolCallID string `json:"toolCallId"`
	} `json:"message"`
}

// ReduceLaneSnapshot applies one harness event to a mutable lane snapshot.
// Navigation completion requires a fresh snapshot, so it reports "rebase".
func ReduceLaneSnapshot(snapshot *LaneSnapshot, event *LaneEvent) LaneSnapshotReduction {
	if event.Lane != nil && *event.Lane != snapshot.Lane && event.Type != "usage" {
		return ""
	}
	switch event.Type {
	case "run_start":
		snapshot.Operation = &LaneSnapshotOperation{
			ID:           event.RunID,
			Kind:         "run",
			StartedAt:    event.StartedAt,
			FromTipID:    snapshot.TipID,
			Status:       "open",
			RunningTools: []LaneSnapshotTool{},
		}
		return ""
	case "compaction_start":
		if snapshot.Operation != nil {
			return ""
		}
		snapshot.Operation = &LaneSnapshotOperation{
			ID:           event.RunID,
			Kind:         "compaction",
			StartedAt:    event.StartedAt,
			FromTipID:    snapshot.TipID,
			Status:       "open",
			RunningTools: []LaneSnapshotTool{},
		}
		return ""
	case "navigation_start":
		snapshot.Operation = &LaneSnapshotOperation{
			ID:           event.RunID,
			Kind:         "navigation",
			StartedAt:    event.StartedAt,
			FromTipID:    snapshot.TipID,
			Status:       "open",
			RunningTools: []LaneSnapshotTool{},
		}
		return ""
	case "operation_abort":
		if operation := matchingOperation(snapshot, event.OperationID); operation != nil {
			operation.Status = "aborting"
		}
		return ""
	case "run_resume":
		if operation := matchingOperation(snapshot, event.RunID); operation != nil {
			operation.Deferred = nil
		}
		return ""
	case "run_suspend":
		operation := matchingOperation(snapshot, event.RunID)
		if operation == nil {
			return ""
		}
		operation.StreamingMessage = nil
		operation.Deferred = &LaneOperationDeferred{Handle: event.Deferred, Poll: event.Poll}
		return ""
	case "retry_scheduled":
		if operation := matchingOperation(snapshot, event.RunID); operation != nil {
			operation.Retry = &LaneOperationRetry{
				Attempt:       event.Attempt,
				MaxAttempts:   event.MaxAttempts,
				NextAttemptAt: event.NotBefore,
			}
		}
		return ""
	case "retry_start", "retry_end":
		if operation := matchingOperation(snapshot, event.RunID); operation != nil {
			operation.Retry = nil
		}
		return ""
	case "message_start":
		if !event.HasRunID {
			return ""
		}
		message := parseMinimalMessage(event.Message)
		if message.Role != "assistant" || message.StopReason != "pending" {
			return ""
		}
		if operation := matchingOperation(snapshot, event.RunID); operation != nil {
			operation.StreamingMessage = event.Message
		}
		return ""
	case "message_update":
		if parseMinimalMessage(event.Message).Role != "assistant" {
			return ""
		}
		if operation := matchingOperation(snapshot, event.RunID); operation != nil {
			operation.StreamingMessage = event.Message
		}
		return ""
	case "message_end":
		if !event.HasRunID {
			return ""
		}
		if operation := matchingOperation(snapshot, event.RunID); operation != nil {
			operation.StreamingMessage = nil
		}
		return ""
	case "tool_start":
		operation := matchingOperation(snapshot, event.RunID)
		if operation == nil {
			return ""
		}
		upsertTool(operation, LaneSnapshotTool{
			Status:     "running",
			ToolCallID: event.ToolCallID,
			ToolName:   event.ToolName,
			Args:       event.Args,
		})
		return ""
	case "tool_update":
		operation := matchingOperation(snapshot, event.RunID)
		if operation == nil {
			return ""
		}
		for index := range operation.RunningTools {
			tool := &operation.RunningTools[index]
			if tool.ToolCallID == event.ToolCallID && tool.Status == "running" {
				tool.Result = event.PartialResult
			}
		}
		return ""
	case "tool_end":
		operation := matchingOperation(snapshot, event.RunID)
		if operation == nil {
			return ""
		}
		for index := range operation.RunningTools {
			current := operation.RunningTools[index]
			if current.ToolCallID != event.ToolCallID {
				continue
			}
			isError := event.IsError
			operation.RunningTools[index] = LaneSnapshotTool{
				Status:     "settled",
				ToolCallID: event.ToolCallID,
				ToolName:   event.ToolName,
				Args:       current.Args,
				Result:     event.Result,
				IsError:    &isError,
			}
			return ""
		}
		return ""
	case "entry_added":
		entry := minimalEntry{}
		if len(event.Entry) > 0 {
			_ = json.Unmarshal(event.Entry, &entry)
		}
		if entry.Type == "message" && entry.Message.Role == "toolResult" && snapshot.Operation != nil {
			toolCallID := entry.Message.ToolCallID
			for index := range snapshot.Operation.RunningTools {
				if snapshot.Operation.RunningTools[index].ToolCallID == toolCallID {
					snapshot.Operation.RunningTools = append(
						snapshot.Operation.RunningTools[:index],
						snapshot.Operation.RunningTools[index+1:]...,
					)
					break
				}
			}
		}
		if entry.Type == "compaction" {
			snapshot.Transcript = []json.RawMessage{event.Entry}
		} else {
			snapshot.Transcript = append(snapshot.Transcript, event.Entry)
		}
		snapshot.TipID = &entry.ID
		if entry.Type == "message" {
			snapshot.Stats.MessageCount++
		}
		return ""
	case "queue_update":
		snapshot.Queues = event.Queues
		return ""
	case "usage":
		snapshot.Stats.Usage = event.Totals
		return ""
	case "config_update":
		if event.Lane == nil || *event.Lane != snapshot.Lane {
			return ""
		}
		switch event.Property {
		case "model":
			var identity harnesstypes.ModelIdentity
			if json.Unmarshal(event.Value, &identity) == nil {
				snapshot.Configuration.Model = identity
			}
		case "thinkingLevel":
			var level string
			if json.Unmarshal(event.Value, &level) == nil {
				snapshot.Configuration.ThinkingLevel = agenttypes.ThinkingLevel(level)
			}
		case "activeTools":
			var names []string
			if json.Unmarshal(event.Value, &names) == nil {
				snapshot.Configuration.ActiveToolNames = names
			}
		}
		return ""
	case "run_end":
		operation := matchingOperation(snapshot, event.RunID)
		if operation == nil || operation.Kind != "run" {
			return ""
		}
		record := harnesstypes.OperationResultRecord{
			OperationID: event.RunID,
			Kind:        "run",
			Status:      harnesstypes.TerminalStatus(event.Status),
			Error:       nil,
			FromTipID:   event.FromTipID,
			TipID:       event.TipID,
			StartedAt:   operation.StartedAt,
			EndedAt:     event.EndedAt,
		}
		if event.Status == "failed" {
			record.Error = event.Error
		}
		snapshot.LastResult = &record
		snapshot.Operation = nil
		snapshot.TipID = event.TipID
		return ""
	case "compaction_end":
		operation := matchingOperation(snapshot, event.RunID)
		if operation == nil || operation.Kind != "compaction" {
			return ""
		}
		record := harnesstypes.OperationResultRecord{
			OperationID: event.RunID,
			Kind:        "compaction",
			Status:      harnesstypes.TerminalStatus(event.Status),
			FromTipID:   operation.FromTipID,
			TipID:       snapshot.TipID,
			StartedAt:   operation.StartedAt,
			EndedAt:     event.EndedAt,
		}
		if event.Status == "failed" {
			record.Error = event.Error
		}
		snapshot.LastResult = &record
		snapshot.Operation = nil
		return ""
	case "navigation_end":
		return LaneSnapshotReductionRebase
	case "fault":
		faulted := true
		snapshot.Faulted = &faulted
		return ""
	case "handler_error", "turn_start", "turn_end", "value_update", "lane_created":
		return ""
	default:
		return ""
	}
}
