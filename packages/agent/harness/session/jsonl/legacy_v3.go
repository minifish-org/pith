// This file carries jsonl/legacy-v3.ts: reading and normalizing format-3
// sessions into format-4 writes.
package jsonl

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	usageutils "github.com/minifish-org/pith/packages/agent/harness/utils/usage"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// LegacyV3Source is a captured legacy file exposed as repeatable logical v4
// writes.
type LegacyV3Source struct {
	Header        JsonlStorageHeader
	ImportedUsage aitypes.Usage
	NextSeq       int
	Values        []session.CommittedWrite

	fileSystem harnesstypes.FileSystem
	path       string
	entries    map[string]*legacyIndexEntry
	order      []string
}

type legacyRawEntry struct {
	Type            string          `json:"type"`
	ID              string          `json:"id"`
	ParentID        *string         `json:"parentId"`
	Timestamp       string          `json:"timestamp"`
	Message         json.RawMessage `json:"message,omitempty"`
	CustomType      string          `json:"customType,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	Content         json.RawMessage `json:"content,omitempty"`
	Details         json.RawMessage `json:"details,omitempty"`
	Display         *bool           `json:"display,omitempty"`
	FromID          *string         `json:"fromId,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	Usage           json.RawMessage `json:"usage,omitempty"`
	FromHook        *bool           `json:"fromHook,omitempty"`
	FirstKeptEntry  string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore    *float64        `json:"tokensBefore,omitempty"`
	Provider        string          `json:"provider,omitempty"`
	ModelID         string          `json:"modelId,omitempty"`
	ThinkingLevel   string          `json:"thinkingLevel,omitempty"`
	ActiveToolNames []string        `json:"activeToolNames,omitempty"`
	Name            *string         `json:"name,omitempty"`
	TargetID        *string         `json:"targetId,omitempty"`
	Label           *string         `json:"label,omitempty"`
}

type legacyIndexEntry struct {
	ID               string
	ParentID         *string
	MappedID         *string
	Retained         bool
	Seq              int
	Type             string
	FromID           *string
	FirstKeptEntryID string
	Provider         string
	ModelID          string
	ThinkingLevel    string
	ActiveToolNames  []string
	TargetID         *string
	Label            *string
	Name             *string
	TimestampMs      float64
}

func parseLegacyV3Entry(line string) (*legacyRawEntry, error) {
	var entry legacyRawEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil, fmt.Errorf("Invalid legacy v3 JSONL record: not valid JSON: %w", err)
	}
	switch entry.Type {
	case "message", "custom", "custom_message", "branch_summary", "compaction",
		"model_change", "thinking_level_change", "active_tools_change", "session_info", "label":
		return &entry, nil
	default:
		return nil, fmt.Errorf("Unsupported legacy v3 record type: %q", entry.Type)
	}
}

func legacyRetained(entryType string) bool {
	switch entryType {
	case "model_change", "thinking_level_change", "active_tools_change", "session_info", "label":
		return false
	default:
		return true
	}
}

func parseLegacyTimestamp(value string) float64 {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, time.ANSIC, time.UnixDate} {
			if parsedTime, parseErr := time.Parse(layout, value); parseErr == nil {
				parsed = parsedTime
				err = nil
				break
			}
		}
	}
	if err != nil {
		return 0
	}
	return float64(parsed.UnixMilli())
}

func (s *LegacyV3Source) resolveID(legacyID string) (string, error) {
	entry, ok := s.entries[legacyID]
	if !ok || entry.MappedID == nil {
		return "", fmt.Errorf("Missing legacy v3 entry reference: %s", legacyID)
	}
	return *entry.MappedID, nil
}

func (s *LegacyV3Source) resolveParent(legacyID *string) (*string, error) {
	if legacyID == nil {
		return nil, nil
	}
	resolved, err := s.resolveID(*legacyID)
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}

func (s *LegacyV3Source) resolveBranchSummaryFromID(legacyFromID string) (*string, error) {
	if legacyFromID == "root" {
		return nil, nil
	}
	resolved, err := s.resolveID(legacyFromID)
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}

// EntryStructures yields the retained structural projection.
func (s *LegacyV3Source) EntryStructures() []struct {
	ID       string
	ParentID *string
	Seq      int
} {
	out := []struct {
		ID       string
		ParentID *string
		Seq      int
	}{}
	for _, id := range s.order {
		entry := s.entries[id]
		if !entry.Retained || entry.MappedID == nil {
			continue
		}
		parent, err := s.resolveParent(entry.ParentID)
		if err != nil {
			continue
		}
		out = append(out, struct {
			ID       string
			ParentID *string
			Seq      int
		}{ID: *entry.MappedID, ParentID: parent, Seq: entry.Seq})
	}
	return out
}

