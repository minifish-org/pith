package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// OutputItem is one item of script output. The frozen wire format is a union:
// text items carry `text`, image items carry `data` and `mimeType`.
type OutputItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// Call records one tool invocation (globals are not recorded).
type Call struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	DurationMs float64 `json:"durationMs"`
}

// StoreWrites holds the keys a successful script changed with store().
type StoreWrites struct {
	Set    map[string]json.RawMessage `json:"set"`
	Delete []string                   `json:"delete"`
}

// ExecError mirrors the frozen CodemodeError shape.
type ExecError struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

// Outcome is the runtime-level result of one execution.
type Outcome struct {
	OK          bool
	Value       json.RawMessage
	Output      []OutputItem
	Calls       []Call
	StoreWrites *StoreWrites
	Error       *ExecError
}

// ToolSpec describes one callable tool or global handed to the runtime.
type ToolSpec struct {
	Name        string
	JSName      string
	Description string
	Spread      bool
	IsGlobal    bool
	Execute     func(context.Context, json.RawMessage) (json.RawMessage, error)
}

// ExecSpec is one script execution request.
type ExecSpec struct {
	Code             string
	Tools            []ToolSpec
	Store            map[string]string
	MemoryLimitBytes uint64
	MaxStackBytes    uint64
	TimeoutMs        int64 // for the timeout diagnostic; <= 0 means unbounded
}

// Execute runs one script in a fresh VM. It never panics for script failures;
// those are reported in Outcome.Error.
func (e *Engine) Execute(ctx context.Context, spec ExecSpec) Outcome {
	if err := ctx.Err(); err != nil {
		return Outcome{Error: ctxError(ctx)}
	}
	vm, err := e.NewVM(ctx)
	if err != nil {
		return Outcome{Error: &ExecError{Kind: "sandbox", Message: err.Error()}}
	}
	return vm.run(spec)
}

func ctxError(ctx context.Context) *ExecError {
	if ctx.Err() == context.DeadlineExceeded {
		return &ExecError{Kind: "timeout", Message: "Execution timed out"}
	}
	msg := "Execution aborted"
	if cause := context.Cause(ctx); cause != nil && cause != context.Canceled {
		msg = cause.Error()
	}
	return &ExecError{Kind: "aborted", Message: msg}
}

func (v *VM) run(spec ExecSpec) Outcome {
	v.specTools = nil
	v.specGlobals = nil
	for _, t := range spec.Tools {
		if t.IsGlobal {
			v.specGlobals = append(v.specGlobals, t)
		} else {
			v.specTools = append(v.specTools, t)
		}
	}
	if _, err := v.call("qjs_init"); err != nil {
		return v.crash(err)
	}
	if spec.MemoryLimitBytes > 0 {
		if _, err := v.call("qjs_set_memory_limit", spec.MemoryLimitBytes); err != nil {
			return v.crash(err)
		}
	}
	maxStack := spec.MaxStackBytes
	if maxStack == 0 {
		maxStack = 512 * 1024
	}
	if _, err := v.call("qjs_set_max_stack_size", maxStack); err != nil {
		return v.crash(err)
	}
	if _, err := v.call("qjs_set_interrupt_handler", 1); err != nil {
		return v.crash(err)
	}

	// Watch the execution context so a spinning CPU loop is interrupted.
	stop := make(chan struct{})
	go func() {
		select {
		case <-v.ctx.Done():
			v.interrupt.Store(true)
		case <-stop:
		}
	}()
	defer close(stop)
	defer func() { _ = v.dispose(context.Background()) }()

	bridgeH, err := v.registerHostFunction("bridge", bridgeFunc)
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(bridgeH)

	preludeFn, err := v.eval(preludeSource, "codemode-prelude.js", 0)
	if err != nil {
		if err == errException {
			info := v.exceptionInfo(preludeFn)
			v.freeValue(preludeFn)
			info.Kind = "sandbox"
			return v.finishOutcome(info)
		}
		if preludeFn != 0 {
			v.freeValue(preludeFn)
		}
		return v.crash(fmt.Errorf("prelude failed to evaluate: %w", err))
	}
	defer v.freeValue(preludeFn)

	toolsJSON := marshalTools(spec.Tools, false)
	globalsJSON := marshalTools(spec.Tools, true)
	storeJSON := marshalStore(spec.Store)

	undef, err := v.undefined()
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(undef)
	bridgeArg, err := v.dupValue(bridgeH)
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(bridgeArg)
	toolsStr, err := newStringHandle(v, toolsJSON)
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(toolsStr)
	globalsStr, err := newStringHandle(v, globalsJSON)
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(globalsStr)
	storeStr, err := newStringHandle(v, storeJSON)
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(storeStr)

	apiH, err := v.callFunc(preludeFn, undef, bridgeArg, toolsStr, globalsStr, storeStr)
	if err != nil {
		return v.ctxOrCrash(err)
	}
	if v.isException(apiH) {
		info := v.exceptionInfo(apiH)
		v.freeValue(apiH)
		info.Kind = "sandbox"
		return v.finishOutcome(info)
	}
	v.apiH = apiH
	defer v.freeValue(apiH)

	v.settleH, err = v.getPropString(apiH, "settle")
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(v.settleH)
	v.runH, err = v.getPropString(apiH, "run")
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(v.runH)
	v.stalledH, err = v.getPropString(apiH, "stalled")
	if err != nil {
		return v.crash(err)
	}
	defer v.freeValue(v.stalledH)

	// The prefix shares the first line with the script so line numbers match.
	fnH, err := v.eval("(async (tools, console) => {"+spec.Code+"\n})", "codemode.js", 0)
	if err != nil {
		if err == errException {
			info := v.exceptionInfo(fnH)
			v.freeValue(fnH)
			return v.finishOutcome(info)
		}
		if fnH != 0 {
			v.freeValue(fnH)
		}
		return v.crash(err)
	}
	defer v.freeValue(fnH)

	if r, err := v.callFunc(v.runH, apiH, fnH); err != nil {
		return v.ctxOrCrash(err)
	} else {
		v.freeValue(r)
	}

	v.loop(apiH)
	// Unawaited (and still-pending) tool calls are cancelled when the script
	// settles, matching the release host's finish().
	v.finalizePending()
	return v.finishOutcome(nil)
}

