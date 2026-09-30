# Running a prepared migration

The original AI/Core/Tools run and additive SDK run have completed. Their accepted progress must not be cleared to regenerate code. For a new checkout, prepare/review the intended plan and accepted baseline first.

```sh
cd ~/work/portsmith
npm ci
npm run build
cd ~/work/pith
node ../portsmith/dist/cli.js migrate --plan migration/sdk --check
```

On the completed local run this reports complete. A new prepared plan reports ready and canStart: true. Checking makes no model calls. To execute an unfinished prepared SDK plan:

```sh
node ../portsmith/dist/cli.js migrate --plan migration/sdk --commit --env-file ../omni-pi/.env
```

This reads existing DeepSeek configuration, performs paid model calls, advances automatically and commits the accepted module; it does not push. Explicit max-turns/timeout/max-attempts budgets are optional and default to unlimited. Ctrl-C preserves progress. Rerun the same command to resume; do not remove journals or frozen references.

The SDK increment has seven automatic steps. Its candidate sessions and reports live under `.portsmith/sdk/runs/`, and its journal is `.portsmith/sdk/modules.json`. The original plan uses `.portsmith/runs/` and `.portsmith/modules.json`. Formal outputs are accepted after cumulative and actual-project verification, with receipts under `migration/results/`.

See [the SDK plan guide](sdk/README.md) and [Portsmith manual](https://github.com/minifish-org/portsmith/blob/main/docs/user-manual.md). Preparation checks are not proof that generated behavior is correct; final acceptance and real-provider smoke testing are separate evidence.
