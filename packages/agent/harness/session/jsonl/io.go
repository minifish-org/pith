// This file carries jsonl/io.ts: atomic publication and the committed-write
// codec.
package jsonl

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// FileValue unwraps a FileSystem result or returns an action-scoped error.
func FileValue[T any](result harnesstypes.Result[T, harnesstypes.FileError], action string) (T, error) {
	if !result.OK {
		var zero T
		return zero, fmt.Errorf("%s: %s: %w", action, result.Error.Message, &result.Error)
	}
	return result.Value, nil
}

// ReadJsonlHeader reads and parses the first line of a JSONL file.
func ReadJsonlHeader(reader harnesstypes.TextLineReader, path string, ctx harnesstypes.Context) (JsonlParsedSessionHeader, error) {
	line, err := FileValue(reader.ReadLine(ctx), "Failed to read JSONL storage "+path)
	if err != nil {
		return JsonlParsedSessionHeader{}, err
	}
	if line == nil || !line.Terminated || line.Text == "" {
		return JsonlParsedSessionHeader{}, fmt.Errorf("Invalid JSONL storage %s: missing header", path)
	}
	parsed, parseErr := ParseJsonlSessionHeader(line.Text)
	if parseErr != nil {
		return JsonlParsedSessionHeader{}, fmt.Errorf("Invalid JSONL storage %s: invalid header: %w", path, parseErr)
	}
	return parsed, nil
}

func requireSafeInteger(value any, field string, minimum float64) error {
	if !isSafeIntegerAtLeast(value, minimum) {
		return fmt.Errorf("Invalid JSONL %s", field)
	}
	return nil
}

func decodeEntry(record map[string]any) (harnesstypes.Entry, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	entryType, _ := record["type"].(string)
	switch entryType {
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
		return nil, fmt.Errorf("Invalid JSONL entry type: %q", entryType)
	}
}

func parseCommittedWrite(value any) (session.CommittedWrite, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return session.CommittedWrite{}, errors.New("Invalid JSONL transaction write")
	}
	if err := requireSafeInteger(record["seq"], "write seq", 1); err != nil {
		return session.CommittedWrite{}, err
	}
	seq := int(record["seq"].(float64))
	kind, _ := record["kind"].(string)
	switch kind {
	case "entry":
		if err := requireSafeInteger(record["timestamp"], "entry timestamp", 0); err != nil {
			return session.CommittedWrite{}, err
		}
		entry, err := decodeEntry(record)
		if err != nil {
			return session.CommittedWrite{}, err
		}
		return session.CommittedWrite{Kind: "entry", Entry: entry, Seq: seq, Timestamp: record["timestamp"].(float64)}, nil
	case "usage":
		raw, err := json.Marshal(record)
		if err != nil {
			return session.CommittedWrite{}, err
		}
		var row harnesstypes.UsageRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return session.CommittedWrite{}, err
		}
		return session.CommittedWrite{Kind: "usage", Usage: &row, Seq: seq}, nil
	case "value":
		op, _ := record["op"].(string)
		if op != "set" && op != "delete" {
			return session.CommittedWrite{}, fmt.Errorf("Invalid JSONL value operation: %q", op)
		}
		namespace, _ := record["namespace"].(string)
		key, _ := record["key"].(string)
		return session.CommittedWrite{Kind: "value", Op: op, Namespace: namespace, Key: key, Value: record["value"], Seq: seq}, nil
	case "list":
		op, _ := record["op"].(string)
		if op != "append" && op != "delete" {
			return session.CommittedWrite{}, fmt.Errorf("Invalid JSONL list operation: %q", op)
		}
		namespace, _ := record["namespace"].(string)
		key, _ := record["key"].(string)
		return session.CommittedWrite{Kind: "list", Op: op, Namespace: namespace, Key: key, Value: record["value"], Seq: seq}, nil
	default:
		return session.CommittedWrite{}, fmt.Errorf("Invalid JSONL write kind: %q", kind)
	}
}

