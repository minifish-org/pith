# Language audit

Runtime messages, errors, help, maintained public guides and new SDK documentation use English. The audit is recorded in [language-audit.json](language-audit.json).

The product implementation under packages/ and cmd/ has no Chinese prose; the remaining Chinese in Go files is test input for Unicode behavior. Historical migration inputs, contracts, fixtures and reproduction scripts retain their original bytes. Accepted receipts and snapshots refer to those hashes, so rewriting them as English would invalidate the evidence. Raw upstream fixtures and Chinese payloads also exercise real multilingual behavior; translating them would change the tests. The manifest lists each preserved file and its SHA-256.

Use the English [SDK guide](sdk/README.md), [migration overview](../migration/README.md), [execution guide](../migration/start.md), and [ongoing-port design](../migration/continuous-port.md). Historical records are not the current user interface.
