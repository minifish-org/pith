// This file carries in-memory-storage-state.ts: the complete materialized
// session state shared by MemoryStorage and JsonlStorage.
package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	usageutils "github.com/minifish-org/pith/packages/agent/harness/utils/usage"
)

// EntryID returns the identity of a session entry.
func EntryID(entry harnesstypes.Entry) string {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.ID
	case *harnesstypes.MessageEntry:
		return typed.ID
	case harnesstypes.CompactionEntry:
		return typed.ID
	case *harnesstypes.CompactionEntry:
		return typed.ID
	case harnesstypes.BranchSummaryEntry:
		return typed.ID
	case *harnesstypes.BranchSummaryEntry:
		return typed.ID
	case harnesstypes.CustomEntry:
		return typed.ID
	case *harnesstypes.CustomEntry:
		return typed.ID
	default:
		return ""
	}
}

// EntryParentID returns the parent identity of a session entry.
func EntryParentID(entry harnesstypes.Entry) *string {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.ParentID
	case *harnesstypes.MessageEntry:
		return typed.ParentID
	case harnesstypes.CompactionEntry:
		return typed.ParentID
	case *harnesstypes.CompactionEntry:
		return typed.ParentID
	case harnesstypes.BranchSummaryEntry:
		return typed.ParentID
	case *harnesstypes.BranchSummaryEntry:
		return typed.ParentID
	case harnesstypes.CustomEntry:
		return typed.ParentID
	case *harnesstypes.CustomEntry:
		return typed.ParentID
	default:
		return nil
	}
}

// EntrySeqOf returns the assigned sequence of a session entry.
func EntrySeqOf(entry harnesstypes.Entry) int {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.Seq
	case *harnesstypes.MessageEntry:
		return typed.Seq
	case harnesstypes.CompactionEntry:
		return typed.Seq
	case *harnesstypes.CompactionEntry:
		return typed.Seq
	case harnesstypes.BranchSummaryEntry:
		return typed.Seq
	case *harnesstypes.BranchSummaryEntry:
		return typed.Seq
	case harnesstypes.CustomEntry:
		return typed.Seq
	case *harnesstypes.CustomEntry:
		return typed.Seq
	default:
		return 0
	}
}

// EntryTimestampOf returns the commit timestamp of a session entry.
func EntryTimestampOf(entry harnesstypes.Entry) float64 {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.Timestamp
	case *harnesstypes.MessageEntry:
		return typed.Timestamp
	case harnesstypes.CompactionEntry:
		return typed.Timestamp
	case *harnesstypes.CompactionEntry:
		return typed.Timestamp
	case harnesstypes.BranchSummaryEntry:
		return typed.Timestamp
	case *harnesstypes.BranchSummaryEntry:
		return typed.Timestamp
	case harnesstypes.CustomEntry:
		return typed.Timestamp
	case *harnesstypes.CustomEntry:
		return typed.Timestamp
	default:
		return 0
	}
}

// EntryTypeOf returns the discriminator of a session entry.
func EntryTypeOf(entry harnesstypes.Entry) harnesstypes.EntryType {
	if entry == nil {
		return ""
	}
	return entry.EntryKind()
}

// EntryCustomTypeOf returns the custom type of a custom entry.
func EntryCustomTypeOf(entry harnesstypes.Entry) *string {
	switch typed := entry.(type) {
	case harnesstypes.CustomEntry:
		return &typed.CustomType
	case *harnesstypes.CustomEntry:
		return &typed.CustomType
	default:
		return nil
	}
}

// EntryStructureOf projects an entry onto its cheap structural form.
func EntryStructureOf(entry harnesstypes.Entry) harnesstypes.EntryStructure {
	return harnesstypes.EntryStructure{
		ID:         EntryID(entry),
		ParentID:   EntryParentID(entry),
		Seq:        EntrySeqOf(entry),
		Timestamp:  EntryTimestampOf(entry),
		Type:       EntryTypeOf(entry),
		CustomType: EntryCustomTypeOf(entry),
	}
}

type storedListSnapshot struct {
	address  harnesstypes.ValueList
	elements []harnesstypes.ListElement
}

