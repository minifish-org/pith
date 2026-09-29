// This file is a Go port of packages/ai/src/providers/radius.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/auth"
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// RadiusProviderOptions configure the Radius gateway provider.
//
// Ports `RadiusProviderOptions` from packages/ai/src/providers/radius.ts.
type RadiusProviderOptions struct {
	ID      string
	Name    string
	Gateway string
}

// radiusProviderImpl is the Radius gateway provider with a persisted,
// dynamically refreshed catalog.
type radiusProviderImpl struct {
	id      string
	name    string
	gateway string
	auth    authtypes.ProviderAuth
	streams types.ProviderStreams

	baseline []types.Model

	mu      sync.Mutex
	dynamic []types.Model
}

func (p *radiusProviderImpl) ID() string                     { return p.id }
func (p *radiusProviderImpl) Name() string                   { return p.name }
func (p *radiusProviderImpl) BaseURL() string                { return "" }
func (p *radiusProviderImpl) Headers() types.ProviderHeaders { return nil }
func (p *radiusProviderImpl) Auth() authtypes.ProviderAuth   { return p.auth }

func (p *radiusProviderImpl) GetModels() []types.Model {
	p.mu.Lock()
	merged := append([]types.Model(nil), p.baseline...)
	dynamic := append([]types.Model(nil), p.dynamic...)
	p.mu.Unlock()
	for _, model := range dynamic {
		index := -1
		for i := range merged {
			if merged[i].Id == model.Id {
				index = i
				break
			}
		}
		if index >= 0 {
			merged[index] = model
		} else {
			merged = append(merged, model)
		}
	}
	return merged
}

func (p *radiusProviderImpl) setDynamic(models []types.Model) {
	p.mu.Lock()
	p.dynamic = models
	p.mu.Unlock()
}

// RefreshModels restores the persisted catalog, imports legacy gateway-config
// catalogs, then optionally refreshes from the gateway.
func (p *radiusProviderImpl) RefreshModels(ctx context.Context, refresh *ai.RefreshModelsContext) error {
	if refresh == nil {
		return nil
	}
	stored := refresh.Stored
	if stored != nil {
		restored := make([]types.Model, 0, len(stored.Models))
		for _, model := range stored.Models {
			if string(model.Provider) == p.id {
				restored = append(restored, model)
			}
		}
		published, err := refresh.Publish(ai.ModelsPublication{Update: func() { p.setDynamic(restored) }})
		if err != nil {
			return err
		}
		if !published {
			return nil
		}
	}

	// Import catalogs cached by the pre-ModelsStore Radius implementation.
	if stored == nil {
		if oauthCredential, ok := refresh.Credential.(*authtypes.OAuthCredential); ok && oauthCredential != nil {
			legacy := catalog.GetRadiusModels(p.id, radiusCredentialFromOAuth(oauthCredential))
			if len(legacy) > 0 {
				checkedAt := float64(time.Now().UnixMilli())
				published, err := refresh.Publish(ai.ModelsPublication{
					Persist: &ai.ModelsStoreEntry{Models: legacy, CheckedAt: &checkedAt},
					Update:  func() { p.setDynamic(legacy) },
				})
				if err != nil {
					return err
				}
				if !published {
					return nil
				}
			}
		}
	}

	if !refresh.AllowNetwork {
		return nil
	}
	signal := refresh.Signal
	if signal == nil {
		signal = ctx
	}
	if signal != nil && signal.Err() != nil {
		return nil
	}
	config, err := catalog.LoadRadiusGatewayConfig(ctx, p.gateway, radiusAPIKey(refresh.Credential), nil)
	if err != nil {
		return err
	}
	if signal != nil && signal.Err() != nil {
		return nil
	}
	refreshed := catalog.GetRadiusModelsFromConfig(p.id, config)
	checkedAt := float64(time.Now().UnixMilli())
	_, err = refresh.Publish(ai.ModelsPublication{
		Persist: &ai.ModelsStoreEntry{Models: refreshed, CheckedAt: &checkedAt},
		Update:  func() { p.setDynamic(refreshed) },
	})
	return err
}

