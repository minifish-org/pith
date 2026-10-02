package runtime

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// quickJSWasmBase64 is the immutable quickjs-wasi 3.6.2 release artifact,
// shipped as base64 text and decoded at runtime. It is covered by
// LICENSES/quickjs-wasi.txt (MIT).
//
//go:embed quickjs.wasm.base64.txt
var quickJSWasmBase64 string

var (
	wasmBytesOnce sync.Once
	wasmBytesVal  []byte
	wasmBytesErr  error
)

// QuickJSWasmBytes returns the decoded quickjs-wasi WASM module bytes.
func QuickJSWasmBytes() ([]byte, error) {
	wasmBytesOnce.Do(func() {
		wasmBytesVal, wasmBytesErr = base64.StdEncoding.DecodeString(strings.TrimSpace(quickJSWasmBase64))
	})
	return wasmBytesVal, wasmBytesErr
}

// Engine owns a wazero runtime with the compiled QuickJS module and the host
// (env/WASI) imports. A fresh VM module is instantiated per execution.
type Engine struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule

	seq       atomic.Uint64
	instances sync.Map // module name -> *VM

	closed atomic.Bool
}

// NewEngine compiles the embedded QuickJS module. pageLimit caps the WASM
// linear memory (in 64 KiB pages) so a runaway guest cannot exhaust the host.
func NewEngine(ctx context.Context, pageLimit uint32) (*Engine, error) {
	bytes, err := QuickJSWasmBytes()
	if err != nil {
		return nil, fmt.Errorf("codemode: decode quickjs wasm: %w", err)
	}
	if pageLimit == 0 {
		pageLimit = 65536
	}
	cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(pageLimit)
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	e := &Engine{runtime: r}

	if _, err := r.NewHostModuleBuilder("env").
		NewFunctionBuilder().WithFunc(e.hostCall).Export("host_call").
		NewFunctionBuilder().WithFunc(e.hostInterrupt).Export("host_interrupt").
		NewFunctionBuilder().WithFunc(e.hostPromiseRejection).Export("host_promise_rejection").
		NewFunctionBuilder().WithFunc(e.hostModuleNormalize).Export("host_module_normalize").
		NewFunctionBuilder().WithFunc(e.hostModuleLoad).Export("host_module_load").
		NewFunctionBuilder().WithFunc(e.hostGetTimezoneOffset).Export("host_get_timezone_offset").
		Instantiate(ctx); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("codemode: instantiate env imports: %w", err)
	}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("codemode: instantiate wasi: %w", err)
	}
	cm, err := r.CompileModule(ctx, bytes)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("codemode: compile quickjs: %w", err)
	}
	e.compiled = cm
	return e, nil
}

// Close releases the wazero runtime. In-flight VMs are not drained here; the
// caller closes their execution contexts first.
func (e *Engine) Close(ctx context.Context) error {
	if e.closed.Swap(true) {
		return nil
	}
	return e.runtime.Close(ctx)
}

// NewVM instantiates a fresh QuickJS VM for one execution.
func (e *Engine) NewVM(ctx context.Context) (*VM, error) {
	if e.closed.Load() {
		return nil, fmt.Errorf("codemode: engine closed")
	}
	name := fmt.Sprintf("codemode-%d", e.seq.Add(1))
	v := &VM{
		ctx:     ctx,
		engine:  e,
		funcs:   map[string]api.Function{},
		host:    map[string]hostFunc{},
		pending: map[int]*pendingCall{},
		results: make(chan toolResult, 1024),
	}
	mod, err := e.runtime.InstantiateModule(ctx, e.compiled, wazero.NewModuleConfig().
		WithName(name).
		WithStartFunctions().
		WithStdout(io.Discard).
		WithStderr(io.Discard))
	if err != nil {
		return nil, fmt.Errorf("codemode: instantiate quickjs: %w", err)
	}
	v.mod = mod
	v.mem = mod.Memory()
	for _, fn := range []string{
		"qjs_init", "qjs_set_memory_limit", "qjs_set_max_stack_size", "qjs_set_interrupt_handler",
		"qjs_eval", "qjs_is_exception", "qjs_get_exception", "qjs_throw", "qjs_new_error",
		"qjs_new_string", "qjs_new_number", "qjs_get_undefined", "qjs_get_null", "qjs_get_true", "qjs_get_false",
		"qjs_is_undefined", "qjs_get_float64", "qjs_get_bool", "qjs_get_string_len", "qjs_free_cstring",
		"qjs_dup_value", "qjs_free_value", "qjs_get_prop_string", "qjs_set_prop_string", "qjs_get_prop_uint32",
		"qjs_new_host_function", "qjs_call", "qjs_is_job_pending", "qjs_execute_pending_job", "qjs_get_global",
		"wasm_malloc", "wasm_free",
	} {
		f := mod.ExportedFunction(fn)
		if f == nil {
			_ = v.dispose(ctx)
			return nil, fmt.Errorf("codemode: missing wasm export %q", fn)
		}
		v.funcs[fn] = f
	}
	e.instances.Store(name, v)
	return v, nil
}

func (v *VM) dispose(ctx context.Context) error {
	if v.mod == nil {
		return nil
	}
	name := v.mod.Name()
	v.engine.instances.Delete(name)
	return v.mod.Close(ctx)
}

// ---- host imports ----

func (e *Engine) vmFor(m api.Module) *VM {
	if x, ok := e.instances.Load(m.Name()); ok {
		return x.(*VM)
	}
	return nil
}

func (e *Engine) hostCall(ctx context.Context, m api.Module, namePtr, nameLen, thisPtr, argc, argvPtr uint32) uint32 {
	v := e.vmFor(m)
	if v == nil {
		return 0
	}
	return v.dispatchHostCall(namePtr, nameLen, thisPtr, argc, argvPtr)
}

func (e *Engine) hostInterrupt(ctx context.Context, m api.Module) uint32 {
	v := e.vmFor(m)
	if v != nil && v.interrupt.Load() {
		return 1
	}
	return 0
}

func (e *Engine) hostPromiseRejection(ctx context.Context, m api.Module, promisePtr, reasonPtr, isHandled uint32) {
	v := e.vmFor(m)
	if v == nil {
		return
	}
	// No unhandled-rejection handler is enabled; release the heap values the
	// trampoline handed us.
	v.freeValue(jsValue(promisePtr))
	v.freeValue(jsValue(reasonPtr))
}

func (e *Engine) hostModuleNormalize(ctx context.Context, m api.Module, baseNamePtr, namePtr uint32) uint32 {
	return 0
}

func (e *Engine) hostModuleLoad(ctx context.Context, m api.Module, namePtr, outLenPtr uint32) uint32 {
	return 0
}

func (e *Engine) hostGetTimezoneOffset(ctx context.Context, m api.Module, hi, lo uint32) uint32 {
	return 0
}