// TranslateForkEntryID maps a legacy entry id to its reminted id.
func (s *LegacyV3Source) TranslateForkEntryID(legacyID string) (string, error) {
	entry, ok := s.entries[legacyID]
	if !ok {
		return "", fmt.Errorf("Legacy v3 fork entry does not exist: %s", legacyID)
	}
	if !entry.Retained || entry.MappedID == nil {
		return "", fmt.Errorf("Legacy v3 fork entry is not a retained entry: %s", legacyID)
	}
	return *entry.MappedID, nil
}

// Writes streams normalized entries followed by derived current values.
func (s *LegacyV3Source) Writes(ctx harnesstypes.Context, isEntrySelected func(id string) bool) ([]session.CommittedWrite, error) {
	requiredTails := s.collectRequiredTailMessageIDs(isEntrySelected)
	tailMessages := map[string]agenttypes.AgentMessage{}
	writes := []session.CommittedWrite{}
	reader, err := FileValue(s.fileSystem.OpenTextLineReader(s.path, ctx), "Failed to reopen legacy v3 source "+s.path)
	if err != nil {
		return nil, err
	}
	defer reader.Close(ctx)
	if _, err := ReadJsonlHeader(reader, s.path, ctx); err != nil {
		return nil, err
	}
	for _, id := range s.order {
		indexed := s.entries[id]
		line, err := FileValue(reader.ReadLine(ctx), "Failed to reread legacy v3 source")
		if err != nil {
			return nil, err
		}
		if line == nil || !line.Terminated {
			return nil, errors.New("Legacy v3 source ended before captured entries")
		}
		entry, err := parseLegacyV3Entry(line.Text)
		if err != nil {
			return nil, err
		}
		if entry.ID != indexed.ID || entry.Type != indexed.Type {
			return nil, errors.New("Legacy v3 source changed")
		}
		if requiredTails[entry.ID] {
			if message, ok := s.projectContextMessage(entry); ok {
				tailMessages[entry.ID] = message
			}
		}
		if !legacyRetained(indexed.Type) || !indexed.Retained || indexed.MappedID == nil {
			continue
		}
		if isEntrySelected != nil && !isEntrySelected(*indexed.MappedID) {
			continue
		}
		retainedTail := []agenttypes.AgentMessage{}
		if indexed.Type == "compaction" {
			tailIDs, err := s.retainedTailStructure(indexed)
			if err != nil {
				return nil, err
			}
			for _, tailID := range tailIDs {
				if message, ok := tailMessages[tailID]; ok {
					retainedTail = append(retainedTail, message)
				}
			}
			for left, right := 0, len(retainedTail)-1; left < right; left, right = left+1, right-1 {
				retainedTail[left], retainedTail[right] = retainedTail[right], retainedTail[left]
			}
		}
		write, err := s.normalizeRetainedEntry(entry, indexed, retainedTail)
		if err != nil {
			return nil, err
		}
		writes = append(writes, write)
	}
	writes = append(writes, s.Values...)
	return writes, nil
}

// AllWrites normalizes every captured entry.
func (s *LegacyV3Source) AllWrites() []session.CommittedWrite {
	writes, err := s.Writes(harnesstypes.Context(nil), nil)
	if err != nil {
		return nil
	}
	return writes
}

func (s *LegacyV3Source) collectRequiredTailMessageIDs(isEntrySelected func(id string) bool) map[string]bool {
	required := map[string]bool{}
	for _, id := range s.order {
		entry := s.entries[id]
		if entry.Type != "compaction" {
			continue
		}
		if isEntrySelected != nil && (entry.MappedID == nil || !isEntrySelected(*entry.MappedID)) {
			continue
		}
		tailIDs, err := s.retainedTailStructure(entry)
		if err != nil {
			continue
		}
		for _, tailID := range tailIDs {
			tail := s.entries[tailID]
			if tail.Retained && tail.Type != "custom" {
				required[tailID] = true
			}
		}
	}
	return required
}

