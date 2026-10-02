package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	cruntime "github.com/minifish-org/pith/packages/codemode/runtime"
)

const defaultTimeout = 300 * time.Second

// reservedGlobals may not be shadowed by configured globals.
var reservedGlobals = map[string]bool{
	"tools":      true,
	"ALL_TOOLS":  true,
	"console":    true,
	"text":       true,
	"image":      true,
	"exit":       true,
	"globalThis": true,
	"store":      true,
	"load":       true,
}

// Sandbox runs JavaScript in fresh QuickJS VMs. It is safe for concurrent use;
// Close aborts in-flight executions.
type Sandbox struct {
	mu      sync.Mutex
	closed  bool
	engine  *cruntime.Engine
	tools   []Tool
	globals []Tool
	timeout time.Duration
	memory  uint64
	stack   uint64
	running map[*execution]struct{}
	wg      sync.WaitGroup
}

type execution struct {
	cancel func(error)
}

// NewSandbox validates the tool/global tables and compiles the embedded
// QuickJS module into a reusable wazero engine.
func NewSandbox(options SandboxOptions) (*Sandbox, error) {
	s := &Sandbox{
		timeout: options.Timeout,
		memory:  options.MemoryLimitBytes,
		stack:   options.MaxStackBytes,
		running: map[*execution]struct{}{},
	}
	seenTools := map[string]bool{}
	for _, tool := range options.Tools {
		if seenTools[tool.Name] {
			return nil, fmt.Errorf("Tool %q is already registered", tool.Name)
		}
		seenTools[tool.Name] = true
		s.tools = append(s.tools, tool)
	}
	globalsByName := map[string]bool{}
	namespaces := map[string]bool{}
	for _, g := range options.Globals {
		parts := splitGlobalName(g.Name)
		if len(parts) > 2 {
			return nil, fmt.Errorf("Invalid global name %q", g.Name)
		}
		valid := true
		for _, part := range parts {
			if !isIdentifier(part) {
				valid = false
				break
			}
		}
		if !valid || reservedGlobals[parts[0]] {
			return nil, fmt.Errorf("Invalid global name %q", g.Name)
		}
		if globalsByName[g.Name] {
			return nil, fmt.Errorf("Global %q is already registered", g.Name)
		}
		if len(parts) == 2 {
			namespaces[parts[0]] = true
		}
		globalsByName[g.Name] = true
		s.globals = append(s.globals, g)
	}
	for namespace := range namespaces {
		if globalsByName[namespace] {
			return nil, fmt.Errorf("Global %q conflicts with the namespace %q", namespace, namespace)
		}
	}

	engine, err := cruntime.NewEngine(context.Background(), pageLimitFor(options.MemoryLimitBytes))
	if err != nil {
		return nil, err
	}
	s.engine = engine
	return s, nil
}

func pageLimitFor(memoryLimit uint64) uint32 {
	const page = 65536
	if memoryLimit == 0 {
		return 8192
	}
	pages := memoryLimit / page
	pages = pages*4 + 256
	if pages > 65536 {
		pages = 65536
	}
	if pages < 256 {
		pages = 256
	}
	return uint32(pages)
}

func splitGlobalName(name string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			parts = append(parts, name[start:i])
			start = i + 1
		}
	}
	parts = append(parts, name[start:])
	return parts
}

// RegisterTool adds a tool. It returns an error if the name is already taken.
func (s *Sandbox) RegisterTool(tool Tool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tools {
		if t.Name == tool.Name {
			return fmt.Errorf("Tool %q is already registered", tool.Name)
		}
	}
	s.tools = append(s.tools, tool)
	return nil
}

// UnregisterTool removes a tool, reporting whether it existed.
func (s *Sandbox) UnregisterTool(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tools {
		if t.Name == name {
			s.tools = append(s.tools[:i], s.tools[i+1:]...)
			return true
		}
	}
	return false
}

// Tools returns a copy of the registered tools.
func (s *Sandbox) Tools() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tool, len(s.tools))
	copy(out, s.tools)
	return out
}

// Globals returns a copy of the registered globals.
func (s *Sandbox) Globals() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tool, len(s.globals))
	copy(out, s.globals)
	return out
}

