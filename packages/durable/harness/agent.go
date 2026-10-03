package harness

import (
	"context"
	"encoding/json"

	"github.com/minifish-org/pith/packages/durable"
)

// InstructionsKey is the reserved section key of the agent's instructions.
const InstructionsKey = "instructions"

// AgentDoc is the built-in rewindable agent document.
var AgentDoc, _ = durable.DefineDoc(durable.DocumentDefinition{
	Kind:    "pi.agent",
	Version: 1,
	Scope:   durable.ScopeConversation,
	History: durable.HistoryRewindable,
	Fork:    durable.ForkAsOf,
	Initial: func(json.RawMessage) (durable.JsonObject, error) { return durable.JsonObject{}, nil },
	CheckpointWhen: func(durable.JsonObject, []chordOp, durable.CheckpointInfo) (bool, error) {
		return true, nil
	},
})

func agentAddress(conversationID durable.ConversationID) durable.DocumentAddress {
	return durable.ConversationAddress(AgentDoc, conversationID, nil)
}

// configure applies one raw AgentChange patch to a conversation's agent doc.
func configure(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, change json.RawMessage) error {
	if len(change) == 0 {
		return nil
	}
	draft, err := tx.Doc(ctx, *AgentDoc, agentAddress(conversationID), nil)
	if err != nil {
		return err
	}
	state, err := draft.Value()
	if err != nil {
		return err
	}
	return applyAgentChange(state, change)
}

func applyAgentChange(state map[string]any, change json.RawMessage) error {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(change, &patch); err != nil {
		return err
	}
	if _, ok := patch["model"]; ok {
		if err := setField(state, "model", patch["model"], func(raw json.RawMessage) (any, error) {
			value, err := decodeObject(raw)
			if err != nil {
				return nil, err
			}
			return map[string]any{"provider": value["provider"], "modelId": value["modelId"]}, nil
		}); err != nil {
			return err
		}
	}
	if raw, ok := patch["thinkingLevel"]; ok {
		if err := setField(state, "thinkingLevel", raw, func(r json.RawMessage) (any, error) {
			var value any
			if err := json.Unmarshal(r, &value); err != nil {
				return nil, err
			}
			return value, nil
		}); err != nil {
			return err
		}
	}
	if raw, ok := patch["extensions"]; ok {
		if err := setField(state, "extensions", raw, normalizeSelection); err != nil {
			return err
		}
	}
	if raw, ok := patch["tools"]; ok {
		if err := setField(state, "tools", raw, normalizeSelection); err != nil {
			return err
		}
	}
	if raw, ok := patch["instructions"]; ok {
		if err := setScalar(state, "instructions", raw); err != nil {
			return err
		}
	}
	if raw, ok := patch["cwd"]; ok {
		if err := setScalar(state, "cwd", raw); err != nil {
			return err
		}
	}
	return nil
}

func setScalar(state map[string]any, key string, raw json.RawMessage) error {
	if isNull(raw) {
		delete(state, key)
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	state[key] = value
	return nil
}

func setField(state map[string]any, key string, raw json.RawMessage, convert func(json.RawMessage) (any, error)) error {
	if isNull(raw) {
		delete(state, key)
		return nil
	}
	value, err := convert(raw)
	if err != nil {
		return err
	}
	state[key] = value
	return nil
}

// normalizeSelection converts an agent selection patch into names or a
// {add,remove}/{remove} object.
func normalizeSelection(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case []any:
		return selectionNames(typed), nil
	case map[string]any:
		out := map[string]any{}
		if add, ok := typed["add"]; ok {
			out["add"] = selectionNames(add.([]any))
		}
		if remove, ok := typed["remove"]; ok {
			out["remove"] = selectionNames(remove.([]any))
		}
		return out, nil
	}
	return value, nil
}

