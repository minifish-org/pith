// This file is a Go port of packages/ai/src/images-models.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The image side of the runtime model system: an image-generation provider owns
// id/name metadata, auth, model listing and generation; ImagesModels collects
// providers, resolves auth and normalizes generation failures into an
// AssistantImages with stopReason "error".
package ai

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/ai/auth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/types"
)

// ImagesProvider is an image-generation provider: the image-side counterpart of
// Provider.
type ImagesProvider interface {
	ID() string
	Name() string
	// Auth is required: at least one of APIKey/OAuth must be present.
	Auth() authtypes.ProviderAuth
	// GetModels returns the current known models. It must not panic; ImagesModels
	// treats a panicking implementation as having no models.
	GetModels() []types.ImagesModel
	GenerateImages(model types.ImagesModel, context types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error)
}

// RefreshableImagesProvider is implemented by dynamic image providers.
type RefreshableImagesProvider interface {
	RefreshModels(ctx context.Context) error
}

// ImagesModels is the runtime collection of image-generation providers.
type ImagesModels interface {
	GetProviders() []ImagesProvider
	GetProvider(id string) ImagesProvider
	GetModels(provider ...string) []types.ImagesModel
	GetModel(provider string, id string) *types.ImagesModel

	// Refresh re-fetches dynamic providers. With a provider id it returns a
	// ModelsError ("model_source") on that provider's fetch failure; without one
	// it refreshes all providers best-effort.
	Refresh(ctx context.Context, provider ...string) error

	GetAuth(ctx context.Context, providerID string, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error)
	GetAuthForModel(ctx context.Context, model types.ImagesModel, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error)

	// GenerateImages never returns an error; failures are returned as an
	// AssistantImages with stopReason "error".
	GenerateImages(ctx context.Context, model types.ImagesModel, request types.ImagesContext, options *types.ImagesOptions) *types.AssistantImages
}

// MutableImagesModels is an ImagesModels collection that can be reconfigured.
type MutableImagesModels interface {
	ImagesModels
	SetProvider(provider ImagesProvider)
	DeleteProvider(id string)
	ClearProviders()
}

// CreateImagesProviderOptions are the parts of an image provider.
type CreateImagesProviderOptions struct {
	ID   string
	Name string
	// Auth is required — every provider has auth semantics, even ambient/keyless
	// ones.
	Auth authtypes.ProviderAuth
	// Models is the initial model list (empty for purely dynamic providers).
	Models []types.ImagesModel
	// RefreshModels fetches the current list for dynamic providers. Concurrent
	// calls share one in-flight fetch; on failure the stored list stays at its
	// last-known state and a later call retries.
	RefreshModels func(ctx context.Context) ([]types.ImagesModel, error)
	API           types.ProviderImages
}

// imagesProviderImpl is the static ImagesProvider produced by
// CreateImagesProvider.
type imagesProviderImpl struct {
	id     string
	name   string
	auth   authtypes.ProviderAuth
	api    types.ProviderImages
	mu     sync.Mutex
	models []types.ImagesModel
}

func (p *imagesProviderImpl) ID() string                   { return p.id }
func (p *imagesProviderImpl) Name() string                 { return p.name }
func (p *imagesProviderImpl) Auth() authtypes.ProviderAuth { return p.auth }

func (p *imagesProviderImpl) GetModels() []types.ImagesModel {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]types.ImagesModel(nil), p.models...)
}

func (p *imagesProviderImpl) setModels(models []types.ImagesModel) {
	p.mu.Lock()
	p.models = models
	p.mu.Unlock()
}

func (p *imagesProviderImpl) GenerateImages(model types.ImagesModel, request types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	return p.api.GenerateImages(&model, &request, options)
}

// refreshableImagesProviderImpl adds the dynamic refresh capability to an image
// provider. A provider without RefreshModels is not refreshable, matching the
// upstream optional method.
type refreshableImagesProviderImpl struct {
	*imagesProviderImpl
	refreshMu sync.Mutex
	inflight  *imagesRefreshCall
	refresh   func(ctx context.Context) ([]types.ImagesModel, error)
}

type imagesRefreshCall struct {
	done   chan struct{}
	models []types.ImagesModel
	err    error
}

