# Third-party notices

Pith is distributed under the GNU Affero General Public License v3.0 (see the
repository `LICENSE`). Pi-derived material retains its upstream MIT attribution;
the full upstream notice is preserved in `migration/UPSTREAM-LICENSE`.
This file records the third-party components
that are vendored, embedded or linked into the delivered scope, together with
their licenses and provenance.

## Upstream Pi source

The initial Go foundations were migrated from Pi revision
`f07218c4d4bbc12bef056a7058c3dd49dfe41abe` (v0.87.1). The recorded Pi 1.0
headless SDK update targets `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
See `migration/sync.json` for the maintained sync baseline and migration
receipts for the accepted scope; this is not a claim that all Pi UI components
were ported. Pi source is MIT-licensed, Copyright (c) 2025 Mario Zechner.
Upstream notices remain in source headers, asset licenses,
`migration/UPSTREAM-LICENSE` and the repository `NOTICE`.

## Embedded assets

| Component | Location | Version | License |
| --- | --- | --- | --- |
| Model Context Protocol TypeScript SDK (adapted) | `packages/mcp/LICENSES/modelcontextprotocol-typescript-sdk.txt` | v1.29.0 | MIT, Copyright (c) 2024 Anthropic, PBC |
| quickjs-wasi | `packages/codemode/LICENSES/quickjs-wasi.txt`, `packages/codemode/runtime/quickjs.wasm.base64.txt` | 3.6.2 | MIT, Copyright (c) 2026 Vercel, Inc. |
| QuickJS-ng | `packages/codemode/LICENSES/quickjs-ng.txt` | — | MIT, Copyright (c) 2017-2026 Fabrice Bellard and contributors |
| Pi model catalog JSON (legacy) | `packages/ai/catalog/data/` (with `data/LICENSE.txt`) | manifest `schemaVersion: 3` | MIT, Copyright (c) 2025 Mario Zechner |
| Pi 1.0 release model catalog JSON | `packages/ai/catalog/v1data/` | manifest `schemaVersion: 6` | MIT, Copyright (c) 2025 Mario Zechner |

The `quickjs-wasi` WASM module is committed as immutable base64 text and decoded
at runtime. It is executed by the pure-Go `wazero` runtime; no cgo, Node.js or
external QuickJS shared library is used. The catalog JSON directories are
embedded read-only and their publication manifests record per-file SHA-256
digests.

## Go module dependencies

The delivered build uses the following direct and transitive Go modules. License
identifiers are from the license file shipped in each module; Apache-2.0 modules
also ship a `NOTICE` that must be preserved.

| Module | Version | License |
| --- | --- | --- |
| `github.com/tetratelabs/wazero` | v1.9.0 | Apache-2.0 (with NOTICE) |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.2 | Apache-2.0 |
| `github.com/aws/aws-sdk-go-v2` and `github.com/aws/smithy-go` | v1.47.1 / v1.28.1 | Apache-2.0 (with NOTICE) |
| `github.com/coder/websocket` | v1.8.15 | ISC |
| `github.com/dlclark/regexp2` | v1.11.0 | MIT |
| `github.com/goccy/go-yaml` | v1.19.2 | MIT |
| `github.com/sabhiram/go-gitignore` | v0.0.0-20210923224102-525f6e181f06 | MIT |
| `github.com/sergi/go-diff` | v1.4.0 | MIT |
| `golang.org/x/oauth2` | v0.28.0 | BSD-3-Clause |
| `golang.org/x/text` | v0.14.0 | BSD-3-Clause |
| `modernc.org/sqlite` | v1.44.3 | BSD-3-Clause |
| `modernc.org/libc` | v1.67.6 | BSD-3-Clause |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause |
| `modernc.org/memory` | v1.11.0 | BSD-3-Clause |
| `github.com/ncruces/go-strftime` | v1.0.0 | MIT |
| `github.com/dustin/go-humanize` | v1.0.1 | MIT |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/mattn/go-isatty` | v0.0.20 | MIT |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause |
| `golang.org/x/exp` | v0.0.0-20251023183803-a4bb9ffd2546 | BSD-3-Clause |
| `golang.org/x/sys` | v0.37.0 | BSD-3-Clause |

The Go standard library is distributed under the Go BSD-style license.

## Pi Durable and Chord ports

`packages/durable` and `packages/chord` are native Go ports of Pi revision
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`, specifically the Durable runtime
(`packages/durable/src/**`) and the Chord dependency closure
(`packages/chord/src/**`). Pi source is MIT-licensed, Copyright (c) 2025 Mario
Zechner; the full upstream notice is preserved in `migration/UPSTREAM-LICENSE`
and upstream headers remain in source. The port is idiomatic Go, not a
line-for-line or binary-compatible reimplementation; `docs/sdk/durable-source-map.json`
records reviewed source ownership, and `docs/sdk/durable.md` documents the
explicit host/runtime format differences. No upstream licence is changed.

### Pure Go SQLite

The Durable SQLite adapter uses `database/sql` with the CGO-free
`modernc.org/sqlite` driver (v1.44.3) and its matching `modernc.org/libc`
(v1.67.6). Both are BSD-3-Clause. The bundled SQLite amalgamation is public
domain. The driver and its transitive modules are pinned in the dependency
manifests; no CGO SQLite driver or system SQLite library is used, so ordinary
builds and cross-builds run with `CGO_ENABLED=0`.

## Distribution

- The delivered scope builds with `CGO_ENABLED=0`. `wazero` is pure Go, so the
  Codemode sandbox and the whole binary cross-compile without a C toolchain.
- No Node.js executable, npm package, shell JavaScript engine or external QuickJS
  library is required at build or runtime.
- The license and notice files listed above are part of the repository and must
  not be removed or rewritten.

This list covers the delivered scope. It is not a legal opinion and not a
complete inventory of every transitive test-only dependency.
