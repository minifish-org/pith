// This file is a Go port of packages/ai/src/compat.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// compat.ts is the deprecated global pi-ai API surface: api-dispatch
// stream()/complete() with env API-key injection, the api registry, the
// generated catalog reads (getModel/getModels/getProviders), per-API lazy
// stream wrappers and image generation. The generated catalog reads and the
// type re-exports live at their real declarations in packages/ai/providers; this
// package adds the api registry and the dispatch entry points. The registry is
// process-global state and is guarded for concurrent access.
package compat

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/providers"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// BuiltinProvider is the set of providers present in the generated catalog.
// It is the real declaration in packages/ai/providers, re-exported here for the
// compat surface.
type BuiltinProvider = providers.BuiltinProvider

// GetModel is the deprecated static catalog read.
//
// Deprecated: use providers.GetBuiltinModel or Models.GetModel.
func GetModel(provider BuiltinProvider, modelID string) *types.Model {
	return providers.GetBuiltinModel(provider, modelID)
}

// GetModels is the deprecated static catalog read. It returns every catalog
// model of a provider.
//
// Deprecated: use providers.GetBuiltinModels or Models.GetModels.
func GetModels(provider BuiltinProvider) []types.Model {
	return providers.GetBuiltinModels(provider)
}

// GetProviders is the deprecated static catalog read. It returns the provider
// ids present in the generated catalog in a stable sorted order.
//
// Deprecated: use providers.GetBuiltinProviders or Models.GetProviders.
func GetProviders() []BuiltinProvider {
	return providers.GetBuiltinProviders()
}

// ApiStreamFunction is the api-dispatch stream function shape.
type ApiStreamFunction = types.StreamFunction

// ApiStreamSimpleFunction is the simple api-dispatch stream function shape.
type ApiStreamSimpleFunction func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream

// ApiProvider pairs an api id with its stream and simple-stream
// implementations. A later registration for the same api replaces the earlier
// one.
type ApiProvider struct {
	API          types.Api
	Stream       ApiStreamFunction
	StreamSimple ApiStreamSimpleFunction
}

type registeredApiProvider struct {
	provider *ApiProvider
	sourceID *string
}

var (
	apiRegistryMu               sync.RWMutex
	apiProviderRegistry         = map[types.Api]registeredApiProvider{}
	builtinApiProviderInstances = map[types.Api]*ApiProvider{}
	fauxProviderSequence        uint64
)

func wrapStream(apiID types.Api, fn ApiStreamFunction) ApiStreamFunction {
	return func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
		if model == nil || model.Api != apiID {
			return errorStream(model, fmt.Errorf("Mismatched api: %s expected %s", modelAPI(model), apiID))
		}
		if fn == nil {
			return errorStream(model, fmt.Errorf("No stream implementation registered for api: %s", apiID))
		}
		return fn(model, context, options)
	}
}

func wrapStreamSimple(apiID types.Api, fn ApiStreamSimpleFunction) ApiStreamSimpleFunction {
	return func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
		if model == nil || model.Api != apiID {
			return errorStream(model, fmt.Errorf("Mismatched api: %s expected %s", modelAPI(model), apiID))
		}
		if fn == nil {
			return errorStream(model, fmt.Errorf("No simple stream implementation registered for api: %s", apiID))
		}
		return fn(model, context, options)
	}
}

func modelAPI(model *types.Model) types.Api {
	if model == nil {
		return ""
	}
	return model.Api
}

// errorStream terminates a stream with an error message, matching the lazy
// setup-error path used elsewhere in the SDK. It is the Go equivalent of the
// synchronous throw in the upstream wrappers.
func errorStream(model *types.Model, err error) *types.AssistantMessageEventStream {
	var target *types.Model
	if model != nil {
		copy := *model
		target = &copy
	}
	return api.LazyStream(context.Background(), target, func(context.Context) (*types.AssistantMessageEventStream, error) {
		return nil, err
	})
}

