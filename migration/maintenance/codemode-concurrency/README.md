# Codemode concurrency maintenance

This follow-up repairs a Go test fixture and documents the deliberate callback
scheduling adaptation discovered in the Pi 1.0 CI run. It keeps the runtime's
parallel callback execution. See [the contract](contracts/concurrency.md).

The plan freezes the current committed Pith target, uses the original pinned Pi
1.0 source, and runs a new Portsmith Go journal. Historical migration materials
and the upstream sync baseline remain unchanged. Codex prepared the contract
and independent judges; Portsmith Go with Pith and DeepSeek Flash generates the
candidate and records acceptance evidence.

From the Pith repository root:

```sh
../portsmith-go/bin/portsmith migrate \
  --plan migration/maintenance/codemode-concurrency --check

env PORTSMITH_MODEL=deepseek-flash \
  PORTSMITH_BASE_URL=https://api.deepseek.com/v1 \
  ../portsmith-go/bin/portsmith migrate \
  --plan migration/maintenance/codemode-concurrency \
  --commit --env-file ../omni-pi/.env
```

Normal builds and verification run with CGO disabled. The Go race detector alone
uses CGO as required by Go. No live provider is needed for the independent tests.
