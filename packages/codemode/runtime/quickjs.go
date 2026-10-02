package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync/atomic"

	"github.com/tetratelabs/wazero/api"
)

// This file ports the QuickJS host ABI used by the frozen quickjs-wasi release
// wrapper (packages/codemode/runtime/testdata/quickjs-wasi-index.js.txt) onto
// wazero. Handles are i32 pointers to heap-allocated JSValue boxes inside the
// WASM linear memory; every qjs_* function that returns a handle transfers a
// reference the caller owns and must release with freeValue.

// jsValue is an opaque pointer to a heap-allocated QuickJS JSValue box.
type jsValue uint32

// hostFunc is a Go callback registered as a QuickJS host function. It is called
// synchronously from inside the VM while guest code executes.
type hostFunc func(v *VM, this jsValue, args []jsValue) (jsValue, error)

// VM is one QuickJS instance (one WASM module instantiation).
type VM struct {
	ctx    context.Context
	engine *Engine
	mod    api.Module
	mem    api.Memory
	funcs  map[string]api.Function

	host map[string]hostFunc

	// execCtx bounds the lifetime of the whole execution (deadline, caller
	// cancel, sandbox close). Tool call arguments get child contexts.
	execCtx context.Context

	interrupt atomic.Bool

	specTools   []ToolSpec
	specGlobals []ToolSpec

	// Prelude function handles, live for the duration of one execution.
	apiH     jsValue
	settleH  jsValue
	runH     jsValue
	stalledH jsValue

	// Bridge state. Only touched from the VM goroutine.
	pending     map[int]*pendingCall
	callRecords []Call
	outputs     []OutputItem
	results     chan toolResult
	done        bool
	doneOK      bool
	doneValue   string
	doneHasVal  bool
	doneWrites  string
	doneErr     *ExecError
}

type pendingCall struct {
	record   int // index into callRecords, -1 for globals
	started  int64
	cancel   context.CancelFunc
	isGlobal bool
}

type toolResult struct {
	id         int
	ok         bool
	payload    string
	hasPayload bool
}

// call invokes an exported WASM function and returns its single result.
func (v *VM) call(name string, args ...uint64) (uint64, error) {
	f := v.funcs[name]
	if f == nil {
		return 0, fmt.Errorf("codemode: missing wasm export %q", name)
	}
	res, err := f.Call(v.ctx, args...)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

func (v *VM) mustCall(name string, args ...uint64) uint64 {
	r, err := v.call(name, args...)
	if err != nil {
		panic(err)
	}
	return r
}

// ---- memory helpers ----

func (v *VM) malloc(size uint32) (uint32, error) {
	r, err := v.call("wasm_malloc", uint64(size))
	if err != nil {
		return 0, err
	}
	return uint32(r), nil
}

func (v *VM) free(ptr uint32) {
	if ptr == 0 {
		return
	}
	_, _ = v.call("wasm_free", uint64(ptr))
}

// writeCString copies s plus a NUL terminator into WASM memory. The caller
// releases the returned pointer with free.
func (v *VM) writeCString(s string) (uint32, error) {
	b := []byte(s)
	ptr, err := v.malloc(uint32(len(b) + 1))
	if err != nil {
		return 0, err
	}
	if ptr == 0 {
		return 0, fmt.Errorf("codemode: wasm_malloc failed")
	}
	buf := make([]byte, len(b)+1)
	copy(buf, b)
	if !v.mem.Write(ptr, buf) {
		return 0, fmt.Errorf("codemode: out of bounds write")
	}
	return ptr, nil
}

func (v *VM) readBytes(ptr, length uint32) ([]byte, bool) {
	return v.mem.Read(ptr, length)
}

func (v *VM) readCString(ptr uint32) (string, bool) {
	if ptr == 0 {
		return "", true
	}
	var out []byte
	for {
		b, ok := v.mem.Read(ptr, 1)
		if !ok {
			return "", false
		}
		if b[0] == 0 {
			break
		}
		out = append(out, b[0])
		ptr++
	}
	return string(out), true
}

func (v *VM) readUint32(ptr uint32) (uint32, bool) {
	b, ok := v.mem.Read(ptr, 4)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b), true
}

func (v *VM) writeUint32(ptr, val uint32) bool {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], val)
	return v.mem.Write(ptr, b[:])
}

// ---- value constructors ----

func (v *VM) newString(s string) (jsValue, error) {
	ptr, err := v.writeCString(s)
	if err != nil {
		return 0, err
	}
	r, err := v.call("qjs_new_string", uint64(ptr), uint64(len(s)))
	v.free(ptr)
	if err != nil {
		return 0, err
	}
	return jsValue(r), nil
}

