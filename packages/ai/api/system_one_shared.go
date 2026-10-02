// This file is a Go port of packages/ai/src/api/system-one-shared.ts from Pi at
// the frozen target revision.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// System One is the shared structured-classification protocol behind TypeSafe's
// native endpoint and the Cloudflare Workers AI REST endpoint. This file holds
// the transport-independent request/response mapping: public `bool` questions
// become wire-level `noul`, the response answers are normalized back to the
// public union, usage is priced from the model catalog, and malformed or missing
// answers fail the whole result instead of fabricating partial data.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// SystemOneWireRequest is the transport-independent System One request body.
type SystemOneWireRequest struct {
	State     types.JsonObject `json:"state"`
	Questions map[string]any   `json:"questions"`
}

// SystemOneTransport captures the differences between services that serve
// System One models.
type SystemOneTransport struct {
	// Api is the ClassifierApi implemented by this transport.
	Api string
	// Label is the service name used in error messages.
	Label string
	// URL returns the absolute request URL for a model.
	URL func(model *types.ClassifierModel) string
	// Payload wraps the System One request in the service envelope.
	Payload func(model *types.ClassifierModel, request SystemOneWireRequest) any
	// Output extracts the System One output (`{ answers, usage }`) from the
	// service response envelope.
	Output func(body any) (map[string]any, error)
}

// systemOneWireQuestion is one wire question. Criteria is preserved verbatim so
// the choice/score/bool union survives serialization.
type systemOneWireQuestion struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

// systemOneURL joins the `systemone` path onto an OpenAI-compatible base URL,
// mirroring `new URL("systemone", base + "/")`.
func systemOneURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/systemone"
}

// classifierModelAsModel projects a classifier model onto the shared model view
// used by the payload/response hooks.
func classifierModelAsModel(model *types.ClassifierModel) *types.Model {
	if model == nil {
		return nil
	}
	return &types.Model{
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
}

// classifierRequiredNumber reads a finite JSON number field.
func classifierRequiredNumber(label string, value any, field string) (float64, error) {
	number, ok := classifierNumber(value)
	if !ok {
		return 0, fmt.Errorf("%s returned an invalid %s", label, field)
	}
	return number, nil
}

func classifierNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		if isFinite(number) {
			return number, true
		}
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		if parsed, err := number.Float64(); err == nil && isFinite(parsed) {
			return parsed, true
		}
	}
	return 0, false
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// classifierProbabilities reads a `{ label: probability }` record.
func classifierProbabilities(label string, value any, id string) (map[string]float64, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned invalid probabilities for %s", label, id)
	}
	out := make(map[string]float64, len(record))
	for key, probability := range record {
		number, err := classifierRequiredNumber(label, probability, fmt.Sprintf("probability for %s.%s", id, key))
		if err != nil {
			return nil, err
		}
		out[key] = number
	}
	return out, nil
}

// classifierParseAnswers normalizes every answer of a System One response.
func classifierParseAnswers(label string, value any, request *types.ClassifierContext) (map[string]types.ClassifierAnswer, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned an unexpected response", label)
	}
	answers := make(map[string]types.ClassifierAnswer)
	if request == nil {
		return answers, nil
	}
	for id, question := range request.Questions {
		answerValue, present := record[id]
		answer, ok := answerValue.(map[string]any)
		if !present || !ok {
			return nil, fmt.Errorf("%s did not return an answer for %s", label, id)
		}
		switch question.Type {
		case "choice":
			if answer["type"] != "choice" {
				return nil, fmt.Errorf("%s did not return a choice answer for %s", label, id)
			}
			choice, ok := answer["choice"].(string)
			if !ok {
				return nil, fmt.Errorf("%s did not return a choice answer for %s", label, id)
			}
			probabilities, err := classifierProbabilities(label, answer["probabilities"], id)
			if err != nil {
				return nil, err
			}
			confidence, err := classifierRequiredNumber(label, answer["confidence"], "confidence for "+id)
			if err != nil {
				return nil, err
			}
			choiceCopy := choice
			confidenceCopy := confidence
			answers[id] = types.ClassifierAnswer{
				Type:          "choice",
				Choice:        &choiceCopy,
				Probabilities: probabilities,
				Confidence:    &confidenceCopy,
			}
		case "score":
			if answer["type"] != "score" {
				return nil, fmt.Errorf("%s did not return a score answer for %s", label, id)
			}
			score, err := classifierRequiredNumber(label, answer["score"], "score for "+id)
			if err != nil {
				return nil, err
			}
			confidence, err := classifierRequiredNumber(label, answer["confidence"], "confidence for "+id)
			if err != nil {
				return nil, err
			}
			scoreCopy := score
			confidenceCopy := confidence
			answers[id] = types.ClassifierAnswer{Type: "score", Score: &scoreCopy, Confidence: &confidenceCopy}
		default:
			if answer["type"] != "noul" {
				return nil, fmt.Errorf("%s did not return a bool answer for %s", label, id)
			}
			probability, err := classifierRequiredNumber(label, answer["noul"], "probability for "+id)
			if err != nil {
				return nil, err
			}
			probabilityCopy := probability
			answers[id] = types.ClassifierAnswer{Type: "bool", Probability: &probabilityCopy}
		}
	}
	return answers, nil
}

