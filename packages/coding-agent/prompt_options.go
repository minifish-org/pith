// Headless prompt inputs ported from Pi's agent-session.ts at
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	"errors"
	"fmt"
	"os"
	"strings"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// Steer queues text and images for the next turn boundary. Skill
// and template expansion defaults to enabled. StreamingBehavior is only used
// by Prompt; this method always steers.
func (s *AgentSession) Steer(text string, input ...PromptOptions) error {
	options, err := promptOptions(input)
	if err != nil {
		return err
	}
	return s.queuePromptInput(text, options, true)
}

// FollowUp queues text and images when the agent would otherwise
// stop. Skill and template expansion defaults to enabled.
func (s *AgentSession) FollowUp(text string, input ...PromptOptions) error {
	options, err := promptOptions(input)
	if err != nil {
		return err
	}
	return s.queuePromptInput(text, options, false)
}

// One optional options value mirrors Pi's optional prompt options argument.
func promptOptions(input []PromptOptions) (PromptOptions, error) {
	if len(input) > 1 {
		return PromptOptions{}, errors.New("provide at most one PromptOptions value")
	}
	if len(input) == 0 {
		return PromptOptions{}, nil
	}
	return input[0], nil
}

func (s *AgentSession) queuePromptInput(text string, options PromptOptions, steer bool) error {
	if options.StreamingBehavior != "" {
		return errors.New("streaming behavior is only valid with Prompt")
	}
	text, images, err := s.preparePromptInput(text, options)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.agent == nil {
		return ErrAgentSessionClosed
	}
	if s.compacting {
		return ErrAgentSessionBusy
	}
	message := userAgentMessageWithImages(text, images)
	message.QueueID = options.QueueID
	if steer {
		s.agent.Steer(message)
	} else {
		s.agent.FollowUp(message)
	}
	return nil
}

// preparePromptInput takes a detached snapshot, then expands outside the lock.
// It only reads resources already selected by the host; it never executes a
// command or discovers additional paths. Image data strings are immutable.
func (s *AgentSession) preparePromptInput(text string, options PromptOptions) (string, []aitypes.ImageContent, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", nil, ErrAgentSessionClosed
	}
	skills := append([]Skill(nil), s.resources.Skills...)
	templates := append([]PromptTemplate(nil), s.resources.Templates...)
	s.mu.Unlock()
	images := append([]aitypes.ImageContent(nil), options.Images...)
	if options.ExpandPromptTemplates != nil && !*options.ExpandPromptTemplates {
		return text, images, nil
	}
	expanded, err := expandSkillInput(text, skills)
	if err != nil {
		return "", nil, err
	}
	return ExpandPromptTemplate(expanded, templates), images, nil
}

func expandSkillInput(text string, skills []Skill) (string, error) {
	if !strings.HasPrefix(text, "/skill:") {
		return text, nil
	}
	name, args, _ := strings.Cut(text[7:], " ")
	for _, skill := range skills {
		if skill.Name != name {
			continue
		}
		data, err := os.ReadFile(skill.File)
		if err != nil {
			// Unlike the TS extension host, the headless API has no extension
			// error channel. Fail before a request rather than hide the failure.
			return "", fmt.Errorf("expand skill %s: %w", name, err)
		}
		_, body, err := parseFrontmatter(string(stripBOM(data)))
		if err != nil {
			return "", fmt.Errorf("expand skill %s: %w", name, err)
		}
		block := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\nReferences are relative to %s.\n\n%s\n</skill>", skill.Name, skill.File, skill.BaseDir, strings.TrimSpace(body))
		if strings.TrimSpace(args) != "" {
			block += "\n\n" + strings.TrimSpace(args)
		}
		return block, nil
	}
	return text, nil
}
