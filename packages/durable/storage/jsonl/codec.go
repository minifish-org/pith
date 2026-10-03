package jsonl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
)

// This file ports the persisted wire format of Pi
// packages/durable/src/storage/jsonl/storage.ts: the format-1 main marker and
// sidecar record grammar, the strict validators, and the encoder. The decoding
// structs mirror the upstream lower-camel JSON field names so on-disk recovery
// interoperates with the original TypeScript format.
//
// Documented adaptation: the Go encoder emits the same fields, but JSON object
// key order is irrelevant to correctness and Go does not preserve insertion
// order, so callers must not rely on byte-identical output.

const (
	formatVersion = 1
	mainFileName  = "main.jsonl"
	reclaimSuffix = ".reclaim"
)

var (
	sidecarNamePattern = regexp.MustCompile(`^(?:doc|task)-(?:0|[1-9][0-9]*)\.jsonl$`)
	reclaimNamePattern = regexp.MustCompile(`^(?:doc|task)-(?:0|[1-9][0-9]*)\.jsonl\.reclaim$`)
)

// CorruptionError reports a structurally invalid or inconsistent JSONL file.
// Reopen is required; the backend never silently resets committed state.
type CorruptionError struct {
	Message string
	Cause   error
}

func (e *CorruptionError) Error() string { return e.Message }

// Unwrap exposes the optional underlying cause.
func (e *CorruptionError) Unwrap() error { return e.Cause }

func corrupt(message string, cause error) *CorruptionError {
	return &CorruptionError{Message: message, Cause: cause}
}

func corruptf(format string, args ...any) *CorruptionError {
	return &CorruptionError{Message: fmt.Sprintf(format, args...)}
}

// errorFromFile wraps an execution-environment file error with JSONL action
// context, mirroring upstream errorFromFile.
func errorFromFile(action string, err error) error {
	return fmt.Errorf("JSONL %s failed: %w", action, err)
}

func sidecarFileName(kind string, id durable.DocumentID) string {
	return kind + "-" + strconv.FormatInt(int64(id), 10) + ".jsonl"
}

func sidecarKey(file string, seq durable.Seq, ordinal int) string {
	return file + "\x00" + strconv.FormatInt(int64(seq), 10) + "\x00" + strconv.Itoa(ordinal)
}

func sidecarID(file string) (durable.DocumentID, bool) {
	dash := strings.IndexByte(file, '-')
	if dash < 0 {
		return 0, false
	}
	dot := strings.Index(file, ".jsonl")
	if dot < 0 || dot <= dash+1 {
		return 0, false
	}
	value, err := strconv.ParseInt(file[dash+1:dot], 10, 64)
	if err != nil {
		return 0, false
	}
	return durable.DocumentID(value), true
}

