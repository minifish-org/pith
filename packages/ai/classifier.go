// This file ports the classifier dispatch layer of Pi's models.ts and adds the
// shared classifier result helpers used by the runtime collection.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A classifier provider owns the request behavior for its APIs; the Models
// collection resolves auth and delegates. Classification never returns an error:
// failures (unknown provider, unsupported kind, unconfigured auth, provider
// failure) are represented in the returned ClassifierResult.
package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

// classifierErrorResult builds a failed classifier result for a model. A nil
// model yields a result without identity, matching the error result of an
// unknown provider.
func classifierErrorResult(model *types.ClassifierModel, message string) types.ClassifierResult {
	text := message
	result := types.ClassifierResult{
		Answers:      map[string]types.ClassifierAnswer{},
		StopReason:   types.ClassifierStopReasonError,
		ErrorMessage: &text,
		Timestamp:    float64(time.Now().UnixMilli()),
	}
	if model != nil {
		result.Api = model.Api
		result.Provider = model.Provider
		result.Model = model.Id
	}
	return result
}

// classifierAbortedResult builds an aborted classifier result.
func classifierAbortedResult(model *types.ClassifierModel, message string) types.ClassifierResult {
	result := classifierErrorResult(model, message)
	result.StopReason = types.ClassifierStopReasonAborted
	return result
}

// Classify dispatches a classification to the implementation registered for the
// model's api.
func (p *providerImpl) Classify(ctx context.Context, model types.ClassifierModel, request types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult {
	if len(p.classifiers) == 0 {
		return classifierErrorResult(&model, fmt.Sprintf("Provider %s does not support classification", p.id))
	}
	implementation, ok := p.classifiers[model.Api]
	if !ok {
		return classifierErrorResult(&model, fmt.Sprintf("Provider %s has no classifier implementation for \"%s\"", p.id, string(model.Api)))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return implementation(ctx, model, request, options)
}

// Classify resolves provider auth then delegates to the owning provider. It
// never returns an error; every failure is represented in the result.
func (m *modelsImpl) Classify(ctx context.Context, model types.ClassifierModel, request types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return classifierAbortedResult(&model, err.Error())
	}
	provider := m.GetProvider(string(model.Provider))
	if provider == nil {
		return classifierErrorResult(&model, "Unknown provider: "+string(model.Provider))
	}
	classifier, ok := provider.(ClassifierProvider)
	if !ok {
		return classifierErrorResult(&model, "Provider "+string(model.Provider)+" does not support classification")
	}

	baseModel := types.Model{
		Id:            model.Id,
		Name:          model.Name,
		Api:           types.Api(model.Api),
		Provider:      model.Provider,
		BaseUrl:       model.BaseUrl,
		Input:         model.Input,
		Cost:          model.Cost,
		ContextWindow: model.ContextWindow,
		Headers:       model.Headers,
	}
	var base *types.ProviderRequestOptions
	if options != nil {
		base = &options.ProviderRequestOptions
	}
	requestModel, resolvedOptions, err := m.applyAuth(ctx, baseModel, base, nil)
	if err != nil {
		return classifierErrorResult(&model, err.Error())
	}

	merged := &types.ClassifierOptions{}
	if options != nil {
		*merged = *options
	}
	if resolvedOptions != nil {
		merged.ProviderRequestOptions = *resolvedOptions
	}
	requestModel.BaseUrl = model.BaseUrl
	requestModel.Provider = model.Provider

	classifierModel := model
	classifierModel.BaseUrl = requestModel.BaseUrl
	return classifier.Classify(ctx, classifierModel, request, merged)
}