// RegisterApiProvider registers an api provider. The optional sourceID
// identifies a dynamic/provider source for bulk unregistration.
//
// Ports `registerApiProvider` from packages/ai/src/compat.ts.
func RegisterApiProvider(provider ApiProvider, sourceID ...string) {
	var source *string
	if len(sourceID) > 0 && sourceID[0] != "" {
		value := sourceID[0]
		source = &value
	}
	wrapped := &ApiProvider{
		API:          provider.API,
		Stream:       wrapStream(provider.API, provider.Stream),
		StreamSimple: wrapStreamSimple(provider.API, provider.StreamSimple),
	}
	apiRegistryMu.Lock()
	apiProviderRegistry[provider.API] = registeredApiProvider{provider: wrapped, sourceID: source}
	apiRegistryMu.Unlock()
}

// GetApiProvider returns the registered api provider for an api id, or nil.
//
// Ports `getApiProvider` from packages/ai/src/compat.ts.
func GetApiProvider(apiID types.Api) *ApiProvider {
	apiRegistryMu.RLock()
	defer apiRegistryMu.RUnlock()
	entry, ok := apiProviderRegistry[apiID]
	if !ok {
		return nil
	}
	return entry.provider
}

// GetApiProviders returns every registered api provider.
//
// Ports `getApiProviders` from packages/ai/src/compat.ts.
func GetApiProviders() []*ApiProvider {
	apiRegistryMu.RLock()
	defer apiRegistryMu.RUnlock()
	result := make([]*ApiProvider, 0, len(apiProviderRegistry))
	for _, entry := range apiProviderRegistry {
		result = append(result, entry.provider)
	}
	return result
}

// UnregisterApiProviders removes every provider registered with sourceID.
//
// Ports `unregisterApiProviders` from packages/ai/src/compat.ts.
func UnregisterApiProviders(sourceID string) {
	apiRegistryMu.Lock()
	defer apiRegistryMu.Unlock()
	for apiID, entry := range apiProviderRegistry {
		if entry.sourceID != nil && *entry.sourceID == sourceID {
			delete(apiProviderRegistry, apiID)
		}
	}
}

func clearApiProviders() {
	apiRegistryMu.Lock()
	apiProviderRegistry = map[types.Api]registeredApiProvider{}
	apiRegistryMu.Unlock()
}

// RegisterFauxProvider registers a scripted faux provider and returns its
// registration handle. Calling the handle's Unregister removes it.
//
// Ports `registerFauxProvider` from packages/ai/src/compat.ts.
func RegisterFauxProvider(options *providers.RegisterFauxProviderOptions) providers.FauxProviderRegistration {
	var value providers.RegisterFauxProviderOptions
	if options != nil {
		value = *options
	}
	core := providers.CreateFauxCore(value)
	sourceID := fmt.Sprintf("faux-provider-%s", strconv.FormatUint(atomic.AddUint64(&fauxProviderSequence, 1), 36))
	RegisterApiProvider(apiProviderFromStreams(types.Api(core.API), core), sourceID)
	return providers.FauxProviderRegistration{
		API:                     core.API,
		Models:                  core.Models,
		State:                   core.State,
		GetModel:                core.GetModel,
		SetResponses:            core.SetResponses,
		AppendResponses:         core.AppendResponses,
		GetPendingResponseCount: core.GetPendingResponseCount,
		Unregister: func() {
			UnregisterApiProviders(sourceID)
		},
	}
}

func apiProviderFromStreams(apiID types.Api, streams types.ProviderStreams) ApiProvider {
	return ApiProvider{
		API: apiID,
		Stream: func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
			return streams.Stream(model, context, options)
		},
		StreamSimple: func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
			return streams.StreamSimple(model, context, options)
		},
	}
}

func builtinAPIs() []ApiProvider {
	return []ApiProvider{
		apiProviderFromStreams(types.Api("anthropic-messages"), api.AnthropicMessagesApi()),
		apiProviderFromStreams(types.Api("openai-completions"), api.OpenAICompletionsApi()),
		apiProviderFromStreams(types.Api("openai-responses"), api.OpenAIResponsesApi()),
		apiProviderFromStreams(types.Api("openai-codex-responses"), api.OpenAICodexResponsesApi()),
		apiProviderFromStreams(types.Api("azure-openai-responses"), api.AzureOpenAIResponsesApi()),
		apiProviderFromStreams(types.Api("google-generative-ai"), api.GoogleGenerativeAIApi()),
		apiProviderFromStreams(types.Api("google-vertex"), api.GoogleVertexApi()),
		apiProviderFromStreams(types.Api("mistral-conversations"), api.MistralConversationsApi()),
		apiProviderFromStreams(types.Api("bedrock-converse-stream"), api.BedrockConverseStreamApi()),
		apiProviderFromStreams(types.Api("pi-messages"), api.PiMessagesApi()),
	}
}