// classifierTokenCount coerces a token count, treating anything that is not a
// positive finite number as zero.
func classifierTokenCount(value any) float64 {
	number, ok := classifierNumber(value)
	if !ok || number <= 0 {
		return 0
	}
	return number
}

// classifierParseUsage maps System One's `{ input_tokens, output_tokens }` onto
// priced usage. A missing or malformed usage object leaves the result without
// usage instead of failing it.
func classifierParseUsage(value any, model *types.ClassifierModel) *types.Usage {
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	_, hasInput := record["input_tokens"]
	_, hasOutput := record["output_tokens"]
	if !hasInput && !hasOutput {
		return nil
	}
	input := classifierTokenCount(record["input_tokens"])
	output := classifierTokenCount(record["output_tokens"])
	usage := &types.Usage{Input: input, Output: output, TotalTokens: input + output, Cost: types.UsageCost{}}
	calculateCost(classifierModelAsModel(model), usage)
	return usage
}

// systemOneWireRequest maps public questions onto the wire, renaming the public
// `bool` type to TypeSafe's wire-level `noul`.
func systemOneWireRequest(request *types.ClassifierContext) SystemOneWireRequest {
	questions := map[string]any{}
	var state types.JsonObject
	if request != nil {
		state = request.State
		for id, question := range request.Questions {
			questionType := question.Type
			if questionType == "bool" {
				questionType = "noul"
			}
			questions[id] = systemOneWireQuestion{
				Type:         questionType,
				Instructions: question.Instructions,
				Criteria:     question.Criteria,
			}
		}
	}
	return SystemOneWireRequest{State: state, Questions: questions}
}

// systemOneRequestHeaders builds the request headers: defaults, then the model
// headers, then the request headers, merged case-insensitively.
func systemOneRequestHeaders(model *types.ClassifierModel, apiKey string, optionHeaders types.ProviderHeaders) map[string]string {
	defaults := types.ProviderHeaders{
		"authorization": stringPointer("Bearer " + apiKey),
		"content-type":  stringPointer("application/json"),
	}
	var modelHeaders types.ProviderHeaders
	if model != nil {
		modelHeaders = stringMapToProviderHeaders(model.Headers)
	}
	return utils.ProviderHeadersToRecord(defaults, modelHeaders, optionHeaders)
}

// classifierSignal returns the explicit cancellation signal, if any.
func classifierSignal(options *types.ProviderRequestOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}

// classifierContext couples the caller context with the explicit abort signal
// and the per-attempt request timeout.
func classifierContext(ctx context.Context, options *types.ProviderRequestOptions) (context.Context, context.CancelFunc) {
	base, cancelSignal := contextForSignal(ctx, classifierSignal(options))
	if options != nil && options.TimeoutMs != nil && *options.TimeoutMs > 0 {
		timeoutCtx, cancelTimeout := context.WithTimeout(base, time.Duration(*options.TimeoutMs)*time.Millisecond)
		return timeoutCtx, func() { cancelTimeout(); cancelSignal() }
	}
	return base, cancelSignal
}

// classifierTimeoutError reports a request that exceeded its timeout.
type classifierTimeoutError struct {
	timeoutMs int
}

func (e *classifierTimeoutError) Error() string {
	return fmt.Sprintf("Request timed out after %dms", e.timeoutMs)
}

// classifierHTTPResponse is one successful provider response.
type classifierHTTPResponse struct {
	status  int
	headers http.Header
	body    []byte
}

// classifierRequestOnce performs one attempt, honoring a fresh request timeout.
func classifierRequestOnce(ctx context.Context, options *types.ProviderRequestOptions, url string, headers map[string]string, body []byte) (classifierHTTPResponse, error) {
	attemptCtx, cancel := classifierContext(ctx, options)
	defer cancel()

	response, err := doProviderRequest(attemptCtx, options, "POST", url, headers, body)
	if err != nil {
		if options != nil && options.TimeoutMs != nil && *options.TimeoutMs > 0 &&
			attemptCtx.Err() == context.DeadlineExceeded && !aborted(classifierSignal(options)) {
			return classifierHTTPResponse{}, &classifierTimeoutError{timeoutMs: *options.TimeoutMs}
		}
		return classifierHTTPResponse{}, err
	}
	raw, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		return classifierHTTPResponse{}, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return classifierHTTPResponse{}, &providerError{status: response.StatusCode, body: string(raw)}
	}
	return classifierHTTPResponse{status: response.StatusCode, headers: response.Header, body: raw}, nil
}

