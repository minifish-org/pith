// This file is a Go port of packages/ai/src/models.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The runtime model collection: provider registration, auth application,
// transcript normalization and request dispatch. Providers own stream behavior;
// the collection resolves auth and delegates each request to the provider that
// owns the model. Refresh is generation-checked so a superseded provider refresh
// can never publish stale persistence, and cancellation is carried by
// context.Context.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/auth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// ModelsError re-exports the auth/model resolution error and its code so
// callers of this package can classify failures without importing auth.
type ModelsError = auth.ModelsError

// ModelsErrorCode re-exports the stable error discriminator.
type ModelsErrorCode = auth.ModelsErrorCode

// ModelsErrorCode values.
const (
	ModelsErrorModelSource     = auth.ModelsErrorModelSource
	ModelsErrorModelValidation = auth.ModelsErrorModelValidation
	ModelsErrorProvider        = auth.ModelsErrorProvider
	ModelsErrorStream          = auth.ModelsErrorStream
	ModelsErrorAuth            = auth.ModelsErrorAuth
	ModelsErrorOAuth           = auth.ModelsErrorOAuth
)

// ModelsPublication is one generation-checked catalog publication.
//
// Persist selects the provider-owned persisted catalog. A nil Persist with
// PersistDelete false leaves storage unchanged; PersistDelete deletes it.
// Update is the optional synchronous update of provider-private in-memory
// catalog state; it runs only after the selected persistence mutation.
type ModelsPublication struct {
	Persist       *ModelsStoreEntry
	PersistDelete bool
	Update        func()
}

// RefreshModelsContext is the context handed to a dynamic provider refresh.
type RefreshModelsContext struct {
	// Credential is the effective configured credential. OAuth credentials are
	// refreshed before network access.
	Credential authtypes.Credential
	// Stored is the immutable provider-scoped catalog snapshot captured before
	// this refresh phase.
	Stored *ModelsStoreEntry
	// Publish persists provider-selected catalog state and runs Update
	// synchronously. It reports false when the refresh was superseded or
	// aborted.
	Publish func(publication ModelsPublication) (bool, error)
	// AllowNetwork is false during offline/cache-only initialization.
	AllowNetwork bool
	// Force bypasses provider freshness checks and fetches immediately when
	// network access is allowed.
	Force *bool
	// Signal is always present, including when the public refresh caller omits
	// its optional signal.
	Signal context.Context
}

// ModelsRefreshOptions selects which providers a refresh touches.
type ModelsRefreshOptions struct {
	AllowNetwork *bool
	// Providers restricts refresh to these provider IDs. Unknown and static
	// providers are ignored.
	Providers []string
	// Force bypasses provider freshness checks and fetches immediately when
	// network access is allowed.
	Force  *bool
	Signal context.Context
}

// ModelsRefreshResult reports the outcome of a refresh. Provider errors and
// cancellation are returned here without rejecting.
type ModelsRefreshResult struct {
	Aborted bool
	Errors  map[string]error
}

// ModelsRequestTransforms are the collection-level request transforms.
type ModelsRequestTransforms struct {
	// TransformHeaders transforms fully assembled model/auth/request headers
	// before provider dispatch.
	TransformHeaders func(headers types.ProviderHeaders) (types.ProviderHeaders, error)
}

// ModelsApiStreamOptions are the full stream options plus the collection
// request transforms.
type ModelsApiStreamOptions struct {
	types.StreamOptions
	ModelsRequestTransforms
}

// ModelsSimpleStreamOptions are the simple stream options plus the collection
// request transforms.
type ModelsSimpleStreamOptions struct {
	types.SimpleStreamOptions
	ModelsRequestTransforms
}

// ModelsDeferredFetchOptions are the deferred fetch options plus the collection
// request transforms.
type ModelsDeferredFetchOptions struct {
	types.DeferredFetchOptions
	ModelsRequestTransforms
}

// ModelsDeferredCancelOptions are the deferred cancel options plus the
// collection request transforms.
type ModelsDeferredCancelOptions struct {
	types.DeferredCancelOptions
	ModelsRequestTransforms
}

