// Tool registry for the embedded SDK.
//
// This file owns the default tool set, activation policy and schema-validated
// execution. The registry registers the accepted read/write/edit/bash tools
// plus the Go grep/find/ls tools, keeps an explicit active set, and surfaces
// model-visible declarations for the next request. Duplicate names and unknown
// activations are rejected; the deny list is applied last. Registration and
// activation never race execution, and hooks run outside the internal lock.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	harnesstools "github.com/minifish-org/pith/packages/agent/harness/tools"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// ToolName is the closed vocabulary of built-in tool names.
type ToolName = string

// Built-in tool names.
const (
	ToolNameRead       ToolName = "read"
	ToolNameBash       ToolName = "bash"
	ToolNamePowerShell ToolName = "powershell"
	ToolNameEdit       ToolName = "edit"
	ToolNameWrite      ToolName = "write"
	ToolNameGrep       ToolName = "grep"
	ToolNameFind       ToolName = "find"
	ToolNameLs         ToolName = "ls"
)

// AllToolNames lists every upstream tool name in declaration order. It includes
// PowerShell for source parity even though the headless registry does not
// register it.
var AllToolNames = []ToolName{
	ToolNameRead,
	ToolNameBash,
	ToolNamePowerShell,
	ToolNameEdit,
	ToolNameWrite,
	ToolNameGrep,
	ToolNameFind,
	ToolNameLs,
}

// ToolsOptions groups the per-tool options used by the constructor helpers.
type ToolsOptions struct {
	Read       *ReadToolOptions
	Bash       *BashToolOptions
	PowerShell *PowerShellToolOptions
	Write      *WriteToolOptions
	Edit       *EditToolOptions
	Grep       *GrepToolOptions
	Find       *FindToolOptions
	Ls         *LsToolOptions
}

// defaultActiveToolNames is the tool set active when no allow list is supplied.
func defaultActiveToolNames() map[string]bool {
	return map[string]bool{
		ToolNameRead:  true,
		ToolNameWrite: true,
		ToolNameEdit:  true,
		ToolNameBash:  true,
	}
}

// builtinToolNames is the set of tools the headless registry registers.
func builtinToolNames() map[string]bool {
	return map[string]bool{
		ToolNameRead:  true,
		ToolNameWrite: true,
		ToolNameEdit:  true,
		ToolNameBash:  true,
		ToolNameGrep:  true,
		ToolNameFind:  true,
		ToolNameLs:    true,
	}
}

// newBuiltinToolDefinitions builds the registered built-ins sharing one
// execution environment so edit/write mutations of the same path serialize.
func newBuiltinToolDefinitions(cwd string) []ToolDefinition {
	env := newToolExecutionEnv(cwd)
	return []ToolDefinition{
		harnessToolToDefinition(harnesstools.CreateReadTool(nil), env),
		harnessToolToDefinition(harnesstools.CreateWriteTool(), env),
		harnessToolToDefinition(harnesstools.CreateEditTool(), env),
		harnessToolToDefinition(harnesstools.CreateBashTool(nil), env),
		CreateGrepToolDefinition(cwd, nil),
		CreateFindToolDefinition(cwd, nil),
		CreateLsToolDefinition(cwd, nil),
	}
}

// ToolRegistry owns the registered tool definitions and the active set.
type ToolRegistry struct {
	hooks ToolHooks

	mu            sync.RWMutex
	cwd           string
	order         []string
	tools         map[string]ToolDefinition
	active        map[string]bool
	builtin       map[string]bool
	deny          map[string]bool
	allow         map[string]bool
	explicitAllow bool
	closables     []func() error
	codemodeStore *CodemodeStore
}