func isCurrentOnlyCreate(record durable.DocumentCreate) bool {
	return record.Scope.Kind != durable.ScopeConversation || record.History == durable.HistoryLatest
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func safeInteger(value int64) bool {
	return value >= 0 && value <= durable.MaxSafeInteger
}

// ---------------------------------------------------------------------------
// Decoded forms.
// ---------------------------------------------------------------------------

// mainOperation is one validated operation in a confirmed main marker.
type mainOperation struct {
	kind    string
	value   json.RawMessage // conversation/entry/submission/task record
	id      durable.DocumentID
	ordinal int
	record  *durable.DocumentCreate
}

// parsedMainMarker is one validated commit marker line.
type parsedMainMarker struct {
	seq    durable.Seq
	writes []mainOperation
}

// sidecarRecord is one validated sidecar record line. raw holds the exact line
// text so reclamation reproduces the retained bytes.
type sidecarRecord struct {
	seq     durable.Seq
	ordinal int
	kind    string // "task" or "document"
	task    json.RawMessage
	docID   durable.DocumentID
	content *durable.DocumentContent
	raw     string
}

// persistedContent is the on-disk document content form. Value is kept as raw
// JSON so an empty base object ({}) is not dropped by omitempty.
type persistedContent struct {
	Kind    string          `json:"kind"`
	Version *int            `json:"version"`
	Value   json.RawMessage `json:"value"`
	Ops     *[]chord.Op     `json:"ops"`
}

// ---------------------------------------------------------------------------
// Raw decoding structs.
// ---------------------------------------------------------------------------

type rawMarker struct {
	Format *int              `json:"format"`
	Type   string            `json:"type"`
	Seq    *int64            `json:"seq"`
	Writes []json.RawMessage `json:"writes"`
}

type rawMainWrite struct {
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value"`
	ID      *int64          `json:"id"`
	Ordinal *int64          `json:"ordinal"`
	Record  json.RawMessage `json:"record"`
}

type rawSidecar struct {
	Format  *int            `json:"format"`
	Type    string          `json:"type"`
	Seq     *int64          `json:"seq"`
	Ordinal *int64          `json:"ordinal"`
	Payload json.RawMessage `json:"payload"`
}

type rawPayload struct {
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value"`
	ID      *int64          `json:"id"`
	Content json.RawMessage `json:"content"`
}

type rawValueProbe struct {
	ID    *int64           `json:"id"`
	State *json.RawMessage `json:"state"`
}

type rawStateProbe struct {
	Status string `json:"status"`
}

// ---------------------------------------------------------------------------
// Wire encoding structs.
// ---------------------------------------------------------------------------

type wireMainValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type wireMainRetire struct {
	Type string             `json:"type"`
	ID   durable.DocumentID `json:"id"`
}

type wireMainSidecar struct {
	Type    string             `json:"type"`
	ID      durable.DocumentID `json:"id"`
	Ordinal int                `json:"ordinal"`
}

type wireMainCreate struct {
	Type    string                  `json:"type"`
	Record  *durable.DocumentCreate `json:"record"`
	Ordinal int                     `json:"ordinal"`
}

type wireMainChange struct {
	Type    string             `json:"type"`
	ID      durable.DocumentID `json:"id"`
	Ordinal int                `json:"ordinal"`
}

type wireMarker struct {
	Format int               `json:"format"`
	Type   string            `json:"type"`
	Seq    durable.Seq       `json:"seq"`
	Writes []json.RawMessage `json:"writes"`
}

type wireContent struct {
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Value   json.RawMessage `json:"value,omitempty"`
	Ops     *[]chord.Op     `json:"ops,omitempty"`
}

type wireSidecarTask struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type wireSidecarDocument struct {
	Type    string             `json:"type"`
	ID      durable.DocumentID `json:"id"`
	Content *wireContent       `json:"content"`
}

type wireSidecarRecord struct {
	Format  int             `json:"format"`
	Type    string          `json:"type"`
	Seq     durable.Seq     `json:"seq"`
	Ordinal int             `json:"ordinal"`
	Payload json.RawMessage `json:"payload"`
}

// ---------------------------------------------------------------------------
// Parsing and validation.
// ---------------------------------------------------------------------------

// parseMainMarker validates one main.jsonl commit-marker line.
func parseMainMarker(text string, line int) (parsedMainMarker, error) {
	description := fmt.Sprintf("%s line %d", mainFileName, line)
	var raw rawMarker
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return parsedMainMarker{}, corrupt("Malformed complete "+description, err)
	}
	if raw.Format == nil || *raw.Format != formatVersion || raw.Type != "commit" ||
		raw.Seq == nil || *raw.Seq < 1 || !safeInteger(*raw.Seq) || raw.Writes == nil {
		return parsedMainMarker{}, corruptf("Invalid commit marker in %s", description)
	}
	writes := make([]mainOperation, 0, len(raw.Writes))
	for _, encoded := range raw.Writes {
		operation, err := parseMainOperation(encoded, description)
		if err != nil {
			return parsedMainMarker{}, err
		}
		writes = append(writes, operation)
	}
	return parsedMainMarker{seq: durable.Seq(*raw.Seq), writes: writes}, nil
}

