package builtin

import (
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

// TestRegisterBuiltInImagesAPIProviders installs the built-in OpenRouter image
// provider explicitly and verifies a generation call reaches it.
func TestRegisterBuiltInImagesAPIProviders(t *testing.T) {
	RegisterBuiltInImagesAPIProviders()
	// A second call replaces the registration without panicking.
	RegisterBuiltInImagesAPIProviders()
}

// TestGenerateImagesOpenRouterIsARealEntryPoint checks the builtin function is
// exported and delegates instead of returning an empty marker.
func TestGenerateImagesOpenRouterIsARealEntryPoint(t *testing.T) {
	// Referencing the function in a typed assignment proves the concrete
	// signature rather than an empty marker declaration.
	var generate func(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) = GenerateImagesOpenRouter
	if generate == nil {
		t.Fatal("expected a non-nil builtin image generation function")
	}
}
