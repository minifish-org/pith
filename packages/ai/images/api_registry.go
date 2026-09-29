// This file is a Go port of packages/ai/src/images-api-registry.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The image-generation API provider registry. This package is a leaf: it holds
// only the registry and the two DTOs, and it never imports a concrete provider
// factory. Built-in providers are installed explicitly by the higher-level
// compat entry point (the Go replacement for register-builtins.ts's import side
// effect).
package images

import (
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
)

// ImagesAPIFunction is a provider image-generation implementation.
type ImagesAPIFunction func(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error)

// ImagesAPIProvider pairs an API id with its generation implementation.
type ImagesAPIProvider struct {
	API            types.ImagesApi
	GenerateImages ImagesAPIFunction
}

type registeredImagesAPIProvider struct {
	provider ImagesAPIProvider
	sourceID *string
}

var imagesAPIProviderRegistry = map[types.ImagesApi]registeredImagesAPIProvider{}

// wrapImagesGenerateImages validates that a model's api matches the registered
// provider before delegating, mirroring the upstream wrapper.
func wrapImagesGenerateImages(api types.ImagesApi, generateImages ImagesAPIFunction) ImagesAPIFunction {
	return func(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
		if model != nil && types.ImagesApi(model.Api) != api {
			return nil, fmt.Errorf("Mismatched api: %s expected %s", model.Api, api)
		}
		return generateImages(model, context, options)
	}
}

// RegisterImagesAPIProvider registers an image-generation API provider. A later
// registration for the same API replaces the earlier one. The optional sourceID
// identifies a dynamic/provider source for diagnostics.
func RegisterImagesAPIProvider(provider ImagesAPIProvider, sourceID ...string) {
	var source *string
	if len(sourceID) > 0 && sourceID[0] != "" {
		value := sourceID[0]
		source = &value
	}
	imagesAPIProviderRegistry[provider.API] = registeredImagesAPIProvider{
		provider: ImagesAPIProvider{
			API:            provider.API,
			GenerateImages: wrapImagesGenerateImages(provider.API, provider.GenerateImages),
		},
		sourceID: source,
	}
}

// GetImagesAPIProvider returns the registered provider for an API, or nil.
func GetImagesAPIProvider(api types.ImagesApi) *ImagesAPIProvider {
	entry, ok := imagesAPIProviderRegistry[api]
	if !ok {
		return nil
	}
	provider := entry.provider
	return &provider
}

// ClearImagesAPIProviders removes every registered provider. It exists for
// deterministic tests and for hosts that install a custom provider set.
func ClearImagesAPIProviders() {
	imagesAPIProviderRegistry = map[types.ImagesApi]registeredImagesAPIProvider{}
}