func parseMainOperation(encoded json.RawMessage, description string) (mainOperation, error) {
	var write rawMainWrite
	if err := json.Unmarshal(encoded, &write); err != nil {
		return mainOperation{}, corruptf("Invalid write in %s", description)
	}
	switch write.Type {
	case durable.WriteConversation, durable.WriteEntry, durable.WriteSubmission:
		id, err := valueRecordID(write.Value, description, write.Type)
		if err != nil {
			return mainOperation{}, err
		}
		return mainOperation{kind: write.Type, value: write.Value, id: id}, nil
	case durable.WriteTask:
		if !isJSONObject(write.Value) {
			return mainOperation{}, corruptf("Invalid terminal task write in %s", description)
		}
		var probe rawValueProbe
		if err := json.Unmarshal(write.Value, &probe); err != nil || probe.ID == nil || !safeInteger(*probe.ID) {
			return mainOperation{}, corruptf("Invalid terminal task write in %s", description)
		}
		if probe.State == nil {
			return mainOperation{}, corruptf("Invalid terminal task write in %s", description)
		}
		var state rawStateProbe
		if err := json.Unmarshal(*probe.State, &state); err != nil || state.Status != durable.TaskStatusTerminal {
			return mainOperation{}, corruptf("Invalid terminal task write in %s", description)
		}
		return mainOperation{kind: durable.WriteTask, value: write.Value, id: durable.DocumentID(*probe.ID)}, nil
	case durable.WriteDocumentRetire:
		if write.ID == nil || !safeInteger(*write.ID) {
			return mainOperation{}, corruptf("Invalid document retirement in %s", description)
		}
		return mainOperation{kind: durable.WriteDocumentRetire, id: durable.DocumentID(*write.ID)}, nil
	case "task.sidecar":
		if write.ID == nil || !safeInteger(*write.ID) || write.Ordinal == nil || *write.Ordinal < 0 || !safeInteger(*write.Ordinal) {
			return mainOperation{}, corruptf("Invalid task sidecar write in %s", description)
		}
		return mainOperation{kind: "task.sidecar", id: durable.DocumentID(*write.ID), ordinal: int(*write.Ordinal)}, nil
	case durable.WriteDocumentCreate:
		record, err := parseDocumentCreate(write.Record, description)
		if err != nil {
			return mainOperation{}, corruptf("Invalid document creation in %s", description)
		}
		if write.Ordinal == nil || *write.Ordinal < 0 || !safeInteger(*write.Ordinal) {
			return mainOperation{}, corruptf("Invalid document creation in %s", description)
		}
		return mainOperation{kind: durable.WriteDocumentCreate, id: record.ID, ordinal: int(*write.Ordinal), record: record}, nil
	case durable.WriteDocumentChange:
		if write.ID == nil || !safeInteger(*write.ID) || write.Ordinal == nil || *write.Ordinal < 0 || !safeInteger(*write.Ordinal) {
			return mainOperation{}, corruptf("Invalid document change in %s", description)
		}
		return mainOperation{kind: durable.WriteDocumentChange, id: durable.DocumentID(*write.ID), ordinal: int(*write.Ordinal)}, nil
	default:
		return mainOperation{}, corruptf("Unknown write type in %s", description)
	}
}

func valueRecordID(value json.RawMessage, description, kind string) (durable.DocumentID, error) {
	if !isJSONObject(value) {
		return 0, corruptf("Invalid %s write in %s", kind, description)
	}
	var probe rawValueProbe
	if err := json.Unmarshal(value, &probe); err != nil || probe.ID == nil || !safeInteger(*probe.ID) {
		return 0, corruptf("Invalid %s write in %s", kind, description)
	}
	return durable.DocumentID(*probe.ID), nil
}

