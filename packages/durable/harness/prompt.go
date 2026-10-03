package harness

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
	"github.com/minifish-org/pith/packages/durable"
)

// sectionValues is an ordered map of section key to value; a nil value marks a
// removal.
type sectionValues struct {
	keys   []string
	values map[string]*string
}

func newSectionValues() *sectionValues {
	return &sectionValues{values: map[string]*string{}}
}

func (s *sectionValues) set(key, value string) {
	text := value
	if _, ok := s.values[key]; !ok {
		s.keys = append(s.keys, key)
	}
	s.values[key] = &text
}

func (s *sectionValues) setNull(key string) {
	if _, ok := s.values[key]; !ok {
		s.keys = append(s.keys, key)
	}
	s.values[key] = nil
}

func (s *sectionValues) delete(key string) {
	if _, ok := s.values[key]; !ok {
		return
	}
	delete(s.values, key)
	for i, k := range s.keys {
		if k == key {
			s.keys = append(s.keys[:i], s.keys[i+1:]...)
			break
		}
	}
}

func (s *sectionValues) get(key string) (string, bool) {
	value, ok := s.values[key]
	if !ok || value == nil {
		return "", false
	}
	return *value, true
}

// replaySections replays system messages in order: set in place, null deletes,
// re-adding appends.
func replaySections(messages []types.Message) *sectionValues {
	shown := newSectionValues()
	for _, message := range messages {
		if message.System == nil {
			continue
		}
		sections := message.System.Sections
		if len(sections) == 0 {
			continue
		}
		for _, key := range orderedSectionKeys(message.System) {
			value, ok := sections[key]
			if !ok {
				continue
			}
			if value == nil {
				shown.delete(key)
			} else {
				shown.set(key, *value)
			}
		}
	}
	return shown
}