func newStringHandle(v *VM, s string) (jsValue, error) {
	return v.newString(s)
}

func (v *VM) loop(apiH jsValue) {
	for {
		if err := v.executePendingJobs(); err != nil {
			if v.done {
				return
			}
			v.setCtxOrSandbox(err)
			return
		}
		if v.done {
			return
		}
		if v.ctx.Err() != nil {
			v.finalizePending()
			return
		}
		if v.deliverReady() {
			continue
		}
		if len(v.pending) > 0 {
			select {
			case res := <-v.results:
				if err := v.complete(res); err != nil {
					v.setCtxOrSandbox(err)
					return
				}
				continue
			case <-v.ctx.Done():
				v.finalizePending()
				return
			}
		}
		if err := v.callStalled(apiH); err != nil {
			if v.done {
				return
			}
			v.setCtxOrSandbox(err)
			return
		}
		if v.done {
			return
		}
		// Nothing pending and the prelude did not report a stall; treat the
		// execution as stalled rather than spinning forever.
		v.done = true
		v.doneErr = &ExecError{
			Kind:    "script",
			Name:    "Error",
			Message: "The script is waiting on a promise that can never settle: no tool call is pending, and timers do not exist here.",
		}
		return
	}
}

func (v *VM) deliverReady() bool {
	select {
	case res := <-v.results:
		if err := v.complete(res); err != nil {
			v.setCtxOrSandbox(err)
		}
		return true
	default:
		return false
	}
}

func (v *VM) complete(res toolResult) error {
	p := v.pending[res.id]
	if p == nil {
		return nil
	}
	delete(v.pending, res.id)
	if p.record >= 0 {
		c := &v.callRecords[p.record]
		if res.ok {
			c.Status = "ok"
		} else {
			c.Status = "error"
		}
		c.DurationMs = msSince(p.started)
	}
	p.cancel()
	return v.callSettle(res.id, res.ok, res.payload, res.hasPayload)
}

func (v *VM) finalizePending() {
	for id, p := range v.pending {
		if p.record >= 0 {
			c := &v.callRecords[p.record]
			c.Status = "cancelled"
			c.DurationMs = msSince(p.started)
		}
		p.cancel()
		delete(v.pending, id)
	}
}

func (v *VM) callStalled(apiH jsValue) error {
	res, err := v.callFunc(v.stalledH, apiH)
	if err != nil {
		return err
	}
	exc := v.isException(res)
	v.freeValue(res)
	if exc {
		return errException
	}
	return nil
}