// retainedTailStructure walks physical ancestry from a compaction parent to its
// first kept entry, inclusive.
func (s *LegacyV3Source) retainedTailStructure(compaction *legacyIndexEntry) ([]string, error) {
	out := []string{}
	currentID := compaction.ParentID
	for currentID != nil {
		entry, ok := s.entries[*currentID]
		if !ok {
			return nil, fmt.Errorf("Missing legacy v3 entry reference: %s", *currentID)
		}
		out = append(out, entry.ID)
		if entry.ID == compaction.FirstKeptEntryID {
			return out, nil
		}
		currentID = entry.ParentID
	}
	return nil, fmt.Errorf("Legacy v3 compaction %s firstKeptEntryId is not on its parent branch: %s", compaction.ID, compaction.FirstKeptEntryID)
}

func (s *LegacyV3Source) projectContextMessage(entry *legacyRawEntry) (agenttypes.AgentMessage, bool) {
	switch entry.Type {
	case "message":
		var message agenttypes.AgentMessage
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			return agenttypes.AgentMessage{}, false
		}
		return message, true
	case "custom_message":
		return importedCustomMessage(entry), true
	case "branch_summary":
		if entry.Summary == "" {
			return agenttypes.AgentMessage{}, false
		}
		fromID, err := s.resolveBranchSummaryFromID(deref(entry.FromID))
		if err != nil {
			return agenttypes.AgentMessage{}, false
		}
		return branchSummaryAgentMessage(entry.Summary, fromID, parseLegacyTimestamp(entry.Timestamp)), true
	case "compaction":
		tokens := 0.0
		if entry.TokensBefore != nil {
			tokens = *entry.TokensBefore
		}
		return compactionSummaryAgentMessage(entry.Summary, tokens, parseLegacyTimestamp(entry.Timestamp)), true
	default:
		return agenttypes.AgentMessage{}, false
	}
}

func (s *LegacyV3Source) normalizeRetainedEntry(entry *legacyRawEntry, indexed *legacyIndexEntry, retainedTail []agenttypes.AgentMessage) (session.CommittedWrite, error) {
	parentID, err := s.resolveParent(indexed.ParentID)
	if err != nil {
		return session.CommittedWrite{}, err
	}
	timestamp := parseLegacyTimestamp(entry.Timestamp)
	id := deref(indexed.MappedID)
	switch entry.Type {
	case "message":
		var message agenttypes.AgentMessage
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			return session.CommittedWrite{}, err
		}
		return session.CommittedWrite{Kind: "entry", Seq: indexed.Seq, Timestamp: timestamp, Entry: harnesstypes.MessageEntry{
			EntryBase: harnesstypes.EntryBase{ID: id, ParentID: parentID, Seq: indexed.Seq, Timestamp: timestamp, Type: harnesstypes.EntryTypeMessage},
			Message:   message,
		}}, nil
	case "custom_message":
		return session.CommittedWrite{Kind: "entry", Seq: indexed.Seq, Timestamp: timestamp, Entry: harnesstypes.MessageEntry{
			EntryBase: harnesstypes.EntryBase{ID: id, ParentID: parentID, Seq: indexed.Seq, Timestamp: timestamp, Type: harnesstypes.EntryTypeMessage},
			Message:   importedCustomMessage(entry),
		}}, nil
	case "branch_summary":
		fromID, err := s.resolveBranchSummaryFromID(deref(entry.FromID))
		if err != nil {
			return session.CommittedWrite{}, err
		}
		return session.CommittedWrite{Kind: "entry", Seq: indexed.Seq, Timestamp: timestamp, Entry: harnesstypes.BranchSummaryEntry{
			EntryBase: harnesstypes.EntryBase{ID: id, ParentID: parentID, Seq: indexed.Seq, Timestamp: timestamp, Type: harnesstypes.EntryTypeBranchSummary},
			FromID:    fromID,
			Summary:   entry.Summary,
			Details:   rawJSONValue(entry.Details),
			Usage:     rawUsage(entry.Usage),
			FromHook:  entry.FromHook != nil && *entry.FromHook,
		}}, nil
	case "compaction":
		tokens := 0.0
		if entry.TokensBefore != nil {
			tokens = *entry.TokensBefore
		}
		return session.CommittedWrite{Kind: "entry", Seq: indexed.Seq, Timestamp: timestamp, Entry: harnesstypes.CompactionEntry{
			EntryBase:    harnesstypes.EntryBase{ID: id, ParentID: parentID, Seq: indexed.Seq, Timestamp: timestamp, Type: harnesstypes.EntryTypeCompaction},
			Summary:      entry.Summary,
			RetainedTail: retainedTail,
			TokensBefore: tokens,
			Details:      rawJSONValue(entry.Details),
			Usage:        rawUsage(entry.Usage),
			FromHook:     entry.FromHook != nil && *entry.FromHook,
		}}, nil
	default:
		return session.CommittedWrite{Kind: "entry", Seq: indexed.Seq, Timestamp: timestamp, Entry: harnesstypes.CustomEntry{
			EntryBase:  harnesstypes.EntryBase{ID: id, ParentID: parentID, Seq: indexed.Seq, Timestamp: timestamp, Type: harnesstypes.EntryTypeCustom},
			CustomType: entry.CustomType,
			Data:       rawJSONValue(entry.Data),
		}}, nil
	}
}

