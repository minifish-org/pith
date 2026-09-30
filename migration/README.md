# Migration records and current entry points

Pith's original AI/Core/Tools migration completed 26 accepted steps. The additive embedded SDK completed seven more steps, accepted in commit `887fbc1a5b402a3fdfbb9636a17855cfeeea12f7`.

For the current Go integration API, read [the embedded SDK guide](../docs/sdk/README.md) and [compatibility notes](../docs/sdk/compatibility.md). The SDK plan and its independent preparation audit are described in [sdk/README.md](sdk/README.md). Run receipts are in [results](results/); they document the accepted state rather than guaranteeing complete Pi parity.

## Evidence preservation

The original plan, contracts, inventories, source hashes, judge inputs, scripts and receipts are historical evidence. Some original planning prose is Chinese. These bytes are intentionally preserved because accepted steps and fingerprints refer to them. Translating the archived contracts in place would invalidate their relationship to the completed run. Preparation-era labels such as not-started are historical inputs; receipts and accepted commits establish completion.

Use the English [execution overview](execution.md), [command guide](start.md), and [ongoing migration design](continuous-port.md) for current navigation. Do not restart a completed plan by deleting its progress or rewriting its inputs. New source revisions need reviewed incremental plans and explicit accepted baselines.

Multilingual test strings are behavioral inputs, not localized product messages: replacing them with English would remove coverage of Unicode parsing, streaming, truncation, filesystem I/O and protocol preservation. See [the language audit](../docs/language-audit.md) for the exact distinction.
