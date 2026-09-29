// This file is a Go port of packages/ai/src/providers/images/register-builtins.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// register-builtins.ts installed the OpenRouter image provider through an import
// side effect. Go keeps the registry leaf free of concrete providers, so the
// built-in installer lives here in its own package and the final SDK entry point
// calls RegisterBuiltInImagesAPIProviders explicitly.
package builtin

import (
	"github.com/minifish-org/pith/packages/ai/images"
	"github.com/minifish-org/pith/packages/ai/types"
)

// GenerateImagesOpenRouter is the builtin OpenRouter image-generation function.
// Load failures are reported as a failed AssistantImages, matching
// register-builtins.ts.
//
// Ports `generateImagesOpenRouter` from
// packages/ai/src/providers/images/register-builtins.ts.
func GenerateImagesOpenRouter(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	return images.GenerateImagesOpenRouter(model, context, options)
}

// RegisterBuiltInImagesAPIProviders installs the built-in image-generation API
// providers.
//
// Ports `registerBuiltInImagesApiProviders` from
// packages/ai/src/providers/images/register-builtins.ts.
func RegisterBuiltInImagesAPIProviders() {
	images.RegisterBuiltInImagesAPIProviders()
}