func (v *VM) callSettle(id int, ok bool, payload string, hasPayload bool) error {
	idH, err := v.newNumber(float64(id))
	if err != nil {
		return err
	}
	defer v.freeValue(idH)
	okH, err := v.trueV()
	if !ok {
		okH, err = v.falseV()
	}
	if err != nil {
		return err
	}
	defer v.freeValue(okH)
	var pH jsValue
	if hasPayload {
		pH, err = v.newString(payload)
		if err != nil {
			return err
		}
	} else {
		pH, err = v.undefined()
		if err != nil {
			return err
		}
	}
	defer v.freeValue(pH)
	res, err := v.callFunc(v.settleH, v.apiH, idH, okH, pH)
	if err != nil {
		return err
	}
	exc := v.isException(res)
	v.freeValue(res)
	if exc {
		return errException
	}
	return nil
}

// bridgeFunc implements the prelude's `bridge(kind, a, b, c)` host callback.
func bridgeFunc(v *VM, _ jsValue, args []jsValue) (jsValue, error) {
	if len(args) == 0 {
		return v.undefined()
	}
	kind, err := v.toString(args[0])
	if err != nil {
		return 0, err
	}
	switch kind {
	case "call", "global":
		if len(args) < 4 {
			return v.undefined()
		}
		id := int(v.toNumber(args[1]))
		name, err := v.toString(args[2])
		if err != nil {
			return 0, err
		}
		hasArgs := !v.isUndefined(args[3])
		argsJSON := ""
		if hasArgs {
			argsJSON, err = v.toString(args[3])
			if err != nil {
				return 0, err
			}
		}
		v.startCall(kind == "call", id, name, argsJSON, hasArgs)
	case "output":
		if len(args) < 3 {
			return v.undefined()
		}
		sub, err := v.toString(args[1])
		if err != nil {
			return 0, err
		}
		if sub == "image" {
			data, err := v.toString(args[2])
			if err != nil {
				return 0, err
			}
			mime := ""
			if len(args) > 3 {
				mime, err = v.toString(args[3])
				if err != nil {
					return 0, err
				}
			}
			v.outputs = append(v.outputs, OutputItem{Type: "image", Data: data, MimeType: mime})
		} else {
			text, err := v.toString(args[2])
			if err != nil {
				return 0, err
			}
			v.outputs = append(v.outputs, OutputItem{Type: "text", Text: text})
		}
	case "done":
		if len(args) < 2 {
			return v.undefined()
		}
		v.done = true
		if v.toBool(args[1]) {
			v.doneOK = true
			if len(args) > 2 && !v.isUndefined(args[2]) {
				val, err := v.toString(args[2])
				if err != nil {
					return 0, err
				}
				v.doneValue = val
				v.doneHasVal = true
			}
			if len(args) > 3 && !v.isUndefined(args[3]) {
				w, err := v.toString(args[3])
				if err != nil {
					return 0, err
				}
				v.doneWrites = w
			}
		} else {
			v.doneOK = false
			v.doneErr = parseScriptError(v, args[2])
			if ctxErr := v.ctx.Err(); ctxErr != nil {
				v.doneErr = ctxError(v.ctx)
			}
		}
	}
	return v.undefined()
}

func (v *VM) startCall(isTool bool, id int, name, argsJSON string, hasArgs bool) {
	var tool *ToolSpec
	if isTool {
		tool = v.lookup(v.specTools, name)
	} else {
		tool = v.lookup(v.specGlobals, name)
	}
	record := -1
	if isTool {
		v.callRecords = append(v.callRecords, Call{Name: name, Status: "cancelled"})
		record = len(v.callRecords) - 1
	}
	callCtx, cancel := context.WithCancel(v.ctx)
	v.pending[id] = &pendingCall{record: record, started: time.Now().UnixNano(), cancel: cancel, isGlobal: !isTool}
	go func() {
		ok := false
		payload := ""
		hasPayload := false
		if tool == nil {
			kind := "tool"
			if !isTool {
				kind = "global"
			}
			payload = fmt.Sprintf("Unknown %s %q", kind, name)
		} else {
			var raw json.RawMessage
			if hasArgs {
				raw = json.RawMessage(argsJSON)
			}
			out, err := tool.Execute(callCtx, raw)
			if err != nil {
				payload = err.Error()
				hasPayload = true
			} else {
				ok = true
				if out != nil {
					payload = string(out)
					hasPayload = true
				}
			}
		}
		select {
		case v.results <- toolResult{id: id, ok: ok, payload: payload, hasPayload: hasPayload}:
		default:
		}
	}()
}

