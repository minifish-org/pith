// This file is a Go port of packages/ai/src/bedrock-provider.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// bedrock-provider.ts is the Bun-binary provider module: it exposes the Bedrock
// ConverseStream API implementation under the `bedrockProviderModule` name so a
// standalone build can statically link it instead of relying on the
// bundler-opaque lazy import. Go links the implementation as a normal package,
// so this is a thin typed value over the same lazy API module.
package sdk

import (
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// BedrockProviderModule is the statically linked Bedrock ConverseStream API
// module. It mirrors the upstream `bedrockProviderModule` value with its
// stream/streamSimple members, and can be installed with
// api.SetBedrockProviderModule.
var BedrockProviderModule types.ProviderStreams = api.BedrockConverseStreamApi()