func parseDocumentCreate(raw json.RawMessage, description string) (*durable.DocumentCreate, error) {
	if !isJSONObject(raw) {
		return nil, corruptf("Invalid document creation in %s", description)
	}
	var probe struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.ID == nil || !safeInteger(*probe.ID) {
		return nil, corruptf("Invalid document creation in %s", description)
	}
	var record durable.DocumentCreate
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, corruptf("Invalid document creation in %s", description)
	}
	return &record, nil
}

// parseSidecarRecord validates one sidecar record line.
func parseSidecarRecord(text, file string, line int) (sidecarRecord, error) {
	description := fmt.Sprintf("%s line %d", file, line)
	var raw rawSidecar
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return sidecarRecord{}, corrupt("Malformed complete "+description, err)
	}
	if raw.Format == nil || *raw.Format != formatVersion || raw.Type != "record" ||
		raw.Seq == nil || *raw.Seq < 1 || !safeInteger(*raw.Seq) ||
		raw.Ordinal == nil || *raw.Ordinal < 0 || !safeInteger(*raw.Ordinal) ||
		!isJSONObject(raw.Payload) {
		return sidecarRecord{}, corruptf("Invalid sidecar record in %s", description)
	}
	var payload rawPayload
	if err := json.Unmarshal(raw.Payload, &payload); err != nil {
		return sidecarRecord{}, corruptf("Invalid sidecar record in %s", description)
	}
	record := sidecarRecord{seq: durable.Seq(*raw.Seq), ordinal: int(*raw.Ordinal), raw: text}
	switch payload.Type {
	case "task":
		if !isJSONObject(payload.Value) {
			return sidecarRecord{}, corruptf("Invalid live task record in %s", description)
		}
		var probe rawValueProbe
		if err := json.Unmarshal(payload.Value, &probe); err != nil || probe.ID == nil || !safeInteger(*probe.ID) {
			return sidecarRecord{}, corruptf("Invalid live task record in %s", description)
		}
		if probe.State == nil {
			return sidecarRecord{}, corruptf("Invalid live task record in %s", description)
		}
		var state rawStateProbe
		if err := json.Unmarshal(*probe.State, &state); err != nil || state.Status == durable.TaskStatusTerminal {
			return sidecarRecord{}, corruptf("Invalid live task record in %s", description)
		}
		record.kind = "task"
		record.task = payload.Value
		return record, nil
	case "document":
		if payload.ID == nil || !safeInteger(*payload.ID) {
			return sidecarRecord{}, corruptf("Invalid document record in %s", description)
		}
		content, err := validateDocumentContent(payload.Content, description)
		if err != nil {
			return sidecarRecord{}, err
		}
		record.kind = "document"
		record.docID = durable.DocumentID(*payload.ID)
		record.content = content
		return record, nil
	default:
		return sidecarRecord{}, corruptf("Unknown sidecar record type in %s", description)
	}
}

func validateDocumentContent(raw json.RawMessage, description string) (*durable.DocumentContent, error) {
	if !isJSONObject(raw) {
		return nil, corruptf("Invalid document content in %s", description)
	}
	var content persistedContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, corruptf("Invalid document content in %s", description)
	}
	if content.Version == nil || *content.Version < 1 || !safeInteger(int64(*content.Version)) {
		return nil, corruptf("Invalid document content in %s", description)
	}
	switch content.Kind {
	case durable.ContentBase:
		if !isJSONObject(content.Value) {
			return nil, corruptf("Invalid document content in %s", description)
		}
		var value map[string]any
		if err := json.Unmarshal(content.Value, &value); err != nil {
			return nil, corruptf("Invalid document content in %s", description)
		}
		return &durable.DocumentContent{Kind: durable.ContentBase, Version: *content.Version, Value: durable.JsonObject(value)}, nil
	case durable.ContentDelta:
		if content.Ops == nil {
			return nil, corruptf("Invalid document content in %s", description)
		}
		ops := *content.Ops
		return &durable.DocumentContent{Kind: durable.ContentDelta, Version: *content.Version, Ops: ops}, nil
	default:
		return nil, corruptf("Invalid document content in %s", description)
	}
}

