package harness

import (
	"context"

	"github.com/minifish-org/pith/packages/durable"
)

// DefineExtension is the identity helper that types an extension.
func DefineExtension(ext Extension) Extension { return ext }

// DefineTool is the identity helper that types a tool registration.
func DefineTool(tool ToolRegistration) ToolRegistration { return tool }

// Section builds a prompt section; tagged unless untagged is true.
func Section(key string, render func(context.Context, PromptInput) (string, error), untagged bool) PromptSection {
	return PromptSection{Key: key, Render: render, Untagged: untagged}
}

// Hook builds hook handlers for a task's name.
func Hook(task durable.TaskDefinition, handlers map[string]HookHandler) HookRegistration {
	return HookRegistration{Task: task.Name, Handlers: handlers}
}

// WrapTool wraps the tool with the given name.
func WrapTool(name string, wrapper func(ToolRegistration) (ToolRegistration, error)) Wrap {
	return Wrap{Tool: name, WrapTool: wrapper}
}

// WrapSection wraps the section with the given key.
func WrapSection(key string, wrapper func(PromptSection) (PromptSection, error)) Wrap {
	return Wrap{Section: key, WrapSection: wrapper}
}

// Configure applies one raw AgentChange patch to a conversation's pi.agent doc
// inside a transaction.
func Configure(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, change []byte) error {
	return configure(ctx, tx, conversationID, change)
}

// DefineTask is the Go counterpart of upstream defineTask.
func DefineTask(def durable.TaskDefinition) durable.TaskDefinition { return durable.DefineTask(def) }