// Execute runs source as an async function body. It never panics for script
// failures; those are reported in Result.Error.
func (s *Sandbox) Execute(ctx context.Context, source string, options ExecuteOptions) Result {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Result{Error: &ExecutionError{Kind: "aborted", Message: "Sandbox is closed"}}
	}
	tools := make([]Tool, len(s.tools))
	copy(tools, s.tools)
	globals := make([]Tool, len(s.globals))
	copy(globals, s.globals)
	defaultTimeout := s.timeout
	memory := s.memory
	stack := s.stack
	s.mu.Unlock()

	parsed, err := ParseCodemodeSource(source)
	if err != nil {
		// The facade accepts an empty script (it returns undefined), matching
		// the sandbox execute contract; only a present-but-invalid options line
		// is an error.
		if strings.TrimSpace(source) == "" {
			parsed = ParsedCodemodeSource{Code: source}
		} else {
			return Result{Error: &ExecutionError{Kind: "script", Name: "CodemodeSourceError", Message: err.Error()}}
		}
	}
	code := parsed.Code

	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout == 0 && parsed.Options.TimeoutMs != nil {
		timeout = time.Duration(*parsed.Options.TimeoutMs) * time.Millisecond
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}

	execCtx, cancel, err := s.beginExecution(ctx, timeout)
	if err != nil {
		return Result{Error: &ExecutionError{Kind: "aborted", Message: err.Error()}}
	}
	defer cancel(nil)

	spec := cruntime.ExecSpec{
		Code:             code,
		Tools:            buildSpecs(tools, globals),
		Store:            serializeStore(options.Store),
		MemoryLimitBytes: memory,
		MaxStackBytes:    stack,
		TimeoutMs:        timeout.Milliseconds(),
	}
	out := s.engine.Execute(execCtx, spec)
	return fromOutcome(out)
}

func (s *Sandbox) beginExecution(ctx context.Context, timeout time.Duration) (context.Context, func(error), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, func(error) {}, fmt.Errorf("Sandbox is closed")
	}
	base := ctx
	var timeoutCancel context.CancelFunc = func() {}
	if timeout > 0 {
		base, timeoutCancel = context.WithTimeout(ctx, timeout)
	}
	execCtx, cancelCause := context.WithCancelCause(base)
	e := &execution{cancel: func(err error) {
		cancelCause(err)
		timeoutCancel()
	}}
	s.running[e] = struct{}{}
	s.wg.Add(1)
	return execCtx, func(err error) {
		s.mu.Lock()
		delete(s.running, e)
		s.mu.Unlock()
		e.cancel(err)
		s.wg.Done()
	}, nil
}

// Close aborts in-flight executions and releases the engine.
func (s *Sandbox) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	executions := make([]*execution, 0, len(s.running))
	for e := range s.running {
		executions = append(executions, e)
	}
	s.mu.Unlock()

	for _, e := range executions {
		e.cancel(errSandboxClosed)
	}
	s.wg.Wait()
	if s.engine != nil {
		return s.engine.Close(ctx)
	}
	return nil
}

type sandboxClosedError struct{}

func (sandboxClosedError) Error() string { return "Sandbox closed" }

var errSandboxClosed = sandboxClosedError{}

func buildSpecs(tools, globals []Tool) []cruntime.ToolSpec {
	specs := make([]cruntime.ToolSpec, 0, len(tools)+len(globals))
	for _, t := range tools {
		specs = append(specs, cruntime.ToolSpec{
			Name:        t.Name,
			JSName:      ToIdentifier(t.Name),
			Description: t.Description,
			Execute:     t.Execute,
		})
	}
	for _, g := range globals {
		specs = append(specs, cruntime.ToolSpec{
			Name:     g.Name,
			Spread:   g.Spread,
			IsGlobal: true,
			Execute:  g.Execute,
		})
	}
	return specs
}

func serializeStore(store map[string]json.RawMessage) map[string]string {
	if store == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(store))
	for key, value := range store {
		if len(value) == 0 {
			continue
		}
		out[key] = string(value)
	}
	return out
}

func fromOutcome(o cruntime.Outcome) Result {
	r := Result{
		OK:     o.OK,
		Value:  o.Value,
		Output: make([]OutputItem, len(o.Output)),
		Calls:  make([]Call, len(o.Calls)),
	}
	for i, item := range o.Output {
		r.Output[i] = OutputItem{Type: item.Type, Text: item.Text, Data: item.Data, MimeType: item.MimeType}
	}
	for i, call := range o.Calls {
		r.Calls[i] = Call{Name: call.Name, Status: call.Status, DurationMs: call.DurationMs}
	}
	if o.StoreWrites != nil {
		r.StoreWrites = &StoreWrites{Set: map[string]json.RawMessage{}, Delete: append([]string{}, o.StoreWrites.Delete...)}
		for k, v := range o.StoreWrites.Set {
			r.StoreWrites.Set[k] = v
		}
	}
	if o.Error != nil {
		r.Error = &ExecutionError{Kind: o.Error.Kind, Name: o.Error.Name, Message: o.Error.Message, Stack: o.Error.Stack}
	}
	return r
}
