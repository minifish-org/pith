// Command codemode runs a JavaScript snippet inside the native Codemode
// sandbox without a Node.js or JavaScript-engine dependency.
//
// The sandbox embeds the immutable quickjs-wasi WASM release and drives it with
// the pure-Go wazero runtime, so this example builds and runs with
// CGO_ENABLED=0. Every tool is an injected Go function; the example never
// touches the network or a paid provider.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/minifish-org/pith/packages/codemode"
)

const script = `
const note = await tools.read_note({name: "greeting"});
console.log("loaded", note.name);
text(note.message);
store("cached-greeting", note);
return {name: note.name, length: note.message.length};
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "codemode example:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sandbox, err := codemode.NewSandbox(codemode.SandboxOptions{
		Timeout:          5 * time.Second,
		MemoryLimitBytes: 32 << 20,
		Tools: []codemode.Tool{{
			Name:         "read-note",
			Description:  "Read a note by name.",
			InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"message":{"type":"string"}}}`),
			Execute: func(_ context.Context, arguments json.RawMessage) (json.RawMessage, error) {
				var request struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(arguments, &request); err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{
					"name":    request.Name,
					"message": "Hello from the embedded Codemode sandbox.",
				})
			},
		}},
	})
	if err != nil {
		return err
	}
	defer sandbox.Close(context.Background())

	// Declarations render the tool table as TypeScript for an agent prompt.
	declarations, err := codemode.RenderDeclarations(sandbox.Tools())
	if err != nil {
		return err
	}
	fmt.Println("--- declarations ---")
	fmt.Println(declarations)

	result := sandbox.Execute(ctx, script, codemode.ExecuteOptions{
		Store: map[string]json.RawMessage{"seed": json.RawMessage(`1`)},
	})
	if !result.OK {
		return fmt.Errorf("script failed: %+v", result.Error)
	}
	fmt.Println("--- output ---")
	for _, item := range result.Output {
		fmt.Printf("%s: %s\n", item.Type, item.Text)
	}
	fmt.Println("--- calls ---")
	for _, call := range result.Calls {
		fmt.Printf("%s %s (%.1fms)\n", call.Name, call.Status, call.DurationMs)
	}
	fmt.Printf("--- value ---\n%s\n", result.Value)
	if result.StoreWrites != nil {
		fmt.Printf("--- store writes ---\ncached-greeting=%s\n", result.StoreWrites.Set["cached-greeting"])
	}
	return nil
}
