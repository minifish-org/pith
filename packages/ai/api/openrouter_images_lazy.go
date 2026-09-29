// This file is a Go port of packages/ai/src/api/openrouter-images.lazy.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import "github.com/minifish-org/pith/packages/ai/types"

// OpenRouterImagesApi returns the lazily loaded OpenRouter images API module.
// It mirrors the upstream `openrouterImagesApi()` factory: the implementation is
// only reached on first generation and a load failure is reported as a failed
// AssistantImages by the builtin wrapper.
func OpenRouterImagesApi() types.ProviderImages {
	return openRouterImagesAPI{}
}

type openRouterImagesAPI struct{}

// GenerateImages delegates to the OpenRouter images implementation.
func (openRouterImagesAPI) GenerateImages(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	return GenerateOpenRouterImages(model, context, options)
}
