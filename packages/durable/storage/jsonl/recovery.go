package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
)

// parsedLine is one complete, newline-terminated line together with its byte
// start offset and exact text.
type parsedLine[T any] struct {
	value T
	start int
	raw   string
}

// parsedFile is one complete file with its name and resolved path.
type parsedFile[T any] struct {
	name  string
	path  string
	lines []parsedLine[T]
}

// readLines reads a whole file, truncates an incomplete final line at a byte
// boundary, rejects invalid UTF-8 in complete lines, and parses each line. A
// missing file yields no lines.
func readLines[T any](fs env.FileSystem, path, name string, ctx context.Context, parse func(string, int) (T, error)) (parsedFile[T], error) {
	data, err := fs.ReadBinaryFile(ctx, path)
	if err != nil {
		var fileErr *env.FileError
		if errors.As(err, &fileErr) && fileErr.Code == env.FileErrorNotFound {
			return parsedFile[T]{name: name, path: path}, nil
		}
		return parsedFile[T]{}, errorFromFile("read of "+name, err)
	}
	completeSize := len(data)
	if completeSize > 0 && data[completeSize-1] != '\n' {
		completeSize = bytes.LastIndexByte(data, '\n') + 1
		if err := fs.TruncateFile(ctx, path, int64(completeSize)); err != nil {
			return parsedFile[T]{}, errorFromFile("torn-line truncation of "+name, err)
		}
	}
	lines := []parsedLine[T]{}
	start := 0
	lineNumber := 1
	for end := 0; end < completeSize; end++ {
		if data[end] != '\n' {
			continue
		}
		chunk := data[start:end]
		if !utf8.Valid(chunk) {
			return parsedFile[T]{}, corruptf("Invalid UTF-8 in complete %s line %d", name, lineNumber)
		}
		text := string(chunk)
		value, err := parse(text, lineNumber)
		if err != nil {
			return parsedFile[T]{}, err
		}
		lines = append(lines, parsedLine[T]{value: value, start: start, raw: text})
		start = end + 1
		lineNumber++
	}
	return parsedFile[T]{name: name, path: path, lines: lines}, nil
}

