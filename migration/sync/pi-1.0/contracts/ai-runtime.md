# Mixed model runtime and new classification operations

Implement Pi 1.0 models.ts/provider.ts/model stores and provider factories using
the completed tagged AnyModel surface. Retain chat-only GetModels/GetModel and
GetAvailable signatures. Add mixed GetAllModels/GetAllAvailable and typed lookup
GetModelsOfType/GetModelOfType/GetAvailableOfType. Key catalog/store/refresh overlay
identity by provider, model type and id; chat/image/classifier can share ids.
Legacy missing type is chat. Preserve generation ownership, cancellation, auth
merging, refresh coalescing, persisted model metadata/ETags and defensive copies.

Modern BuiltinProviders/BuiltinModels consume catalog.V1Models; legacy compat
static catalog APIs continue returning the legacy data. Unknown new providers
such as typesafe must be registered through actual factories, not hardcoded
fixture answers. Include image generation through the shared registry with old
ImagesModels wrappers retained. Add real classifier dispatch and request/response
DTOs, including choice/score/bool questions and uncertainty distributions.

Port native System One, Cloudflare System One and llama.cpp classification from
their exact source. Bool becomes noul on the System One wire, normalized answers
become public bool. Llama classifies next-token log probabilities (not free-text
completion), expands missing-token readout depths, and handles underflow safely.
Source-prescribed URLs, auth, timeout, cancellation, cost and errors are required.
Do not replace a classifier with a chat prompt or return fabricated probabilities.

Expose `ai.ModelsAreEqual` using model type as well as provider/id; retain the old
typed pointer calls and support new tagged models through an any-based boundary.
`ai.HasApi` accepts only chat models. Direct Stream/Complete/StreamSimple and image/
classifier paths enforce operation kind and return source-shaped errors. Apply
model samplingParams even for direct requests; explicit request values override.

Independent judge exercises built-in catalog freshness and type identity. Add
offline candidate tests using httptest for all classifier protocols, image/chat
dispatch misuse, store restart, cache metadata, auth overrides, cancellation and
mixed same-id overlays. Also run all existing runtime/conformance tests.

Historical ai_providers oracles call the named no-argument factory functions and
compare their entire catalog JSON. Preserve those no-argument factory catalogs
as an explicit legacy compatibility surface too. Build modern BuiltinProviders
using a reviewed V1 catalog option or V1 provider wrapper/factory, retaining real
auth/transport methods. Do not make wrappers inspect test names, fixture paths,
environment flags or expected data. Document the version distinction. New typesafe
factories have no legacy catalog and use V1 directly. Modern SDK construction must
resolve through the V1 registry rather than deprecated GetBuiltinModel lookups.

Freeze native classifier adapter entry point as
`api.TypesafeSystemOneClassify(ctx context.Context, model *types.ClassifierModel,
request *types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult`.
ClassifierOptions embeds ProviderRequestOptions. Source errors are represented in
the returned result. Context/question/answer DTOs use source JSON field names;
choice/score/bool union details must survive JSON decoding. A malformed or missing
answer fails the entire result rather than returning fabricated partial answers.
