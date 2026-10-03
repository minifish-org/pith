# Bedrock request-body maintenance

This follow-up repairs the Go HTTP request-body lifetime race that can close a
Bedrock response connection while its event stream is still arriving. It is a
local Go transport adaptation of the pinned Pi 1.0 provider, not a new upstream
sync. See [the contract](contracts/request-body.md) and
[diagnostic evidence](evidence.json).

The plan freezes Pith commit a58643e64ef3c2613b7a5b5d00b815718e2c0950 and
uses Pi commit a13d35a742c6ef8462812a28fbe1d8c8b7431c32. Only the Bedrock
provider file, a small transport helper, and its candidate-owned tests are
writable. The existing conformance adapters, fixtures, and judges are unchanged.
Historical migration plans, receipts, and the upstream sync baseline remain
unchanged. Future Pi provider updates must retain this Go-specific adaptation
unless an independent regression demonstrates that it is no longer needed.

Codex prepares the diagnosis, contract, and independent judges. Portsmith Go
with Pith and DeepSeek Flash generates the implementation and records cumulative
and whole-project acceptance. A passing focused fixture alone is insufficient.

From the Pith repository root:

```sh
../portsmith-go/bin/portsmith migrate \
  --plan migration/maintenance/bedrock-request-body --check

env PORTSMITH_MODEL=deepseek-flash \
  PORTSMITH_BASE_URL=https://api.deepseek.com/v1 \
  ../portsmith-go/bin/portsmith migrate \
  --plan migration/maintenance/bedrock-request-body \
  --commit --env-file ../omni-pi/.env
```

Normal compilation and verification run with CGO disabled. The Go race detector
alone uses CGO as required by Go. Tests use local fixtures and need no live AWS
account, credentials, or provider calls. Preparation must be checked after the
independent judge is installed; the model must not author that judge.