func orderedSectionKeys(message *types.SystemMessage) []string {
	if len(message.SectionOrder) > 0 {
		return message.SectionOrder
	}
	keys := make([]string, 0, len(message.Sections))
	for key := range message.Sections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// renderSections renders the agent's sections in order.
func renderSections(ctx context.Context, sections []PromptSection, input PromptInput, shown *sectionValues, report func(error)) *sectionValues {
	desired := newSectionValues()
	for _, section := range sections {
		text, err := section.Render(ctx, input)
		if err != nil {
			report(err)
			if kept, ok := shown.get(section.Key); ok {
				desired.set(section.Key, kept)
			}
			continue
		}
		if section.Untagged {
			desired.set(section.Key, text)
			continue
		}
		desired.set(section.Key, "<"+section.Key+">\n"+text+"\n</"+section.Key+">")
	}
	return desired
}

// toolChanges is a set of tool removals and additions.
type toolChanges struct {
	removed []types.ToolReference
	added   []types.Tool
}

// planSystemEntries plans the pi.system entries that make the replayed sections
// and tools equal desired and tools.
func planSystemEntries(view ContextView, desired *sectionValues, tools []types.Tool, timestamp float64) []durable.EntryDraft {
	head := view.Head
	hasNewerSystem := false
	if head != nil {
		for _, entry := range view.Entries {
			if entry.Kind == entryKindSystem && entry.ID > head.ID {
				hasNewerSystem = true
				break
			}
		}
	}
	if head != nil && !hasNewerSystem {
		var edits []durable.ContextEdit
		for _, entry := range view.Entries {
			if entry.Kind == entryKindSystem {
				edits = append(edits, durable.ContextEdit{Target: entry.ID, Action: "omit"})
			}
		}
		added := make([]types.Tool, 0, len(tools))
		added = append(added, tools...)
		baseline := systemEntry(desired, &toolChanges{added: added}, timestamp, edits)
		return []durable.EntryDraft{baseline}
	}
	patches := planSections(replaySections(view.Messages), desired)
	changes := planTools(utils.GetCurrentTools(view.Messages), tools)
	if len(changes.removed) == 0 && len(changes.added) == 0 {
		entries := make([]durable.EntryDraft, 0, len(patches))
		for _, patch := range patches {
			entries = append(entries, systemEntry(patch, nil, timestamp, nil))
		}
		return entries
	}
	if len(patches) == 0 {
		return []durable.EntryDraft{systemEntry(nil, &changes, timestamp, nil)}
	}
	entries := make([]durable.EntryDraft, 0, len(patches))
	for index, patch := range patches {
		if index == len(patches)-1 {
			entries = append(entries, systemEntry(patch, &changes, timestamp, nil))
		} else {
			entries = append(entries, systemEntry(patch, nil, timestamp, nil))
		}
	}
	return entries
}

func planTools(offered []types.Tool, desired []types.Tool) toolChanges {
	wanted := map[string]types.Tool{}
	for _, tool := range desired {
		wanted[tool.Name] = tool
	}
	var kept []types.Tool
	for _, tool := range offered {
		next, ok := wanted[tool.Name]
		if ok && utils.DeclarationsEqual(tool, next) {
			kept = append(kept, tool)
		}
	}
	keptNames := map[string]bool{}
	for _, tool := range kept {
		keptNames[tool.Name] = true
	}
	var added []types.Tool
	for _, tool := range desired {
		if !keptNames[tool.Name] {
			added = append(added, utils.ToToolDeclaration(tool))
		}
	}
	replayed := append(append([]types.Tool{}, kept...), added...)
	orderDiffers := false
	for index := range replayed {
		if index >= len(desired) || replayed[index].Name != desired[index].Name {
			orderDiffers = true
			break
		}
	}
	if orderDiffers {
		removed := make([]types.ToolReference, 0, len(offered))
		for _, tool := range offered {
			removed = append(removed, types.ToolReference{Name: tool.Name})
		}
		addedAll := make([]types.Tool, 0, len(desired))
		for _, tool := range desired {
			addedAll = append(addedAll, utils.ToToolDeclaration(tool))
		}
		return toolChanges{removed: removed, added: addedAll}
	}
	var removed []types.ToolReference
	for _, tool := range offered {
		if !keptNames[tool.Name] {
			removed = append(removed, types.ToolReference{Name: tool.Name})
		}
	}
	return toolChanges{removed: removed, added: added}
}

// planSections returns the minimal patch, or a remove-all/re-add-all pair when
// the order would differ.
func planSections(shown, desired *sectionValues) []*sectionValues {
	var patchedOrder []string
	for _, key := range shown.keys {
		if _, ok := desired.values[key]; ok {
			patchedOrder = append(patchedOrder, key)
		}
	}
	for _, key := range desired.keys {
		if _, ok := shown.values[key]; !ok {
			patchedOrder = append(patchedOrder, key)
		}
	}
	orderDiffers := len(patchedOrder) != len(desired.keys)
	if !orderDiffers {
		for index, key := range patchedOrder {
			if key != desired.keys[index] {
				orderDiffers = true
				break
			}
		}
	}
	if orderDiffers {
		removeAll := newSectionValues()
		for _, key := range shown.keys {
			removeAll.setNull(key)
		}
		reAdd := newSectionValues()
		for _, key := range desired.keys {
			if value := desired.values[key]; value != nil {
				reAdd.set(key, *value)
			}
		}
		return []*sectionValues{removeAll, reAdd}
	}
	patch := newSectionValues()
	for _, key := range shown.keys {
		next, ok := desired.values[key]
		if !ok || next == nil {
			patch.setNull(key)
			continue
		}
		current := shown.values[key]
		if current == nil || *current != *next {
			patch.set(key, *next)
		}
	}
	for _, key := range desired.keys {
		if _, ok := shown.values[key]; !ok {
			if value := desired.values[key]; value != nil {
				patch.set(key, *value)
			}
		}
	}
	if len(patch.keys) == 0 {
		return nil
	}
	return []*sectionValues{patch}
}

func systemEntry(sections *sectionValues, tools *toolChanges, timestamp float64, edits []durable.ContextEdit) durable.EntryDraft {
	message := types.SystemMessage{
		Role:      types.SystemMessageRole,
		Content:   types.SystemContentText(""),
		Timestamp: timestamp,
	}
	if sections != nil {
		message.Sections = types.SystemSections{}
		for _, key := range sections.keys {
			value := sections.values[key]
			message.Sections[key] = value
			message.SectionOrder = append(message.SectionOrder, key)
		}
	}
	if tools != nil {
		message.ToolsRemoved = tools.removed
		message.ToolsAdded = tools.added
	}
	return durable.EntryDraft{
		Kind:  entryKindSystem,
		Model: []types.Message{types.NewSystemMessageVariant(message)},
		Edits: edits,
	}
}

var _ = json.Marshal
