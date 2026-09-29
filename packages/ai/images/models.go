// This file is a Go port of packages/ai/src/image-models.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The static image-model catalog lookups. The data itself lives in
// packages/ai/catalog (generated from image-models.generated.ts); this package
// is a leaf and only reads it.
package images

import (
	"sort"

	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// imageModelRegistry is the two-level provider/model lookup built once from the
// generated catalog, mirroring the runtime Map built by image-models.ts.
var imageModelRegistry = buildImageModelRegistry()

func buildImageModelRegistry() map[types.ImagesProviderId]map[string]types.ImagesModel {
	registry := make(map[types.ImagesProviderId]map[string]types.ImagesModel, len(catalog.IMAGE_MODELS))
	for provider, models := range catalog.IMAGE_MODELS {
		providerModels := make(map[string]types.ImagesModel, len(models))
		for id, model := range models {
			providerModels[id] = model
		}
		registry[provider] = providerModels
	}
	return registry
}

// GetImageModel returns the image model for a provider and model id, or nil when
// it is not known.
func GetImageModel(provider types.ImagesProviderId, modelID string) *types.ImagesModel {
	providerModels, ok := imageModelRegistry[provider]
	if !ok {
		return nil
	}
	model, ok := providerModels[modelID]
	if !ok {
		return nil
	}
	return &model
}

// GetImageProviders returns the known image provider ids.
//
// Deliberate difference: upstream preserves object insertion order; the Go
// catalog is a map without insertion order, so the result is sorted. The
// currently generated catalog has a single provider, so this is only observable
// once a second image provider is added.
func GetImageProviders() []types.ImagesProviderId {
	providers := make([]types.ImagesProviderId, 0, len(imageModelRegistry))
	for provider := range imageModelRegistry {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	return providers
}

// GetImageModels returns every known model for a provider. An unknown provider
// yields an empty slice.
func GetImageModels(provider types.ImagesProviderId) []types.ImagesModel {
	providerModels, ok := imageModelRegistry[provider]
	if !ok {
		return []types.ImagesModel{}
	}
	ids := make([]string, 0, len(providerModels))
	for id := range providerModels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	models := make([]types.ImagesModel, 0, len(ids))
	for _, id := range ids {
		models = append(models, providerModels[id])
	}
	return models
}