// InMemoryStorageState is the complete materialized session state.
type InMemoryStorageState struct {
	entries      map[string]harnesstypes.Entry
	entriesBySeq []harnesstypes.Entry
	scalarValues map[string]harnesstypes.StoredValue
	listValues   map[string]*storedListSnapshot
	usage        map[string]harnesstypes.UsageRow
	stats        harnesstypes.SessionStats
	nextSeq      int
}

// NewInMemoryStorageState builds empty state.
func NewInMemoryStorageState() *InMemoryStorageState {
	return &InMemoryStorageState{
		entries:      map[string]harnesstypes.Entry{},
		entriesBySeq: []harnesstypes.Entry{},
		scalarValues: map[string]harnesstypes.StoredValue{},
		listValues:   map[string]*storedListSnapshot{},
		usage:        map[string]harnesstypes.UsageRow{},
		stats:        harnesstypes.SessionStats{MessageCount: 0, Usage: usageutils.EmptyUsage()},
		nextSeq:      1,
	}
}

func physicalKey(namespace, key string) string { return namespace + "\x00" + key }

// PrepareCommit stamps and validates a transaction.
func (s *InMemoryStorageState) PrepareCommit(writes []harnesstypes.Write, timestamp float64) (PreparedCommit, error) {
	prepared := PrepareStorageCommit(writes, s.nextSeq, timestamp)
	if err := s.ValidateCommitted(prepared.Writes); err != nil {
		return PreparedCommit{}, err
	}
	return prepared, nil
}

// ValidateCommitted validates already stamped writes against current state.
func (s *InMemoryStorageState) ValidateCommitted(writes []CommittedWrite) error {
	return ValidateCommittedWrites(writes, s.nextSeq, s)
}

// HasEntryOrUsageID reports whether the id is already stored.
func (s *InMemoryStorageState) HasEntryOrUsageID(id string) bool {
	if _, ok := s.entries[id]; ok {
		return true
	}
	_, ok := s.usage[id]
	return ok
}

// HasEntryID reports whether the entry id is already stored.
func (s *InMemoryStorageState) HasEntryID(id string) bool {
	_, ok := s.entries[id]
	return ok
}

// ApplyValidated applies accepted writes and returns the post-apply totals.
func (s *InMemoryStorageState) ApplyValidated(writes []CommittedWrite) harnesstypes.SessionStats {
	for _, write := range writes {
		switch write.Kind {
		case "entry":
			if write.Entry == nil {
				continue
			}
			id := EntryID(write.Entry)
			s.entries[id] = write.Entry
			s.entriesBySeq = append(s.entriesBySeq, write.Entry)
			if write.Entry.EntryKind() == harnesstypes.EntryTypeMessage {
				s.stats.MessageCount++
			}
		case "usage":
			if write.Usage == nil {
				continue
			}
			s.usage[write.Usage.ID] = *write.Usage
			s.stats.Usage = usageutils.AddUsage(s.stats.Usage, write.Usage.Usage)
		case "value":
			if write.Op == "delete" {
				delete(s.scalarValues, physicalKey(write.Namespace, write.Key))
			} else {
				s.applyValueSetOrListAppend(write)
			}
		case "list":
			if write.Op == "delete" {
				delete(s.listValues, physicalKey(write.Namespace, write.Key))
			} else {
				s.applyValueSetOrListAppend(write)
			}
		}
		s.nextSeq = write.Seq + 1
	}
	return s.stats
}

// applyValueSetOrListAppend applies one already validated set or append.
func (s *InMemoryStorageState) applyValueSetOrListAppend(write CommittedWrite) {
	key := physicalKey(write.Namespace, write.Key)
	if write.Kind == "value" {
		address := harnesstypes.Value{Namespace: write.Namespace, Key: write.Key, Kind: "value"}
		s.scalarValues[key] = harnesstypes.StoredValue{Address: address, Value: write.Value, Seq: write.Seq}
		return
	}
	element := harnesstypes.ListElement{Seq: write.Seq, Value: write.Value}
	stored, ok := s.listValues[key]
	if !ok {
		address := harnesstypes.ValueList{Namespace: write.Namespace, Key: write.Key, Kind: "list"}
		s.listValues[key] = &storedListSnapshot{address: address, elements: []harnesstypes.ListElement{element}}
		return
	}
	stored.elements = append(stored.elements, element)
}