// RefreshModels fetches the current model list. Concurrent callers share one
// in-flight fetch.
func (p *refreshableImagesProviderImpl) RefreshModels(ctx context.Context) error {
	p.refreshMu.Lock()
	if p.inflight != nil {
		call := p.inflight
		p.refreshMu.Unlock()
		<-call.done
		return call.err
	}
	call := &imagesRefreshCall{done: make(chan struct{})}
	p.inflight = call
	p.refreshMu.Unlock()

	models, err := p.refresh(ctx)
	if err == nil {
		p.setModels(models)
	}
	call.models = models
	call.err = err

	p.refreshMu.Lock()
	p.inflight = nil
	p.refreshMu.Unlock()
	close(call.done)
	return err
}

// CreateImagesProvider builds an image-generation provider from parts.
func CreateImagesProvider(input CreateImagesProviderOptions) ImagesProvider {
	name := input.Name
	if name == "" {
		name = input.ID
	}
	base := &imagesProviderImpl{
		id:     input.ID,
		name:   name,
		auth:   input.Auth,
		api:    input.API,
		models: append([]types.ImagesModel(nil), input.Models...),
	}
	if input.RefreshModels == nil {
		return base
	}
	return &refreshableImagesProviderImpl{imagesProviderImpl: base, refresh: input.RefreshModels}
}

// imagesModelsImpl is the MutableImagesModels implementation.
type imagesModelsImpl struct {
	providersMu sync.Mutex
	providers   map[string]ImagesProvider
	order       []string

	credentials authtypes.CredentialStore
	authContext authtypes.AuthContext
}

// CreateImagesModels builds an empty runtime image-model collection.
func CreateImagesModels(options *CreateModelsOptions) MutableImagesModels {
	credentials, _, authContext := createModelsDependencies(options)
	return &imagesModelsImpl{
		providers:   map[string]ImagesProvider{},
		credentials: credentials,
		authContext: authContext,
	}
}

func (m *imagesModelsImpl) SetProvider(provider ImagesProvider) {
	m.providersMu.Lock()
	if _, exists := m.providers[provider.ID()]; !exists {
		m.order = append(m.order, provider.ID())
	}
	m.providers[provider.ID()] = provider
	m.providersMu.Unlock()
}