func (v *VM) newNumber(f float64) (jsValue, error) {
	r, err := v.call("qjs_new_number", api.EncodeF64(f))
	return jsValue(r), err
}

func (v *VM) undefined() (jsValue, error) {
	r, err := v.call("qjs_get_undefined")
	return jsValue(r), err
}

func (v *VM) null() (jsValue, error) {
	r, err := v.call("qjs_get_null")
	return jsValue(r), err
}

func (v *VM) trueV() (jsValue, error) {
	r, err := v.call("qjs_get_true")
	return jsValue(r), err
}

func (v *VM) falseV() (jsValue, error) {
	r, err := v.call("qjs_get_false")
	return jsValue(r), err
}

func (v *VM) getGlobal() (jsValue, error) {
	r, err := v.call("qjs_get_global")
	return jsValue(r), err
}

// ---- value inspection / conversion ----

func (v *VM) isUndefined(h jsValue) bool {
	r, _ := v.call("qjs_is_undefined", uint64(h))
	return r != 0
}

func (v *VM) isException(h jsValue) bool {
	r, _ := v.call("qjs_is_exception", uint64(h))
	return r != 0
}

func (v *VM) toNumber(h jsValue) float64 {
	r, _ := v.call("qjs_get_float64", uint64(h))
	return math.Float64frombits(r)
}

func (v *VM) toBool(h jsValue) bool {
	r, _ := v.call("qjs_get_bool", uint64(h))
	return r != 0
}

// toString reads a value as a JS string (running guest conversions for
// non-strings). WTF-8 decoding collapses lone surrogates to U+FFFD because Go
// strings are UTF-8; see NOTES.md.
func (v *VM) toString(h jsValue) (string, error) {
	lenPtr, err := v.malloc(4)
	if err != nil {
		return "", err
	}
	if lenPtr == 0 {
		return "", fmt.Errorf("codemode: wasm_malloc failed")
	}
	defer v.free(lenPtr)
	cstrPtr, err := v.call("qjs_get_string_len", uint64(h), uint64(lenPtr))
	if err != nil {
		return "", err
	}
	if cstrPtr == 0 {
		return "<null>", nil
	}
	length, ok := v.readUint32(lenPtr)
	if !ok {
		return "", fmt.Errorf("codemode: out of bounds length read")
	}
	b, ok := v.readBytes(uint32(cstrPtr), length)
	if !ok {
		return "", fmt.Errorf("codemode: out of bounds string read")
	}
	out := string(b)
	_, _ = v.call("qjs_free_cstring", cstrPtr)
	return out, nil
}

func (v *VM) dupValue(h jsValue) (jsValue, error) {
	r, err := v.call("qjs_dup_value", uint64(h))
	return jsValue(r), err
}

func (v *VM) freeValue(h jsValue) {
	if h == 0 {
		return
	}
	_, _ = v.call("qjs_free_value", uint64(h))
}

// ---- properties ----

func (v *VM) getPropString(obj jsValue, name string) (jsValue, error) {
	ptr, err := v.writeCString(name)
	if err != nil {
		return 0, err
	}
	r, err := v.call("qjs_get_prop_string", uint64(obj), uint64(ptr))
	v.free(ptr)
	return jsValue(r), err
}

func (v *VM) setPropString(obj jsValue, name string, val jsValue) error {
	ptr, err := v.writeCString(name)
	if err != nil {
		return err
	}
	_, err = v.call("qjs_set_prop_string", uint64(obj), uint64(ptr), uint64(val))
	v.free(ptr)
	return err
}

func (v *VM) getPropUint32(obj jsValue, idx uint32) (jsValue, error) {
	r, err := v.call("qjs_get_prop_uint32", uint64(obj), uint64(idx))
	return jsValue(r), err
}

// ---- functions ----

func (v *VM) registerHostFunction(name string, fn hostFunc) (jsValue, error) {
	if v.host[name] != nil {
		return 0, fmt.Errorf("codemode: host callback %q already registered", name)
	}
	v.host[name] = fn
	ptr, err := v.writeCString(name)
	if err != nil {
		return 0, err
	}
	r, err := v.call("qjs_new_host_function", uint64(ptr), uint64(len(name)), 0)
	v.free(ptr)
	if err != nil {
		return 0, err
	}
	return jsValue(r), nil
}