// AdvanceNextSeq raises the sequence high-water mark.
func (s *InMemoryStorageState) AdvanceNextSeq(nextSeq int) error {
	if nextSeq < 1 {
		return fmt.Errorf("Invalid storage sequence high-water mark: %d", nextSeq)
	}
	if nextSeq > s.nextSeq {
		s.nextSeq = nextSeq
	}
	return nil
}

// GetNextSeq returns the next sequence a commit would use.
func (s *InMemoryStorageState) GetNextSeq() int { return s.nextSeq }

// GetEntries resolves the requested entry ids.
func (s *InMemoryStorageState) GetEntries(ids []string) map[string]harnesstypes.Entry {
	found := map[string]harnesstypes.Entry{}
	for _, id := range ids {
		if entry, ok := s.entries[id]; ok {
			found[id] = entry
		}
	}
	return found
}

// GetValue resolves one scalar value.
func (s *InMemoryStorageState) GetValue(address harnesstypes.Value) (harnesstypes.StoredValue, bool) {
	stored, ok := s.scalarValues[physicalKey(address.Namespace, address.Key)]
	return stored, ok
}

// ScanValues returns all scalar values under a namespace prefix.
func (s *InMemoryStorageState) ScanValues(prefix harnesstypes.Value) []harnesstypes.StoredValue {
	out := []harnesstypes.StoredValue{}
	for _, stored := range s.scalarValues {
		if stored.Address.Namespace != prefix.Namespace {
			continue
		}
		if len(prefix.Key) > 0 && !strings.HasPrefix(stored.Address.Key, prefix.Key) {
			continue
		}
		out = append(out, stored)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return compareKeys(out[i].Address.Key, out[j].Address.Key) < 0
	})
	return out
}

// ReadList reads one page of a list.
func (s *InMemoryStorageState) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions) ([]harnesstypes.ListElement, error) {
	resolved, err := ResolveListReadOptions(options)
	if err != nil {
		return nil, err
	}
	stored := s.listValues[physicalKey(address.Namespace, address.Key)]
	all := []harnesstypes.ListElement{}
	if stored != nil {
		all = stored.elements
	}
	filtered := make([]harnesstypes.ListElement, 0, len(all))
	for _, element := range all {
		if resolved.Cursor != nil {
			if resolved.Order == "asc" {
				if element.Seq <= resolved.Cursor.Seq {
					continue
				}
			} else if element.Seq >= resolved.Cursor.Seq {
				continue
			}
		}
		filtered = append(filtered, element)
	}
	if resolved.Order != "asc" {
		for left, right := 0, len(filtered)-1; left < right; left, right = left+1, right-1 {
			filtered[left], filtered[right] = filtered[right], filtered[left]
		}
	}
	if len(filtered) > resolved.Limit {
		filtered = filtered[:resolved.Limit]
	}
	return filtered, nil
}

// ScanBranch walks one entry branch with stop, filter, cursor and limit
// semantics.
func (s *InMemoryStorageState) ScanBranch(query harnesstypes.StorageBranchScan) ([]harnesstypes.Entry, error) {
	start, ok := s.entries[query.Start]
	if !ok {
		return nil, fmt.Errorf("Unknown branch start: %s", query.Start)
	}
	path := []harnesstypes.Entry{}
	entry := start
	for {
		path = append(path, entry)
		parent := EntryParentID(entry)
		if parent == nil {
			break
		}
		next, ok := s.entries[*parent]
		if !ok {
			return nil, errors.New("Corrupt branch: missing parent")
		}
		entry = next
	}
	if query.Order != nil && *query.Order == "oldestFirst" {
		for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
			path[left], path[right] = path[right], path[left]
		}
	}

	stopped := []harnesstypes.Entry{}
	for _, candidate := range path {
		stopped = append(stopped, candidate)
		if query.StopAtID != nil && EntryID(candidate) == *query.StopAtID {
			break
		}
		if query.StopAtType != nil && candidate.EntryKind() == *query.StopAtType {
			break
		}
	}

	filtered := make([]harnesstypes.Entry, 0, len(stopped))
	for _, candidate := range stopped {
		if query.Type != nil && candidate.EntryKind() != *query.Type {
			continue
		}
		if query.CustomType != nil {
			customType := EntryCustomTypeOf(candidate)
			if customType == nil || *customType != *query.CustomType {
				continue
			}
		}
		if query.Cursor != nil {
			order := "newestFirst"
			if query.Order != nil {
				order = *query.Order
			}
			if order == "oldestFirst" {
				if EntrySeqOf(candidate) <= query.Cursor.Seq {
					continue
				}
			} else if EntrySeqOf(candidate) >= query.Cursor.Seq {
				continue
			}
		}
		filtered = append(filtered, candidate)
	}
	if query.Limit != nil {
		limit := *query.Limit
		if limit < 0 {
			limit = 0
		}
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
	}
	return filtered, nil
}