// decodeValueWrite converts one validated value operation back into a detached
// StorageWrite for replay.
func (op mainOperation) storageWrite() (durable.StorageWrite, error) {
	switch op.kind {
	case durable.WriteConversation:
		var record durable.ConversationRecord
		if err := json.Unmarshal(op.value, &record); err != nil {
			return durable.StorageWrite{}, err
		}
		return durable.StorageWrite{Type: durable.WriteConversation, Conversation: &record}, nil
	case durable.WriteEntry:
		var record durable.EntryRecord
		if err := json.Unmarshal(op.value, &record); err != nil {
			return durable.StorageWrite{}, err
		}
		return durable.StorageWrite{Type: durable.WriteEntry, Entry: &record}, nil
	case durable.WriteSubmission:
		var record durable.SubmissionRecord
		if err := json.Unmarshal(op.value, &record); err != nil {
			return durable.StorageWrite{}, err
		}
		return durable.StorageWrite{Type: durable.WriteSubmission, Submission: &record}, nil
	case durable.WriteTask:
		var record durable.TaskRecord
		if err := json.Unmarshal(op.value, &record); err != nil {
			return durable.StorageWrite{}, err
		}
		return durable.StorageWrite{Type: durable.WriteTask, Task: &record}, nil
	case durable.WriteDocumentRetire:
		return durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: op.id}, nil
	default:
		return durable.StorageWrite{}, errors.New("jsonl: operation is not a value write")
	}
}

// ---------------------------------------------------------------------------
// Encoding.
// ---------------------------------------------------------------------------

type encodedSidecar struct {
	name    string
	content []byte
}

type encodedCommit struct {
	marker   []byte
	sidecars []encodedSidecar
	byName   map[string][]byte
}

