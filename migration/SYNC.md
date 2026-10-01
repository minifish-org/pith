# Continuous Pi migration

The native [Portsmith Go executor](https://github.com/minifish-org/portsmith-go)
can prepare and execute increments from Pi. Its model backend is Pith, with the
existing DeepSeek configuration. TypeScript Portsmith is the historical
migration reference.

`sync.json` records the last accepted upstream revision and reviewed source
ownership. The initial config imports 694 TypeScript inputs from the original
and embedded-SDK migration plans, including their TS reference files, and maps
them to 403 existing Go files. Fine source maps take precedence over conservative
batch ownership. These counts describe the initial import, not a claim of
one-to-one file parity or exhaustive semantic equivalence.

`upstream.json`, the original plans and their receipts remain historical
provenance. After future acceptance, `sync.json.revision` becomes the current
incremental baseline; original evidence is not rewritten.

From the sibling Portsmith Go checkout:

```sh
CGO_ENABLED=0 go build -mod=readonly -o bin/portsmith ./cmd/portsmith
./bin/portsmith sync --project ../pith --upstream <full-new-Pi-hash>
```

This creates `migration/sync/pi-<12-character-hash>/` without calling a model.
Review the recorded differences and impact, complete contracts and independent
judges, and record new/changed ownership in `new-mappings.json`. Then:

```sh
./bin/portsmith sync --project ../pith --upstream <full-new-Pi-hash> --check
./bin/portsmith sync --project ../pith --upstream <full-new-Pi-hash> --commit \
  --env-file ../omni-pi/.env
```

The execution command automatically advances through all reviewed steps and
commits accepted modules. It advances `sync.json` only after complete
acceptance. Ctrl-C preserves candidates and journals; repeat the same command
to resume. It never pushes and does not spend model credits during preparation
or `--check`. Existing TS executor journals are not reused.

The [complete incremental guide](https://github.com/minifish-org/portsmith-go/blob/main/docs/INCREMENTAL.md)
explains local Git checkout support, frozen update manifests, recovery and
current limitations. A new hash creates factual migration material; it does not
replace independent review of changed behavior.