// ScanBranchStructure is ScanBranch projected onto structural entries.
func (s *InMemoryStorageState) ScanBranchStructure(query harnesstypes.StorageBranchScan) ([]harnesstypes.EntryStructure, error) {
	entries, err := s.ScanBranch(query)
	if err != nil {
		return nil, err
	}
	out := make([]harnesstypes.EntryStructure, 0, len(entries))
	for _, entry := range entries {
		out = append(out, EntryStructureOf(entry))
	}
	return out, nil
}

// ScanEntries scans the global entry index.
func (s *InMemoryStorageState) ScanEntries(query harnesstypes.EntryScan) []harnesstypes.Entry {
	limit := len(s.entriesBySeq)
	if query.Limit != nil {
		if *query.Limit < 0 {
			limit = 0
		} else {
			limit = *query.Limit
		}
	}
	descending := query.Order != nil && *query.Order == "desc"
	out := []harnesstypes.Entry{}
	if descending {
		for index := len(s.entriesBySeq) - 1; index >= 0 && len(out) < limit; index-- {
			entry := s.entriesBySeq[index]
			if s.entryMatches(entry, query) {
				out = append(out, entry)
			}
		}
		return out
	}
	for index := 0; index < len(s.entriesBySeq) && len(out) < limit; index++ {
		entry := s.entriesBySeq[index]
		if s.entryMatches(entry, query) {
			out = append(out, entry)
		}
	}
	return out
}

func (s *InMemoryStorageState) entryMatches(entry harnesstypes.Entry, query harnesstypes.EntryScan) bool {
	if query.Type != nil && entry.EntryKind() != *query.Type {
		return false
	}
	if query.CustomType != nil {
		customType := EntryCustomTypeOf(entry)
		if customType == nil || *customType != *query.CustomType {
			return false
		}
	}
	if query.FromSeq != nil && EntrySeqOf(entry) < *query.FromSeq {
		return false
	}
	if query.ToSeq != nil && EntrySeqOf(entry) > *query.ToSeq {
		return false
	}
	return true
}

// ScanUsage scans the usage ledger.
func (s *InMemoryStorageState) ScanUsage(query harnesstypes.UsageScan) []harnesstypes.UsageRow {
	rows := make([]harnesstypes.UsageRow, 0, len(s.usage))
	for _, row := range s.usage {
		if query.FromSeq != nil && row.Seq < *query.FromSeq {
			continue
		}
		if query.ToSeq != nil && row.Seq > *query.ToSeq {
			continue
		}
		rows = append(rows, row)
	}
	descending := query.Order != nil && *query.Order == "desc"
	sort.SliceStable(rows, func(i, j int) bool {
		if descending {
			return rows[i].Seq > rows[j].Seq
		}
		return rows[i].Seq < rows[j].Seq
	})
	if query.Limit != nil && *query.Limit >= 0 && len(rows) > *query.Limit {
		rows = rows[:*query.Limit]
	}
	return rows
}

// GetStats returns the current session totals.
func (s *InMemoryStorageState) GetStats() harnesstypes.SessionStats { return s.stats }

type memoryForkPlan struct {
	scope          string
	branch         string
	destinationTip *string
	entryIDs       map[string]bool
}