// recover replays only confirmed markers into the memory working set, truncates
// unconfirmed tails, and reclaims sidecars whose physical data is no longer
// required.
func (s *Storage) recover(ctx context.Context) error {
	main, err := readLines(s.fs, s.mainPath, mainFileName, ctx, parseMainMarker)
	if err != nil {
		return err
	}
	previous := durable.Seq(0)
	for _, line := range main.lines {
		if line.value.seq <= previous {
			return corruptf("Commit sequence does not strictly increase in %s", mainFileName)
		}
		previous = line.value.seq
	}

	listed, err := s.fs.ListDir(ctx, s.directory)
	if err != nil {
		return errorFromFile("directory listing", err)
	}
	for _, info := range listed {
		if info.Kind == "file" && reclaimNamePattern.MatchString(info.Name) {
			_ = s.fs.Remove(ctx, info.Path, env.RemoveOptions{Force: true})
		}
	}

	sidecarNames := []string{}
	for _, info := range listed {
		if info.Kind == "file" && sidecarNamePattern.MatchString(info.Name) {
			sidecarNames = append(sidecarNames, info.Name)
		}
	}
	sort.Strings(sidecarNames)

	parsedFiles := []parsedFile[sidecarRecord]{}
	recordByKey := map[string]sidecarRecord{}
	for _, file := range sidecarNames {
		path, err := s.fs.JoinPath(ctx, s.directory, file)
		if err != nil {
			return errorFromFile("path join", err)
		}
		parsed, err := readLines(s.fs, path, file, ctx, func(text string, line int) (sidecarRecord, error) {
			return parseSidecarRecord(text, file, line)
		})
		if err != nil {
			return err
		}
		parsedFiles = append(parsedFiles, parsed)
		var previousRecord *sidecarRecord
		for index := range parsed.lines {
			record := parsed.lines[index].value
			if previousRecord != nil &&
				(record.seq < previousRecord.seq || (record.seq == previousRecord.seq && record.ordinal <= previousRecord.ordinal)) {
				return corruptf("Sidecar records are out of order in %s", file)
			}
			copyOfRecord := record
			previousRecord = &copyOfRecord
			recordByKey[sidecarKey(file, record.seq, record.ordinal)] = record
		}
	}

	currentOnly := map[durable.DocumentID]struct{}{}
	retired := map[durable.DocumentID]struct{}{}
	finalTaskIsLive := map[durable.TaskID]bool{}
	for _, line := range main.lines {
		for _, operation := range line.value.writes {
			switch operation.kind {
			case durable.WriteDocumentCreate:
				if operation.record != nil && isCurrentOnlyCreate(*operation.record) {
					currentOnly[operation.record.ID] = struct{}{}
				}
			case durable.WriteDocumentRetire:
				retired[operation.id] = struct{}{}
			case durable.WriteTask:
				finalTaskIsLive[durable.TaskID(operation.id)] = false
			case "task.sidecar":
				finalTaskIsLive[durable.TaskID(operation.id)] = true
			}
		}
	}

	retiredCurrentOnly := map[durable.DocumentID]struct{}{}
	for id := range retired {
		if _, ok := currentOnly[id]; ok {
			retiredCurrentOnly[id] = struct{}{}
		}
	}

	latestBases := map[durable.DocumentID]sidecarRecord{}
	for _, line := range main.lines {
		for _, operation := range line.value.writes {
			if operation.kind != durable.WriteDocumentCreate && operation.kind != durable.WriteDocumentChange {
				continue
			}
			id := operation.id
			if operation.kind == durable.WriteDocumentCreate && operation.record != nil {
				id = operation.record.ID
			}
			if _, ok := currentOnly[id]; !ok {
				continue
			}
			record, ok := recordByKey[sidecarKey(sidecarFileName("doc", id), line.value.seq, operation.ordinal)]
			if !ok || record.kind != "document" || record.docID != id || record.content == nil || record.content.Kind != durable.ContentBase {
				continue
			}
			existing, present := latestBases[id]
			if !present || record.seq > existing.seq || (record.seq == existing.seq && record.ordinal > existing.ordinal) {
				latestBases[id] = record
			}
		}
	}

	isBeforeLatestBase := func(id durable.DocumentID, seq durable.Seq, ordinal int) bool {
		base, ok := latestBases[id]
		return ok && (seq < base.seq || (seq == base.seq && ordinal < base.ordinal))
	}

	terminalTasks := map[durable.TaskID]struct{}{}
	for id, live := range finalTaskIsLive {
		if !live {
			terminalTasks[id] = struct{}{}
		}
	}

	confirmed := map[string]struct{}{}
	for _, line := range main.lines {
		marker := line.value
		writes := []durable.StorageWrite{}
		for _, operation := range marker.writes {
			switch operation.kind {
			case durable.WriteConversation, durable.WriteEntry, durable.WriteSubmission, durable.WriteTask:
				write, err := operation.storageWrite()
				if err != nil {
					return corruptf("Invalid committed state at sequence %d", marker.seq)
				}
				writes = append(writes, write)
			case durable.WriteDocumentRetire:
				writes = append(writes, durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: operation.id})
			case "task.sidecar":
				_, optional := terminalTasks[durable.TaskID(operation.id)]
				file := sidecarFileName("task", operation.id)
				record, err := confirmRecord(marker.seq, operation.ordinal, file, recordByKey, confirmed, optional)
				if err != nil {
					return err
				}
				if record != nil {
					if record.kind != "task" {
						return corruptf("Confirmed task sidecar data does not match commit %d", marker.seq)
					}
					var task durable.TaskRecord
					if err := json.Unmarshal(record.task, &task); err != nil || durable.TaskID(task.ID) != durable.TaskID(operation.id) {
						return corruptf("Confirmed task sidecar data does not match commit %d", marker.seq)
					}
					if !optional {
						writes = append(writes, durable.StorageWrite{Type: durable.WriteTask, Task: &task})
					}
				}
			case durable.WriteDocumentCreate:
				id := operation.record.ID
				reclaimed := isReclaimed(retiredCurrentOnly, id) || isBeforeLatestBase(id, marker.seq, operation.ordinal)
				file := sidecarFileName("doc", id)
				record, err := confirmRecord(marker.seq, operation.ordinal, file, recordByKey, confirmed, reclaimed)
				if err != nil {
					return err
				}
				var content *durable.DocumentContent
				if record != nil {
					if record.kind != "document" || record.docID != id {
						return corruptf("Confirmed document sidecar data does not match commit %d", marker.seq)
					}
					content = record.content
				}
				if content != nil && content.Kind != durable.ContentBase {
					return corruptf("Document creation lacks a confirmed base in commit %d", marker.seq)
				}
				if reclaimed || content == nil {
					content = &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}
				}
				writes = append(writes, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: operation.record, Content: content})
			case durable.WriteDocumentChange:
				id := operation.id
				reclaimed := isReclaimed(retiredCurrentOnly, id) || isBeforeLatestBase(id, marker.seq, operation.ordinal)
				file := sidecarFileName("doc", id)
				record, err := confirmRecord(marker.seq, operation.ordinal, file, recordByKey, confirmed, reclaimed)
				if err != nil {
					return err
				}
				if record != nil && (record.kind != "document" || record.docID != id) {
					return corruptf("Confirmed document sidecar data does not match commit %d", marker.seq)
				}
				if !reclaimed && record != nil && record.content != nil {
					writes = append(writes, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: id, Content: record.content})
				}
			}
		}
		prepared, err := s.memory.PrepareCommitAt(marker.seq, writes)
		if err != nil {
			return &CorruptionError{Message: fmt.Sprintf("Invalid committed state at sequence %d", marker.seq), Cause: err}
		}
		prepared.Apply()
	}

	reclamations, err := s.planRecoveredReclamations(ctx, parsedFiles, confirmed, terminalTasks, retiredCurrentOnly, latestBases, isBeforeLatestBase)
	if err != nil {
		return err
	}
	s.reclaimSidecars(reclamations, ctx)

	for id := range currentOnly {
		s.currentOnlyDocuments[id] = struct{}{}
	}
	for id, live := range finalTaskIsLive {
		if live {
			s.liveTaskSidecars[id] = struct{}{}
		}
	}
	return nil
}