func (v *VM) lookup(list []ToolSpec, name string) *ToolSpec {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func (v *VM) finishOutcome(scriptErr *ExecError) Outcome {
	if scriptErr == nil {
		scriptErr = v.doneErr
	}
	if scriptErr == nil && v.ctx.Err() != nil {
		scriptErr = ctxError(v.ctx)
	}
	if scriptErr == nil && !v.done {
		scriptErr = &ExecError{Kind: "sandbox", Message: "execution ended without a result"}
	}
	calls := make([]Call, len(v.callRecords))
	copy(calls, v.callRecords)
	output := make([]OutputItem, len(v.outputs))
	copy(output, v.outputs)
	if scriptErr == nil && v.doneOK {
		o := Outcome{OK: true, Output: output, Calls: calls}
		if v.doneHasVal {
			o.Value = json.RawMessage(v.doneValue)
		}
		o.StoreWrites = parseStoreWrites(v.doneWrites)
		return o
	}
	if scriptErr == nil {
		scriptErr = &ExecError{Kind: "sandbox", Message: "execution ended without a result"}
	}
	return Outcome{OK: false, Output: output, Calls: calls, Error: scriptErr}
}

func (v *VM) setCtxOrSandbox(err error) {
	if ctxErr := v.ctx.Err(); ctxErr != nil {
		v.doneErr = ctxError(v.ctx)
		return
	}
	v.doneErr = &ExecError{Kind: "sandbox", Message: err.Error()}
}

func (v *VM) ctxOrCrash(err error) Outcome {
	if ctxErr := v.ctx.Err(); ctxErr != nil {
		v.doneErr = ctxError(v.ctx)
	} else {
		v.doneErr = &ExecError{Kind: "sandbox", Message: err.Error()}
	}
	return v.finishOutcome(nil)
}

func (v *VM) crash(err error) Outcome {
	return Outcome{Error: &ExecError{Kind: "sandbox", Message: err.Error()}}
}

func parseScriptError(v *VM, raw jsValue) *ExecError {
	info := &ExecError{Kind: "script"}
	if raw == 0 || v.isUndefined(raw) {
		info.Name = "Error"
		return info
	}
	s, err := v.toString(raw)
	if err != nil {
		info.Name = "Error"
		info.Message = err.Error()
		return info
	}
	var parsed struct {
		Name    string `json:"name"`
		Message string `json:"message"`
		Stack   string `json:"stack"`
	}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		info.Name = "Error"
		info.Message = s
		return info
	}
	info.Name = parsed.Name
	info.Message = parsed.Message
	info.Stack = parsed.Stack
	if info.Name == "" {
		info.Name = "Error"
	}
	return info
}

func parseStoreWrites(raw string) *StoreWrites {
	writes := &StoreWrites{Set: map[string]json.RawMessage{}, Delete: []string{}}
	if raw == "" {
		return writes
	}
	var entries [][]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return writes
	}
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		var key string
		if err := json.Unmarshal(entry[0], &key); err != nil {
			continue
		}
		if len(entry) < 2 {
			writes.Delete = append(writes.Delete, key)
			continue
		}
		// The prelude stores JSON text as a JSON string; unwrap it, mirroring
		// the release host's JSON.parse(value).
		var inner string
		if err := json.Unmarshal(entry[1], &inner); err == nil {
			writes.Set[key] = json.RawMessage(inner)
		} else {
			writes.Set[key] = entry[1]
		}
	}
	return writes
}

func msSince(unixNano int64) float64 {
	return float64(time.Since(time.Unix(0, unixNano)).Nanoseconds()) / 1e6
}

// ---- JSON marshalling ----

type wireTool struct {
	Name        string `json:"name"`
	JSName      string `json:"jsName"`
	Description string `json:"description"`
}

type wireGlobal struct {
	Name   string `json:"name"`
	Spread bool   `json:"spread"`
}

func marshalTools(specs []ToolSpec, globals bool) string {
	if globals {
		list := make([]wireGlobal, 0, len(specs))
		for _, s := range specs {
			if !s.IsGlobal {
				continue
			}
			list = append(list, wireGlobal{Name: s.Name, Spread: s.Spread})
		}
		b, _ := json.Marshal(list)
		return string(b)
	}
	list := make([]wireTool, 0, len(specs))
	for _, s := range specs {
		if s.IsGlobal {
			continue
		}
		list = append(list, wireTool{Name: s.Name, JSName: s.JSName, Description: s.Description})
	}
	b, _ := json.Marshal(list)
	return string(b)
}

func marshalStore(store map[string]string) string {
	if store == nil {
		store = map[string]string{}
	}
	b, _ := json.Marshal(store)
	return string(b)
}
