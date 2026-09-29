// This file is a Go port of packages/ai/src/api/openrouter-images.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Image generation through OpenRouter's chat-completions surface: the model
// returns generated images as data URLs in an assistant message.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// GenerateOpenRouterImages is the OpenRouter image-generation function.
//
// It never returns an error: request failures are encoded in the returned
// AssistantImages with stopReason "error"/"aborted", matching the upstream
// processor contract.
func GenerateOpenRouterImages(model *types.ImagesModel, context *types.ImagesContext, options *types.ImagesOptions) (*types.AssistantImages, error) {
	output := &types.AssistantImages{
		Api:        types.ImagesApi(model.Api),
		Provider:   types.ImagesProviderId(model.Provider),
		Model:      model.Id,
		Output:     []types.ContentBlock{},
		StopReason: types.ImagesStopReasonStop,
		Timestamp:  nowMillis(),
	}

	apiKey := (*string)(nil)
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == nil || *apiKey == "" {
		output.StopReason = types.ImagesStopReasonError
		message := fmt.Sprintf("No API key for provider: %s", model.Provider)
		output.ErrorMessage = &message
		return output, nil
	}

	params := buildOpenRouterImagesParams(model, context)
	if options != nil && options.OnPayload != nil {
		next, err := options.OnPayload(params, &model.Model)
		if err != nil {
			openRouterImagesFail(output, options, err)
			return output, nil
		}
		if nextMap, ok := next.(map[string]any); ok {
			params = nextMap
		}
	}

	response, err := requestOpenRouterImages(model, params, *apiKey, options)
	if err != nil {
		openRouterImagesFail(output, options, err)
		return output, nil
	}
	raw, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		openRouterImagesFail(output, options, readErr)
		return output, nil
	}

	if options != nil && options.OnResponse != nil {
		options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, &model.Model)
	}

	imageResponse, err := decodeOpenRouterImagesResponse(raw)
	if err != nil {
		openRouterImagesFail(output, options, err)
		return output, nil
	}

	if id, ok := imageResponse["id"].(string); ok && id != "" {
		output.ResponseId = &id
	}
	if rawUsage, ok := imageResponse["usage"].(map[string]any); ok {
		usage := parseOpenRouterImagesUsage(rawUsage, model)
		output.Usage = &usage
	}

	choices, _ := imageResponse["choices"].([]any)
	if len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		if message != nil {
			if content, ok := message["content"].(string); ok && content != "" {
				output.Output = append(output.Output, types.TextBlock(content))
			}
			images, _ := message["images"].([]any)
			for _, imageValue := range images {
				image, ok := imageValue.(map[string]any)
				if !ok {
					continue
				}
				imageURL := ""
				switch value := image["image_url"].(type) {
				case string:
					imageURL = value
				case map[string]any:
					imageURL, _ = value["url"].(string)
				}
				if !strings.HasPrefix(imageURL, "data:") {
					continue
				}
				matches := openRouterDataURLPattern.FindStringSubmatch(imageURL)
				if matches == nil {
					continue
				}
				output.Output = append(output.Output, types.ImageBlock(matches[2], matches[1]))
			}
		}
	}

	return output, nil
}

var openRouterDataURLPattern = regexp.MustCompile(`^data:([^;]+);base64,(.+)$`)

func openRouterImagesFail(output *types.AssistantImages, options *types.ImagesOptions, err error) {
	output.StopReason = types.ImagesStopReasonError
	if options != nil && options.Signal != nil && aborted(options.Signal) {
		output.StopReason = types.ImagesStopReasonAborted
	}
	message := utils.FormatProviderError(utils.NormalizeProviderError(toError(err)), nil)
	output.ErrorMessage = &message
}

func toError(err error) error {
	if err == nil {
		return fmt.Errorf("unknown error")
	}
	return err
}

func requestOpenRouterImages(model *types.ImagesModel, params map[string]any, apiKey string, options *types.ImagesOptions) (*http.Response, error) {
	headers := map[string]string{
		"authorization": "Bearer " + apiKey,
		"content-type":  "application/json",
	}
	for key, value := range utils.ProviderHeadersToRecord(openRouterMergedHeaders(model, options)) {
		headers[key] = value
	}

	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	var signal <-chan struct{}
	var reqOptions *types.ProviderRequestOptions
	if options != nil {
		reqOptions = &options.ProviderRequestOptions
		signal = options.Signal
	}
	ctxSignal, cancelSignal := contextForSignal(contextBackground(), signal)
	defer cancelSignal()

	response, _, err := performRequestWithRetry(ctxSignal, reqOptions, http.MethodPost, strings.TrimRight(model.BaseUrl, "/")+"/chat/completions", headers, body)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func openRouterMergedHeaders(model *types.ImagesModel, options *types.ImagesOptions) types.ProviderHeaders {
	merged := types.ProviderHeaders{}
	for key, value := range model.Headers {
		v := value
		merged[key] = &v
	}
	if options != nil {
		for key, value := range options.Headers {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func buildOpenRouterImagesParams(model *types.ImagesModel, context *types.ImagesContext) map[string]any {
	content := make([]map[string]any, 0, len(context.Input))
	for _, item := range context.Input {
		switch item.Type {
		case types.ContentTypeText:
			if item.Text == nil {
				continue
			}
			content = append(content, map[string]any{"type": "text", "text": utils.SanitizeSurrogates(item.Text.Text)})
		case types.ContentTypeImage:
			if item.Image == nil {
				continue
			}
			content = append(content, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + item.Image.MimeType + ";base64," + item.Image.Data,
				},
			})
		}
	}

	modalities := []string{"image"}
	for _, modality := range model.Output {
		if modality == types.ImagesModelOutputText {
			modalities = []string{"image", "text"}
			break
		}
	}

	return map[string]any{
		"model": model.Id,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"stream":     false,
		"modalities": modalities,
	}
}

func decodeOpenRouterImagesResponse(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var response map[string]any
	if err := decoder.Decode(&response); err != nil {
		return nil, err
	}
	return response, nil
}

func parseOpenRouterImagesUsage(rawUsage map[string]any, model *types.ImagesModel) types.Usage {
	promptTokens := numberOrZero(rawUsage["prompt_tokens"])
	details, _ := rawUsage["prompt_tokens_details"].(map[string]any)
	reportedCachedTokens := numberOrZero(details["cached_tokens"])
	cacheWriteTokens := numberOrZero(details["cache_write_tokens"])
	cacheReadTokens := reportedCachedTokens
	if cacheWriteTokens > 0 {
		cacheReadTokens = maxFloat(0, reportedCachedTokens-cacheWriteTokens)
	}
	input := maxFloat(0, promptTokens-cacheReadTokens-cacheWriteTokens)
	outputTokens := numberOrZero(rawUsage["completion_tokens"])

	usage := types.Usage{
		Input:       input,
		Output:      outputTokens,
		CacheRead:   cacheReadTokens,
		CacheWrite:  cacheWriteTokens,
		TotalTokens: input + outputTokens + cacheReadTokens + cacheWriteTokens,
	}
	usage.Cost.Input = (model.Cost.Input / 1000000) * input
	usage.Cost.Output = (model.Cost.Output / 1000000) * outputTokens
	usage.Cost.CacheRead = (model.Cost.CacheRead / 1000000) * cacheReadTokens
	usage.Cost.CacheWrite = (model.Cost.CacheWrite / 1000000) * cacheWriteTokens
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage
}
