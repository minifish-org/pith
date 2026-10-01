# Incremental migration review

This draft contains actual old/new source and a frozen target baseline. No implementation model has run.

Review sync-report.json and source ownership. Add new sources to the mapping or explicitly document exclusions. Add candidate tests to outputs, a behavior contract and frozen independent judges. Fill workflow.batches.changes.steps, mark it ready, then run migrate --check and migrate --commit. Do not reuse historical executor journals.

Baseline files remain read-only. Only updates.files are existing writable files; manifests, independent judges and historical receipts are immutable. Automatic Go file deletion and symbol rename decisions are not inferred from TS file changes.
