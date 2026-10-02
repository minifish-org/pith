// This file is a Go port of packages/ai/src/api/typesafe-system-one.ts from Pi
// at the frozen target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// TypeSafe's native System One protocol. OpenRouter serves the same protocol at
// a different base URL, so both use this module with different models.
package api

import (
	"context"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
)

// typesafeSystemOneTransport is the TypeSafe/OpenRouter System One envelope.
var typesafeSystemOneTransport = SystemOneTransport{
	Api:   string(types.ClassifierApiTypesafeSystemOne),
	Label: "System One API",
	URL: func(model *types.ClassifierModel) string {
		return systemOneURL(model.BaseUrl)
	},
	Payload: func(model *types.ClassifierModel, request SystemOneWireRequest) any {
		return map[string]any{
			"model":     model.Id,
			"state":     request.State,
			"questions": request.Questions,
		}
	},
	Output: func(body any) (map[string]any, error) {
		record, ok := body.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("System One API returned an unexpected response")
		}
		return record, nil
	},
}

// TypesafeSystemOneClassify performs a TypeSafe System One classification with
// public `bool` values mapped to wire-level `noul`. Request failures are
// represented in the returned result.
func TypesafeSystemOneClassify(ctx context.Context, model *types.ClassifierModel, request *types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult {
	return classifySystemOne(ctx, typesafeSystemOneTransport, model, request, options)
}