// NewToolRegistry builds a registry rooted at cwd with the supplied custom
// tools, activation policy and hooks. A nil allow list activates the default
// read/write/edit/bash set plus every custom tool; an explicit empty allow list
// activates nothing. The deny list is applied last.
func NewToolRegistry(cwd string, custom []ToolDefinition, allow, deny []string, hooks ToolHooks) (*ToolRegistry, error) {
	registry := &ToolRegistry{
		hooks:   hooks,
		cwd:     cwd,
		tools:   map[string]ToolDefinition{},
		active:  map[string]bool{},
		builtin: builtinToolNames(),
		deny:    toNameSet(deny),
	}
	for _, definition := range newBuiltinToolDefinitions(cwd) {
		if err := registry.registerLocked(definition); err != nil {
			return nil, err
		}
	}
	for _, definition := range custom {
		if err := registry.registerLocked(definition); err != nil {
			return nil, err
		}
	}
	if allow != nil {
		registry.explicitAllow = true
		registry.allow = map[string]bool{}
		for _, name := range allow {
			if _, ok := registry.tools[name]; !ok {
				return nil, fmt.Errorf("unknown tool activation: %s", name)
			}
			registry.allow[name] = true
		}
	}
	registry.applyPolicyLocked(allow)
	return registry, nil
}

func toNameSet(names []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range names {
		set[name] = true
	}
	return set
}

func (r *ToolRegistry) registerLocked(definition ToolDefinition) error {
	name := strings.TrimSpace(definition.Name)
	if name == "" {
		return errors.New("tool name is required")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("duplicate tool registration: %s", name)
	}
	if definition.Execute == nil {
		return fmt.Errorf("tool %s has no execute function", name)
	}
	r.tools[name] = definition
	r.order = append(r.order, name)
	if definition.close != nil {
		r.closables = append(r.closables, definition.close)
	}
	return nil
}

// defaultActiveLocked reports whether a registered tool is active under the
// default policy. Built-in tools follow the built-in default set; custom tools
// are active unless their exposure keeps them out of the model loadout
// (codemode and deferred tools are callable but never auto-activated).
func (r *ToolRegistry) defaultActiveLocked(name string) bool {
	if r.builtin[name] {
		return defaultActiveToolNames()[name]
	}
	switch r.tools[name].exposure() {
	case ExposureCodemode, ExposureDeferred, ExposureHidden:
		return false
	default:
		return true
	}
}

// applyPolicyLocked recomputes the active set from the supplied allow list (nil
// means the default policy) and the deny list.
func (r *ToolRegistry) applyPolicyLocked(allow []string) {
	active := map[string]bool{}
	if allow == nil {
		for _, name := range r.order {
			if r.defaultActiveLocked(name) {
				active[name] = true
			}
		}
	} else {
		for _, name := range allow {
			active[name] = true
		}
	}
	for name := range r.deny {
		delete(active, name)
	}
	r.active = active
}

// Register adds a tool. Duplicate names are rejected. When the registry has no
// explicit allow list the new tool becomes active immediately unless denied or
// its exposure keeps it out of the default loadout; with an explicit allow list
// it is active only when it was allowed.
func (r *ToolRegistry) Register(tool ToolDefinition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.registerLocked(tool); err != nil {
		return err
	}
	name := strings.TrimSpace(tool.Name)
	if r.deny[name] {
		return nil
	}
	if r.explicitAllow {
		if r.allow[name] {
			r.active[name] = true
		}
		return nil
	}
	if r.defaultActiveLocked(name) {
		r.active[name] = true
	}
	return nil
}

// SetActive replaces the active set. A nil slice restores the default policy;
// a non-nil empty slice disables every tool. Unknown names are rejected without
// changing the active set.
func (r *ToolRegistry) SetActive(names []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		if _, ok := r.tools[name]; !ok {
			return fmt.Errorf("unknown tool activation: %s", name)
		}
	}
	active := map[string]bool{}
	if names == nil {
		for _, name := range r.order {
			if r.defaultActiveLocked(name) {
				active[name] = true
			}
		}
	} else {
		for _, name := range names {
			active[name] = true
		}
	}
	for name := range r.deny {
		delete(active, name)
	}
	r.active = active
	return nil
}

// Names returns the active tool names in registration order.
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.active))
	for _, name := range r.order {
		if r.active[name] {
			names = append(names, name)
		}
	}
	return names
}

// Definitions returns the active tool definitions in registration order.
func (r *ToolRegistry) Definitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]ToolDefinition, 0, len(r.active))
	for _, name := range r.order {
		if !r.active[name] {
			continue
		}
		definition := r.tools[name]
		if definition.exposure() == ExposureHidden {
			continue
		}
		definitions = append(definitions, definition)
	}
	return definitions
}

