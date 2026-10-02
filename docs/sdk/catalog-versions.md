# Catalog versions

Pith ships two model catalogs. They are separate, immutable, embedded JSON
assets with separate Go access paths, so a reader can always tell which release
a record came from. This document records both paths and the compatibility
guarantees around them.

## Summary

| Path | Assets | Go accessor | Schema | Kinds | Used by |
| --- | --- | --- | --- | --- | --- |
| Legacy | `packages/ai/catalog/data/*.json` | `catalog.MODELS`, `catalog.IMAGE_MODELS`, `FlattenModelCatalog`, `MustFlatten`, `MustDocument` | 3 | chat, image | Historical callers, `compat` aliases |
| V1 release | `packages/ai/catalog/v1data/*.json` | `catalog.V1Models`, `catalog.V1Manifest`, `catalog.V1ProviderNames` | 6 | chat, image, classifier | Modern `providers.BuiltinModels`, registry and cache metadata |

Both directories are embedded read-only (`//go:embed`). `V1Models` and
`V1Manifest` return defensive copies, so a caller cannot corrupt the compiled-in
catalog by mutating a returned slice. The independent catalog conformance test
asserts that: it overwrites the first byte of a returned record and re-reads the
same provider.

## Legacy catalog

`packages/ai/catalog/data/` is the frozen pre-1.0 catalog with its own
publication manifest (`data/manifest.json`, `schemaVersion: 3`). The generated Go
tables in `models_generated.go` / `image_models_generated.go` expose it as
`catalog.MODELS` and `catalog.IMAGE_MODELS`. The legacy accessors have no model
kind: a legacy chat model is `types.Model` with no `type` field.

Legacy callers keep working through `packages/ai/compat`:

- `compat.GetModel`, `compat.GetModels`, `compat.GetProviders` are the
  deprecated static reads and now delegate to `providers.GetBuiltinModel`,
  `providers.GetBuiltinModels` and `providers.GetBuiltinProviders`.
- The `compat` package also owns the deprecated global api registry and the
  api-dispatch `stream`/`complete` entry points.
- `packages/ai/legacy_api_aliases.go` keeps the `StreamAnthropic`,
  `StreamOpenAICompletions`, ... aliases pointing at the same lazy API modules
  the modern entry points use, so switching callers does not change behavior.

Deprecated does not mean removed: the legacy constants and aliases remain
compiled and are covered by the cumulative regression tests.

## V1 release catalog

`packages/ai/catalog/v1data/` is the Pi 1.0 release catalog
(`catalog-manifest.json`, `schemaVersion: 6`). It records a per-provider SHA-256
digest in the manifest, and the embedded documents are byte-identical to those
digests. This is the provenance contract the independent judge checks:
`catalog.V1Manifest()` must deep-equal the pinned manifest fixture, and
`catalog.V1Models(provider, kind)` must reproduce every frozen record for
`chat`, `image` and `classifier`.

`V1Models(provider, kind)` returns records in stable `api/kind:id` order. An
unknown provider or unknown kind returns an empty result (never a nil dereference
and never a partial record). Each record is the verbatim upstream JSON object, so
`type`, costs, limits, compatibility flags, extra provider settings and null
thinking-level mappings round-trip unchanged.

### Model kinds

The V1 identity is `(provider, api, kind, id)`. `types.GetModelType` decides the
kind:

- a missing or `null` `type` means `chat`;
- `image` and `classifier` are the other shipped kinds;
- any other explicit `type` is preserved verbatim and is never coerced to chat.

`types.Model` is the chat DTO, `types.ImagesModel` the image DTO and
`types.ClassifierModel` the classifier DTO. `types.AnyModel` is the tagged union
that can hold any of them. `ai.ModelsAreEqual` and `ai.HasApi` compare identity
including the kind, so a chat record never collides with an image or classifier
record that happens to share an `id`.

### What the runtime uses

`providers.BuiltinModels(nil)` builds the modern registry from the V1 catalog.
Its two collection reads differ deliberately:

- `Models.GetModels(provider)` returns chat models only, preserving the legacy
  chat-shaped `[]types.Model` listing.
- `Models.GetAllModels(provider)` returns the mixed `[]types.AnyModel`, including
  image and classifier records.

`Models.GetModelOfType(kind, provider, id)` narrows to one kind. A classifier
record therefore appears in `GetAllModels("typesafe")` and in
`GetModelsOfType("classifier", "typesafe")`, but never in `GetModels("typesafe")`.
This is the "mixed catalog" behavior the delivery examples and the integration
test exercise.

## Compatibility rules

- The V1 assets and manifest are release data. Do not rewrite them, reformat
  them or regenerate them from the legacy catalog.
- Keep the legacy catalog and the V1 catalog separate. They have different
  schema versions and different identity rules; do not merge or overlay them.
- New code should read the V1 catalog through the model registry or
  `catalog.V1Models`. The legacy accessors exist for compatibility only.
- Passing the catalog tests proves the pinned fixtures round-trip. It does not
  claim the upstream catalog is complete or that every provider is reachable.

## Related documents

- [pi-1.0.md](./pi-1.0.md) — feature and adaptation table
- [compatibility.md](./compatibility.md) — embedded SDK compatibility scope
