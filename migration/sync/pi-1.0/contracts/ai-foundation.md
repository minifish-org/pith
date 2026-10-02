# Pi 1.0 AI types, utilities and versioned catalogs

Port the frozen target source, not latest main. Read all old/new definitions and
upstream tests in this step. Keep exported legacy Go DTOs and their JSON stable.
Missing model `type` means chat; explicit chat/image/classifier types participate
in identity. Retain `types.Model` as the legacy chat DTO. Add idiomatic tagged
`types.AnyModel` for mixed lists without losing operation-specific fields. Expose
`types.GetModelType(model any) string`, `types.IsModelType(model any, kind string) bool`
and checked narrowing. These helpers must also accept legacy Model/ImagesModel,
pointers, nil and raw JSON (unknown type must not become chat).

Update classifier/image/request/result DTOs, tool outputSchema/structuredContent,
assistant thinkingLevel, sampling parameters, provider metadata and error types
from source. Every new field must preserve absent vs zero and unknown/raw JSON.
Do not rename/remove the old image API; adapt it to the new typed operations.
Keep existing legacy fields/signatures and historical conformance fixtures.

`utils.ProviderHeadersToRecord(...types.ProviderHeaders)` merges case-insensitively;
later spelling wins, nil removes earlier values and empty returns nil. Retry and
overflow classifiers follow the new source (including subscription-sharing error
codes and Z.AI CN overflow). Apply the upstream retry-after fallback, normalization,
JSON schema validation and OAuth page behavior. Native platform helpers remain Go.

Frozen `assets/catalog/*.json` come from the exact Pi AI 1.0.0 npm release whose
gitHead is the target commit. They are immutable assets, not model-generated data.
Embed them under catalog/v1data, preserving all chat/image/classifier records.
Export `catalog.V1Models(provider string, kind string) []json.RawMessage` returning
defensive copies in stable API/id order; unknown provider or kind returns empty.
The returned records retain original JSON fields, including type, costs, limits,
compatibility, extra provider settings and null thinking-level mappings.

Historical `catalog.MODELS`, `IMAGE_MODELS`, provider constants and deprecated
compat.GetModels/GetModel/GetProviders remain the frozen legacy catalog. Use a
separate explicitly versioned data path. Modern BuiltinProviders/BuiltinModels
must use the V1 data in the runtime step. Do not keep the entire application on
the old catalog merely to satisfy a historical oracle. Explain this compatibility
split in docs; do not edit historical tests, receipts or data.

Candidate self-tests must cover all three kinds, nil/unknown model tags, same-id
cross-kind collisions, immutable reads, caller header non-mutation, null overrides,
retry classification and provider catalog round-trip. No network or credentials.

Expose catalog.V1Manifest() json.RawMessage as a defensive copy of the pinned
manifest. Embed the hidden upstream manifest under catalog-manifest.json so it
fits the native asset allowlist. Do not count it as provider model data. Preserve
schemaVersion, generatedAt, structureHash and every per-provider digest; modern
registry/cache metadata uses this V1 provenance, while the legacy timestamp API
retains its old compatibility behavior.