func selectionNames(items []any) []any {
	names := make([]any, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			names = append(names, typed)
		case map[string]any:
			if name, ok := typed["name"]; ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// createAgent is the built-in part of every commit that creates or forks a
// conversation.
func createAgent(ctx context.Context, tx durable.Tx, conversation durable.ConversationRecord) error {
	if conversation.Parent != nil {
		return nil
	}
	draft, err := tx.Doc(ctx, *AgentDoc, agentAddress(conversation.ID), nil)
	if err != nil {
		return err
	}
	if conversation.Owner == nil {
		return nil
	}
	ownerDraft, err := tx.Doc(ctx, *AgentDoc, agentAddress(conversation.Owner.ConversationID), nil)
	if err != nil {
		return err
	}
	ownerValue, err := ownerDraft.Value()
	if err != nil {
		return err
	}
	value, err := draft.Value()
	if err != nil {
		return err
	}
	detached, err := decodeObject(mustJSON(ownerValue))
	if err != nil {
		return err
	}
	for key := range value {
		delete(value, key)
	}
	for key, child := range detached {
		value[key] = child
	}
	return nil
}

// resolveAgent resolves an agent from its stored state, a registry snapshot and
// resolved settings.
func resolveAgent(state map[string]any, snapshot RegistrySnapshot, settings Settings, report func(error)) Agent {
	extensions := selectExtensions(state, snapshot, settings)
	composed := map[string]ToolRegistration{}
	var order []string
	for _, ext := range extensions {
		for _, tool := range ext.Tools {
			if _, ok := composed[tool.Declaration.Name]; !ok {
				order = append(order, tool.Declaration.Name)
			}
			composed[tool.Declaration.Name] = tool
		}
	}
	sections := map[string]PromptSection{}
	var sectionOrder []string
	for _, ext := range extensions {
		for _, section := range ext.Sections {
			if _, ok := sections[section.Key]; !ok {
				sectionOrder = append(sectionOrder, section.Key)
			}
			sections[section.Key] = section
		}
	}
	for _, ext := range extensions {
		for _, wrap := range ext.Wraps {
			if wrap.Tool != "" {
				applyToolWrap(composed, order, wrap.Tool, wrap.WrapTool, report)
			} else if wrap.Section != "" {
				applySectionWrap(sections, sectionOrder, wrap.Section, wrap.WrapSection, report)
			}
		}
	}
	tools := selectTools(state, composed, order)
	agentSections := make([]PromptSection, 0, len(sectionOrder)+1)
	for _, key := range sectionOrder {
		if section, ok := sections[key]; ok {
			agentSections = append(agentSections, section)
		}
	}
	instructions, _ := state["instructions"].(string)
	if _, present := state["instructions"]; present {
		agentSections = append(agentSections, PromptSection{Key: InstructionsKey, Render: func(context.Context, PromptInput) (string, error) {
			return instructions, nil
		}})
	}
	agent := Agent{
		ThinkingLevel: "off",
		Extensions:    extensions,
		Tools:         tools,
		Sections:      agentSections,
		Instructions:  instructions,
	}
	if model, ok := state["model"].(map[string]any); ok {
		ref := &ModelRef{}
		if provider, ok := model["provider"].(string); ok {
			ref.Provider = provider
		}
		if modelID, ok := model["modelId"].(string); ok {
			ref.ModelID = modelID
		}
		agent.Model = ref
	}
	if level, ok := state["thinkingLevel"].(string); ok {
		agent.ThinkingLevel = thinkingLevel(level)
	}
	if cwd, ok := state["cwd"].(string); ok {
		agent.CWD = cwd
	}
	return agent
}

func selectTools(state map[string]any, composed map[string]ToolRegistration, order []string) []ToolRegistration {
	filter, ok := state["tools"]
	if !ok {
		tools := make([]ToolRegistration, 0, len(order))
		for _, name := range order {
			tools = append(tools, composed[name])
		}
		return tools
	}
	if names, ok := filter.([]any); ok {
		seen := map[string]bool{}
		var tools []ToolRegistration
		for _, name := range names {
			key, ok := name.(string)
			if !ok || seen[key] {
				continue
			}
			seen[key] = true
			if tool, ok := composed[key]; ok {
				tools = append(tools, tool)
			}
		}
		return tools
	}
	if edit, ok := filter.(map[string]any); ok {
		removed := map[string]bool{}
		if remove, ok := edit["remove"].([]any); ok {
			for _, name := range remove {
				if key, ok := name.(string); ok {
					removed[key] = true
				}
			}
		}
		var tools []ToolRegistration
		for _, name := range order {
			if !removed[name] {
				tools = append(tools, composed[name])
			}
		}
		return tools
	}
	tools := make([]ToolRegistration, 0, len(order))
	for _, name := range order {
		tools = append(tools, composed[name])
	}
	return tools
}

func selectExtensions(state map[string]any, snapshot RegistrySnapshot, settings Settings) []Extension {
	var selected []string
	stored, present := state["extensions"]
	if names, ok := stored.([]any); present && ok {
		for _, name := range names {
			if key, ok := name.(string); ok {
				selected = append(selected, key)
			}
		}
	} else {
		var base []string
		if settings.Extensions != nil {
			for _, ext := range settings.Extensions {
				base = append(base, ext.Name)
			}
		} else {
			for _, ext := range snapshot.Installed() {
				base = append(base, ext.Name)
			}
		}
		removed := map[string]bool{}
		var add []string
		if edit, ok := stored.(map[string]any); ok {
			if remove, ok := edit["remove"].([]any); ok {
				for _, name := range remove {
					if key, ok := name.(string); ok {
						removed[key] = true
					}
				}
			}
			if additions, ok := edit["add"].([]any); ok {
				for _, name := range additions {
					if key, ok := name.(string); ok {
						add = append(add, key)
					}
				}
			}
		}
		selected = append(base, add...)
		filtered := selected[:0]
		for _, name := range selected {
			if !removed[name] {
				filtered = append(filtered, name)
			}
		}
		selected = filtered
	}
	var extensions []Extension
	seen := map[string]bool{}
	for _, name := range selected {
		if seen[name] {
			continue
		}
		seen[name] = true
		if ext, ok := snapshot.Extension(name); ok {
			extensions = append(extensions, ext)
		}
	}
	return extensions
}

func applyToolWrap(composed map[string]ToolRegistration, order []string, target string, wrap func(ToolRegistration) (ToolRegistration, error), report func(error)) {
	item, ok := composed[target]
	if !ok || wrap == nil {
		return
	}
	wrapped, err := wrap(item)
	if err != nil {
		delete(composed, target)
		report(err)
		return
	}
	if wrapped.Declaration.Name != target {
		delete(composed, target)
		report(fmtErrorf("Wrapper renamed %s to %s", target, wrapped.Declaration.Name))
		return
	}
	composed[target] = wrapped
}

func applySectionWrap(sections map[string]PromptSection, order []string, target string, wrap func(PromptSection) (PromptSection, error), report func(error)) {
	item, ok := sections[target]
	if !ok || wrap == nil {
		return
	}
	wrapped, err := wrap(item)
	if err != nil {
		delete(sections, target)
		report(err)
		return
	}
	if wrapped.Key != target {
		delete(sections, target)
		report(fmtErrorf("Wrapper renamed %s to %s", target, wrapped.Key))
		return
	}
	sections[target] = wrapped
}

// addTools appends names a tool round requested to the stored selection.
func addTools(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, added []string) error {
	draft, err := tx.Doc(ctx, *AgentDoc, agentAddress(conversationID), nil)
	if err != nil {
		return err
	}
	state, err := draft.Value()
	if err != nil {
		return err
	}
	tools, ok := state["tools"]
	if !ok {
		return nil
	}
	if names, ok := tools.([]any); ok {
		for _, name := range added {
			found := false
			for _, existing := range names {
				if existing == name {
					found = true
					break
				}
			}
			if !found {
				names = append(names, name)
			}
		}
		state["tools"] = names
		return nil
	}
	if edit, ok := tools.(map[string]any); ok {
		remove, _ := edit["remove"].([]any)
		var kept []any
		for _, name := range remove {
			if !containsString(added, name) {
				kept = append(kept, name)
			}
		}
		if len(kept) != len(remove) {
			edit["remove"] = kept
		}
	}
	return nil
}

func containsString(items []string, value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, item := range items {
		if item == text {
			return true
		}
	}
	return false
}

// agentHooks returns the selected extensions' hook handler maps for a task.
func agentHooks(ctx context.Context, runtime taskRuntime, taskName string) []map[string]HookHandler {
	agent, err := runtime.agent(ctx)
	if err != nil {
		return nil
	}
	var handlers []map[string]HookHandler
	for _, ext := range agent.Extensions {
		for _, hook := range ext.Hooks {
			if hook.Task == taskName {
				handlers = append(handlers, hook.Handlers)
			}
		}
	}
	return handlers
}

func isNull(raw json.RawMessage) bool {
	trimmed := raw
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\t' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	return string(trimmed) == "null"
}