func (v *VM) callFunc(fn, this jsValue, args ...jsValue) (jsValue, error) {
	argc := len(args)
	argvPtr := uint32(0)
	if argc > 0 {
		p, err := v.malloc(uint32(argc * 4))
		if err != nil {
			return 0, err
		}
		if p == 0 {
			return 0, fmt.Errorf("codemode: wasm_malloc failed")
		}
		argvPtr = p
		for i, a := range args {
			if !v.writeUint32(argvPtr+uint32(i*4), uint32(a)) {
				return 0, fmt.Errorf("codemode: out of bounds argv write")
			}
		}
	}
	r, err := v.call("qjs_call", uint64(fn), uint64(this), uint64(argc), uint64(argvPtr))
	if argvPtr != 0 {
		v.free(argvPtr)
	}
	if err != nil {
		return 0, err
	}
	return jsValue(r), nil
}

// ---- evaluation ----

const evalFlagAsync = 1 << 7

func (v *VM) eval(code, filename string, flags uint32) (jsValue, error) {
	codePtr, err := v.writeCString(code)
	if err != nil {
		return 0, err
	}
	fnPtr, err := v.writeCString(filename)
	if err != nil {
		v.free(codePtr)
		return 0, err
	}
	r, err := v.call("qjs_eval", uint64(codePtr), uint64(len(code)), uint64(fnPtr), uint64(flags))
	v.free(codePtr)
	v.free(fnPtr)
	if err != nil {
		return 0, err
	}
	h := jsValue(r)
	if v.isException(h) {
		v.freeValue(h)
		exc, _ := v.getException()
		return exc, errException
	}
	return h, nil
}

var errException = fmt.Errorf("codemode: javascript exception")

func (v *VM) executePendingJobs() error {
	for {
		r, err := v.call("qjs_is_job_pending")
		if err != nil {
			return err
		}
		if r == 0 {
			return nil
		}
		if _, err := v.call("qjs_execute_pending_job"); err != nil {
			return err
		}
	}
}

// ---- exceptions ----

func (v *VM) getException() (jsValue, error) {
	r, err := v.call("qjs_get_exception")
	return jsValue(r), err
}

// throwError raises a guest Error with the given message, mirroring
// QuickJS.newError + qjs_throw from the release wrapper.
func (v *VM) throwError(msg string) {
	errH, err := v.call("qjs_new_error")
	if err != nil {
		return
	}
	h := jsValue(errH)
	msgH, err := v.newString(msg)
	if err == nil {
		_ = v.setPropString(h, "message", msgH)
		v.freeValue(msgH)
	}
	_, _ = v.call("qjs_throw", uint64(h))
	v.freeValue(h)
}

// exceptionInfo reads name/message/stack from a QuickJS exception value.
func (v *VM) exceptionInfo(exc jsValue) *ExecError {
	info := &ExecError{Kind: "script", Name: "Error"}
	if h, err := v.getPropString(exc, "name"); err == nil {
		if !v.isUndefined(h) {
			if s, err := v.toString(h); err == nil && s != "" {
				info.Name = s
			}
		}
		v.freeValue(h)
	}
	if h, err := v.getPropString(exc, "message"); err == nil {
		if !v.isUndefined(h) {
			if s, err := v.toString(h); err == nil {
				info.Message = s
			}
		}
		v.freeValue(h)
	}
	if h, err := v.getPropString(exc, "stack"); err == nil {
		if !v.isUndefined(h) {
			if s, err := v.toString(h); err == nil {
				info.Stack = s
			}
		}
		v.freeValue(h)
	}
	// Match the release worker's describeException: prefix the frames with
	// "Name: message" so the text reads like a V8 error.
	if info.Stack != "" {
		head := info.Name
		if info.Message != "" {
			head = info.Name + ": " + info.Message
		}
		info.Stack = head + "\n" + strings.TrimRight(info.Stack, " \t\r\n")
	}
	return info
}

// ---- interrupt handling ----

func (v *VM) dispatchHostCall(namePtr, nameLen, thisPtr, argc, argvPtr uint32) uint32 {
	nameBytes, ok := v.mem.Read(namePtr, nameLen)
	if !ok {
		return 0
	}
	name := string(nameBytes)
	fn := v.host[name]
	if fn == nil {
		v.throwError(fmt.Sprintf("Host callback %q is not registered", name))
		return 0
	}
	args := make([]jsValue, 0, argc)
	for i := uint32(0); i < argc; i++ {
		p, ok := v.readUint32(argvPtr + i*4)
		if !ok {
			return 0
		}
		args = append(args, jsValue(p))
	}
	result, err := fn(v, jsValue(thisPtr), args)
	if err != nil {
		v.throwError(err.Error())
		return 0
	}
	dup, err := v.dupValue(result)
	if err != nil {
		return 0
	}
	v.freeValue(result)
	return uint32(dup)
}
