// Package tools is the Go port of the optional Durable coding tools at Pi
// revision a13d35a742c6ef8462812a28fbe1d8c8b7431c32 (packages/durable/src/
// tools/** and src/truncate.ts).
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The package exposes the four built-in tools (read, write, edit, bash) as
// harness.ToolRegistration values that take their environment from the
// invocation's harness.ToolAPI, stream progress through api.Output and report
// notices as api.Diagnostic entries. Nothing installs the tools automatically:
// an integrator opts in by adding CodingTools to the registry.
//
// # Replay policy
//
// Every built-in declaration keeps the upstream default of unsafe replay. A
// function called read is not evidence that it is replay-safe; a host that
// knows better may install its own registration with Replay == "safe".
//
// # Host/runtime differences
//
// The upstream tools throw JavaScript errors and use @earendil-works/chord
// contexts. The Go tools return ordinary errors and take a standard
// context.Context first; cancellation is checked at the same boundaries. Go has
// no language-level file lock, so edit/write share an in-process mutation queue
// keyed by the environment's namespace id and canonical path; it is not a lock
// against bash or another process. Builtin tools alone do not enforce
// filesystem permissions and make no exactly-once external-effect claim.
package tools

import "github.com/minifish-org/pith/packages/durable/harness"

// CodingTools is the opt-in `coding-tools` extension: read, write, edit and
// bash, in their stable source order. It is explicitly installed by the
// integrator and is never silently added to a harness.
func CodingTools() harness.Extension {
	return harness.Extension{
		Name: "coding-tools",
		Tools: []harness.ToolRegistration{
			CreateReadTool(),
			CreateWriteTool(),
			CreateEditTool(),
			CreateBashTool(),
		},
	}
}
