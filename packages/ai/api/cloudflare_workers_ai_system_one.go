// This file is a Go port of
// packages/ai/src/api/cloudflare-workers-ai-system-one.ts from Pi at the frozen
// target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// System One models on the Workers AI REST endpoint: `POST .../run` with
// `{ model, input }`. The REST API wraps the model output in Cloudflare's API
// envelope and a run record:
// `{ success, result: { state: "Completed", result: { answers, usage } } }`.
// https://developers.cloudflare.com/ai/models/typesafe/jev/
package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

const cloudflareSystemOneLabel = "Cloudflare Workers AI"

// cloudflareErrorMessage renders the Cloudflare `errors` array.
func cloudflareErrorMessage(errorsValue any) string {
	if list, ok := errorsValue.([]any); ok {
		messages := make([]string, 0, len(list))
		for _, entry := range list {
			record, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if message, ok := record["message"].(string); ok {
				messages = append(messages, message)
			}
		}
		if len(messages) > 0 {
			return cloudflareSystemOneLabel + " error: " + strings.Join(messages, "; ")
		}
	}
	return cloudflareSystemOneLabel + " request failed"
}

// cloudflareSystemOneOutput unwraps Cloudflare's API/run envelope.
func cloudflareSystemOneOutput(body any) (map[string]any, error) {
	record, ok := body.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned an unexpected response", cloudflareSystemOneLabel)
	}
	if success, ok := record["success"].(bool); ok && !success {
		return nil, fmt.Errorf("%s", cloudflareErrorMessage(record["errors"]))
	}
	run, ok := record["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned an unexpected response", cloudflareSystemOneLabel)
	}
	if state, _ := run["state"].(string); state != "Completed" {
		return nil, fmt.Errorf("%s run did not complete (state: %v)", cloudflareSystemOneLabel, run["state"])
	}
	result, ok := run["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned an unexpected response", cloudflareSystemOneLabel)
	}
	return result, nil
}

// cloudflareSystemOneTransport is the Cloudflare Workers AI System One envelope.
var cloudflareSystemOneTransport = SystemOneTransport{
	Api:   string(types.ClassifierApiCloudflareWorkersAISystemOne),
	Label: cloudflareSystemOneLabel,
	URL: func(model *types.ClassifierModel) string {
		return strings.TrimRight(model.BaseUrl, "/") + "/run"
	},
	Payload: func(model *types.ClassifierModel, request SystemOneWireRequest) any {
		return map[string]any{"model": model.Id, "input": request}
	},
	Output: cloudflareSystemOneOutput,
}

// CloudflareWorkersAISystemOneClassify performs a Cloudflare Workers AI System
// One classification with public `bool` values mapped to wire-level `noul`.
func CloudflareWorkersAISystemOneClassify(ctx context.Context, model *types.ClassifierModel, request *types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult {
	return classifySystemOne(ctx, cloudflareSystemOneTransport, model, request, options)
}