func classifierRetryableStatus(status int) bool {
	return status == 408 || status == 409 || status == 429 || status >= 500
}

func classifierRetryDelayMs(retryIndex int) float64 {
	delay := 0.5 * float64(int64(1)<<uint(retryIndex))
	if delay > 8 {
		delay = 8
	}
	return delay * 1000
}

// classifierRequest performs a POST with the upstream retry policy
// (default 2 retries, exponential backoff, fresh timeout per attempt).
func classifierRequest(ctx context.Context, options *types.ProviderRequestOptions, url string, headers map[string]string, body []byte) (classifierHTTPResponse, error) {
	maxRetries := 2
	if options != nil && options.MaxRetries != nil {
		maxRetries = *options.MaxRetries
	}
	retryIndex := 0
	for {
		result, err := classifierRequestOnce(ctx, options, url, headers, body)
		if err == nil {
			return result, nil
		}
		if ctx != nil && ctx.Err() != nil {
			return classifierHTTPResponse{}, err
		}
		if aborted(classifierSignal(options)) {
			return classifierHTTPResponse{}, err
		}
		retryable := false
		var timeoutErr *classifierTimeoutError
		if errors.As(err, &timeoutErr) {
			retryable = true
		}
		var httpErr *providerError
		if errors.As(err, &httpErr) && classifierRetryableStatus(httpErr.status) {
			retryable = true
		}
		if retryIndex >= maxRetries || !retryable {
			return classifierHTTPResponse{}, err
		}
		if sleepErr := utils.Sleep(ctx, classifierRetryDelayMs(retryIndex)); sleepErr != nil {
			return classifierHTTPResponse{}, err
		}
		retryIndex++
	}
}

// classifierStopReason selects the terminal stop reason for a failed request.
func classifierStopReason(ctx context.Context, options *types.ClassifierOptions, err error) types.ClassifierStopReason {
	if options != nil && aborted(options.Signal) {
		return types.ClassifierStopReasonAborted
	}
	if ctx != nil && ctx.Err() != nil {
		return types.ClassifierStopReasonAborted
	}
	return types.ClassifierStopReasonError
}

// classifierErrorMessage renders a provider error with the transport label as a
// prefix when a status was extracted.
func classifierErrorMessage(err error, label string) string {
	prefix := label + " error"
	return fmtProviderError(err, &prefix)
}

// classifySystemOne runs one System One classification over the given
// transport. The result always carries a stop reason; request failures are
// represented in the returned result rather than returned as an error.
func classifySystemOne(ctx context.Context, transport SystemOneTransport, model *types.ClassifierModel, request *types.ClassifierContext, options *types.ClassifierOptions) types.ClassifierResult {
	output := types.ClassifierResult{
		Answers:    map[string]types.ClassifierAnswer{},
		StopReason: types.ClassifierStopReasonStop,
		Timestamp:  nowMillis(),
	}
	if model != nil {
		output.Api = model.Api
		output.Provider = model.Provider
		output.Model = model.Id
	}

	err := func() error {
		if model == nil {
			return fmt.Errorf("No classifier model provided")
		}
		if string(model.Api) != transport.Api {
			return fmt.Errorf("Unsupported classifier API: %s", model.Api)
		}
		if options == nil || options.APIKey == nil || *options.APIKey == "" {
			return fmt.Errorf("No API key for provider: %s", model.Provider)
		}
		apiKey := *options.APIKey

		var payload any = transport.Payload(model, systemOneWireRequest(request))
		if options.OnPayload != nil {
			transformed, err := options.OnPayload(payload, classifierModelAsModel(model))
			if err != nil {
				return err
			}
			if transformed != nil {
				payload = transformed
			}
		}
		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		headers := systemOneRequestHeaders(model, apiKey, options.Headers)
		response, err := classifierRequest(ctx, &options.ProviderRequestOptions, transport.URL(model), headers, bodyBytes)
		if err != nil {
			return err
		}
		if options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.status, Headers: utils.HeadersToRecord(response.headers)}, classifierModelAsModel(model))
		}
		var body any
		if err := json.Unmarshal(response.body, &body); err != nil {
			return fmt.Errorf("%s returned an unexpected response", transport.Label)
		}
		result, err := transport.Output(body)
		if err != nil {
			return err
		}
		// Set before parsing answers: a request with malformed answers was still
		// billed.
		if usage := classifierParseUsage(result["usage"], model); usage != nil {
			output.Usage = usage
		}
		answers, err := classifierParseAnswers(transport.Label, result["answers"], request)
		if err != nil {
			return err
		}
		output.Answers = answers
		return nil
	}()

	if err != nil {
		message := classifierErrorMessage(err, transport.Label)
		output.StopReason = classifierStopReason(ctx, options, err)
		output.ErrorMessage = &message
	}
	return output
}