func (p *radiusProviderImpl) FilterModels(models []types.Model, _ authtypes.Credential) []types.Model {
	return models
}

func (p *radiusProviderImpl) Stream(model types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	return p.streams.Stream(&model, context, options)
}

func (p *radiusProviderImpl) StreamSimple(model types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return p.streams.StreamSimple(&model, context, options)
}

func (p *radiusProviderImpl) FetchDeferred(model types.Model, handle types.DeferredHandle, options *types.DeferredFetchOptions) (*types.AssistantMessageEventStream, error) {
	return nil, fmt.Errorf("Provider %s does not support deferred responses", p.id)
}

func (p *radiusProviderImpl) CancelDeferred(model types.Model, handle types.DeferredHandle, options *types.DeferredCancelOptions) error {
	return fmt.Errorf("Provider %s cannot cancel deferred responses", p.id)
}

func (p *radiusProviderImpl) SupportsRefreshModels() bool  { return true }
func (p *radiusProviderImpl) SupportsFilterModels() bool   { return false }
func (p *radiusProviderImpl) SupportsFetchDeferred() bool  { return false }
func (p *radiusProviderImpl) SupportsCancelDeferred() bool { return false }

// radiusCredentialFromOAuth adapts an OAuth credential to the gateway-config
// interchange read by the catalog helpers.
func radiusCredentialFromOAuth(credential *authtypes.OAuthCredential) *catalog.RadiusOAuthCredential {
	if credential == nil {
		return nil
	}
	result := &catalog.RadiusOAuthCredential{OAuthCredential: catalog.OAuthCredential{Type: "oauth"}}
	if raw, ok := credential.Extra["gatewayConfig"]; ok {
		var config catalog.RadiusGatewayConfig
		if err := json.Unmarshal(raw, &config); err == nil {
			result.GatewayConfig = &config
		}
	}
	return result
}

func radiusAPIKey(credential authtypes.Credential) string {
	switch value := credential.(type) {
	case *authtypes.OAuthCredential:
		if value != nil {
			return value.Access
		}
	case *authtypes.ApiKeyCredential:
		if value != nil && value.Key != nil {
			return *value.Key
		}
	}
	return ""
}

// RadiusProvider builds the Radius gateway provider with a persisted,
// dynamically refreshed catalog.
//
// Ports `radiusProvider` from packages/ai/src/providers/radius.ts.
func RadiusProvider(options RadiusProviderOptions) ai.Provider {
	id := options.ID
	if id == "" {
		id = "radius"
	}
	name := options.Name
	if name == "" {
		name = "Radius"
	}
	gatewayOption := options.Gateway
	if gatewayOption == "" {
		gatewayOption = catalog.DEFAULT_RADIUS_GATEWAY
	}
	gateway := catalog.NormalizeRadiusGatewayUrl(gatewayOption)

	baselineModels := []types.Model{}
	if gateway == catalog.NormalizeRadiusGatewayUrl(catalog.DEFAULT_RADIUS_GATEWAY) {
		for _, model := range catalogModelList(catalog.RADIUS_MODELS) {
			model.Provider = types.ProviderId(id)
			baselineModels = append(baselineModels, model)
		}
	}
	dynamicModels := catalog.GetRadiusModels(id, nil)

	apiKey := auth.EnvApiKeyAuth("Radius API key", []string{"RADIUS_API_KEY"})
	gatewayCopy := gateway
	nameCopy := name
	radiusOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name: name,
		Load: func() (*authtypes.OAuthAuth, error) {
			return oauth.LoadRadiusOAuth(oauth.RadiusOAuthOptions{Name: nameCopy, Gateway: gatewayCopy})
		},
	})

	return &radiusProviderImpl{
		id:       id,
		name:     name,
		gateway:  gateway,
		auth:     authtypes.ProviderAuth{APIKey: &apiKey, OAuth: &radiusOAuth},
		streams:  api.PiMessagesApi(),
		baseline: baselineModels,
		dynamic:  dynamicModels,
	}
}