// allDefinitions returns every registered definition in registration order,
// regardless of activation or exposure.
func (r *ToolRegistry) allDefinitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		definitions = append(definitions, r.tools[name])
	}
	return definitions
}

// CloseTools closes per-tool resources such as Codemode sandboxes. It is
// idempotent and safe to call on a registry with no closables.
func (r *ToolRegistry) CloseTools() error {
	r.mu.Lock()
	closables := r.closables
	r.closables = nil
	r.mu.Unlock()
	var firstErr error
	for _, closeFn := range closables {
		if err := closeFn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SetCodemodeStore binds the persisted Codemode store this registry's codemode
// tool reads and writes. A nil store disables persistence.
func (r *ToolRegistry) SetCodemodeStore(store *CodemodeStore) {
	r.mu.Lock()
	r.codemodeStore = store
	r.mu.Unlock()
}

func (r *ToolRegistry) getCodemodeStore() *CodemodeStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.codemodeStore
}

// Hooks returns the configured execution hooks. It lets the nested tool-call
// runner apply the same permission and transformation callbacks as a top-level
// call without duplicating the registry state.
func (r *ToolRegistry) Hooks() ToolHooks {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hooks
}

// ExecuteNested runs a tool that was requested from a nested context (for
// example a Codemode script). Unlike Execute it does not require the tool to be
// model-active, but it still enforces the deny list, schema validation and the
// Before/After hooks.
func (r *ToolRegistry) ExecuteNested(ctx context.Context, call ToolCall) (ToolResult, error) {
	return r.executeTool(ctx, call, false)
}

// executeTool is the shared execution path. When requireActive is true the tool
// must be in the model-active set; a nested call relaxes only that check, never
// the deny list, validation or hooks.
func (r *ToolRegistry) executeTool(ctx context.Context, call ToolCall, requireActive bool) (ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	r.mu.RLock()
	if r.deny[call.Name] {
		r.mu.RUnlock()
		return ToolResult{}, fmt.Errorf("tool %q is denied", call.Name)
	}
	definition, ok := r.tools[call.Name]
	if requireActive && ok && !r.active[call.Name] {
		ok = false
	}
	r.mu.RUnlock()
	if !ok {
		return ToolResult{}, fmt.Errorf("tool %q is not active", call.Name)
	}
	if definition.exposure() == ExposureHidden {
		return ToolResult{}, fmt.Errorf("tool %q is not reachable", call.Name)
	}
	declaration := aitypes.Tool{
		Name:        definition.Name,
		Description: definition.Description,
		Input:       aitypes.JSONSchemaToolInput(definition.Parameters),
	}
	if _, err := aiutils.ValidateToolArguments(declaration, aitypes.ToolCall{
		Id:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	}); err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if err := r.hooks.RunBefore(ctx, call); err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	result, err := definition.Execute(ctx, call.Arguments)
	if err != nil {
		return ToolResult{}, err
	}
	transformed, err := r.hooks.RunAfter(ctx, call, result)
	if err != nil {
		return ToolResult{}, err
	}
	return transformed, nil
}

// Declarations returns the model-visible declarations of the active tools.
// A codemode or deferred tool enters this set only after it is explicitly
// activated (for example by tool search), matching the upstream active set.
func (r *ToolRegistry) Declarations() []aitypes.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	declarations := make([]aitypes.Tool, 0, len(r.active))
	for _, name := range r.order {
		if !r.active[name] {
			continue
		}
		definition := r.tools[name]
		if definition.exposure() == ExposureHidden {
			continue
		}
		declarations = append(declarations, aitypes.Tool{
			Name:        definition.Name,
			Description: definition.Description,
			Input:       aitypes.JSONSchemaToolInput(definition.Parameters),
		})
	}
	return declarations
}

func (r *ToolRegistry) lookupActive(name string) (ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.active[name] {
		return ToolDefinition{}, false
	}
	definition, ok := r.tools[name]
	return definition, ok
}

