package conformance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only test bridge for the ai-openai batch. It translates input
// operations into calls to the real exported Go SDK. It does not reimplement SDK
// behavior, read fixtures or invoke Node.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Op     string          `json:"op"`
		File   string          `json:"file"`
		API    string          `json:"api"`
		Mode   string          `json:"mode"`
		Body   string          `json:"body"`
		Status int             `json:"status"`
		Args   json.RawMessage `json:"args"`
		Fn     string          `json:"fn"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid adapter input: %w", err)
	}

	switch request.Op {
	case "protocol":
		return runProtocol(ctx, request.API, request.Mode, request.Body, request.Status)
	case "call":
		return runCall(request.Fn, request.Args)
	default:
		return nil, fmt.Errorf("unsupported ai-openai operation %q", request.Op)
	}
}

type recordedRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body"`
}

type protocolResult struct {
	Requests   []recordedRequest    `json:"requests"`
	Events     []string             `json:"events"`
	Content    []types.ContentBlock `json:"content"`
	Usage      types.Usage          `json:"usage"`
	StopReason types.StopReason     `json:"stopReason"`
	ResponseID *string              `json:"responseId"`
}

func runProtocol(ctx context.Context, apiName, mode, body string, status int) (json.RawMessage, error) {
	var (
		mu       sync.Mutex
		requests []recordedRequest
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &decoded)
		}
		mu.Lock()
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: decoded})
		mu.Unlock()

		contentType := "text/event-stream"
		if status != 200 {
			contentType = "application/json"
		}
		w.Header().Set("content-type", contentType)
		w.WriteHeader(status)
		if mode == "fragmented" {
			flusher, _ := w.(http.Flusher)
			for i := 0; i < len(body); i += 3 {
				end := i + 3
				if end > len(body) {
					end = len(body)
				}
				_, _ = w.Write([]byte(body[i:end]))
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		_, _ = w.Write([]byte(body))
	})

	server := httptest.NewServer(handler)
	defer server.Close()
	baseURL := server.URL

	model := types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.Api(apiName),
		Provider:      types.ProviderId("fixture"),
		BaseUrl:       baseURL,
		Reasoning:     false,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 8192,
		MaxTokens:     1024,
	}

	apiKey := "fixture-key"
	if apiName == "openai-codex-responses" {
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"fixture"}}`))
		apiKey = "x." + payload + ".x"
	}

	var signal <-chan struct{}
	if mode == "cancelled" {
		closed := make(chan struct{})
		close(closed)
		signal = closed
	}

	maxTokens := 16
	temperature := 0.0
	transport := types.TransportSSE
	base := types.StreamOptions{
		ProviderRequestOptions: types.ProviderRequestOptions{
			APIKey: &apiKey,
			Signal: signal,
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Transport:   &transport,
	}

	transcript := types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	})

	var stream *types.AssistantMessageEventStream
	switch types.Api(apiName) {
	case types.ApiOpenAICompletions:
		stream = api.OpenAICompletionsStream(&model, transcript, &api.OpenAICompletionsOptions{StreamOptions: base})
	case types.ApiOpenAIResponses:
		stream = api.OpenAIResponsesStream(&model, transcript, &api.OpenAIResponsesOptions{StreamOptions: base})
	case types.ApiAzureOpenAIResponses:
		stream = api.AzureOpenAIResponsesStream(&model, transcript, &api.AzureOpenAIResponsesOptions{StreamOptions: base})
	case types.ApiOpenAICodexResponses:
		stream = api.OpenAICodexResponsesStream(&model, transcript, &api.OpenAICodexResponsesOptions{StreamOptions: base})
	default:
		return nil, fmt.Errorf("unsupported protocol api %q", apiName)
	}

	events := []string{}
	for {
		item, ok := <-stream.Next()
		if !ok || item.Done {
			break
		}
		events = append(events, string(item.Value.Type))
	}

	result, err := stream.Result(ctx)
	if err != nil {
		return nil, err
	}
	if result.Content == nil {
		result.Content = []types.ContentBlock{}
	}

	mu.Lock()
	recorded := append([]recordedRequest(nil), requests...)
	mu.Unlock()
	if recorded == nil {
		recorded = []recordedRequest{}
	}

	response := protocolResult{
		Requests:   recorded,
		Events:     events,
		Content:    result.Content,
		Usage:      result.Usage,
		StopReason: result.StopReason,
		ResponseID: result.ResponseId,
	}
	return json.Marshal(response)
}

func runCall(fn string, args json.RawMessage) (json.RawMessage, error) {
	var values []json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &values); err != nil {
			return nil, fmt.Errorf("invalid call args: %w", err)
		}
	}
	switch fn {
	case "clampOpenAIPromptCacheKey":
		var key *string
		if len(values) > 0 && string(values[0]) != "null" {
			var text string
			if err := json.Unmarshal(values[0], &text); err != nil {
				return nil, err
			}
			key = &text
		}
		clamped := api.ClampOpenAIPromptCacheKey(key)
		return json.Marshal(encodeOptional(clamped))
	case "inferCopilotInitiator":
		messages, err := decodeMessages(values)
		if err != nil {
			return nil, err
		}
		return json.Marshal(string(api.InferCopilotInitiator(messages)))
	case "hasCopilotVisionInput":
		messages, err := decodeMessages(values)
		if err != nil {
			return nil, err
		}
		return json.Marshal(api.HasCopilotVisionInput(messages))
	case "buildCopilotDynamicHeaders":
		messages, err := decodeMessages(values)
		if err != nil {
			return nil, err
		}
		hasImages := api.HasCopilotVisionInput(messages)
		return json.Marshal(api.BuildCopilotDynamicHeaders(messages, hasImages))
	default:
		return nil, fmt.Errorf("unsupported call %q", fn)
	}
}

func encodeOptional(value *string) any {
	if value == nil {
		return map[string]bool{"$undefined": true}
	}
	return *value
}

func decodeMessages(values []json.RawMessage) ([]types.Message, error) {
	if len(values) == 0 {
		return []types.Message{}, nil
	}
	var messages []types.Message
	if err := json.Unmarshal(values[0], &messages); err != nil {
		return nil, err
	}
	if messages == nil {
		messages = []types.Message{}
	}
	return messages, nil
}

var _ = strings.TrimSpace