// ReadLegacyV3Source scans a legacy v3 file without modifying it.
func ReadLegacyV3Source(fileSystem harnesstypes.FileSystem, path string, ctx harnesstypes.Context) (*LegacyV3Source, error) {
	reader, err := FileValue(fileSystem.OpenTextLineReader(path, ctx), "Failed to open legacy v3 source "+path)
	if err != nil {
		return nil, err
	}
	defer reader.Close(ctx)
	parsed, err := ReadJsonlHeader(reader, path, ctx)
	if err != nil {
		return nil, err
	}
	if parsed.Format != "v3-legacy" || parsed.V3 == nil {
		return nil, fmt.Errorf("Invalid legacy v3 JSONL storage %s: expected format 3 header", path)
	}
	entries, order, nextSeq, importedUsage, name, finalID, err := readLegacyV3Inventory(reader, ctx)
	if err != nil {
		return nil, err
	}
	header, err := NormalizeLegacyV3Header(fileSystem, *parsed.V3, ctx)
	if err != nil {
		return nil, err
	}
	source := &LegacyV3Source{
		Header:        header,
		ImportedUsage: importedUsage,
		fileSystem:    fileSystem,
		path:          path,
		entries:       entries,
		order:         order,
	}
	source.Values = normalizeLegacyV3Values(entries, order, name, finalID, nextSeq)
	source.NextSeq = nextSeq + len(source.Values)
	return source, nil
}

func readLegacyV3Inventory(reader harnesstypes.TextLineReader, ctx harnesstypes.Context) (map[string]*legacyIndexEntry, []string, int, aitypes.Usage, *string, *string, error) {
	entries := map[string]*legacyIndexEntry{}
	order := []string{}
	nextSeq := 1
	importedUsage := usageutils.EmptyUsage()
	var name *string
	var finalID *string
	for {
		line, err := FileValue(reader.ReadLine(ctx), "Failed to read legacy v3 source")
		if err != nil {
			return nil, nil, 0, aitypes.Usage{}, nil, nil, err
		}
		if line == nil || !line.Terminated {
			break
		}
		entry, err := parseLegacyV3Entry(line.Text)
		if err != nil {
			return nil, nil, 0, aitypes.Usage{}, nil, nil, err
		}
		if _, exists := entries[entry.ID]; exists {
			return nil, nil, 0, aitypes.Usage{}, nil, nil, fmt.Errorf("Duplicate legacy v3 entry id: %s", entry.ID)
		}
		indexed := indexLegacyV3Entry(entry, nextSeq, entries)
		entries[entry.ID] = indexed
		order = append(order, entry.ID)
		if indexed.Retained {
			nextSeq++
		}
		id := entry.ID
		finalID = &id
		if entry.Type == "session_info" {
			name = entry.Name
		}
		if usage := legacyEntryUsage(entry); usage != nil {
			importedUsage = usageutils.AddUsage(importedUsage, *usage)
		}
	}
	return entries, order, nextSeq, importedUsage, name, finalID, nil
}

func indexLegacyV3Entry(entry *legacyRawEntry, seq int, entries map[string]*legacyIndexEntry) *legacyIndexEntry {
	var mappedParentID *string
	if entry.ParentID != nil {
		if parent, ok := entries[*entry.ParentID]; ok {
			mappedParentID = parent.MappedID
		}
	}
	indexed := &legacyIndexEntry{
		ID:               entry.ID,
		ParentID:         entry.ParentID,
		MappedID:         mappedParentID,
		Retained:         legacyRetained(entry.Type),
		Type:             entry.Type,
		Provider:         entry.Provider,
		ModelID:          entry.ModelID,
		ThinkingLevel:    entry.ThinkingLevel,
		ActiveToolNames:  entry.ActiveToolNames,
		TargetID:         entry.TargetID,
		Label:            entry.Label,
		Name:             entry.Name,
		TimestampMs:      parseLegacyTimestamp(entry.Timestamp),
		FromID:           entry.FromID,
		FirstKeptEntryID: entry.FirstKeptEntry,
	}
	if indexed.Retained {
		minted, err := aiutils.UUIDv7(&indexed.TimestampMs)
		if err == nil {
			indexed.MappedID = &minted
		}
		indexed.Seq = seq
	}
	return indexed
}