func encodeCommittedWrite(write session.CommittedWrite) (any, error) {
	switch write.Kind {
	case "entry":
		if write.Entry == nil {
			return nil, errors.New("JSONL entry write is missing its entry")
		}
		raw, err := json.Marshal(write.Entry)
		if err != nil {
			return nil, err
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		record["kind"] = "entry"
		return record, nil
	case "usage":
		if write.Usage == nil {
			return nil, errors.New("JSONL usage write is missing its row")
		}
		raw, err := json.Marshal(write.Usage)
		if err != nil {
			return nil, err
		}
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		record["kind"] = "usage"
		return record, nil
	case "value", "list":
		record := map[string]any{
			"kind":      write.Kind,
			"op":        write.Op,
			"seq":       write.Seq,
			"namespace": write.Namespace,
			"key":       write.Key,
		}
		if write.Op == "set" || write.Op == "append" {
			record["value"] = write.Value
		}
		return record, nil
	default:
		return nil, fmt.Errorf("Invalid JSONL write kind: %q", write.Kind)
	}
}

// ParseJsonlTransaction parses one transaction line into committed writes.
func ParseJsonlTransaction(line string) ([]session.CommittedWrite, error) {
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		return nil, fmt.Errorf("Invalid JSONL transaction: not valid JSON: %w", err)
	}
	values := []any{}
	switch typed := value.(type) {
	case []any:
		values = typed
	default:
		values = []any{typed}
	}
	writes := make([]session.CommittedWrite, 0, len(values))
	for _, item := range values {
		write, err := parseCommittedWrite(item)
		if err != nil {
			return nil, err
		}
		writes = append(writes, write)
	}
	return writes, nil
}

// SerializeJsonlTransaction serializes committed writes to one line.
func SerializeJsonlTransaction(writes []session.CommittedWrite) (string, error) {
	encoded := make([]any, 0, len(writes))
	for _, write := range writes {
		value, err := encodeCommittedWrite(write)
		if err != nil {
			return "", err
		}
		encoded = append(encoded, value)
	}
	var payload any
	if len(encoded) == 1 {
		payload = encoded[0]
	} else {
		payload = encoded
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// PublishFileAtomically stages content in a temp file and renames it into
// place only after the callback succeeds.
func PublishFileAtomically(
	fileSystem harnesstypes.FileSystem,
	destinationPath string,
	ctx harnesstypes.Context,
	writeContent func(append func(content string) error) error,
) error {
	tempPath := destinationPath + ".tmp"
	if _, err := FileValue(fileSystem.WriteFile(tempPath, nil, ctx), "Failed to stage JSONL storage "+destinationPath); err != nil {
		return err
	}
	appendContent := func(content string) error {
		_, err := FileValue(fileSystem.AppendFile(tempPath, []byte(content), ctx), "Failed to append JSONL storage "+destinationPath)
		return err
	}
	if err := writeContent(appendContent); err != nil {
		_, _ = FileValue(fileSystem.Remove(tempPath, &harnesstypes.RemoveOptions{Force: boolPointer(true)}, ctx), "Failed to remove staging file")
		return err
	}
	if _, err := FileValue(fileSystem.RenameFile(tempPath, destinationPath, ctx), "Failed to publish JSONL storage "+destinationPath); err != nil {
		_, _ = FileValue(fileSystem.Remove(tempPath, &harnesstypes.RemoveOptions{Force: boolPointer(true)}, ctx), "Failed to remove staging file")
		return err
	}
	return nil
}

// PublishJsonl streams a header and complete transactions through the shared
// atomic publisher.
func PublishJsonl(
	fileSystem harnesstypes.FileSystem,
	destinationPath string,
	header JsonlStorageHeader,
	ctx harnesstypes.Context,
	writeTransactions func(append func(writes []session.CommittedWrite) error) error,
) error {
	return PublishFileAtomically(fileSystem, destinationPath, ctx, func(appendContent func(content string) error) error {
		headerLine, err := json.Marshal(header)
		if err != nil {
			return err
		}
		if err := appendContent(string(headerLine) + "\n"); err != nil {
			return err
		}
		return writeTransactions(func(writes []session.CommittedWrite) error {
			line, err := SerializeJsonlTransaction(writes)
			if err != nil {
				return err
			}
			return appendContent(line + "\n")
		})
	})
}

func boolPointer(value bool) *bool { return &value }

var _ = harnesstypes.FileError{}
