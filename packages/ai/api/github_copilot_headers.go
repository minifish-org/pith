// This file is a Go port of packages/ai/src/api/github-copilot-headers.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import "github.com/minifish-org/pith/packages/ai/types"

// CopilotInitiator is the value of the Copilot X-Initiator header.
type CopilotInitiator string

// Copilot initiator values.
const (
	CopilotInitiatorUser  CopilotInitiator = "user"
	CopilotInitiatorAgent CopilotInitiator = "agent"
)

// InferCopilotInitiator returns whether a request is user-initiated or
// agent-initiated (e.g. a follow-up after assistant/tool messages).
func InferCopilotInitiator(messages []types.Message) CopilotInitiator {
	if len(messages) == 0 {
		return CopilotInitiatorUser
	}
	last := messages[len(messages)-1]
	if last.Role != types.UserMessageRole {
		return CopilotInitiatorAgent
	}
	return CopilotInitiatorUser
}

// HasCopilotVisionInput reports whether any user or tool-result message carries
// an image, which Copilot requires the Copilot-Vision-Request header for.
func HasCopilotVisionInput(messages []types.Message) bool {
	for _, message := range messages {
		switch message.Role {
		case types.UserMessageRole:
			if message.User != nil && message.User.Content.Structured {
				for _, block := range message.User.Content.Blocks {
					if block.Type == types.ContentTypeImage {
						return true
					}
				}
			}
		case types.ToolResultMessageRole:
			if message.ToolResult != nil {
				for _, block := range message.ToolResult.Content {
					if block.Type == types.ContentTypeImage {
						return true
					}
				}
			}
		}
	}
	return false
}

// BuildCopilotDynamicHeaders builds the Copilot-specific dynamic request
// headers for a conversation.
func BuildCopilotDynamicHeaders(messages []types.Message, hasImages bool) map[string]string {
	headers := map[string]string{
		"X-Initiator":   string(InferCopilotInitiator(messages)),
		"Openai-Intent": "conversation-edits",
	}
	if hasImages {
		headers["Copilot-Vision-Request"] = "true"
	}
	return headers
}
