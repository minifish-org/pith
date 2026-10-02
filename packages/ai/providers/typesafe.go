// This file is a Go port of packages/ai/src/providers/typesafe.ts from Pi at the
// frozen target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// TypeSafe is a classifier-only provider. It has no legacy chat catalog; its
// mixed catalog comes directly from the V1 release records (catalog.V1Models).
package providers

import (
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// TypesafeProvider builds the TypeSafe classifier provider.
func TypesafeProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:        "typesafe",
		Name:      "TypeSafe",
		Auth:      envAuth("TypeSafe API key", "TYPESAFE_API_KEY"),
		AllModels: catalog.V1AnyModels("typesafe"),
		Classifiers: map[types.ClassifierApi]ai.ClassifierImplementation{
			types.ClassifierApiTypesafeSystemOne: typesafeSystemOneClassifier(),
		},
	})
}