func normalizeLegacyV3Values(entries map[string]*legacyIndexEntry, order []string, name *string, finalID *string, nextSeq int) []session.CommittedWrite {
	seq := nextSeq
	values := []session.CommittedWrite{}
	if name != nil && *name != "" {
		values = append(values, session.CommittedWrite{Kind: "value", Op: "set", Namespace: session.NamespaceSessionName, Key: "", Value: *name, Seq: seq})
		seq++
	}
	labels := map[string]string{}
	labelOrder := []string{}
	for _, id := range order {
		entry := entries[id]
		if entry.Type != "label" || entry.TargetID == nil {
			continue
		}
		target, ok := entries[*entry.TargetID]
		if !ok || target.MappedID == nil {
			continue
		}
		key := *target.MappedID
		if entry.Label != nil && *entry.Label != "" {
			if _, exists := labels[key]; !exists {
				labelOrder = append(labelOrder, key)
			}
			labels[key] = *entry.Label
		} else {
			delete(labels, key)
		}
	}
	for _, key := range labelOrder {
		label, ok := labels[key]
		if !ok {
			continue
		}
		values = append(values, session.CommittedWrite{Kind: "value", Op: "set", Namespace: session.NamespaceEntryLabel, Key: key, Value: label, Seq: seq})
		seq++
	}
	var tip *string
	if finalID != nil {
		if entry, ok := entries[*finalID]; ok {
			tip = entry.MappedID
		}
	}
	values = append(values, session.CommittedWrite{Kind: "value", Op: "set", Namespace: session.NamespaceBranchTip, Key: "main", Value: tip, Seq: seq})
	seq++
	configuration, ok := selectedConfiguration(entries, finalID)
	if ok {
		values = append(values, session.CommittedWrite{Kind: "value", Op: "set", Namespace: session.NamespaceLaneConfig, Key: "main", Value: configuration, Seq: seq})
		seq++
		values = append(values, session.CommittedWrite{Kind: "value", Op: "set", Namespace: session.NamespaceLaneState, Key: "main", Value: harnesstypes.LaneState{Inbox: []harnesstypes.InboxItem{}}, Seq: seq})
		seq++
	}
	return values
}

func selectedConfiguration(entries map[string]*legacyIndexEntry, selectedID *string) (harnesstypes.LaneConfiguration, bool) {
	remaining := map[string]bool{"model_change": true, "thinking_level_change": true, "active_tools_change": true}
	var model *harnesstypes.ModelIdentity
	var thinkingLevel string
	var activeToolNames []string
	currentID := selectedID
	for currentID != nil && len(remaining) != 0 {
		entry, ok := entries[*currentID]
		if !ok {
			break
		}
		if remaining[entry.Type] {
			delete(remaining, entry.Type)
			switch entry.Type {
			case "model_change":
				model = &harnesstypes.ModelIdentity{Provider: entry.Provider, ModelID: entry.ModelID}
			case "thinking_level_change":
				thinkingLevel = entry.ThinkingLevel
			case "active_tools_change":
				activeToolNames = append([]string{}, entry.ActiveToolNames...)
			}
		}
		currentID = entry.ParentID
	}
	if model == nil || thinkingLevel == "" {
		return harnesstypes.LaneConfiguration{}, false
	}
	if activeToolNames == nil {
		activeToolNames = []string{}
	}
	return harnesstypes.LaneConfiguration{
		Model:           *model,
		ThinkingLevel:   agenttypes.ThinkingLevel(thinkingLevel),
		ActiveToolNames: activeToolNames,
	}, true
}