// hasTool reports whether a tool name is registered, regardless of activation
// or exposure.
func (r *ToolRegistry) hasTool(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// nestedAgentTool builds the agent-runtime view of one registered tool for a
// nested call, preserving structured content. Activation is deliberately not
// required; the caller still enforces the deny list and hooks.
func (r *ToolRegistry) nestedAgentTool(name string) (agenttypes.AgentTool[any, any], bool) {
	r.mu.RLock()
	definition, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return agenttypes.AgentTool[any, any]{}, false
	}
	return agenttypes.AgentTool[any, any]{
		Tool: aitypes.Tool{
			Name:        definition.Name,
			Description: definition.Description,
			Input:       aitypes.JSONSchemaToolInput(definition.Parameters),
		},
		Label:            definition.Name,
		PrepareArguments: func(args any) (any, error) { return prepareRawArguments(args) },
		Execute: func(toolCallID string, params any, signal <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
			raw, err := prepareRawArguments(params)
			if err != nil {
				return agenttypes.AgentToolResult[any]{}, err
			}
			result, err := definition.Execute(contextFromSignal(signal), raw)
			if err != nil {
				return agenttypes.AgentToolResult[any]{}, err
			}
			return agenttypes.AgentToolResult[any]{
				Content:           result.Content,
				Details:           decodeDetails(result.Details),
				StructuredContent: result.StructuredContent,
				IsError:           result.IsError,
			}, nil
		},
	}, true
}

// Execute validates the arguments against the active tool's schema, runs the
// Before hook, executes the tool and runs the After hook. All hooks run outside
// the registry lock.
func (r *ToolRegistry) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	return r.executeTool(ctx, call, true)
}

// AgentTools returns the active tools adapted to the accepted agent runtime.
// Execution routes through Execute so activation checks, schema validation and
// the Before/After hooks run for every model-requested call.
func (r *ToolRegistry) AgentTools() []Tool {
	definitions := r.Definitions()
	tools := make([]Tool, 0, len(definitions))
	for _, definition := range definitions {
		def := definition
		tools = append(tools, agenttypes.AgentTool[any, any]{
			Tool: aitypes.Tool{
				Name:        def.Name,
				Description: def.Description,
				Input:       aitypes.JSONSchemaToolInput(def.Parameters),
			},
			Label:            def.Name,
			PrepareArguments: func(args any) (any, error) { return prepareRawArguments(args) },
			Execute: func(toolCallID string, params any, signal <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
				raw, err := prepareRawArguments(params)
				if err != nil {
					return agenttypes.AgentToolResult[any]{}, err
				}
				result, err := r.Execute(contextFromSignal(signal), ToolCall{ID: toolCallID, Name: def.Name, Arguments: raw})
				if err != nil {
					return agenttypes.AgentToolResult[any]{}, err
				}
				converted := agenttypes.AgentToolResult[any]{
					Content: result.Content,
					Details: decodeDetails(result.Details),
				}
				if result.IsError {
					return converted, errors.New(toolResultText(result))
				}
				return converted, nil
			},
		})
	}
	return tools
}

