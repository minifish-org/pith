// This file is a Go port of packages/ai/src/providers/google.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// GoogleProvider builds the Google provider.
func GoogleProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderGoogle,
		Name:    "Google",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta",
		Auth:    envAuth("Gemini API key", "GEMINI_API_KEY"),
		Models:  catalogModelList(catalog.GOOGLE_MODELS),
		API:     api.GoogleGenerativeAIApi(),
	})
}
