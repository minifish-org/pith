// This file carries fork.ts: building a complete logical destination state for
// backends that snapshot source entries and current values.
package session

import (
	"fmt"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// ForkSourceSnapshot is the captured source state used to build a fork.
type ForkSourceSnapshot struct {
	Entries []harnesstypes.Entry
	// ScalarValues are the current scalar rows of the source.
	ScalarValues []harnesstypes.StoredValue
	// EntriesComplete is false when a backend supplied only the requested
	// branch rather than the full tree. Nil means complete.
	EntriesComplete *bool
}

// ForkDestinationSnapshot is the complete logical state of a fork destination.
type ForkDestinationSnapshot struct {
	Entries      map[string]harnesstypes.Entry
	ScalarValues []harnesstypes.StoredValue
	NextSeq      int
}

func storedValuesInNamespace(values []harnesstypes.StoredValue, address harnesstypes.Value) []harnesstypes.StoredValue {
	out := []harnesstypes.StoredValue{}
	for _, stored := range values {
		if stored.Address.Namespace == address.Namespace {
			out = append(out, stored)
		}
	}
	return out
}

func findStoredValue(values []harnesstypes.StoredValue, address harnesstypes.Value) (harnesstypes.StoredValue, bool) {
	for _, stored := range values {
		if stored.Address.Namespace == address.Namespace && stored.Address.Key == address.Key {
			return stored, true
		}
	}
	return harnesstypes.StoredValue{}, false
}

// CreateForkSnapshot builds the complete logical state for a forked destination
// session.
func CreateForkSnapshot(source ForkSourceSnapshot, options harnesstypes.ForkOptions) (ForkDestinationSnapshot, error) {
	sourceEntries := map[string]harnesstypes.Entry{}
	for _, entry := range source.Entries {
		sourceEntries[EntryID(entry)] = entry
	}
	sourceTips := storedValuesInNamespace(source.ScalarValues, BranchTip(""))
	if err := validateForkSourceSnapshot(source, sourceEntries, sourceTips, options); err != nil {
		return ForkDestinationSnapshot{}, err
	}
	entryIDs, plan, err := selectForkContents(sourceEntries, sourceTips, options)
	if err != nil {
		return ForkDestinationSnapshot{}, err
	}
	entries := map[string]harnesstypes.Entry{}
	for id := range entryIDs {
		entries[id] = sourceEntries[id]
	}
	nextSeq := 1
	for _, entry := range entries {
		if EntrySeqOf(entry)+1 > nextSeq {
			nextSeq = EntrySeqOf(entry) + 1
		}
	}
	if nextSeq < 1 {
		nextSeq = 1
	}
	scalarValues := []harnesstypes.StoredValue{}
	for _, stored := range source.ScalarValues {
		projected, projectErr := ProjectForkCurrentStateWrite(CommittedWrite{
			Kind:      "value",
			Op:        "set",
			Seq:       stored.Seq,
			Namespace: stored.Address.Namespace,
			Key:       stored.Address.Key,
			Value:     stored.Value,
		}, plan, func(entryID string) bool { return entryIDs[entryID] })
		if projectErr != nil {
			return ForkDestinationSnapshot{}, projectErr
		}
		if projected != nil {
			scalarValues = append(scalarValues, harnesstypes.StoredValue{
				Address: harnesstypes.Value{Namespace: projected.Namespace, Key: projected.Key, Kind: "value"},
				Value:   projected.Value,
				Seq:     nextSeq,
			})
			nextSeq++
		}
	}
	return ForkDestinationSnapshot{Entries: entries, ScalarValues: scalarValues, NextSeq: nextSeq}, nil
}

func selectForkContents(
	sourceEntries map[string]harnesstypes.Entry,
	sourceTips []harnesstypes.StoredValue,
	options harnesstypes.ForkOptions,
) (map[string]bool, ForkCurrentStatePlan, error) {
	entryIDs := map[string]bool{}
	if options.Scope == "tree" {
		for id := range sourceEntries {
			entryIDs[id] = true
		}
		return entryIDs, ForkCurrentStatePlan{Scope: "tree"}, nil
	}
	branch := ""
	if options.Branch != nil {
		branch = *options.Branch
	}
	tipKnown := false
	var tip *string
	for _, stored := range sourceTips {
		if stored.Address.Key == branch {
			tipKnown = true
			tip = valueStringPointer(stored.Value)
			break
		}
	}
	plan, err := SelectBranchFork(options, ForkSourceView{
		Tip:      tip,
		TipKnown: tipKnown,
		GetParent: func(entryID string) (*string, bool) {
			entry, ok := sourceEntries[entryID]
			if !ok {
				return nil, false
			}
			return EntryParentID(entry), true
		},
		SelectEntry: func(entryID string) { entryIDs[entryID] = true },
	})
	if err != nil {
		return nil, ForkCurrentStatePlan{}, err
	}
	return entryIDs, plan, nil
}

func validateForkSourceSnapshot(
	source ForkSourceSnapshot,
	sourceEntries map[string]harnesstypes.Entry,
	sourceTips []harnesstypes.StoredValue,
	options harnesstypes.ForkOptions,
) error {
	sourceTipKeys := map[string]bool{}
	for _, stored := range sourceTips {
		sourceTipKeys[stored.Address.Key] = true
	}
	for _, stored := range source.ScalarValues {
		if stored.Address.Namespace == NamespaceLaneConfig || stored.Address.Namespace == NamespaceLaneState {
			if !sourceTipKeys[stored.Address.Key] {
				return fmt.Errorf("Source session branch %q is missing branch.tip", stored.Address.Key)
			}
		}
	}
	for _, tip := range sourceTips {
		_, hasConfiguration := findStoredValue(source.ScalarValues, LaneConfig(tip.Address.Key))
		_, hasState := findStoredValue(source.ScalarValues, LaneState(tip.Address.Key))
		if hasConfiguration != hasState {
			return fmt.Errorf("Source session branch %q has incomplete lane state", tip.Address.Key)
		}
		if options.Scope == "branch" && options.Branch != nil && tip.Address.Key == *options.Branch && !hasConfiguration {
			return fmt.Errorf("Source branch %q is not a configured AgentLane", *options.Branch)
		}
		entriesComplete := source.EntriesComplete == nil || *source.EntriesComplete
		if (entriesComplete || options.Scope == "tree") && tip.Value != nil {
			tipID, ok := tip.Value.(string)
			if !ok {
				return fmt.Errorf("Source session branch %q has an invalid tip", tip.Address.Key)
			}
			if _, exists := sourceEntries[tipID]; !exists {
				return fmt.Errorf("Source session branch %q has an unknown tip", tip.Address.Key)
			}
		}
	}
	return nil
}