// RegisterBuiltInApiProviders registers the builtin API implementations into
// the api registry without clobbering existing entries, so compat may load
// after a test or extension already registered an override for a builtin api.
//
// Ports `registerBuiltInApiProviders` from packages/ai/src/compat.ts.
func RegisterBuiltInApiProviders() {
	for _, provider := range builtinAPIs() {
		if GetApiProvider(provider.API) == nil {
			RegisterApiProvider(provider)
		}
		current := GetApiProvider(provider.API)
		apiRegistryMu.Lock()
		builtinApiProviderInstances[provider.API] = current
		apiRegistryMu.Unlock()
	}
}

// ResetApiProviders clears the registry and reinstalls the builtin providers.
//
// Ports `resetApiProviders` from packages/ai/src/compat.ts.
func ResetApiProviders() {
	clearApiProviders()
	apiRegistryMu.Lock()
	builtinApiProviderInstances = map[types.Api]*ApiProvider{}
	apiRegistryMu.Unlock()
	RegisterBuiltInApiProviders()
}

func init() {
	RegisterBuiltInApiProviders()
}

// compatModels is the built-in provider collection used for the resolved-auth
// dispatch path. It is process-global and read-only after package init; the
// collection guards its own mutable state.
var compatModels = providers.BuiltinModels(nil)

const ambientAuthMarker = "<authenticated>"

func hasExplicitApiKey(options *types.StreamOptions) bool {
	return options != nil && options.APIKey != nil && strings.TrimSpace(*options.APIKey) != ""
}

func hasExplicitApiKeySimple(options *types.SimpleStreamOptions) bool {
	return options != nil && options.APIKey != nil && strings.TrimSpace(*options.APIKey) != ""
}

func hasResolvedCloudflareAuthBase(base *types.ProviderRequestOptions) bool {
	if base == nil {
		return false
	}
	if base.APIKey != nil && strings.TrimSpace(*base.APIKey) != "" {
		return true
	}
	value, ok := base.Headers["cf-aig-authorization"]
	return ok && value != nil
}

func withEnvApiKey(model *types.Model, options *types.StreamOptions) *types.StreamOptions {
	if hasExplicitApiKey(options) {
		return options
	}
	var env types.ProviderEnv
	if options != nil {
		env = options.Env
	}
	apiKey := ai.GetEnvApiKey(string(model.Provider), env)
	if apiKey == nil || *apiKey == ambientAuthMarker {
		return options
	}
	var out types.StreamOptions
	if options != nil {
		out = *options
	}
	out.APIKey = apiKey
	return &out
}

func withEnvApiKeySimple(model *types.Model, options *types.SimpleStreamOptions) *types.SimpleStreamOptions {
	if hasExplicitApiKeySimple(options) {
		return options
	}
	var env types.ProviderEnv
	if options != nil {
		env = options.Env
	}
	apiKey := ai.GetEnvApiKey(string(model.Provider), env)
	if apiKey == nil || *apiKey == ambientAuthMarker {
		return options
	}
	var out types.SimpleStreamOptions
	if options != nil {
		out = *options
	}
	out.APIKey = apiKey
	return &out
}

func builtinApiProviderInstance(apiID types.Api) *ApiProvider {
	apiRegistryMu.RLock()
	defer apiRegistryMu.RUnlock()
	return builtinApiProviderInstances[apiID]
}

// getBuiltinProviderForModel returns the runtime provider that owns a model when
// the api dispatch should use the builtin provider path. It is disabled when a
// custom provider has replaced the builtin api implementation, matching the
// upstream identity check.
func getBuiltinProviderForModel(model *types.Model) ai.Provider {
	if model == nil {
		return nil
	}
	current := GetApiProvider(model.Api)
	if current == nil || current != builtinApiProviderInstance(model.Api) {
		return nil
	}
	provider := compatModels.GetProvider(string(model.Provider))
	if provider == nil {
		return nil
	}
	for _, candidate := range provider.GetModels() {
		if candidate.Api == model.Api {
			return provider
		}
	}
	return nil
}

