// Package session is the Go port of
// packages/agent/src/harness/session/*.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package models a durable, branched session over an injected Storage
// capability. Values are addressed by (namespace, key) pair with a scalar or
// list kind; entries form a parent-linked tree with monotonically increasing
// sequences.
package session

import (
	"errors"
	"fmt"
	"math"
	"strings"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// StoredAddressBase is the address shared by scalar values and lists.
type StoredAddressBase = harnesstypes.Value

// Value is a stored scalar-value address.
type Value = harnesstypes.Value

// ValueList is a stored list address.
type ValueList = harnesstypes.ValueList

// StoredValue is one stored value with its sequence.
type StoredValue = harnesstypes.StoredValue

// ListElement is one stored list element.
type ListElement = harnesstypes.ListElement

// ListCursor is a list read cursor.
type ListCursor = harnesstypes.ListCursor

// ListReadOptions bounds one list read.
type ListReadOptions = harnesstypes.ListReadOptions

// ValueSetWrite sets one scalar value.
type ValueSetWrite = harnesstypes.ValueSetWrite

// ValueDeleteWrite deletes one scalar value.
type ValueDeleteWrite = harnesstypes.ValueDeleteWrite

// ValueWrite is a scalar-value mutation.
type ValueWrite = harnesstypes.ValueWrite

// ListAppendWrite appends one list element.
type ListAppendWrite = harnesstypes.ListAppendWrite

// ListDeleteWrite deletes one whole list.
type ListDeleteWrite = harnesstypes.ListDeleteWrite

// ListWrite is a list mutation.
type ListWrite = harnesstypes.ListWrite

// ResolvedListReadOptions carries defaulted list read options.
type ResolvedListReadOptions struct {
	Cursor *ListCursor
	Order  string
	Limit  int
}

func validateAddress(namespace string, key string) {
	if namespace == "" {
		panic(errors.New("Value namespace must not be empty"))
	}
	if strings.ContainsRune(namespace, '\x00') {
		panic(errors.New("Value namespace must not contain \\u0000"))
	}
	if strings.ContainsRune(key, '\x00') {
		panic(errors.New("Value key must not contain \\u0000"))
	}
}

func firstKey(key []string) string {
	if len(key) == 0 {
		return ""
	}
	return key[0]
}

// NewValue builds a scalar-value address. It mirrors the upstream `value`
// helper, which validates the address synchronously.
func NewValue(namespace string, key ...string) Value {
	resolved := firstKey(key)
	validateAddress(namespace, resolved)
	return Value{Namespace: namespace, Key: resolved, Kind: "value"}
}

// NewList builds a list address. It mirrors the upstream `list` helper.
func NewList(namespace string, key ...string) ValueList {
	resolved := firstKey(key)
	validateAddress(namespace, resolved)
	return ValueList{Namespace: namespace, Key: resolved, Kind: "list"}
}

// SetValue builds a scalar set write.
func SetValue(address Value, next any) harnesstypes.ValueSetWrite {
	return harnesstypes.ValueSetWrite{Namespace: address.Namespace, Key: address.Key, Value: next}
}

// DeleteValue builds a scalar delete write.
func DeleteValue(address Value) harnesstypes.ValueDeleteWrite {
	return harnesstypes.ValueDeleteWrite{Namespace: address.Namespace, Key: address.Key}
}

// AppendList builds a list append write.
func AppendList(address ValueList, element any) harnesstypes.ListAppendWrite {
	return harnesstypes.ListAppendWrite{Namespace: address.Namespace, Key: address.Key, Value: element}
}

// DeleteList builds a whole-list delete write.
func DeleteList(address ValueList) harnesstypes.ListDeleteWrite {
	return harnesstypes.ListDeleteWrite{Namespace: address.Namespace, Key: address.Key}
}

// ResolveListReadOptions applies the upstream defaults and bounds. The read
// limit defaults to 1000 and is clamped to at most 10000; a cursor is only
// carried when supplied.
func ResolveListReadOptions(options *ListReadOptions) (ResolvedListReadOptions, error) {
	requestedLimit := 1000
	var order string
	var cursor *ListCursor
	if options != nil {
		if options.Limit != nil {
			requestedLimit = *options.Limit
		}
		if options.Order != nil {
			order = *options.Order
		} else {
			order = "asc"
		}
		cursor = options.Cursor
	} else {
		order = "asc"
	}
	if requestedLimit <= 0 {
		return ResolvedListReadOptions{}, errors.New("List read limit must be a positive safe integer")
	}
	if requestedLimit > math.MaxInt32 {
		// The upstream rejects non-safe integers; a value this large is never a
		// meaningful in-memory page and is treated as invalid.
		return ResolvedListReadOptions{}, fmt.Errorf("List read limit must be a positive safe integer: %d", requestedLimit)
	}
	return ResolvedListReadOptions{Cursor: cursor, Order: order, Limit: min(requestedLimit, 10000)}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Reserved session namespaces.
const (
	NamespaceBranchTip      = "pi.branch.tip"
	NamespaceLaneConfig     = "pi.lane.config"
	NamespaceLaneState      = "pi.lane.state"
	NamespaceOperation      = "pi.result"
	NamespaceOperationMeta  = "pi.op.meta"
	NamespaceOperationState = "pi.op.state"
	NamespaceOperationTools = "pi.op.tool_args"
	NamespaceOperationMemo  = "pi.op.tool_memo"
	NamespaceOperationPrep  = "pi.op.preparation"
	NamespacePendingEntry   = "pi.pending.entry"
	NamespacePendingTool    = "pi.pending.tool_output"
	NamespacePendingFrames  = "pi.pending.assistant_frame"
	NamespaceSessionName    = "pi.session.name"
	NamespaceEntryLabel     = "pi.entry.label"
)

// BranchTip addresses the tip entry of one branch.
func BranchTip(branch string) Value { return NewValue(NamespaceBranchTip, branch) }

// BranchTipInventoryPrefix addresses all branch tips.
func BranchTipInventoryPrefix() Value { return NewValue(NamespaceBranchTip) }

// LaneConfig addresses the configuration of one lane.
func LaneConfig(lane string) Value { return NewValue(NamespaceLaneConfig, lane) }

// LaneState addresses the durable state of one lane.
func LaneState(lane string) Value { return NewValue(NamespaceLaneState, lane) }

// OperationResult addresses one operation result record.
func OperationResult(operationID string) Value { return NewValue(NamespaceOperation, operationID) }

// OperationMeta addresses one operation metadata record.
func OperationMeta(operationID string) Value { return NewValue(NamespaceOperationMeta, operationID) }

// OperationState addresses one durable operation state.
func OperationState(operationID string) Value { return NewValue(NamespaceOperationState, operationID) }

// OperationToolArgs addresses the tool arguments of one source index.
func OperationToolArgs(operationID string, stepID string, sourceIndex int) Value {
	return NewValue(NamespaceOperationTools, fmt.Sprintf("%s:%s:%d", operationID, stepID, sourceIndex))
}

// OperationToolMemo addresses one invocation-scoped tool memo.
func OperationToolMemo(operationID string, invocationID string, name string) Value {
	return NewValue(NamespaceOperationMemo, fmt.Sprintf("%s:%s:%s", operationID, invocationID, name))
}

// OperationPreparation addresses one durable structural preparation.
func OperationPreparation(operationID string, taskID string) Value {
	return NewValue(NamespaceOperationPrep, fmt.Sprintf("%s:%s", operationID, taskID))
}

// OperationToolArgsPrefix addresses every tool argument row of an operation.
func OperationToolArgsPrefix(operationID string, stepID ...string) Value {
	if len(stepID) == 0 {
		return NewValue(NamespaceOperationTools, operationID+":")
	}
	return NewValue(NamespaceOperationTools, operationID+":"+stepID[0]+":")
}

// OperationToolMemoPrefix addresses every tool memo row of an operation.
func OperationToolMemoPrefix(operationID string, invocationID ...string) Value {
	if len(invocationID) == 0 {
		return NewValue(NamespaceOperationMemo, operationID+":")
	}
	return NewValue(NamespaceOperationMemo, operationID+":"+invocationID[0]+":")
}

// OperationPreparationPrefix addresses every preparation of an operation.
func OperationPreparationPrefix(operationID string) Value {
	return NewValue(NamespaceOperationPrep, operationID+":")
}

// PendingEntry addresses a reserved pending entry payload.
func PendingEntry(entryID string) Value { return NewValue(NamespacePendingEntry, entryID) }

// PendingToolOutput addresses a reserved pending tool result.
func PendingToolOutput(operationID string, invocationID string) Value {
	return NewValue(NamespacePendingTool, operationID+":"+invocationID)
}

// PendingAssistantFrames addresses the ordered pending assistant frames of a
// response entry.
func PendingAssistantFrames(operationID string, responseEntryID string) ValueList {
	return NewList(NamespacePendingFrames, operationID+":"+responseEntryID)
}

// PendingToolOutputPrefix addresses every pending tool result of an operation.
func PendingToolOutputPrefix(operationID string) Value {
	return NewValue(NamespacePendingTool, operationID+":")
}

// SessionName addresses the session display name.
var SessionName = NewValue(NamespaceSessionName)

// EntryLabel addresses one entry label.
func EntryLabel(entryID string) Value { return NewValue(NamespaceEntryLabel, entryID) }