func isReclaimed(set map[durable.DocumentID]struct{}, id durable.DocumentID) bool {
	_, ok := set[id]
	return ok
}

// planRecoveredReclamations truncates every unconfirmed sidecar tail, rejects a
// confirmed record that follows one, and computes the physical rewrites that
// retain only the data the confirmed state still needs.
func (s *Storage) planRecoveredReclamations(
	ctx context.Context,
	parsedFiles []parsedFile[sidecarRecord],
	confirmed map[string]struct{},
	terminalTasks map[durable.TaskID]struct{},
	retiredCurrentOnly map[durable.DocumentID]struct{},
	latestBases map[durable.DocumentID]sidecarRecord,
	isBeforeLatestBase func(durable.DocumentID, durable.Seq, int) bool,
) ([]replacement, error) {
	reclamations := []replacement{}
	for _, parsed := range parsedFiles {
		file := parsed.name
		unconfirmedAt := -1
		for _, line := range parsed.lines {
			key := sidecarKey(file, line.value.seq, line.value.ordinal)
			if _, ok := confirmed[key]; ok {
				if unconfirmedAt >= 0 {
					return nil, corruptf("Confirmed record follows an unconfirmed tail in %s", file)
				}
			} else if unconfirmedAt < 0 {
				unconfirmedAt = line.start
			}
		}
		if unconfirmedAt >= 0 {
			if err := s.fs.TruncateFile(ctx, parsed.path, int64(unconfirmedAt)); err != nil {
				return nil, errorFromFile("tail truncation of "+file, err)
			}
		}

		id, hasID := sidecarID(file)
		confirmedLines := []parsedLine[sidecarRecord]{}
		for _, line := range parsed.lines {
			if _, ok := confirmed[sidecarKey(file, line.value.seq, line.value.ordinal)]; ok {
				confirmedLines = append(confirmedLines, line)
			}
		}
		var retained []parsedLine[sidecarRecord]
		retainedSet := false
		if hasID {
			if len(file) >= 5 && file[:5] == "task-" {
				if _, terminal := terminalTasks[durable.TaskID(id)]; terminal {
					retained = []parsedLine[sidecarRecord]{}
					retainedSet = true
				}
			} else if len(file) >= 4 && file[:4] == "doc-" {
				if _, retiredOnly := retiredCurrentOnly[id]; retiredOnly {
					retained = []parsedLine[sidecarRecord]{}
					retainedSet = true
				} else if _, hasBase := latestBases[id]; hasBase {
					retained = []parsedLine[sidecarRecord]{}
					for _, line := range confirmedLines {
						if !isBeforeLatestBase(id, line.value.seq, line.value.ordinal) {
							retained = append(retained, line)
						}
					}
					retainedSet = true
				}
			}
		}
		if retainedSet && (len(retained) < len(confirmedLines) || len(retained) == 0) {
			var buffer bytes.Buffer
			for _, line := range retained {
				buffer.WriteString(line.raw)
				buffer.WriteByte('\n')
			}
			reclamations = append(reclamations, replacement{file: file, content: buffer.String()})
		}
	}
	return reclamations, nil
}

func confirmRecord(markerSeq durable.Seq, ordinal int, file string, recordByKey map[string]sidecarRecord, confirmed map[string]struct{}, optional bool) (*sidecarRecord, error) {
	key := sidecarKey(file, markerSeq, ordinal)
	if _, ok := confirmed[key]; ok {
		return nil, corruptf("Sidecar record is confirmed more than once")
	}
	record, ok := recordByKey[key]
	if !ok {
		if optional {
			return nil, nil
		}
		return nil, corruptf("Missing confirmed sidecar record %s at sequence %d", file, markerSeq)
	}
	confirmed[key] = struct{}{}
	return &record, nil
}