// MetadataFromLegacyV3Header derives format-4 metadata from a v3 header.
func MetadataFromLegacyV3Header(fileSystem harnesstypes.FileSystem, header LegacyV3SessionHeader, ctx harnesstypes.Context) (JsonlSessionMetadata, error) {
	metadata := JsonlSessionMetadata{
		SessionMetadata: harnesstypes.SessionMetadata{
			ID:             header.ID,
			CreatedAt:      parseLegacyTimestamp(header.Timestamp),
			StorageVersion: JSONL_STORAGE_VERSION,
		},
		Cwd: header.Cwd,
	}
	if header.ParentSession != nil {
		parentID, err := resolveLegacyV3ParentSessionID(fileSystem, *header.ParentSession, ctx)
		if err == nil && parentID != nil {
			metadata.ParentSessionID = parentID
		} else {
			legacyPath := *header.ParentSession
			metadata.LegacyParentSessionPath = &legacyPath
		}
	}
	return metadata, nil
}

func resolveLegacyV3ParentSessionID(fileSystem harnesstypes.FileSystem, parentPath string, ctx harnesstypes.Context) (*string, error) {
	lines, err := FileValue(fileSystem.ReadTextLines(parentPath, &harnesstypes.ReadTextLinesOptions{MaxLines: intPointer(1)}, ctx), "Failed to read legacy parent session")
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	parsed, parseErr := ParseJsonlSessionHeader(lines[0])
	if parseErr != nil {
		return nil, parseErr
	}
	if parsed.V4 != nil {
		id := parsed.V4.ID
		return &id, nil
	}
	if parsed.V3 != nil {
		id := parsed.V3.ID
		return &id, nil
	}
	return nil, nil
}

// NormalizeLegacyV3Header converts a v3 header into a v4 header.
func NormalizeLegacyV3Header(fileSystem harnesstypes.FileSystem, header LegacyV3SessionHeader, ctx harnesstypes.Context) (JsonlStorageHeader, error) {
	metadata, err := MetadataFromLegacyV3Header(fileSystem, header, ctx)
	if err != nil {
		return JsonlStorageHeader{}, err
	}
	return JsonlStorageHeader{
		V:                       JSONL_FORMAT_VERSION,
		Kind:                    "header",
		ID:                      metadata.ID,
		StorageVersion:          JSONL_STORAGE_VERSION,
		CreatedAt:               metadata.CreatedAt,
		Cwd:                     metadata.Cwd,
		ParentSessionID:         metadata.ParentSessionID,
		LegacyParentSessionPath: metadata.LegacyParentSessionPath,
	}, nil
}

func importedCustomMessage(entry *legacyRawEntry) agenttypes.AgentMessage {
	content := json.RawMessage("null")
	if len(entry.Content) > 0 {
		content = entry.Content
	}
	details := json.RawMessage("null")
	if len(entry.Details) > 0 {
		details = entry.Details
	}
	display := false
	if entry.Display != nil {
		display = *entry.Display
	}
	payload := map[string]any{
		"role":       "custom",
		"customType": entry.CustomType,
		"content":    content,
		"details":    details,
		"display":    display,
		"timestamp":  parseLegacyTimestamp(entry.Timestamp),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage("custom", raw)
}

func branchSummaryAgentMessage(summary string, fromID *string, timestamp float64) agenttypes.AgentMessage {
	payload := map[string]any{"role": "branchSummary", "summary": summary, "fromId": fromID, "timestamp": timestamp}
	raw, err := json.Marshal(payload)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage("branchSummary", raw)
}

func compactionSummaryAgentMessage(summary string, tokensBefore float64, timestamp float64) agenttypes.AgentMessage {
	payload := map[string]any{"role": "compactionSummary", "summary": summary, "tokensBefore": tokensBefore, "timestamp": timestamp}
	raw, err := json.Marshal(payload)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage("compactionSummary", raw)
}

func legacyEntryUsage(entry *legacyRawEntry) *aitypes.Usage {
	switch entry.Type {
	case "message":
		var message agenttypes.AgentMessage
		if err := json.Unmarshal(entry.Message, &message); err != nil || message.Message == nil {
			return nil
		}
		if message.Message.Assistant != nil {
			return &message.Message.Assistant.Usage
		}
		if message.Message.ToolResult != nil {
			return message.Message.ToolResult.Usage
		}
		return nil
	case "compaction", "branch_summary":
		return rawUsage(entry.Usage)
	default:
		return nil
	}
}

func rawUsage(raw json.RawMessage) *aitypes.Usage {
	if len(raw) == 0 {
		return nil
	}
	var usage aitypes.Usage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return nil
	}
	return &usage
}

func rawJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPointer(value int) *int { return &value }