// Provider is the concrete runtime unit: id/name/base metadata, auth methods,
// model listing and stream behavior.
//
// Optional capabilities are reported by the Supports* methods so an absent
// upstream method stays distinguishable from a present no-op, which a Go
// method set cannot express on its own.
type Provider interface {
	ID() string
	Name() string
	BaseURL() string
	Headers() types.ProviderHeaders

	// Auth is required: at least one of APIKey/OAuth must be present. Every
	// provider has auth semantics, even ambient/keyless ones.
	Auth() authtypes.ProviderAuth

	// GetModels returns the current known models. It must not panic; the
	// collection treats a panicking implementation as having no models.
	GetModels() []types.Model

	// RefreshModels restores the stored catalog and optionally fetches a newer
	// list. It is only invoked when SupportsRefreshModels reports true.
	RefreshModels(ctx context.Context, refresh *RefreshModelsContext) error
	// FilterModels is the optional credential-specific model availability
	// policy. It is only invoked when SupportsFilterModels reports true.
	FilterModels(models []types.Model, credential authtypes.Credential) []types.Model

	Stream(model types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream
	StreamSimple(model types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream

	// FetchDeferred and CancelDeferred are only invoked when their Supports*
	// method reports true.
	FetchDeferred(model types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error)
	CancelDeferred(model types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error

	SupportsRefreshModels() bool
	SupportsFilterModels() bool
	SupportsFetchDeferred() bool
	SupportsCancelDeferred() bool
}

// Models is the runtime collection of providers plus auth application and
// stream convenience.
type Models interface {
	GetProviders() []Provider
	GetProvider(id string) Provider
	GetModels(provider ...string) []types.Model
	GetModel(provider string, id string) *types.Model

	// GetModelsOfType returns the last-known models of one kind from one
	// provider or all providers.
	GetModelsOfType(kind string, provider ...string) []types.AnyModel
	// GetModelOfType looks up one model kind against the last-known lists.
	GetModelOfType(kind string, provider string, id string) *types.AnyModel
	// GetAllModels returns the last-known models of every kind.
	GetAllModels(provider ...string) []types.AnyModel

	Refresh(ctx context.Context, options *ModelsRefreshOptions) (*ModelsRefreshResult, error)
	CheckAuth(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) (*authtypes.AuthCheck, error)
	GetAvailable(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) ([]types.Model, error)
	// GetAvailableOfType returns the configured models of one kind.
	GetAvailableOfType(ctx context.Context, kind string, providerID string, options *authtypes.AuthOperationOptions) ([]types.AnyModel, error)
	// GetAllAvailable returns the configured models of every kind.
	GetAllAvailable(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) ([]types.AnyModel, error)

	// GetAuth resolves provider-scoped auth by provider id.
	GetAuth(ctx context.Context, providerID string, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error)
	// GetAuthForModel resolves provider auth plus the model's static headers.
	GetAuthForModel(ctx context.Context, model types.Model, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error)

	Login(ctx context.Context, providerID string, authType authtypes.AuthType, interaction authtypes.ProviderAuthInteraction) (authtypes.Credential, error)
	Logout(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) error

	Stream(ctx context.Context, model types.Model, request types.Context, options *ModelsApiStreamOptions) *types.AssistantMessageEventStream
	Complete(ctx context.Context, model types.Model, request types.Context, options *ModelsApiStreamOptions) (*types.AssistantMessage, error)
	StreamSimple(ctx context.Context, model types.Model, request types.Context, options *ModelsSimpleStreamOptions) *types.AssistantMessageEventStream
	CompleteSimple(ctx context.Context, model types.Model, request types.Context, options *ModelsSimpleStreamOptions) (*types.AssistantMessage, error)
	StreamDeferred(ctx context.Context, model types.Model, handle types.DeferredHandle, options *ModelsDeferredFetchOptions) *types.AssistantMessageEventStream
	FetchDeferred(ctx context.Context, model types.Model, handle types.DeferredHandle, options *ModelsDeferredFetchOptions) (*types.AssistantMessage, error)
	CancelDeferred(ctx context.Context, model types.Model, handle types.DeferredHandle, options *ModelsDeferredCancelOptions) error

	// Classify dispatches a structured classification to the owning provider. It
	// never returns an error: failures are represented in the result.
	Classify(ctx context.Context, model types.ClassifierModel, request types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult
}

// MutableModels is a Models collection that can be reconfigured at runtime.
type MutableModels interface {
	Models
	// SetProvider upserts/replaces by provider.ID. Provider ids are unique.
	SetProvider(provider Provider)
	DeleteProvider(id string)
	ClearProviders()
}

// CreateModelsOptions are the injectable dependencies of a Models collection.
type CreateModelsOptions struct {
	Credentials authtypes.CredentialStore
	ModelsStore ModelsStore
	AuthContext authtypes.AuthContext
}

// DeferredStreamsCapability lets an API stream module declare deferred-response
// support. The frozen types.ProviderStreams interface always declares the
// deferred methods, so createProvider consults this optional interface to
// preserve the upstream optionality. A module that omits it is treated as not
// supporting deferred responses.
type DeferredStreamsCapability interface {
	DeferredCapabilities() (fetch bool, cancel bool)
}

// CreateProviderOptions are the parts of a provider.
type CreateProviderOptions struct {
	ID      string
	Name    string
	BaseURL string
	Headers types.ProviderHeaders
	// Auth is required.
	Auth authtypes.ProviderAuth
	// Models is the static chat baseline model list (empty for purely dynamic
	// providers).
	Models []types.Model
	// AllModels is the static baseline of every model kind. When set it governs
	// the mixed catalog; Models is then only used as the chat fallback.
	AllModels []types.AnyModel
	// FetchModels fetches a dynamic chat model overlay. CreateProvider restores
	// and publishes it transactionally.
	FetchModels  func(ctx context.Context, refresh *RefreshModelsContext) ([]types.Model, error)
	FilterModels func(models []types.Model, credential authtypes.Credential) []types.Model
	// FilterAllModels is the optional credential-specific availability policy
	// across every model kind.
	FilterAllModels func(models []types.AnyModel, credential authtypes.Credential) []types.AnyModel
	// API is a single implementation for all models.
	API types.ProviderStreams
	// APIs maps the model API to its implementation for mixed-API providers.
	APIs map[types.Api]types.ProviderStreams
	// Classifiers maps a classifier API to its implementation.
	Classifiers map[types.ClassifierApi]ClassifierImplementation
}

// providerImpl is the Provider produced by CreateProvider.
type providerImpl struct {
	id      string
	name    string
	baseURL string
	headers types.ProviderHeaders
	auth    authtypes.ProviderAuth

	mu         sync.Mutex
	allBase    []types.AnyModel
	dynamicAny []types.AnyModel

	fetchModels     func(ctx context.Context, refresh *RefreshModelsContext) ([]types.Model, error)
	filterModels    func(models []types.Model, credential authtypes.Credential) []types.Model
	filterAllModels func(models []types.AnyModel, credential authtypes.Credential) []types.AnyModel

	classifiers map[types.ClassifierApi]ClassifierImplementation

	single types.ProviderStreams
	byAPI  map[types.Api]types.ProviderStreams

	supportsFetchDeferred  bool
	supportsCancelDeferred bool
}

func (p *providerImpl) ID() string                     { return p.id }
func (p *providerImpl) Name() string                   { return p.name }
func (p *providerImpl) BaseURL() string                { return p.baseURL }
func (p *providerImpl) Headers() types.ProviderHeaders { return p.headers }
func (p *providerImpl) Auth() authtypes.ProviderAuth   { return p.auth }
func (p *providerImpl) SupportsRefreshModels() bool    { return p.fetchModels != nil }
func (p *providerImpl) SupportsFilterModels() bool     { return p.filterModels != nil }
func (p *providerImpl) SupportsFilterAllModels() bool  { return p.filterAllModels != nil }
func (p *providerImpl) SupportsFetchDeferred() bool    { return p.supportsFetchDeferred }
func (p *providerImpl) SupportsCancelDeferred() bool   { return p.supportsCancelDeferred }

// GetModels returns the chat models of the merged mixed catalog.
func (p *providerImpl) GetModels() []types.Model {
	return anyModelsToChat(p.GetAllModels())
}

// GetAllModels merges the static baseline with the dynamic overlay by
// (model type, id).
func (p *providerImpl) GetAllModels() []types.AnyModel {
	p.mu.Lock()
	base := append([]types.AnyModel(nil), p.allBase...)
	dynamic := append([]types.AnyModel(nil), p.dynamicAny...)
	p.mu.Unlock()

	return mergeAnyModels(base, dynamic)
}

func (p *providerImpl) setDynamic(models []types.Model) {
	p.setDynamicAny(anyChatModels(models))
}

func (p *providerImpl) setDynamicAny(models []types.AnyModel) {
	p.mu.Lock()
	p.dynamicAny = models
	p.mu.Unlock()
}

// FilterModels applies the optional credential-specific filter.
func (p *providerImpl) FilterModels(models []types.Model, credential authtypes.Credential) []types.Model {
	if p.filterModels == nil {
		return models
	}
	return p.filterModels(models, credential)
}

// FilterAllModels applies the optional any-kind credential filter.
func (p *providerImpl) FilterAllModels(models []types.AnyModel, credential authtypes.Credential) []types.AnyModel {
	if p.filterAllModels == nil {
		return models
	}
	return p.filterAllModels(models, credential)
}

// RefreshModels restores the stored catalog and optionally fetches a newer one.
func (p *providerImpl) RefreshModels(ctx context.Context, refresh *RefreshModelsContext) error {
	if p.fetchModels == nil || refresh == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	signal := refresh.Signal
	if signal == nil {
		signal = ctx
	}

	if refresh.Stored != nil {
		restored := anyModelsForProvider(storedAnyModels(refresh.Stored), p.id)
		published, err := refresh.Publish(ModelsPublication{Update: func() { p.setDynamicAny(restored) }})
		if err != nil {
			return err
		}
		if !published {
			return nil
		}
	}
	if !refresh.AllowNetwork || signal.Err() != nil {
		return nil
	}
	refreshed, err := p.fetchModels(ctx, refresh)
	if err != nil {
		return err
	}
	if signal.Err() != nil {
		return nil
	}
	checkedAt := float64(time.Now().UnixMilli())
	_, err = refresh.Publish(ModelsPublication{
		Persist: &ModelsStoreEntry{Models: refreshed, CheckedAt: &checkedAt},
		Update:  func() { p.setDynamic(refreshed) },
	})
	return err
}

func (p *providerImpl) apiFor(model types.Model) types.ProviderStreams {
	if p.single != nil {
		return p.single
	}
	if p.byAPI != nil {
		return p.byAPI[model.Api]
	}
	return nil
}

// Stream dispatches to the API implementation for the model's api.
func (p *providerImpl) Stream(model types.Model, transcript *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	streams := p.apiFor(model)
	if streams == nil {
		modelCopy := model
		return api.LazyStream(context2Background(), &modelCopy, func(ctx context.Context) (*types.AssistantMessageEventStream, error) {
			return nil, auth.NewModelsError(
				auth.ModelsErrorStream,
				fmt.Sprintf("Provider %s has no API implementation for \"%s\"", p.id, string(model.Api)),
				nil,
			)
		})
	}
	return streams.Stream(&model, transcript, options)
}

// StreamSimple dispatches to the API implementation for the model's api.
func (p *providerImpl) StreamSimple(model types.Model, transcript *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	streams := p.apiFor(model)
	if streams == nil {
		modelCopy := model
		return api.LazyStream(context2Background(), &modelCopy, func(ctx context.Context) (*types.AssistantMessageEventStream, error) {
			return nil, auth.NewModelsError(
				auth.ModelsErrorStream,
				fmt.Sprintf("Provider %s has no API implementation for \"%s\"", p.id, string(model.Api)),
				nil,
			)
		})
	}
	return streams.StreamSimple(&model, transcript, options)
}

// FetchDeferred fetches a deferred response through the owning API.
func (p *providerImpl) FetchDeferred(model types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	if !p.supportsFetchDeferred {
		return nil, auth.NewModelsError(auth.ModelsErrorProvider, fmt.Sprintf("Provider %s does not support deferred responses", p.id), nil)
	}
	implementation := p.apiFor(model)
	if implementation == nil {
		return nil, auth.NewModelsError(auth.ModelsErrorProvider, fmt.Sprintf("Provider %s does not support deferred responses for \"%s\"", p.id, string(model.Api)), nil)
	}
	return implementation.FetchDeferred(&model, handle, options)
}

// CancelDeferred cancels a deferred response through the owning API.
func (p *providerImpl) CancelDeferred(model types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error {
	if !p.supportsCancelDeferred {
		return auth.NewModelsError(auth.ModelsErrorProvider, fmt.Sprintf("Provider %s cannot cancel deferred responses", p.id), nil)
	}
	implementation := p.apiFor(model)
	if implementation == nil {
		return auth.NewModelsError(auth.ModelsErrorProvider, fmt.Sprintf("Provider %s cannot cancel deferred responses for \"%s\"", p.id, string(model.Api)), nil)
	}
	return implementation.CancelDeferred(&model, handle, options)
}

// context2Background returns context.Background(). It exists so the provider
// dispatch call sites read explicitly that no caller context is available at
// that layer.
func context2Background() context.Context { return context.Background() }

// CreateProvider builds a provider from parts. A single API streams all models;
// an API map dispatches on model.Api, and a model whose api has no entry
// produces a stream error.
func CreateProvider(input CreateProviderOptions) Provider {
	allBase := input.AllModels
	if allBase == nil {
		allBase = anyChatModels(input.Models)
	}
	provider := &providerImpl{
		id:              input.ID,
		name:            input.Name,
		baseURL:         input.BaseURL,
		headers:         input.Headers,
		auth:            input.Auth,
		allBase:         allBase,
		fetchModels:     input.FetchModels,
		filterModels:    input.FilterModels,
		filterAllModels: input.FilterAllModels,
		classifiers:     input.Classifiers,
		single:          input.API,
		byAPI:           input.APIs,
	}
	if provider.name == "" {
		provider.name = input.ID
	}

	var streams []types.ProviderStreams
	if provider.single != nil {
		streams = append(streams, provider.single)
	} else {
		for _, implementation := range provider.byAPI {
			if implementation != nil {
				streams = append(streams, implementation)
			}
		}
	}
	for _, implementation := range streams {
		capability, ok := implementation.(DeferredStreamsCapability)
		if !ok {
			continue
		}
		fetch, cancel := capability.DeferredCapabilities()
		if fetch {
			provider.supportsFetchDeferred = true
		}
		if cancel {
			provider.supportsCancelDeferred = true
		}
	}
	return provider
}

// modelsImpl is the MutableModels implementation.
type modelsImpl struct {
	providersMu   sync.Mutex
	providers     map[string]Provider
	providerOrder []string

	credentials authtypes.CredentialStore
	modelsStore ModelsStore
	authContext authtypes.AuthContext

	refreshMu          sync.Mutex
	refreshGenerations map[string]int
	refreshCancels     map[string]context.CancelFunc

	publicationMu   sync.Mutex
	publicationSems map[string]chan struct{}
}

// CreateModels builds an empty runtime model collection.
func CreateModels(options *CreateModelsOptions) MutableModels {
	credentials, modelsStore, authContext := createModelsDependencies(options)
	return &modelsImpl{
		providers:          map[string]Provider{},
		credentials:        credentials,
		modelsStore:        modelsStore,
		authContext:        authContext,
		refreshGenerations: map[string]int{},
		refreshCancels:     map[string]context.CancelFunc{},
		publicationSems:    map[string]chan struct{}{},
	}
}

func createModelsDependencies(options *CreateModelsOptions) (authtypes.CredentialStore, ModelsStore, authtypes.AuthContext) {
	var credentials authtypes.CredentialStore = auth.NewInMemoryCredentialStore()
	var modelsStore ModelsStore = NewInMemoryModelsStore()
	var authContext authtypes.AuthContext = auth.DefaultProviderAuthContext()
	if options != nil {
		if options.Credentials != nil {
			credentials = options.Credentials
		}
		if options.ModelsStore != nil {
			modelsStore = options.ModelsStore
		}
		if options.AuthContext != nil {
			authContext = options.AuthContext
		}
	}
	return credentials, modelsStore, authContext
}

func (m *modelsImpl) SetProvider(provider Provider) {
	m.supersedeProviderRefresh(provider.ID())
	m.providersMu.Lock()
	if _, exists := m.providers[provider.ID()]; !exists {
		m.providerOrder = append(m.providerOrder, provider.ID())
	}
	m.providers[provider.ID()] = provider
	m.providersMu.Unlock()
}

func (m *modelsImpl) DeleteProvider(id string) {
	m.supersedeProviderRefresh(id)
	m.providersMu.Lock()
	if _, exists := m.providers[id]; exists {
		delete(m.providers, id)
		for i, orderID := range m.providerOrder {
			if orderID == id {
				m.providerOrder = append(m.providerOrder[:i], m.providerOrder[i+1:]...)
				break
			}
		}
	}
	m.providersMu.Unlock()
}

func (m *modelsImpl) ClearProviders() {
	m.providersMu.Lock()
	ids := append([]string(nil), m.providerOrder...)
	m.providersMu.Unlock()

	m.refreshMu.Lock()
	for id := range m.refreshCancels {
		ids = append(ids, id)
	}
	m.refreshMu.Unlock()

	for _, id := range ids {
		m.supersedeProviderRefresh(id)
	}

	m.providersMu.Lock()
	m.providers = map[string]Provider{}
	m.providerOrder = nil
	m.providersMu.Unlock()
}

func (m *modelsImpl) GetProviders() []Provider {
	m.providersMu.Lock()
	defer m.providersMu.Unlock()
	out := make([]Provider, 0, len(m.providerOrder))
	for _, id := range m.providerOrder {
		if provider, ok := m.providers[id]; ok {
			out = append(out, provider)
		}
	}
	return out
}

func (m *modelsImpl) GetProvider(id string) Provider {
	m.providersMu.Lock()
	defer m.providersMu.Unlock()
	return m.providers[id]
}

func (m *modelsImpl) GetModels(provider ...string) []types.Model {
	if len(provider) > 0 {
		entry := m.GetProvider(provider[0])
		if entry == nil {
			return []types.Model{}
		}
		return safeModels(entry)
	}
	models := []types.Model{}
	for _, entry := range m.GetProviders() {
		models = append(models, safeModels(entry)...)
	}
	return models
}

func safeModels(provider Provider) (models []types.Model) {
	defer func() {
		if recover() != nil {
			models = nil
		}
	}()
	return provider.GetModels()
}

func (m *modelsImpl) GetModel(provider string, id string) *types.Model {
	for _, model := range m.GetModels(provider) {
		if model.Id == id {
			copy := model
			return &copy
		}
	}
	return nil
}

// GetAllModels returns the last-known models of every kind from one provider or
// all providers.
func (m *modelsImpl) GetAllModels(provider ...string) []types.AnyModel {
	if len(provider) > 0 {
		entry := m.GetProvider(provider[0])
		if entry == nil {
			return []types.AnyModel{}
		}
		return safeAllModels(entry)
	}
	models := []types.AnyModel{}
	for _, entry := range m.GetProviders() {
		models = append(models, safeAllModels(entry)...)
	}
	return models
}

// safeAllModels reads a provider's mixed catalog, treating a panicking provider
// as having no models.
func safeAllModels(provider Provider) (models []types.AnyModel) {
	defer func() {
		if recover() != nil {
			models = nil
		}
	}()
	if all, ok := provider.(AllModelsProvider); ok {
		return all.GetAllModels()
	}
	return anyChatModels(provider.GetModels())
}

// GetModelsOfType returns the last-known models of one kind.
func (m *modelsImpl) GetModelsOfType(kind string, provider ...string) []types.AnyModel {
	return filterAnyModelsByType(m.GetAllModels(provider...), kind)
}

// GetModelOfType looks up one model kind by id.
func (m *modelsImpl) GetModelOfType(kind string, provider string, id string) *types.AnyModel {
	for _, model := range m.GetModelsOfType(kind, provider) {
		identity, ok := identityOf(model)
		if ok && identity.id == id {
			copy := model
			return &copy
		}
	}
	return nil
}

func (m *modelsImpl) supersedeProviderRefresh(providerID string) int {
	m.refreshMu.Lock()
	generation := m.refreshGenerations[providerID] + 1
	m.refreshGenerations[providerID] = generation
	cancel := m.refreshCancels[providerID]
	delete(m.refreshCancels, providerID)
	m.refreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return generation
}

func (m *modelsImpl) beginProviderRefresh(providerID string, parent context.Context) (int, context.Context, context.CancelFunc) {
	generation := m.supersedeProviderRefresh(providerID)
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.refreshMu.Lock()
	m.refreshCancels[providerID] = cancel
	m.refreshMu.Unlock()
	return generation, ctx, cancel
}

func (m *modelsImpl) finishProviderRefresh(providerID string, generation int, cancel context.CancelFunc) {
	m.refreshMu.Lock()
	if m.refreshGenerations[providerID] == generation {
		delete(m.refreshCancels, providerID)
	}
	m.refreshMu.Unlock()
	cancel()
}

func (m *modelsImpl) currentGeneration(providerID string) int {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	return m.refreshGenerations[providerID]
}

func (m *modelsImpl) publicationSem(providerID string) chan struct{} {
	m.publicationMu.Lock()
	defer m.publicationMu.Unlock()
	if m.publicationSems == nil {
		m.publicationSems = map[string]chan struct{}{}
	}
	sem, ok := m.publicationSems[providerID]
	if !ok {
		sem = make(chan struct{}, 1)
		m.publicationSems[providerID] = sem
	}
	return sem
}

func (m *modelsImpl) publishProviderModels(providerID string, generation int, signal context.Context, publication ModelsPublication) (bool, error) {
	sem := m.publicationSem(providerID)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-signal.Done():
		return false, abortErr(signal)
	}
	if err := signal.Err(); err != nil {
		return false, abortErr(signal)
	}
	if m.currentGeneration(providerID) != generation {
		return false, nil
	}

	if publication.PersistDelete {
		if err := m.modelsStore.Delete(signal, providerID, &ModelsStoreOperationOptions{Signal: signal}); err != nil {
			return false, err
		}
	} else if publication.Persist != nil {
		if err := m.modelsStore.Write(signal, providerID, publication.Persist, &ModelsStoreOperationOptions{Signal: signal}); err != nil {
			return false, err
		}
	}

	if err := signal.Err(); err != nil {
		return false, abortErr(signal)
	}
	if m.currentGeneration(providerID) != generation {
		return false, nil
	}
	if publication.Update != nil {
		publication.Update()
	}
	return true, nil
}

func (m *modelsImpl) runProviderRefreshPhase(provider Provider, credential authtypes.Credential, allowNetwork bool, force *bool, generation int, signal context.Context) error {
	stored, err := m.modelsStore.Read(signal, provider.ID(), &ModelsStoreOperationOptions{Signal: signal})
	if err != nil {
		return err
	}
	var storedClone *ModelsStoreEntry
	if stored != nil {
		storedClone = stored.Clone()
	}
	refresh := &RefreshModelsContext{
		Credential: credential,
		Stored:     storedClone,
		Publish: func(publication ModelsPublication) (bool, error) {
			return m.publishProviderModels(provider.ID(), generation, signal, publication)
		},
		AllowNetwork: allowNetwork,
		Force:        force,
		Signal:       signal,
	}
	return provider.RefreshModels(signal, refresh)
}

func (m *modelsImpl) Refresh(ctx context.Context, options *ModelsRefreshOptions) (*ModelsRefreshResult, error) {
	if options == nil {
		options = &ModelsRefreshOptions{}
	}
	allowNetwork := true
	if options.AllowNetwork != nil {
		allowNetwork = *options.AllowNetwork
	}
	callerSignal := utils.OperationSignal(ctx)
	if callerSignal.Err() != nil {
		return &ModelsRefreshResult{Aborted: true, Errors: map[string]error{}}, nil
	}

	var selected map[string]bool
	if options.Providers != nil {
		selected = make(map[string]bool, len(options.Providers))
		for _, id := range options.Providers {
			selected[id] = true
		}
	}

	var refreshable []Provider
	for _, provider := range m.GetProviders() {
		if !provider.SupportsRefreshModels() {
			continue
		}
		if selected != nil && !selected[provider.ID()] {
			continue
		}
		refreshable = append(refreshable, provider)
	}

	var errorsMu sync.Mutex
	errorsMap := map[string]error{}
	var waitGroup sync.WaitGroup
	for _, provider := range refreshable {
		waitGroup.Add(1)
		go func(provider Provider) {
			defer waitGroup.Done()
			generation, signal, cancel := m.beginProviderRefresh(provider.ID(), callerSignal)
			defer m.finishProviderRefresh(provider.ID(), generation, cancel)

			operation := func() error {
				storedCredential, credentialError := m.readCredential(signal, provider.ID())

				// Restore cached provider state before auth resolution or network
				// access.
				if err := m.runProviderRefreshPhase(provider, storedCredential, false, nil, generation, signal); err != nil {
					return err
				}
				if credentialError != nil {
					return credentialError
				}
				if !allowNetwork || signal.Err() != nil {
					return nil
				}

				credential, err := m.resolveRefreshCredential(provider, storedCredential, signal)
				if err != nil {
					return err
				}
				if credential == nil {
					return nil
				}
				return m.runProviderRefreshPhase(provider, credential, true, options.Force, generation, signal)
			}

			_, err := utils.RaceWithAbortSignal(func() (struct{}, error) {
				return struct{}{}, operation()
			}, signal)
			if err != nil && signal.Err() == nil {
				errorsMu.Lock()
				errorsMap[provider.ID()] = wrapRefreshError(provider.ID(), err)
				errorsMu.Unlock()
			}
		}(provider)
	}

	done := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-callerSignal.Done():
	}

	errorsMu.Lock()
	snapshot := make(map[string]error, len(errorsMap))
	for providerID, err := range errorsMap {
		snapshot[providerID] = err
	}
	errorsMu.Unlock()
	return &ModelsRefreshResult{Aborted: callerSignal.Err() != nil, Errors: snapshot}, nil
}

func wrapRefreshError(providerID string, err error) error {
	var modelsErr *auth.ModelsError
	if errors.As(err, &modelsErr) {
		return err
	}
	return auth.NewModelsError(auth.ModelsErrorModelSource, "Model refresh failed for "+providerID, err)
}

func (m *modelsImpl) resolveRefreshCredential(provider Provider, stored authtypes.Credential, signal context.Context) (authtypes.Credential, error) {
	if oauthStored, ok := stored.(*authtypes.OAuthCredential); ok && oauthStored != nil {
		oauth := provider.Auth().OAuth
		if oauth == nil {
			return nil, nil
		}
		if auth.NowMs() < oauthStored.Expires {
			return stored, nil
		}
		if signal.Err() != nil {
			return nil, nil
		}
		post, err := m.credentials.Modify(signal, provider.ID(), func(current authtypes.Credential) (authtypes.Credential, error) {
			currentOAuth, ok := current.(*authtypes.OAuthCredential)
			if !ok || auth.NowMs() < currentOAuth.Expires {
				return nil, nil
			}
			if oauth.Refresh == nil {
				return nil, fmt.Errorf("oauth flow has no refresh")
			}
			return oauth.Refresh(signal, currentOAuth)
		}, &authtypes.AuthOperationOptions{Signal: signal})
		if err != nil {
			return nil, err
		}
		if postOAuth, ok := post.(*authtypes.OAuthCredential); ok && postOAuth != nil {
			return postOAuth, nil
		}
		return nil, nil
	}

	apiKey := provider.Auth().APIKey
	if apiKey == nil {
		return nil, nil
	}
	var credential *authtypes.ApiKeyCredential
	if apiKeyStored, ok := stored.(*authtypes.ApiKeyCredential); ok {
		credential = apiKeyStored
	}
	if apiKey.Resolve == nil {
		return nil, nil
	}
	result, err := apiKey.Resolve(signal, authtypes.ApiKeyResolveInput{Ctx: m.authContext, Credential: credential, Signal: signal})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Key: result.Auth.APIKey, Env: result.Env}, nil
}

func (m *modelsImpl) readCredential(signal context.Context, providerID string) (authtypes.Credential, error) {
	credential, err := m.credentials.Read(signal, providerID, &authtypes.AuthOperationOptions{Signal: signal})
	if err != nil {
		return nil, auth.NewModelsError(auth.ModelsErrorAuth, "Credential store read failed for "+providerID, err)
	}
	return credential, nil
}

func (m *modelsImpl) checkProviderAuth(provider Provider, credential authtypes.Credential, signal context.Context) (*authtypes.AuthCheck, error) {
	if oauthCredential, ok := credential.(*authtypes.OAuthCredential); ok && oauthCredential != nil {
		if provider.Auth().OAuth == nil {
			return nil, nil
		}
		source := "OAuth"
		return &authtypes.AuthCheck{Source: &source, Type: authtypes.AuthTypeOAuth}, nil
	}
	apiKey := provider.Auth().APIKey
	if apiKey == nil {
		return nil, nil
	}
	if apiKey.Check != nil {
		var credentialInput *authtypes.ApiKeyCredential
		if apiKeyCredential, ok := credential.(*authtypes.ApiKeyCredential); ok {
			credentialInput = apiKeyCredential
		}
		check, err := apiKey.Check(signal, authtypes.ApiKeyCheckInput{Ctx: m.authContext, Credential: credentialInput, Signal: signal})
		if err != nil {
			return nil, auth.NewModelsError(auth.ModelsErrorAuth, fmt.Sprintf("API key auth check failed for provider %s", provider.ID()), err)
		}
		return check, nil
	}
	resolution, err := auth.ResolveProviderAuth(signal, auth.AuthProviderSpec{ID: provider.ID(), Auth: provider.Auth()}, m.credentials, m.authContext, &auth.AuthResolutionOverrides{Signal: signal})
	if err != nil {
		return nil, err
	}
	if resolution == nil {
		return nil, nil
	}
	return &authtypes.AuthCheck{Source: resolution.Source, Type: authtypes.AuthTypeAPIKey}, nil
}

func (m *modelsImpl) CheckAuth(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) (*authtypes.AuthCheck, error) {
	signal := operationSignal(ctx, options)
	operation := func() (*authtypes.AuthCheck, error) {
		if err := signal.Err(); err != nil {
			return nil, err
		}
		provider := m.GetProvider(providerID)
		if provider == nil {
			return nil, nil
		}
		credential, err := m.readCredential(signal, providerID)
		if err != nil {
			return nil, err
		}
		return m.checkProviderAuth(provider, credential, signal)
	}
	return utils.RaceWithAbortSignal(operation, signal)
}

func (m *modelsImpl) GetAvailable(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) ([]types.Model, error) {
	signal := operationSignal(ctx, options)
	operation := func() ([]types.Model, error) {
		if err := signal.Err(); err != nil {
			return nil, err
		}
		var providers []Provider
		if providerID != "" {
			if provider := m.GetProvider(providerID); provider != nil {
				providers = []Provider{provider}
			}
		} else {
			providers = m.GetProviders()
		}
		available := []types.Model{}
		for _, provider := range providers {
			credential, err := m.readCredential(signal, provider.ID())
			if err != nil {
				return nil, err
			}
			check, err := m.checkProviderAuth(provider, credential, signal)
			if err != nil {
				return nil, err
			}
			if check == nil {
				continue
			}
			models := provider.GetModels()
			if provider.SupportsFilterModels() {
				models = provider.FilterModels(models, credential)
			}
			if models == nil {
				models = []types.Model{}
			}
			available = append(available, models...)
		}
		return available, nil
	}
	return utils.RaceWithAbortSignal(operation, signal)
}

// GetAllAvailable returns the configured models of every kind.
func (m *modelsImpl) GetAllAvailable(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) ([]types.AnyModel, error) {
	signal := operationSignal(ctx, options)
	operation := func() ([]types.AnyModel, error) {
		if err := signal.Err(); err != nil {
			return nil, err
		}
		var providers []Provider
		if providerID != "" {
			if provider := m.GetProvider(providerID); provider != nil {
				providers = []Provider{provider}
			}
		} else {
			providers = m.GetProviders()
		}
		available := []types.AnyModel{}
		for _, provider := range providers {
			credential, err := m.readCredential(signal, provider.ID())
			if err != nil {
				return nil, err
			}
			check, err := m.checkProviderAuth(provider, credential, signal)
			if err != nil {
				return nil, err
			}
			if check == nil {
				continue
			}
			models := safeAllModels(provider)
			if filterAll, ok := provider.(FilterAllModelsProvider); ok {
				models = filterAll.FilterAllModels(models, credential)
			} else if provider.SupportsFilterModels() {
				// Default policy: apply the chat filter to chat models and keep
				// every other kind.
				availableChatIds := map[string]bool{}
				for _, model := range provider.FilterModels(provider.GetModels(), credential) {
					availableChatIds[model.Id] = true
				}
				filtered := make([]types.AnyModel, 0, len(models))
				for _, model := range models {
					identity, _ := identityOf(model)
					if identity.kind != types.ModelTypeChat || availableChatIds[identity.id] {
						filtered = append(filtered, model)
					}
				}
				models = filtered
			}
			if models == nil {
				models = []types.AnyModel{}
			}
			available = append(available, models...)
		}
		return available, nil
	}
	return utils.RaceWithAbortSignal(operation, signal)
}

// GetAvailableOfType returns the configured models of one kind.
func (m *modelsImpl) GetAvailableOfType(ctx context.Context, kind string, providerID string, options *authtypes.AuthOperationOptions) ([]types.AnyModel, error) {
	available, err := m.GetAllAvailable(ctx, providerID, options)
	if err != nil {
		return nil, err
	}
	return filterAnyModelsByType(available, kind), nil
}

func (m *modelsImpl) GetAuth(ctx context.Context, providerID string, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error) {
	provider := m.GetProvider(providerID)
	if provider == nil {
		return nil, nil
	}
	signal := utils.OperationSignal(ctx)
	if overrides != nil && overrides.Signal != nil {
		signal = overrides.Signal
	}
	effective := auth.AuthResolutionOverrides{}
	if overrides != nil {
		effective = *overrides
	}
	effective.Signal = signal
	return auth.ResolveProviderAuth(ctx, auth.AuthProviderSpec{ID: providerID, Auth: provider.Auth()}, m.credentials, m.authContext, &effective)
}

func (m *modelsImpl) GetAuthForModel(ctx context.Context, model types.Model, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error) {
	result, err := m.GetAuth(ctx, string(model.Provider), overrides)
	if err != nil || result == nil {
		return result, err
	}
	if len(model.Headers) == 0 {
		return result, nil
	}
	merged := mergeHeaders(result.Auth.Headers, providerHeadersFromStrings(model.Headers))
	authCopy := result.Auth
	authCopy.Headers = merged
	return &authtypes.AuthResult{Auth: authCopy, Env: result.Env, Source: result.Source}, nil
}

func (m *modelsImpl) Login(ctx context.Context, providerID string, authType authtypes.AuthType, interaction authtypes.ProviderAuthInteraction) (authtypes.Credential, error) {
	signal := utils.OperationSignal(ctx)
	if interaction != nil && interaction.Signal() != nil {
		signal = interaction.Signal()
	}
	if err := signal.Err(); err != nil {
		return nil, abortErr(signal)
	}
	provider := m.GetProvider(providerID)
	if provider == nil {
		return nil, auth.NewModelsError(auth.ModelsErrorProvider, "Unknown provider: "+providerID, nil)
	}
	login, err := providerLoginMethod(provider, authType)
	if err != nil {
		return nil, err
	}
	credential, err := utils.RaceWithAbortSignal(func() (authtypes.Credential, error) {
		return login(signal, interaction)
	}, signal)
	if err != nil {
		return nil, err
	}

	// Persist through the serialized store. If the signal aborts before the
	// mutation starts the login fails; once it has started the mutation is
	// awaited so a completed login is never reported as cancelled.
	started := make(chan struct{})
	var startedOnce sync.Once
	type modifyOutcome struct {
		credential authtypes.Credential
		err        error
	}
	outcomes := make(chan modifyOutcome, 1)
	go func() {
		modified, modifyErr := m.credentials.Modify(signal, providerID, func(current authtypes.Credential) (authtypes.Credential, error) {
			startedOnce.Do(func() { close(started) })
			return credential, nil
		}, &authtypes.AuthOperationOptions{Signal: signal})
		outcomes <- modifyOutcome{credential: modified, err: modifyErr}
	}()

	handle := func(outcome modifyOutcome) (authtypes.Credential, error) {
		if outcome.err != nil {
			if err := signal.Err(); err != nil {
				return nil, abortErr(signal)
			}
			return nil, auth.NewModelsError(auth.ModelsErrorAuth, "Credential store modify failed for "+providerID, outcome.err)
		}
		return credential, nil
	}

	select {
	case <-started:
		return handle(<-outcomes)
	case outcome := <-outcomes:
		if outcome.err != nil {
			return handle(outcome)
		}
		return credential, nil
	case <-signal.Done():
		select {
		case <-started:
			return handle(<-outcomes)
		default:
			return nil, abortErr(signal)
		}
	}
}

func providerLoginMethod(provider Provider, authType authtypes.AuthType) (func(context.Context, authtypes.ProviderAuthInteraction) (authtypes.Credential, error), error) {
	authConfig := provider.Auth()
	if authType == authtypes.AuthTypeOAuth {
		if authConfig.OAuth == nil || authConfig.OAuth.Login == nil {
			return nil, auth.NewModelsError(auth.ModelsErrorAuth, provider.Name()+" does not support oauth login", nil)
		}
		oauth := authConfig.OAuth
		return func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (authtypes.Credential, error) {
			return oauth.Login(ctx, interaction)
		}, nil
	}
	if authConfig.APIKey == nil || authConfig.APIKey.Login == nil {
		return nil, auth.NewModelsError(auth.ModelsErrorAuth, provider.Name()+" does not support api_key login", nil)
	}
	apiKey := authConfig.APIKey
	return func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (authtypes.Credential, error) {
		return apiKey.Login(ctx, interaction)
	}, nil
}

func (m *modelsImpl) Logout(ctx context.Context, providerID string, options *authtypes.AuthOperationOptions) error {
	signal := operationSignal(ctx, options)
	if err := signal.Err(); err != nil {
		return abortErr(signal)
	}
	if err := m.credentials.Delete(signal, providerID, &authtypes.AuthOperationOptions{Signal: signal}); err != nil {
		if err := signal.Err(); err != nil {
			return abortErr(signal)
		}
		return auth.NewModelsError(auth.ModelsErrorAuth, "Credential store delete failed for "+providerID, err)
	}
	return nil
}

// operationSignal picks the effective context for an auth operation.
func operationSignal(ctx context.Context, options *authtypes.AuthOperationOptions) context.Context {
	if options != nil && options.Signal != nil {
		return options.Signal
	}
	return utils.OperationSignal(ctx)
}

func abortErr(signal context.Context) error {
	if signal != nil {
		if err := signal.Err(); err != nil {
			return err
		}
	}
	return context.Canceled
}

// applyAuth resolves auth for a model request and merges the request options.
// Explicit request options win per field; the collection-only transform runs
// last.
func (m *modelsImpl) applyAuth(ctx context.Context, model types.Model, base *types.ProviderRequestOptions, transform func(types.ProviderHeaders) (types.ProviderHeaders, error)) (types.Model, *types.ProviderRequestOptions, error) {
	if m.GetProvider(string(model.Provider)) == nil {
		return model, nil, auth.NewModelsError(auth.ModelsErrorProvider, "Unknown provider: "+string(model.Provider), nil)
	}

	var apiKey *string
	var env types.ProviderEnv
	var headers types.ProviderHeaders
	if base != nil {
		apiKey = base.APIKey
		env = base.Env
		headers = base.Headers
	}
	resolution, err := m.GetAuthForModel(ctx, model, &auth.AuthResolutionOverrides{APIKey: apiKey, Env: env, Signal: ctx})
	if err != nil {
		return model, nil, err
	}
	if resolution == nil {
		return model, nil, auth.NewModelsError(auth.ModelsErrorAuth, "Provider is not configured: "+string(model.Provider), nil)
	}

	requestAuth := resolution.Auth
	finalKey := apiKey
	if finalKey == nil {
		finalKey = requestAuth.APIKey
	}
	mergedHeaders := mergeHeaders(requestAuth.Headers, headers)
	if transform != nil {
		candidate := mergedHeaders
		if candidate == nil {
			candidate = types.ProviderHeaders{}
		}
		mergedHeaders, err = transform(candidate)
		if err != nil {
			return model, nil, err
		}
	}
	finalEnv := mergeProviderEnv(resolution.Env, env)

	requestModel := model
	if requestAuth.BaseURL != nil {
		requestModel.BaseUrl = *requestAuth.BaseURL
	}
	out := &types.ProviderRequestOptions{}
	if base != nil {
		*out = *base
	}
	out.APIKey = finalKey
	out.Headers = mergedHeaders
	out.Env = finalEnv
	return requestModel, out, nil
}

func streamAuthInput(options *ModelsApiStreamOptions) (*types.ProviderRequestOptions, func(types.ProviderHeaders) (types.ProviderHeaders, error)) {
	if options == nil {
		return nil, nil
	}
	return &options.StreamOptions.ProviderRequestOptions, options.TransformHeaders
}

func simpleAuthInput(options *ModelsSimpleStreamOptions) (*types.ProviderRequestOptions, func(types.ProviderHeaders) (types.ProviderHeaders, error)) {
	if options == nil {
		return nil, nil
	}
	return &options.StreamOptions.ProviderRequestOptions, options.TransformHeaders
}

func deferredFetchAuthInput(options *ModelsDeferredFetchOptions) (*types.ProviderRequestOptions, func(types.ProviderHeaders) (types.ProviderHeaders, error)) {
	if options == nil {
		return nil, nil
	}
	return &options.DeferredFetchOptions.ProviderRequestOptions, options.TransformHeaders
}

func deferredCancelAuthInput(options *ModelsDeferredCancelOptions) (*types.ProviderRequestOptions, func(types.ProviderHeaders) (types.ProviderHeaders, error)) {
	if options == nil {
		return nil, nil
	}
	return &options.DeferredCancelOptions.ProviderRequestOptions, options.TransformHeaders
}

func (m *modelsImpl) Stream(ctx context.Context, model types.Model, request types.Context, options *ModelsApiStreamOptions) *types.AssistantMessageEventStream {
	transcript := utils.NormalizeContext(request)
	return api.LazyStream(ctx, &model, func(setupCtx context.Context) (*types.AssistantMessageEventStream, error) {
		base, transform := streamAuthInput(options)
		requestModel, requestOptions, err := m.applyAuth(setupCtx, model, base, transform)
		if err != nil {
			return nil, err
		}
		provider := m.GetProvider(string(model.Provider))
		if provider == nil {
			return nil, auth.NewModelsError(auth.ModelsErrorProvider, "Unknown provider: "+string(model.Provider), nil)
		}
		streamOptions := types.StreamOptions{}
		if options != nil {
			streamOptions = options.StreamOptions
		}
		streamOptions.ProviderRequestOptions = *requestOptions
		return provider.Stream(requestModel, transcript, &streamOptions), nil
	})
}

func (m *modelsImpl) Complete(ctx context.Context, model types.Model, request types.Context, options *ModelsApiStreamOptions) (*types.AssistantMessage, error) {
	result, err := m.Stream(ctx, model, request, options).Result(ctx)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (m *modelsImpl) StreamSimple(ctx context.Context, model types.Model, request types.Context, options *ModelsSimpleStreamOptions) *types.AssistantMessageEventStream {
	transcript := utils.NormalizeContext(request)
	return api.LazyStream(ctx, &model, func(setupCtx context.Context) (*types.AssistantMessageEventStream, error) {
		base, transform := simpleAuthInput(options)
		requestModel, requestOptions, err := m.applyAuth(setupCtx, model, base, transform)
		if err != nil {
			return nil, err
		}
		provider := m.GetProvider(string(model.Provider))
		if provider == nil {
			return nil, auth.NewModelsError(auth.ModelsErrorProvider, "Unknown provider: "+string(model.Provider), nil)
		}
		simpleOptions := types.SimpleStreamOptions{}
		if options != nil {
			simpleOptions = options.SimpleStreamOptions
		}
		simpleOptions.ProviderRequestOptions = *requestOptions
		return provider.StreamSimple(requestModel, transcript, &simpleOptions), nil
	})
}

func (m *modelsImpl) CompleteSimple(ctx context.Context, model types.Model, request types.Context, options *ModelsSimpleStreamOptions) (*types.AssistantMessage, error) {
	result, err := m.StreamSimple(ctx, model, request, options).Result(ctx)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (m *modelsImpl) StreamDeferred(ctx context.Context, model types.Model, handle types.DeferredHandle, options *ModelsDeferredFetchOptions) *types.AssistantMessageEventStream {
	return api.LazyStream(ctx, &model, func(setupCtx context.Context) (*types.AssistantMessageEventStream, error) {
		provider := m.GetProvider(string(model.Provider))
		if provider == nil {
			return nil, auth.NewModelsError(auth.ModelsErrorProvider, "Unknown provider: "+string(model.Provider), nil)
		}
		if !provider.SupportsFetchDeferred() {
			return nil, auth.NewModelsError(auth.ModelsErrorProvider, "Provider "+string(model.Provider)+" does not support deferred responses", nil)
		}
		base, transform := deferredFetchAuthInput(options)
		requestModel, requestOptions, err := m.applyAuth(setupCtx, model, base, transform)
		if err != nil {
			return nil, err
		}
		fetchOptions := types.DeferredFetchOptions{}
		if options != nil {
			fetchOptions = options.DeferredFetchOptions
		}
		fetchOptions.ProviderRequestOptions = *requestOptions
		return provider.FetchDeferred(requestModel, handle, &fetchOptions)
	})
}

func (m *modelsImpl) FetchDeferred(ctx context.Context, model types.Model, handle types.DeferredHandle, options *ModelsDeferredFetchOptions) (*types.AssistantMessage, error) {
	result, err := m.StreamDeferred(ctx, model, handle, options).Result(ctx)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (m *modelsImpl) CancelDeferred(ctx context.Context, model types.Model, handle types.DeferredHandle, options *ModelsDeferredCancelOptions) error {
	provider := m.GetProvider(string(model.Provider))
	if provider == nil {
		return auth.NewModelsError(auth.ModelsErrorProvider, "Unknown provider: "+string(model.Provider), nil)
	}
	if !provider.SupportsCancelDeferred() {
		return auth.NewModelsError(auth.ModelsErrorProvider, "Provider "+string(model.Provider)+" does not support deferred responses", nil)
	}
	base, transform := deferredCancelAuthInput(options)
	requestModel, requestOptions, err := m.applyAuth(ctx, model, base, transform)
	if err != nil {
		return err
	}
	cancelOptions := types.DeferredCancelOptions{}
	if options != nil {
		cancelOptions = options.DeferredCancelOptions
	}
	cancelOptions.ProviderRequestOptions = *requestOptions
	return provider.CancelDeferred(requestModel, handle, &cancelOptions)
}

// mergeHeaders merges override over base case-insensitively. It returns nil
// when both inputs are nil.
func mergeHeaders(base, override types.ProviderHeaders) types.ProviderHeaders {
	if base == nil && override == nil {
		return nil
	}
	merged := types.ProviderHeaders{}
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range override {
		lowerName := strings.ToLower(name)
		for existing := range merged {
			if strings.ToLower(existing) == lowerName {
				delete(merged, existing)
			}
		}
		merged[name] = value
	}
	return merged
}

func mergeProviderEnv(base, overlay types.ProviderEnv) types.ProviderEnv {
	if base == nil && overlay == nil {
		return nil
	}
	merged := types.ProviderEnv{}
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range overlay {
		merged[name] = value
	}
	return merged
}

func providerHeadersFromStrings(headers map[string]string) types.ProviderHeaders {
	if headers == nil {
		return nil
	}
	out := types.ProviderHeaders{}
	for name, value := range headers {
		copied := value
		out[name] = &copied
	}
	return out
}

func anyModelsForProvider(models []types.AnyModel, providerID string) []types.AnyModel {
	out := []types.AnyModel{}
	for _, model := range models {
		identity, ok := identityOf(model)
		if !ok || identity.provider != providerID {
			continue
		}
		out = append(out, model)
	}
	return out
}

// storedAnyModels decodes the every-kind view of a stored catalog, preserving
// the verbatim JSON elements so classifier/image entries survive a restart.
func storedAnyModels(entry *ModelsStoreEntry) []types.AnyModel {
	if entry == nil {
		return nil
	}
	if entry.rawModels != nil {
		out := make([]types.AnyModel, 0, len(entry.rawModels))
		for _, raw := range entry.rawModels {
			var model types.AnyModel
			if err := json.Unmarshal(raw, &model); err == nil {
				out = append(out, model)
			}
		}
		return out
	}
	return anyChatModels(entry.Models)
}

// CalculateCost fills usage.Cost from the model's rates, applying the highest
// matching request-wide tier and doubling base input for 1h cache writes. It
// mutates usage.Cost and returns it.
func CalculateCost(model types.Model, usage *types.Usage) types.UsageCost {
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost.ModelCostRates
	matchedThreshold := -1.0
	for _, tier := range model.Cost.Tiers {
		if inputTokens > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
			rates = tier.ModelCostRates
			matchedThreshold = tier.InputTokensAbove
		}
	}

	longWrite := 0.0
	if usage.CacheWrite1h != nil {
		longWrite = *usage.CacheWrite1h
	}
	shortWrite := usage.CacheWrite - longWrite

	usage.Cost.Input = (rates.Input / 1000000) * usage.Input
	usage.Cost.Output = (rates.Output / 1000000) * usage.Output
	usage.Cost.CacheRead = (rates.CacheRead / 1000000) * usage.CacheRead
	usage.Cost.CacheWrite = (rates.CacheWrite*shortWrite + rates.Input*2*longWrite) / 1000000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage.Cost
}

var extendedThinkingLevels = []types.ModelThinkingLevel{
	types.ThinkingOff,
	types.ThinkingMinimal,
	types.ThinkingLow,
	types.ThinkingMedium,
	types.ThinkingHigh,
	types.ThinkingXHigh,
	types.ThinkingMax,
}

// GetSupportedThinkingLevels returns the thinking levels a model accepts, in
// ascending order. A non-reasoning model only supports "off".
func GetSupportedThinkingLevels(model types.Model) []types.ModelThinkingLevel {
	if !model.Reasoning {
		return []types.ModelThinkingLevel{types.ThinkingOff}
	}
	levels := []types.ModelThinkingLevel{}
	for _, level := range extendedThinkingLevels {
		_, present, supported := model.ThinkingLevelMap.Lookup(level)
		if present && !supported {
			continue
		}
		if (level == types.ThinkingXHigh || level == types.ThinkingMax) && !present {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

// ClampThinkingLevel clamps level to the nearest supported thinking level.
func ClampThinkingLevel(model types.Model, level types.ModelThinkingLevel) types.ModelThinkingLevel {
	availableLevels := GetSupportedThinkingLevels(model)
	if containsThinkingLevel(availableLevels, level) {
		return level
	}
	requestedIndex := indexOfThinkingLevel(extendedThinkingLevels, level)
	if requestedIndex == -1 {
		if len(availableLevels) > 0 {
			return availableLevels[0]
		}
		return types.ThinkingOff
	}
	for i := requestedIndex; i < len(extendedThinkingLevels); i++ {
		if containsThinkingLevel(availableLevels, extendedThinkingLevels[i]) {
			return extendedThinkingLevels[i]
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		if containsThinkingLevel(availableLevels, extendedThinkingLevels[i]) {
			return extendedThinkingLevels[i]
		}
	}
	if len(availableLevels) > 0 {
		return availableLevels[0]
	}
	return types.ThinkingOff
}

func containsThinkingLevel(levels []types.ModelThinkingLevel, level types.ModelThinkingLevel) bool {
	for _, candidate := range levels {
		if candidate == level {
			return true
		}
	}
	return false
}

func indexOfThinkingLevel(levels []types.ModelThinkingLevel, level types.ModelThinkingLevel) int {
	for i, candidate := range levels {
		if candidate == level {
			return i
		}
	}
	return -1
}

// ModelsAreEqual reports whether two models share a type, id and provider. It
// accepts the same representations as identityOf. A nil or unrecognized model
// is never equal to anything.
func ModelsAreEqual(a any, b any) bool {
	left, ok := identityOf(a)
	if !ok {
		return false
	}
	right, ok := identityOf(b)
	if !ok {
		return false
	}
	return left.kind == right.kind && left.id == right.id && left.provider == right.provider
}

// HasApi reports whether a chat model uses the given api. Non-chat models never
// match, even when their api id is equal.
func HasApi(model any, api types.Api) bool {
	identity, ok := identityOf(model)
	if !ok {
		return false
	}
	return identity.kind == types.ModelTypeChat && identity.api == string(api)
}
