# Reproduce the SQLite dependency probe

This is a dependency smoke test, not the migrated Durable adapter. The source
and frozen module metadata are stored as `.txt` so the migration verifier does
not mistake this probe for candidate SDK files.

In a temporary directory, copy `main.go.txt` to `main.go`, `go.mod.txt` to
`go.mod`, and `go.sum.txt` to `go.sum`, then run:

```sh
CGO_ENABLED=0 GOMAXPROCS=2 go run -mod=readonly -p=2 .
```

Expected output: `no-cgo-ok`. This creates a real SQLite table, inserts a row and
reads it through `database/sql` and modernc.org/sqlite v1.44.3.

The same source was cross-built with CGO_ENABLED=0 for darwin/arm64,
darwin/amd64, linux/amd64, linux/arm64, and windows/amd64. Example:

```sh
CGO_ENABLED=0 GOMAXPROCS=2 GOOS=linux GOARCH=amd64   go build -mod=readonly -p=2 -o sqlite-smoke .
```

Results are recorded in `../sqlite-dependency-check.json`. Cross-building
confirms that no CGO toolchain is needed; it does not execute foreign binaries
or prove the future Durable adapter's recovery behavior.