// SortedNames returns the active names sorted alphabetically. It is a
// convenience for diagnostics and deterministic tests.
func (r *ToolRegistry) SortedNames() []string {
	names := r.Names()
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// tool constructor helpers
// ---------------------------------------------------------------------------

// CreateToolDefinition builds one built-in tool definition by name.
func CreateToolDefinition(toolName ToolName, cwd string, options *ToolsOptions) (ToolDefinition, error) {
	switch toolName {
	case ToolNameRead:
		return CreateReadToolDefinition(cwd, options.pickRead()), nil
	case ToolNameBash:
		return CreateBashToolDefinition(cwd, options.pickBash()), nil
	case ToolNamePowerShell:
		return CreatePowerShellToolDefinition(cwd, options.pickPowerShell()), nil
	case ToolNameEdit:
		return CreateEditToolDefinition(cwd, options.pickEdit()), nil
	case ToolNameWrite:
		return CreateWriteToolDefinition(cwd, options.pickWrite()), nil
	case ToolNameGrep:
		return CreateGrepToolDefinition(cwd, options.pickGrep()), nil
	case ToolNameFind:
		return CreateFindToolDefinition(cwd, options.pickFind()), nil
	case ToolNameLs:
		return CreateLsToolDefinition(cwd, options.pickLs()), nil
	default:
		return ToolDefinition{}, fmt.Errorf("Unknown tool name: %s", toolName)
	}
}

// CreateTool builds one built-in tool as an agent-runtime tool.
func CreateTool(toolName ToolName, cwd string, options *ToolsOptions) (Tool, error) {
	definition, err := CreateToolDefinition(toolName, cwd, options)
	if err != nil {
		return Tool{}, err
	}
	return WrapToolDefinition(definition), nil
}

// CreateCodingToolDefinitions builds the default coding tool set.
func CreateCodingToolDefinitions(cwd string, options *ToolsOptions) ([]ToolDefinition, error) {
	return buildToolDefinitions(cwd, options, []ToolName{ToolNameRead, ToolNameBash, ToolNameEdit, ToolNameWrite})
}

// CreateReadOnlyToolDefinitions builds the read-only tool set.
func CreateReadOnlyToolDefinitions(cwd string, options *ToolsOptions) ([]ToolDefinition, error) {
	return buildToolDefinitions(cwd, options, []ToolName{ToolNameRead, ToolNameGrep, ToolNameFind, ToolNameLs})
}

// CreateAllToolDefinitions builds every registered built-in definition.
func CreateAllToolDefinitions(cwd string, options *ToolsOptions) (map[ToolName]ToolDefinition, error) {
	out := map[ToolName]ToolDefinition{}
	for _, name := range []ToolName{ToolNameRead, ToolNameBash, ToolNameEdit, ToolNameWrite, ToolNameGrep, ToolNameFind, ToolNameLs} {
		definition, err := CreateToolDefinition(name, cwd, options)
		if err != nil {
			return nil, err
		}
		out[name] = definition
	}
	return out, nil
}

// CreateCodingTools builds the default coding tool set as agent-runtime tools.
func CreateCodingTools(cwd string, options *ToolsOptions) ([]Tool, error) {
	return buildTools(cwd, options, []ToolName{ToolNameRead, ToolNameBash, ToolNameEdit, ToolNameWrite})
}

// CreateReadOnlyTools builds the read-only tool set as agent-runtime tools.
func CreateReadOnlyTools(cwd string, options *ToolsOptions) ([]Tool, error) {
	return buildTools(cwd, options, []ToolName{ToolNameRead, ToolNameGrep, ToolNameFind, ToolNameLs})
}

// CreateAllTools builds every registered built-in as an agent-runtime tool.
func CreateAllTools(cwd string, options *ToolsOptions) (map[ToolName]Tool, error) {
	out := map[ToolName]Tool{}
	for _, name := range []ToolName{ToolNameRead, ToolNameBash, ToolNameEdit, ToolNameWrite, ToolNameGrep, ToolNameFind, ToolNameLs} {
		tool, err := CreateTool(name, cwd, options)
		if err != nil {
			return nil, err
		}
		out[name] = tool
	}
	return out, nil
}

func buildToolDefinitions(cwd string, options *ToolsOptions, names []ToolName) ([]ToolDefinition, error) {
	definitions := make([]ToolDefinition, 0, len(names))
	for _, name := range names {
		definition, err := CreateToolDefinition(name, cwd, options)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func buildTools(cwd string, options *ToolsOptions, names []ToolName) ([]Tool, error) {
	definitions, err := buildToolDefinitions(cwd, options, names)
	if err != nil {
		return nil, err
	}
	return WrapToolDefinitions(definitions), nil
}

func (o *ToolsOptions) pickRead() *ReadToolOptions {
	if o == nil {
		return nil
	}
	return o.Read
}

func (o *ToolsOptions) pickBash() *BashToolOptions {
	if o == nil {
		return nil
	}
	return o.Bash
}

func (o *ToolsOptions) pickPowerShell() *PowerShellToolOptions {
	if o == nil {
		return nil
	}
	return o.PowerShell
}

func (o *ToolsOptions) pickWrite() *WriteToolOptions {
	if o == nil {
		return nil
	}
	return o.Write
}

func (o *ToolsOptions) pickEdit() *EditToolOptions {
	if o == nil {
		return nil
	}
	return o.Edit
}

func (o *ToolsOptions) pickGrep() *GrepToolOptions {
	if o == nil {
		return nil
	}
	return o.Grep
}

func (o *ToolsOptions) pickFind() *FindToolOptions {
	if o == nil {
		return nil
	}
	return o.Find
}

func (o *ToolsOptions) pickLs() *LsToolOptions {
	if o == nil {
		return nil
	}
	return o.Ls
}
