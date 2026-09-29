// This file carries fork-policy.ts: the pure branch-selection and
// current-state projection shared by MemoryStorage, JsonlStorage and the JSONL
// fork writer.
package session

import (
	"fmt"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// ForkCurrentStatePlan describes the shape of a fork destination.
type ForkCurrentStatePlan struct {
	Scope          string
	Branch         string
	DestinationTip *string
}

// ForkSourceView is the read-only view of a source branch used during branch
// selection.
type ForkSourceView struct {
	// Tip is the current tip. TipKnown distinguishes an unknown branch from an
	// empty branch whose tip is null.
	Tip      *string
	TipKnown bool

	GetParent   func(entryID string) (*string, bool)
	SelectEntry func(entryID string)
}

// SelectBranchFork validates a branch fork and selects the entries to copy.
func SelectBranchFork(options harnesstypes.ForkOptions, source ForkSourceView) (ForkCurrentStatePlan, error) {
	branch := ""
	if options.Branch != nil {
		branch = *options.Branch
	}
	if !source.TipKnown {
		return ForkCurrentStatePlan{}, fmt.Errorf("Unknown source branch: %s", branch)
	}

	var requested *string
	if options.EntryID != nil {
		value := *options.EntryID
		requested = &value
	} else if source.Tip != nil {
		value := *source.Tip
		requested = &value
	}

	found := requested == nil
	var destinationTip *string
	entryID := source.Tip
	for entryID != nil {
		parentID, ok := source.GetParent(*entryID)
		if !ok {
			return ForkCurrentStatePlan{}, fmt.Errorf("Corrupt source branch: missing parent %s", *entryID)
		}
		if requested != nil && *entryID == *requested {
			found = true
			if options.Position != nil && *options.Position == "before" {
				destinationTip = parentID
			} else {
				tip := *entryID
				destinationTip = &tip
				source.SelectEntry(*entryID)
			}
		} else if found {
			source.SelectEntry(*entryID)
		}
		entryID = parentID
	}
	if !found {
		requestedValue := ""
		if requested != nil {
			requestedValue = *requested
		}
		return ForkCurrentStatePlan{}, fmt.Errorf("Fork entry %s is not on source branch %q", requestedValue, branch)
	}
	return ForkCurrentStatePlan{Scope: "branch", Branch: branch, DestinationTip: destinationTip}, nil
}

// ProjectForkCurrentStateWrite projects one current scalar row or surviving
// list element into destination state. It returns nil when the row is
// excluded, and an error for an unknown reserved namespace.
func ProjectForkCurrentStateWrite(
	write CommittedWrite,
	plan ForkCurrentStatePlan,
	isEntryCopied func(entryID string) bool,
) (*CommittedWrite, error) {
	switch write.Namespace {
	case NamespaceSessionName:
		result := write
		return &result, nil
	case NamespaceEntryLabel:
		if isEntryCopied(write.Key) {
			result := write
			return &result, nil
		}
		return nil, nil
	case NamespaceBranchTip:
		if plan.Scope == "tree" {
			result := write
			return &result, nil
		}
		if write.Key == plan.Branch {
			result := write
			if plan.DestinationTip != nil {
				result.Value = *plan.DestinationTip
			} else {
				result.Value = nil
			}
			return &result, nil
		}
		return nil, nil
	case NamespaceLaneConfig:
		if plan.Scope == "tree" || write.Key == plan.Branch {
			result := write
			return &result, nil
		}
		return nil, nil
	case NamespaceLaneState:
		if plan.Scope == "tree" || write.Key == plan.Branch {
			result := write
			result.Value = harnesstypes.LaneState{
				CurrentOperationID: nil,
				LastOperationID:    nil,
				Inbox:              []harnesstypes.InboxItem{},
			}
			return &result, nil
		}
		return nil, nil
	case NamespaceOperation:
		return nil, nil
	}
	if len(write.Namespace) >= 5 && write.Namespace[:5] == "pi.op" {
		return nil, nil
	}
	if len(write.Namespace) >= 11 && write.Namespace[:11] == "pi.pending." {
		return nil, nil
	}
	if write.Namespace == "pi" || (len(write.Namespace) >= 3 && write.Namespace[:3] == "pi.") {
		return nil, fmt.Errorf("Unknown reserved fork namespace: %s", write.Namespace)
	}
	if plan.Scope == "tree" {
		result := write
		return &result, nil
	}
	return nil, nil
}