func marshalJSON(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// encodeCommit serializes one prepared, detached batch into the marker and
// per-file sidecar appends. The sidecar slice preserves first-occurrence order
// so publication writes are deterministic.
func encodeCommit(seq durable.Seq, writes []durable.StorageWrite) (encodedCommit, error) {
	mainWrites := []json.RawMessage{}
	buffers := map[string]*bytes.Buffer{}
	order := []string{}
	nextOrdinal := 0

	addSidecar := func(file string, build func(ordinal int) (json.RawMessage, error)) (int, error) {
		ordinal := nextOrdinal
		nextOrdinal++
		payload, err := build(ordinal)
		if err != nil {
			return 0, err
		}
		buffer := buffers[file]
		if buffer == nil {
			buffer = &bytes.Buffer{}
			buffers[file] = buffer
			order = append(order, file)
		}
		buffer.Write(payload)
		buffer.WriteByte('\n')
		return ordinal, nil
	}
	appendMain := func(operation any) error {
		raw, err := marshalJSON(operation)
		if err != nil {
			return err
		}
		mainWrites = append(mainWrites, raw)
		return nil
	}

	for _, write := range writes {
		switch write.Type {
		case durable.WriteConversation:
			value, err := marshalJSON(write.Conversation)
			if err != nil {
				return encodedCommit{}, err
			}
			if err := appendMain(wireMainValue{Type: durable.WriteConversation, Value: value}); err != nil {
				return encodedCommit{}, err
			}
		case durable.WriteEntry:
			value, err := marshalJSON(write.Entry)
			if err != nil {
				return encodedCommit{}, err
			}
			if err := appendMain(wireMainValue{Type: durable.WriteEntry, Value: value}); err != nil {
				return encodedCommit{}, err
			}
		case durable.WriteSubmission:
			value, err := marshalJSON(write.Submission)
			if err != nil {
				return encodedCommit{}, err
			}
			if err := appendMain(wireMainValue{Type: durable.WriteSubmission, Value: value}); err != nil {
				return encodedCommit{}, err
			}
		case durable.WriteDocumentRetire:
			if err := appendMain(wireMainRetire{Type: durable.WriteDocumentRetire, ID: write.ID}); err != nil {
				return encodedCommit{}, err
			}
		case durable.WriteTask:
			if write.Task == nil {
				return encodedCommit{}, errors.New("jsonl: task write is missing a record")
			}
			value, err := marshalJSON(write.Task)
			if err != nil {
				return encodedCommit{}, err
			}
			if write.Task.State.Status == durable.TaskStatusTerminal {
				if err := appendMain(wireMainValue{Type: durable.WriteTask, Value: value}); err != nil {
					return encodedCommit{}, err
				}
				break
			}
			file := sidecarFileName("task", write.Task.ID)
			ordinal, err := addSidecar(file, func(ordinal int) (json.RawMessage, error) {
				payload, err := marshalJSON(wireSidecarTask{Type: "task", Value: value})
				if err != nil {
					return nil, err
				}
				return marshalJSON(wireSidecarRecord{Format: formatVersion, Type: "record", Seq: seq, Ordinal: ordinal, Payload: payload})
			})
			if err != nil {
				return encodedCommit{}, err
			}
			if err := appendMain(wireMainSidecar{Type: "task.sidecar", ID: write.Task.ID, Ordinal: ordinal}); err != nil {
				return encodedCommit{}, err
			}
		case durable.WriteDocumentCreate, durable.WriteDocumentChange:
			var id durable.DocumentID
			var record *durable.DocumentCreate
			if write.Type == durable.WriteDocumentCreate {
				record = write.Record
				if record == nil {
					return encodedCommit{}, errors.New("jsonl: document creation is missing a record")
				}
				id = record.ID
			} else {
				id = write.ID
			}
			content, err := encodeContent(write.Content)
			if err != nil {
				return encodedCommit{}, err
			}
			file := sidecarFileName("doc", id)
			ordinal, err := addSidecar(file, func(ordinal int) (json.RawMessage, error) {
				payload, err := marshalJSON(wireSidecarDocument{Type: "document", ID: id, Content: content})
				if err != nil {
					return nil, err
				}
				return marshalJSON(wireSidecarRecord{Format: formatVersion, Type: "record", Seq: seq, Ordinal: ordinal, Payload: payload})
			})
			if err != nil {
				return encodedCommit{}, err
			}
			if write.Type == durable.WriteDocumentCreate {
				if err := appendMain(wireMainCreate{Type: durable.WriteDocumentCreate, Record: record, Ordinal: ordinal}); err != nil {
					return encodedCommit{}, err
				}
			} else {
				if err := appendMain(wireMainChange{Type: durable.WriteDocumentChange, ID: id, Ordinal: ordinal}); err != nil {
					return encodedCommit{}, err
				}
			}
		default:
			return encodedCommit{}, fmt.Errorf("jsonl: unsupported storage write type %q", write.Type)
		}
	}

	marker, err := marshalJSON(wireMarker{Format: formatVersion, Type: "commit", Seq: seq, Writes: mainWrites})
	if err != nil {
		return encodedCommit{}, err
	}
	markerText := append(append([]byte{}, marker...), '\n')

	sidecars := make([]encodedSidecar, 0, len(order))
	byName := make(map[string][]byte, len(order))
	for _, file := range order {
		content := buffers[file].Bytes()
		sidecars = append(sidecars, encodedSidecar{name: file, content: content})
		byName[file] = content
	}
	return encodedCommit{marker: markerText, sidecars: sidecars, byName: byName}, nil
}

func encodeContent(content *durable.DocumentContent) (*wireContent, error) {
	if content == nil {
		return nil, nil
	}
	out := &wireContent{Kind: content.Kind, Version: content.Version}
	if content.Value != nil {
		value, err := marshalJSON(map[string]any(content.Value))
		if err != nil {
			return nil, err
		}
		out.Value = value
	}
	if content.Ops != nil {
		ops := content.Ops
		out.Ops = &ops
	}
	return out, nil
}