func resolveApiProvider(apiID types.Api) (*ApiProvider, error) {
	provider := GetApiProvider(apiID)
	if provider == nil {
		return nil, fmt.Errorf("No API provider registered for api: %s", apiID)
	}
	return provider, nil
}

// Stream is the deprecated api-dispatch stream entry point: it normalizes the
// context, applies env API-key injection and delegates to the builtin provider
// or the registered api provider.
//
// Ports `stream` from packages/ai/src/compat.ts.
func Stream(model *types.Model, context types.Context, options *types.ProviderStreamOptions) *types.AssistantMessageEventStream {
	if model == nil {
		return errorStream(nil, fmt.Errorf("No model provided"))
	}
	transcript := utils.NormalizeContext(context)
	base := baseOptions(options)
	builtinProvider := getBuiltinProviderForModel(model)
	if builtinProvider != nil {
		if strings.HasPrefix(string(model.Provider), "cloudflare-") && !hasResolvedCloudflareAuthBase(base) {
			return compatModels.Stream(context2Background(), *model, context, &ai.ModelsApiStreamOptions{StreamOptions: streamOptionsValue(options)})
		}
		return builtinProvider.Stream(*model, transcript, withEnvApiKey(model, derefStreamOptions(options)))
	}
	provider, err := resolveApiProvider(model.Api)
	if err != nil {
		return errorStream(model, err)
	}
	return provider.Stream(model, transcript, withEnvApiKey(model, derefStreamOptions(options)))
}

// Complete is the deprecated api-dispatch completion entry point.
//
// Ports `complete` from packages/ai/src/compat.ts.
func Complete(ctx context.Context, model *types.Model, context types.Context, options *types.ProviderStreamOptions) (*types.AssistantMessage, error) {
	result, err := Stream(model, context, options).Result(ctx)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// StreamSimple is the deprecated simple api-dispatch stream entry point.
//
// Ports `streamSimple` from packages/ai/src/compat.ts.
func StreamSimple(model *types.Model, context types.Context, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	if model == nil {
		return errorStream(nil, fmt.Errorf("No model provided"))
	}
	transcript := utils.NormalizeContext(context)
	base := baseOptionsSimple(options)
	builtinProvider := getBuiltinProviderForModel(model)
	if builtinProvider != nil {
		if strings.HasPrefix(string(model.Provider), "cloudflare-") && !hasResolvedCloudflareAuthBase(base) {
			return compatModels.StreamSimple(context2Background(), *model, context, &ai.ModelsSimpleStreamOptions{SimpleStreamOptions: simpleStreamOptionsValue(options)})
		}
		return builtinProvider.StreamSimple(*model, transcript, withEnvApiKeySimple(model, options))
	}
	provider, err := resolveApiProvider(model.Api)
	if err != nil {
		return errorStream(model, err)
	}
	return provider.StreamSimple(model, transcript, withEnvApiKeySimple(model, options))
}

// CompleteSimple is the deprecated simple api-dispatch completion entry point.
//
// Ports `completeSimple` from packages/ai/src/compat.ts.
func CompleteSimple(ctx context.Context, model *types.Model, context types.Context, options *types.SimpleStreamOptions) (*types.AssistantMessage, error) {
	result, err := StreamSimple(model, context, options).Result(ctx)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func derefStreamOptions(options *types.ProviderStreamOptions) *types.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func streamOptionsValue(options *types.ProviderStreamOptions) types.StreamOptions {
	if options == nil {
		return types.StreamOptions{}
	}
	return options.StreamOptions
}

func simpleStreamOptionsValue(options *types.SimpleStreamOptions) types.SimpleStreamOptions {
	if options == nil {
		return types.SimpleStreamOptions{}
	}
	return *options
}

func baseOptions(options *types.ProviderStreamOptions) *types.ProviderRequestOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions.ProviderRequestOptions
}

func baseOptionsSimple(options *types.SimpleStreamOptions) *types.ProviderRequestOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions.ProviderRequestOptions
}

func context2Background() context.Context { return context.Background() }