func (m *imagesModelsImpl) DeleteProvider(id string) {
	m.providersMu.Lock()
	if _, exists := m.providers[id]; exists {
		delete(m.providers, id)
		for i, orderID := range m.order {
			if orderID == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.providersMu.Unlock()
}

func (m *imagesModelsImpl) ClearProviders() {
	m.providersMu.Lock()
	m.providers = map[string]ImagesProvider{}
	m.order = nil
	m.providersMu.Unlock()
}

func (m *imagesModelsImpl) GetProviders() []ImagesProvider {
	m.providersMu.Lock()
	defer m.providersMu.Unlock()
	out := make([]ImagesProvider, 0, len(m.order))
	for _, id := range m.order {
		if provider, ok := m.providers[id]; ok {
			out = append(out, provider)
		}
	}
	return out
}

func (m *imagesModelsImpl) GetProvider(id string) ImagesProvider {
	m.providersMu.Lock()
	defer m.providersMu.Unlock()
	return m.providers[id]
}

func (m *imagesModelsImpl) GetModels(provider ...string) []types.ImagesModel {
	if len(provider) > 0 {
		entry := m.GetProvider(provider[0])
		if entry == nil {
			return []types.ImagesModel{}
		}
		return safeImagesModels(entry)
	}
	models := []types.ImagesModel{}
	for _, entry := range m.GetProviders() {
		models = append(models, safeImagesModels(entry)...)
	}
	return models
}

func safeImagesModels(provider ImagesProvider) (models []types.ImagesModel) {
	defer func() {
		if recover() != nil {
			models = nil
		}
	}()
	return provider.GetModels()
}

func (m *imagesModelsImpl) GetModel(provider string, id string) *types.ImagesModel {
	for _, model := range m.GetModels(provider) {
		if model.Id == id {
			copy := model
			return &copy
		}
	}
	return nil
}

func (m *imagesModelsImpl) Refresh(ctx context.Context, provider ...string) error {
	if len(provider) > 0 {
		id := provider[0]
		entry := m.GetProvider(id)
		if entry == nil {
			return nil
		}
		refreshable, ok := entry.(RefreshableImagesProvider)
		if !ok {
			return nil
		}
		err := refreshable.RefreshModels(ctx)
		if err == nil {
			return nil
		}
		var modelsErr *auth.ModelsError
		if errors.As(err, &modelsErr) {
			return err
		}
		return auth.NewModelsError(auth.ModelsErrorModelSource, "Model refresh failed for "+id, err)
	}

	var waitGroup sync.WaitGroup
	for _, entry := range m.GetProviders() {
		refreshable, ok := entry.(RefreshableImagesProvider)
		if !ok {
			continue
		}
		waitGroup.Add(1)
		go func(refreshable RefreshableImagesProvider) {
			defer waitGroup.Done()
			_ = refreshable.RefreshModels(ctx)
		}(refreshable)
	}
	waitGroup.Wait()
	return nil
}

func (m *imagesModelsImpl) GetAuth(ctx context.Context, providerID string, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error) {
	provider := m.GetProvider(providerID)
	if provider == nil {
		return nil, nil
	}
	return auth.ResolveProviderAuth(ctx, auth.AuthProviderSpec{ID: providerID, Auth: provider.Auth()}, m.credentials, m.authContext, overrides)
}

func (m *imagesModelsImpl) GetAuthForModel(ctx context.Context, model types.ImagesModel, overrides *auth.AuthResolutionOverrides) (*authtypes.AuthResult, error) {
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

func (m *imagesModelsImpl) GenerateImages(ctx context.Context, model types.ImagesModel, request types.ImagesContext, options *types.ImagesOptions) *types.AssistantImages {
	provider := m.GetProvider(string(model.Provider))
	if provider == nil {
		return imagesErrorResult(model, "Unknown provider: "+string(model.Provider))
	}

	var apiKey *string
	var env types.ProviderEnv
	if options != nil {
		apiKey = options.APIKey
		env = options.Env
	}

	resolution, err := m.GetAuthForModel(ctx, model, &auth.AuthResolutionOverrides{APIKey: apiKey, Env: env, Signal: ctx})
	if err != nil {
		return imagesErrorResult(model, err.Error())
	}
	if resolution == nil {
		result, generateErr := provider.GenerateImages(model, request, options)
		if generateErr != nil {
			return imagesErrorResult(model, generateErr.Error())
		}
		if result == nil {
			return imagesErrorResult(model, "Image generation returned no result")
		}
		return result
	}

	requestModel := model
	if resolution.Auth.BaseURL != nil {
		requestModel.BaseUrl = *resolution.Auth.BaseURL
	}
	finalKey := apiKey
	if finalKey == nil {
		finalKey = resolution.Auth.APIKey
	}

	var headers types.ProviderHeaders
	var optionHeaders types.ProviderHeaders
	if options != nil {
		optionHeaders = options.Headers
	}
	if resolution.Auth.Headers != nil || optionHeaders != nil {
		headers = types.ProviderHeaders{}
		for name, value := range resolution.Auth.Headers {
			headers[name] = value
		}
		for name, value := range optionHeaders {
			headers[name] = value
		}
	}

	var finalEnv types.ProviderEnv
	if resolution.Env != nil || env != nil {
		finalEnv = types.ProviderEnv{}
		for name, value := range resolution.Env {
			finalEnv[name] = value
		}
		for name, value := range env {
			finalEnv[name] = value
		}
	}

	merged := &types.ImagesOptions{}
	if options != nil {
		*merged = *options
	}
	merged.APIKey = finalKey
	merged.Headers = headers
	merged.Env = finalEnv

	result, generateErr := provider.GenerateImages(requestModel, request, merged)
	if generateErr != nil {
		return imagesErrorResult(model, generateErr.Error())
	}
	if result == nil {
		return imagesErrorResult(model, "Image generation returned no result")
	}
	return result
}

func imagesErrorResult(model types.ImagesModel, message string) *types.AssistantImages {
	text := message
	return &types.AssistantImages{
		Api:          types.ImagesApi(model.Api),
		Provider:     types.ImagesProviderId(model.Provider),
		Model:        model.Id,
		Output:       []types.ImagesOutputContent{},
		StopReason:   types.ImagesStopReasonError,
		ErrorMessage: &text,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
}
