package harness

import (
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/minifish-org/pith/packages/durable"
)

var sectionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// BuiltinTasks are the task definitions every registry holds. They are not an
// extension and cannot be removed or replaced.
func BuiltinTasks() []durable.TaskDefinition {
	return []durable.TaskDefinition{GenerationTask(), ToolTask(), CompactionTask()}
}

// registryState is one immutable published registry state.
type registryState struct {
	extensions []Extension
	byName     map[string]Extension
	tasks      []durable.TaskDefinition
	taskByName map[string]durable.TaskDefinition
}

func newRegistryState(extensions []Extension) (*registryState, error) {
	state := &registryState{
		extensions: extensions,
		byName:     map[string]Extension{},
		taskByName: map[string]durable.TaskDefinition{},
	}
	for _, ext := range extensions {
		state.byName[ext.Name] = ext
	}
	for _, task := range BuiltinTasks() {
		state.taskByName[task.Name] = task
		state.tasks = append(state.tasks, task)
	}
	for _, ext := range extensions {
		for _, task := range ext.Tasks {
			if _, ok := state.taskByName[task.Name]; ok {
				return nil, fmt.Errorf("Task %s of extension %s is already installed", task.Name, ext.Name)
			}
			state.taskByName[task.Name] = task
			state.tasks = append(state.tasks, task)
		}
	}
	return state, nil
}

func (s *registryState) Installed() []Extension { return s.extensions }

func (s *registryState) Extension(name string) (Extension, bool) {
	ext, ok := s.byName[name]
	return ext, ok
}

func (s *registryState) Tools() []RegisteredTool {
	var tools []RegisteredTool
	for _, ext := range s.extensions {
		for _, tool := range ext.Tools {
			tools = append(tools, RegisteredTool{Extension: ext, Tool: tool})
		}
	}
	return tools
}

func (s *registryState) Sections() []RegisteredSection {
	var sections []RegisteredSection
	for _, ext := range s.extensions {
		for _, section := range ext.Sections {
			sections = append(sections, RegisteredSection{Extension: ext, Section: section})
		}
	}
	return sections
}

func (s *registryState) Tasks() []durable.TaskDefinition { return s.tasks }

func (s *registryState) Task(name string) (durable.TaskDefinition, bool) {
	task, ok := s.taskByName[name]
	return task, ok
}

// Registry is an application-owned registry of extensions.
type Registry struct {
	mu        sync.Mutex
	current   *registryState
	listeners map[uint64]func()
	nextID    uint64
}

// CreateRegistry creates an application-owned registry holding only the
// built-in tasks.
func CreateRegistry() *Registry {
	state, _ := newRegistryState(nil)
	return &Registry{current: state, listeners: map[uint64]func(){}}
}

// Snapshot returns the current immutable registry state.
func (r *Registry) Snapshot() RegistrySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// Subscribe registers a listener called after every publication.
func (r *Registry) Subscribe(listener func()) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.listeners[id] = listener
	return func() {
		r.mu.Lock()
		delete(r.listeners, id)
		r.mu.Unlock()
	}
}

// Install installs extension, or replaces the installed extension with its name
// in place.
func (r *Registry) Install(ext Extension) error {
	if err := validateExtension(ext); err != nil {
		return err
	}
	r.mu.Lock()
	current := r.current.extensions
	index := -1
	for i, installed := range current {
		if installed.Name == ext.Name {
			index = i
			break
		}
	}
	var next []Extension
	if index < 0 {
		next = append(append([]Extension(nil), current...), ext)
	} else {
		next = append([]Extension(nil), current...)
		next[index] = ext
	}
	return r.publishLocked(next)
}

// Uninstall removes the installed extension with the given name. A later
// install appends.
func (r *Registry) Uninstall(name string) error {
	r.mu.Lock()
	current := r.current.extensions
	found := false
	var next []Extension
	for _, installed := range current {
		if installed.Name == name {
			found = true
			continue
		}
		next = append(next, installed)
	}
	if !found {
		r.mu.Unlock()
		return nil
	}
	return r.publishLocked(next)
}

func (r *Registry) publishLocked(extensions []Extension) error {
	state, err := newRegistryState(extensions)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.current = state
	listeners := make([]func(), 0, len(r.listeners))
	for _, listener := range r.listeners {
		listeners = append(listeners, listener)
	}
	r.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
	return nil
}

func validateExtension(ext Extension) error {
	tools := map[string]bool{}
	for _, tool := range ext.Tools {
		name := tool.Declaration.Name
		if tools[name] {
			return fmt.Errorf("Extension %s has two tools named %s", ext.Name, name)
		}
		tools[name] = true
	}
	sections := map[string]bool{}
	for _, section := range ext.Sections {
		if !sectionKeyPattern.MatchString(section.Key) {
			return fmt.Errorf("Section key %q must match %s", section.Key, sectionKeyPattern.String())
		}
		if section.Key == InstructionsKey {
			return fmt.Errorf("Section key %s is reserved for the agent's instructions", section.Key)
		}
		if sections[section.Key] {
			return fmt.Errorf("Extension %s has two sections with key %s", ext.Name, section.Key)
		}
		sections[section.Key] = true
	}
	return nil
}

var errNotFound = errors.New("not found")
