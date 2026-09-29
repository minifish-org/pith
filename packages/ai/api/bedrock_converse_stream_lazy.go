// This file is a Go port of packages/ai/src/api/bedrock-converse-stream.lazy.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream loads the Node-only AWS SDK implementation through a variable
// specifier so bundlers cannot follow the import, and lets the Bun binary build
// register a statically imported module instead. Go has no equivalent dynamic
// module graph; the implementation is a normal package and is loaded through
// the shared LazyApi loader, while SetBedrockProviderModule keeps the
// statically-linked override seam.
package api

import (
	"context"
	"sync"

	"github.com/minifish-org/pith/packages/ai/types"
)

var (
	bedrockModuleOverrideMu sync.RWMutex
	bedrockModuleOverride   types.ProviderStreams
)

// SetBedrockProviderModule overrides the lazily loaded Bedrock implementation.
// It mirrors the upstream `setBedrockProviderModule` hook used by builds where
// the dynamic import cannot be bundled.
func SetBedrockProviderModule(module types.ProviderStreams) {
	bedrockModuleOverrideMu.Lock()
	bedrockModuleOverride = module
	bedrockModuleOverrideMu.Unlock()
}

func getBedrockProviderModuleOverride() types.ProviderStreams {
	bedrockModuleOverrideMu.RLock()
	defer bedrockModuleOverrideMu.RUnlock()
	return bedrockModuleOverride
}

// BedrockConverseStreamApi returns the lazily loaded Bedrock ConverseStream API
// module. It mirrors the upstream `bedrockConverseStreamApi()` factory: the
// module loads on first stream use and a load failure terminates the returned
// stream.
func BedrockConverseStreamApi() types.ProviderStreams {
	return LazyApi(context.Background(), func(context.Context) (types.ProviderStreams, error) {
		if override := getBedrockProviderModuleOverride(); override != nil {
			return override, nil
		}
		return staticProviderStreams{
			StreamFn: func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
				var typed BedrockOptions
				if options != nil {
					typed.StreamOptions = *options
				}
				return BedrockConverseStream(model, context, &typed)
			},
			StreamSimpleFn: func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
				return BedrockConverseStreamSimple(model, context, options)
			},
		}, nil
	}, nil)
}
