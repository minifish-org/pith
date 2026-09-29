// This file carries jsonl/fork.ts: streaming branch and tree forks into a
// freshly published format-4 destination.
package jsonl

import (
	"fmt"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// JsonlForkSourceMetadata identifies one JSONL fork source.
type JsonlForkSourceMetadata struct {
	ID   string
	Cwd  string
	Path string
}

// JsonlForkInput is a prepared fork input.
type JsonlForkInput struct {
	Kind       string // "open", "closed" or "legacy-v3"
	Metadata   JsonlForkSourceMetadata
	NextSeq    int
	Normalized *LegacyV3Source
}

// JsonlForkRunOptions are the inputs for one JSONL fork.
type JsonlForkRunOptions struct {
	Input             JsonlForkInput
	FileSystem        harnesstypes.FileSystem
	DestinationPath   string
	DestinationHeader JsonlStorageHeader
	Fork              harnesstypes.ForkOptions
}

type jsonlForkIndex struct {
	currentScalarSeqs      map[string]int
	branchTips             map[string]any
	firstSurvivingListSeqs map[string]int
	entryParents           map[string]*string
	copiedEntryIDs         map[string]bool
	laneConfigs            map[string]bool
	laneStates             map[string]bool
}

func newJsonlForkIndex() *jsonlForkIndex {
	return &jsonlForkIndex{
		currentScalarSeqs:      map[string]int{},
		branchTips:             map[string]any{},
		firstSurvivingListSeqs: map[string]int{},
		entryParents:           map[string]*string{},
		copiedEntryIDs:         map[string]bool{},
		laneConfigs:            map[string]bool{},
		laneStates:             map[string]bool{},
	}
}

func (i *jsonlForkIndex) applyEntry(id string, parentID *string) {
	i.entryParents[id] = parentID
}

func (i *jsonlForkIndex) applyWrites(writes []session.CommittedWrite) {
	for _, write := range writes {
		switch write.Kind {
		case "entry":
			i.applyEntry(session.EntryID(write.Entry), session.EntryParentID(write.Entry))
		case "value":
			key := physicalKey(write.Namespace, write.Key)
			if write.Op == "delete" {
				delete(i.currentScalarSeqs, key)
				i.applyLaneValue(write, false)
			} else {
				i.currentScalarSeqs[key] = write.Seq
				i.applyLaneValue(write, true)
			}
		case "list":
			key := physicalKey(write.Namespace, write.Key)
			if write.Op == "delete" {
				delete(i.firstSurvivingListSeqs, key)
			} else if _, ok := i.firstSurvivingListSeqs[key]; !ok {
				i.firstSurvivingListSeqs[key] = write.Seq
			}
		}
	}
}

func (i *jsonlForkIndex) applyLaneValue(write session.CommittedWrite, present bool) {
	switch write.Namespace {
	case session.NamespaceBranchTip:
		if present {
			i.branchTips[write.Key] = write.Value
		} else {
			delete(i.branchTips, write.Key)
		}
	case session.NamespaceLaneConfig:
		if present {
			i.laneConfigs[write.Key] = true
		} else {
			delete(i.laneConfigs, write.Key)
		}
	case session.NamespaceLaneState:
		if present {
			i.laneStates[write.Key] = true
		} else {
			delete(i.laneStates, write.Key)
		}
	}
}

func (i *jsonlForkIndex) getBranchTip(branch string) (any, bool) {
	tip, ok := i.branchTips[branch]
	return tip, ok
}

func (i *jsonlForkIndex) hasCompleteLane(branch string) bool {
	return i.laneConfigs[branch] && i.laneStates[branch]
}

func (i *jsonlForkIndex) getCurrentScalarSeq(namespace, key string) (int, bool) {
	seq, ok := i.currentScalarSeqs[physicalKey(namespace, key)]
	return seq, ok
}

func (i *jsonlForkIndex) isSurvivingListElement(namespace, key string, seq int) bool {
	first, ok := i.firstSurvivingListSeqs[physicalKey(namespace, key)]
	return ok && seq >= first
}

func (i *jsonlForkIndex) getParent(entryID string) (*string, bool) {
	parent, ok := i.entryParents[entryID]
	return parent, ok
}

func (i *jsonlForkIndex) selectEntry(entryID string) { i.copiedEntryIDs[entryID] = true }

func (i *jsonlForkIndex) isEntrySelected(entryID string) bool { return i.copiedEntryIDs[entryID] }

func physicalKey(namespace, key string) string { return namespace + "\x00" + key }

func readJsonlForkHeader(reader harnesstypes.TextLineReader, source JsonlForkSourceMetadata, ctx harnesstypes.Context) (JsonlStorageHeader, error) {
	parsed, err := ReadJsonlHeader(reader, source.Path, ctx)
	if err != nil {
		return JsonlStorageHeader{}, err
	}
	if parsed.Format != "v4" || parsed.V4 == nil {
		return JsonlStorageHeader{}, fmt.Errorf("Invalid JSONL storage %s: expected format 4 header", source.Path)
	}
	header := *parsed.V4
	if header.ID != source.ID || header.Cwd != source.Cwd {
		return JsonlStorageHeader{}, fmt.Errorf("Session identity does not match header: %s", source.ID)
	}
	if header.StorageVersion != JSONL_STORAGE_VERSION {
		return JsonlStorageHeader{}, fmt.Errorf("Session %s uses unsupported storage version %d", source.ID, header.StorageVersion)
	}
	return header, nil
}

func reachesForkBoundary(writes []session.CommittedWrite, stopBeforeSeq *int) (bool, error) {
	if stopBeforeSeq == nil || len(writes) == 0 {
		return false, nil
	}
	first := writes[0]
	last := writes[len(writes)-1]
	if first.Seq >= *stopBeforeSeq {
		return true, nil
	}
	if last.Seq >= *stopBeforeSeq {
		return false, fmt.Errorf("JSONL transaction crosses fork sequence boundary %d", *stopBeforeSeq)
	}
	return false, nil
}

func readJsonlForkTransactions(reader harnesstypes.TextLineReader, path string, stopBeforeSeq *int, ctx harnesstypes.Context) ([][]session.CommittedWrite, error) {
	out := [][]session.CommittedWrite{}
	for {
		line, err := FileValue(reader.ReadLine(ctx), "Failed to read JSONL fork source "+path)
		if err != nil {
			return nil, err
		}
		if line == nil || !line.Terminated {
			break
		}
		writes, parseErr := ParseJsonlTransaction(line.Text)
		if parseErr != nil {
			return nil, parseErr
		}
		if reaches, boundaryErr := reachesForkBoundary(writes, stopBeforeSeq); boundaryErr != nil {
			return nil, boundaryErr
		} else if reaches {
			break
		}
		out = append(out, writes)
	}
	return out, nil
}

func selectJsonlFork(index *jsonlForkIndex, options harnesstypes.ForkOptions) (session.ForkCurrentStatePlan, error) {
	if options.Scope == "tree" {
		return session.ForkCurrentStatePlan{Scope: "tree"}, nil
	}
	branch := ""
	if options.Branch != nil {
		branch = *options.Branch
	}
	tipValue, tipKnown := index.getBranchTip(branch)
	var tip *string
	if text, ok := tipValue.(string); ok {
		tip = &text
	}
	plan, err := session.SelectBranchFork(options, session.ForkSourceView{
		Tip:      tip,
		TipKnown: tipKnown,
		GetParent: func(entryID string) (*string, bool) {
			return index.getParent(entryID)
		},
		SelectEntry: func(entryID string) { index.selectEntry(entryID) },
	})
	if err != nil {
		return session.ForkCurrentStatePlan{}, err
	}
	if !index.hasCompleteLane(branch) {
		return session.ForkCurrentStatePlan{}, fmt.Errorf("Source branch %q is not a configured AgentLane", branch)
	}
	return plan, nil
}

func projectJsonlForkWrite(
	write session.CommittedWrite,
	index *jsonlForkIndex,
	plan session.ForkCurrentStatePlan,
	isEntryCopied func(entryID string) bool,
) (*session.CommittedWrite, error) {
	switch write.Kind {
	case "entry":
		if isEntryCopied(session.EntryID(write.Entry)) {
			result := write
			return &result, nil
		}
		return nil, nil
	case "value":
		if write.Op != "set" {
			return nil, nil
		}
		seq, ok := index.getCurrentScalarSeq(write.Namespace, write.Key)
		if !ok || seq != write.Seq {
			return nil, nil
		}
		return session.ProjectForkCurrentStateWrite(write, plan, isEntryCopied)
	case "list":
		if write.Op != "append" {
			return nil, nil
		}
		if !index.isSurvivingListElement(write.Namespace, write.Key, write.Seq) {
			return nil, nil
		}
		return session.ProjectForkCurrentStateWrite(write, plan, isEntryCopied)
	default:
		return nil, nil
	}
}

func indexForkInput(input JsonlForkInput, fileSystem harnesstypes.FileSystem, ctx harnesstypes.Context) (*jsonlForkIndex, int, error) {
	index := newJsonlForkIndex()
	if input.Kind == "legacy-v3" && input.Normalized != nil {
		for _, structure := range input.Normalized.EntryStructures() {
			index.applyEntry(structure.ID, structure.ParentID)
		}
		index.applyWrites(input.Normalized.Values)
		return index, input.Normalized.NextSeq, nil
	}
	reader, err := FileValue(fileSystem.OpenTextLineReader(input.Metadata.Path, ctx), "Failed to open JSONL fork source "+input.Metadata.Path)
	if err != nil {
		return nil, 0, err
	}
	defer reader.Close(ctx)
	header, err := readJsonlForkHeader(reader, input.Metadata, ctx)
	if err != nil {
		return nil, 0, err
	}
	var stopBeforeSeq *int
	if input.Kind == "open" {
		stop := input.NextSeq
		stopBeforeSeq = &stop
	}
	transactions, err := readJsonlForkTransactions(reader, input.Metadata.Path, stopBeforeSeq, ctx)
	if err != nil {
		return nil, 0, err
	}
	highestCompleteSeq := 0
	for _, writes := range transactions {
		index.applyWrites(writes)
		if len(writes) != 0 {
			highestCompleteSeq = writes[len(writes)-1].Seq
		}
	}
	nextSeq := input.NextSeq
	if input.Kind != "open" {
		nextSeq = highestCompleteSeq + 1
		if header.NextSeq != nil && *header.NextSeq > nextSeq {
			nextSeq = *header.NextSeq
		}
		if nextSeq < 1 {
			nextSeq = 1
		}
	}
	return index, nextSeq, nil
}

func streamForkWrites(input JsonlForkInput, fileSystem harnesstypes.FileSystem, stopBeforeSeq int, isEntryCopied func(entryID string) bool, ctx harnesstypes.Context) ([]session.CommittedWrite, error) {
	if input.Kind == "legacy-v3" && input.Normalized != nil {
		return input.Normalized.Writes(ctx, isEntryCopied)
	}
	reader, err := FileValue(fileSystem.OpenTextLineReader(input.Metadata.Path, ctx), "Failed to open JSONL fork source "+input.Metadata.Path)
	if err != nil {
		return nil, err
	}
	defer reader.Close(ctx)
	if _, err := readJsonlForkHeader(reader, input.Metadata, ctx); err != nil {
		return nil, err
	}
	stop := stopBeforeSeq
	transactions, err := readJsonlForkTransactions(reader, input.Metadata.Path, &stop, ctx)
	if err != nil {
		return nil, err
	}
	out := []session.CommittedWrite{}
	for _, writes := range transactions {
		out = append(out, writes...)
	}
	return out, nil
}

// RunJsonlFork indexes a source, validates the requested fork and atomically
// publishes the selected entries and current state.
func RunJsonlFork(options JsonlForkRunOptions, ctx harnesstypes.Context) error {
	index, nextSeq, err := indexForkInput(options.Input, options.FileSystem, ctx)
	if err != nil {
		return err
	}
	fork := options.Fork
	if options.Input.Kind == "legacy-v3" && options.Input.Normalized != nil && fork.Scope == "branch" && fork.EntryID != nil {
		translated, translateErr := options.Input.Normalized.TranslateForkEntryID(*fork.EntryID)
		if translateErr != nil {
			return translateErr
		}
		fork.EntryID = &translated
	}
	plan, err := selectJsonlFork(index, fork)
	if err != nil {
		return err
	}
	isEntryCopied := func(entryID string) bool {
		if plan.Scope == "tree" {
			return true
		}
		return index.isEntrySelected(entryID)
	}
	header := options.DestinationHeader
	header.NextSeq = &nextSeq
	return PublishJsonl(options.FileSystem, options.DestinationPath, header, ctx, func(append func(writes []session.CommittedWrite) error) error {
		sourceWrites, err := streamForkWrites(options.Input, options.FileSystem, nextSeq, isEntryCopied, ctx)
		if err != nil {
			return err
		}
		for _, write := range sourceWrites {
			projected, projectErr := projectJsonlForkWrite(write, index, plan, isEntryCopied)
			if projectErr != nil {
				return projectErr
			}
			if projected != nil {
				if err := append([]session.CommittedWrite{*projected}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
