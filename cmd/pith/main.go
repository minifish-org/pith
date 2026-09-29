// Command pith is the native agent CLI.
//
// This file is a Go port of the assembled agent entry point of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// pith runs one non-interactive agent turn loop against an OpenAI-compatible
// Chat Completions endpoint using the Go SDK and the built-in read/write/edit/
// bash tools. It has no Node, npm or TypeScript runtime dependency: the binary
// is self-contained. Missing model, base URL or API key fails cleanly with a
// nonzero exit status.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/minifish-org/pith/packages/agent/sdk"
)

// Environment variable consulted when --api-key is not supplied.
const apiKeyEnvVar = "PITH_API_KEY"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pith:", err)
		os.Exit(1)
	}
}

// run parses the command line, validates the configuration and drives one SDK
// run. It is separated from main so tests can exercise flag handling and
// validation without a subprocess.
func run(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("pith", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	prompt := fs.String("prompt", "", "user prompt for this run")
	baseURL := fs.String("base-url", "", "OpenAI-compatible API base URL (for example https://host/v1)")
	model := fs.String("model", "", "model identifier")
	apiKey := fs.String("api-key", "", "API key (falls back to "+apiKeyEnvVar+")")
	cwd := fs.String("cwd", "", "working directory for file and shell tools")
	session := fs.String("session", "", "JSON-lines file used to persist and reload the conversation")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stdout)
			return nil
		}
		return err
	}

	if *model == "" {
		return fmt.Errorf("missing required flag --model")
	}
	if *baseURL == "" {
		return fmt.Errorf("missing required flag --base-url")
	}
	resolvedKey := *apiKey
	if resolvedKey == "" {
		resolvedKey = os.Getenv(apiKeyEnvVar)
	}
	if resolvedKey == "" {
		return fmt.Errorf("missing API key: pass --api-key or set %s", apiKeyEnvVar)
	}

	resolvedCwd := *cwd
	if resolvedCwd == "" {
		workingDir, err := os.Getwd()
		if err != nil {
			return err
		}
		resolvedCwd = workingDir
	}

	return sdk.Run(sdk.RunOptions{
		Context:     context.Background(),
		BaseURL:     *baseURL,
		Model:       *model,
		APIKey:      resolvedKey,
		Cwd:         resolvedCwd,
		SessionPath: *session,
		Prompt:      *prompt,
		Stdout:      stdout,
		Stderr:      stderr,
	})
}

func printUsage(writer io.Writer) {
	fmt.Fprint(writer, `Usage: pith [flags]

Run one agent turn loop against an OpenAI-compatible Chat Completions service
using the native Go SDK and the built-in read, write, edit and bash tools.

Flags:
  --prompt <text>      User prompt for this run.
  --base-url <url>     API base URL, for example https://host/v1.
  --model <id>         Model identifier sent on the wire.
  --api-key <key>      API key. Falls back to the PITH_API_KEY environment
                       variable. Never printed.
  --cwd <dir>          Working directory for the file and shell tools.
  --session <file>     JSON-lines file used to persist and reload the
                       conversation across runs.
  --help               Show this help.

The command is non-interactive: it reads no input from the terminal and prints
only assistant text to stdout.
`)
}
