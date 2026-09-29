// This file is a Go port of packages/ai/src/images.ts together with the
// builtin-registration half of packages/ai/src/providers/images/register-builtins.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// images.ts is the public image-generation entry point: it resolves the
// registered API provider and delegates. register-builtins.ts used an import
// side effect to install the OpenRouter provider; the Go port exposes
// RegisterBuiltInImagesAPIProviders and lets the final SDK entry call it
// explicitly, so importing the registry leaf never silently installs providers.
package images

import (
	"fmt"
	"time"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// GenerateImages resolves the API provider registered for the model and runs the
// request. An unregistered API is an error; provider failures are encoded in the
// returned AssistantImages.
func GenerateImages(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	if model == nil {
		return nil, fmt.Errorf("No model provided")
	}
	provider := GetImagesAPIProvider(types.ImagesApi(model.Api))
	if provider == nil {
		return nil, fmt.Errorf("No API provider registered for api: %s", model.Api)
	}
	return provider.GenerateImages(model, context, options)
}

// GenerateImagesOpenRouter is the builtin OpenRouter image-generation function
// installed by RegisterBuiltInImagesAPIProviders. Load failures are reported as
// a failed AssistantImages, matching register-builtins.ts.
func GenerateImagesOpenRouter(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	output, err := api.OpenRouterImagesApi().GenerateImages(model, context, options)
	if err != nil {
		return createOpenRouterLazyLoadError(model, err), nil
	}
	return output, nil
}

func createOpenRouterLazyLoadError(model *types.ImagesModel, err error) *types.AssistantImages {
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	return &types.AssistantImages{
		Api:          types.ImagesApi(model.Api),
		Provider:     types.ImagesProviderId(model.Provider),
		Model:        model.Id,
		Output:       []types.ContentBlock{},
		StopReason:   types.ImagesStopReasonError,
		ErrorMessage: &message,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
}

// RegisterBuiltInImagesAPIProviders installs the built-in image-generation API
// providers. The final SDK entry point calls this explicitly so provider
// implementations are never pulled in by importing the registry leaf.
func RegisterBuiltInImagesAPIProviders() {
	RegisterImagesAPIProvider(ImagesAPIProvider{
		API:            types.ApiOpenRouterImages,
		GenerateImages: GenerateImagesOpenRouter,
	})
}