// CreateFork builds the destination state for one fork.
func (s *InMemoryStorageState) CreateFork(options harnesstypes.ForkOptions) (*InMemoryStorageState, error) {
	plan, err := s.selectForkPlan(options)
	if err != nil {
		return nil, err
	}
	isEntryCopied := func(entryID string) bool {
		if plan.scope == "tree" {
			return true
		}
		return plan.entryIDs[entryID]
	}
	destination := NewInMemoryStorageState()
	messageCount := 0
	for _, entry := range s.entriesBySeq {
		if !isEntryCopied(EntryID(entry)) {
			continue
		}
		destination.entries[EntryID(entry)] = entry
		destination.entriesBySeq = append(destination.entriesBySeq, entry)
		if entry.EntryKind() == harnesstypes.EntryTypeMessage {
			messageCount++
		}
	}
	destination.stats.MessageCount = messageCount

	for _, stored := range s.scalarValues {
		projected, projectErr := ProjectForkCurrentStateWrite(CommittedWrite{
			Kind:      "value",
			Op:        "set",
			Seq:       stored.Seq,
			Namespace: stored.Address.Namespace,
			Key:       stored.Address.Key,
			Value:     stored.Value,
		}, plan.toPolicyPlan(), isEntryCopied)
		if projectErr != nil {
			return nil, projectErr
		}
		if projected != nil {
			destination.applyValueSetOrListAppend(*projected)
		}
	}
	for _, stored := range s.listValues {
		for _, element := range stored.elements {
			projected, projectErr := ProjectForkCurrentStateWrite(CommittedWrite{
				Kind:      "list",
				Op:        "append",
				Seq:       element.Seq,
				Namespace: stored.address.Namespace,
				Key:       stored.address.Key,
				Value:     element.Value,
			}, plan.toPolicyPlan(), isEntryCopied)
			if projectErr != nil {
				return nil, projectErr
			}
			if projected != nil {
				destination.applyValueSetOrListAppend(*projected)
			}
		}
	}
	destination.nextSeq = s.nextSeq
	return destination, nil
}

func (p memoryForkPlan) toPolicyPlan() ForkCurrentStatePlan {
	if p.scope == "tree" {
		return ForkCurrentStatePlan{Scope: "tree"}
	}
	return ForkCurrentStatePlan{Scope: "branch", Branch: p.branch, DestinationTip: p.destinationTip}
}

func (s *InMemoryStorageState) selectForkPlan(options harnesstypes.ForkOptions) (memoryForkPlan, error) {
	if options.Scope == "tree" {
		return memoryForkPlan{scope: "tree"}, nil
	}
	branch := ""
	if options.Branch != nil {
		branch = *options.Branch
	}
	entryIDs := map[string]bool{}
	var tip *string
	tipKnown := false
	if stored, ok := s.GetValue(BranchTip(branch)); ok {
		tipKnown = true
		tip = valueStringPointer(stored.Value)
	}
	plan, err := SelectBranchFork(options, ForkSourceView{
		Tip:      tip,
		TipKnown: tipKnown,
		GetParent: func(entryID string) (*string, bool) {
			entry, ok := s.entries[entryID]
			if !ok {
				return nil, false
			}
			return EntryParentID(entry), true
		},
		SelectEntry: func(entryID string) { entryIDs[entryID] = true },
	})
	if err != nil {
		return memoryForkPlan{}, err
	}
	if _, ok := s.GetValue(LaneConfig(branch)); !ok {
		return memoryForkPlan{}, fmt.Errorf("Source branch %q is not a configured AgentLane", branch)
	}
	if _, ok := s.GetValue(LaneState(branch)); !ok {
		return memoryForkPlan{}, fmt.Errorf("Source branch %q is not a configured AgentLane", branch)
	}
	return memoryForkPlan{scope: "branch", branch: plan.Branch, destinationTip: plan.DestinationTip, entryIDs: entryIDs}, nil
}

func valueStringPointer(value any) *string {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return &text
	}
	return nil
}

// compareKeys compares two UTF-8 keys by Unicode code point. UTF-8 byte order
// already matches code point order, so a byte comparison is equivalent to the
// upstream Array.from comparison.
func compareKeys(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
