# Pith

A Go port of selected Pi AI, agent-core, and built-in tool capabilities, with an embeddable SDK and native command-line programs.

**Status: experimental; first planned migration completed.** [Portsmith](https://github.com/minifish-org/portsmith), using Pi Coding Agent and DeepSeek, completed 26 accepted steps in three modules. All three module receipts report passing recorded verification. This is not a claim of complete Pi parity, production readiness, or live validation of every provider.

## Build without CGO

Requirements for building: Go >=1.24 and Git. Users of the resulting binary do not need Go, Node.js, or npm. The shell tool still needs an operating-system shell.

```sh
git clone https://github.com/minifish-org/pith.git
cd pith
CGO_ENABLED=0 go build -mod=readonly -trimpath -o ./bin/ ./cmd/...
./bin/pith --help
```

`pith` runs a non-interactive agent loop with read/write/edit/bash tools. `pith-ai` provides lower-level AI commands; inspect its command help for its supported interface. The default SDK/CLI path currently uses an OpenAI-compatible Chat Completions endpoint; the existence of other provider packages does not make every provider available through this CLI.

## Try an agent turn

Set `PITH_API_KEY` in your environment without putting the value in shell history. Then use a disposable working directory:

```sh
mkdir -p /tmp/pith-demo
./bin/pith \
  --base-url https://api.deepseek.com/v1 \
  --model deepseek-flash \
  --cwd /tmp/pith-demo \
  --session /tmp/pith-demo/session.jsonl \
  --prompt 'Create hello.txt containing Hello from Pith, then read it back.'
```

This makes a paid model call and authorizes local file and shell operations. `--cwd` is not an OS sandbox. Review [SECURITY.md](SECURITY.md) before connecting an agent to sensitive directories. No live provider smoke test is claimed by the migration receipts.

## Packages

| Area | Purpose |
| --- | --- |
| `packages/ai/` | Models, provider adapters, streaming, authentication, and utilities |
| `packages/agent/` | Agent execution, harness/session/resource support, SDK, and tools |
| `packages/chord/`, `packages/telemetry/` | Supporting contracts and instrumentation |
| `cmd/pith/`, `cmd/pith-ai/` | Native command-line entry points |
| `internal/conformance/` | Independent migration acceptance tests |
| `migration/` | Pinned inputs, contracts, judges, source maps, and original receipts |

Start embedding at `packages/agent/sdk`. Interfaces and behavior may change before a stable release. Review the implementation and tests for the exact supported surface.

## Verification and evidence

```sh
CGO_ENABLED=0 go test -mod=readonly ./...
CGO_ENABLED=0 go vet -mod=readonly ./...
# Optional race instrumentation requires CGO and a C compiler:
CGO_ENABLED=1 go test -mod=readonly -race ./...
```

The original modules were committed as:

| Module | Accepted steps | Commit |
| --- | ---: | --- |
| AI | 13 | `a5aeab8214f672d5d83e35fec78bf4d0daf67473` |
| Core | 7 | `adbc36268f0c0cad3f5aad7c591b5f0a18f79ac0` |
| Tools | 6 | `0d4d1479739fcbe849fec21fc02cf8ac31aee237` |

See [the evidence summary](docs/evidence/2026-09-29.md) and the original `migration/results/*.json` receipts. Passing a fixed judge suite does not prove behavior outside its contracts. `fullParityProven` is explicitly false in those receipts.

The migration directory is retained as historical evidence, including Chinese planning documents. Preparation-time status labels describe the original inputs; final receipts and commits describe the completed run. Do not rerun the original plan in this populated checkout expecting it to overwrite accepted code.

## Direction

The next experiment is to use **Pith itself as Portsmith's execution backend** to carry reviewed Pi changes into Pith on a regular schedule. That backend and scheduled update loop are planned, not implemented. Upstream diffs, contract changes, dependencies, licenses, tests, and final acceptance still need review.

Not included in this migration: a TUI, Web UI, desktop application, Computer Use, MCP integration, or experimental/pico3.

## License

The repository retains its existing [AGPL-3.0 license](LICENSE). Pi-derived code retains upstream MIT attribution; the full upstream notice is in [migration/UPSTREAM-LICENSE](migration/UPSTREAM-LICENSE). See [NOTICE](NOTICE). This is an independent project, not an official Pi distribution.

[Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)
