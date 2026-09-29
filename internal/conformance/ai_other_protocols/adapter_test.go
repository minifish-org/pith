package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/images"
	"github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only test bridge for the ai-other-protocols batch. It
// translates input operations into calls to the real exported Go SDK. It does
// not reimplement SDK behavior, read fixtures, or invoke Node.
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
		Symbol string          `json:"symbol"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid adapter input: %w", err)
	}

	switch request.Op {
	case "protocol":
		return runAIOtherProtocol(ctx, request.API, request.Mode, request.Body, request.Status)
	case "call":
		return runAIOtherCall(ctx, request.File, request.Fn, request.Args)
	case "catalog":
		return runAIOtherCatalog(request.File, request.Symbol)
	default:
		return nil, fmt.Errorf("unsupported ai-other-protocols operation %q", request.Op)
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

func runAIOtherProtocol(ctx context.Context, apiName, mode, body string, status int) (json.RawMessage, error) {
	var (
		mu       sync.Mutex
		requests []recordedRequest
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded any
		if len(raw) > 0 {
			json.Unmarshal(raw, &decoded)
		}
		mu.Lock()
		requests = append(requests, recordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: decoded})
		mu.Unlock()

		contentType := "text/event-stream"
		if status != http.StatusOK {
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
				w.Write([]byte(body[i:end]))
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		w.Write([]byte(body))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	model := types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.Api(apiName),
		Provider:      types.ProviderId("fixture"),
		BaseUrl:       server.URL,
		Reasoning:     false,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 8192,
		MaxTokens:     1024,
	}

	apiKey := "fixture-key"
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
	case types.ApiMistralConversations:
		stream = api.MistralConversationsStream(&model, transcript, &api.MistralOptions{StreamOptions: base})
	case types.ApiPiMessages:
		stream = api.PiMessagesStream(&model, transcript, &api.PiMessagesOptions{StreamOptions: base})
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

func runAIOtherCatalog(file, symbol string) (json.RawMessage, error) {
	if strings.HasSuffix(file, "image-models.generated.ts") && symbol == "IMAGE_MODELS" {
		return json.Marshal(catalog.IMAGE_MODELS)
	}
	return nil, fmt.Errorf("unsupported catalog %s#%s", file, symbol)
}

func runAIOtherCall(ctx context.Context, file, fn string, args json.RawMessage) (json.RawMessage, error) {
	_ = ctx
	var values []json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &values); err != nil {
			return nil, fmt.Errorf("invalid call args: %w", err)
		}
	}
	switch {
	case strings.HasSuffix(file, "images.ts") && fn == "generateImages":
		if len(values) < 2 {
			return nil, fmt.Errorf("generateImages needs model and context")
		}
		var model types.ImagesModel
		if err := json.Unmarshal(values[0], &model); err != nil {
			return nil, err
		}
		var imagesContext types.ImagesContext
		if err := json.Unmarshal(values[1], &imagesContext); err != nil {
			return nil, err
		}
		var options *types.ImagesOptions
		if len(values) > 2 {
			var decoded types.ImagesOptions
			if err := json.Unmarshal(values[2], &decoded); err != nil {
				return nil, err
			}
			options = &decoded
		}
		output, err := images.GenerateImages(&model, &imagesContext, options)
		if err != nil {
			return nil, err
		}
		return json.Marshal(output)

	case strings.HasSuffix(file, "api/openrouter-images.ts") && fn == "generateImages":
		if len(values) < 2 {
			return nil, fmt.Errorf("generateImages needs model and context")
		}
		var model types.ImagesModel
		if err := json.Unmarshal(values[0], &model); err != nil {
			return nil, err
		}
		var imagesContext types.ImagesContext
		if err := json.Unmarshal(values[1], &imagesContext); err != nil {
			return nil, err
		}
		var options *types.ImagesOptions
		if len(values) > 2 {
			var decoded types.ImagesOptions
			if err := json.Unmarshal(values[2], &decoded); err != nil {
				return nil, err
			}
			options = &decoded
		}
		output, err := api.GenerateOpenRouterImages(&model, &imagesContext, options)
		if err != nil {
			return nil, err
		}
		return json.Marshal(output)

	case strings.HasSuffix(file, "image-models.ts") && fn == "getImageModel":
		var provider types.ImagesProviderId
		var modelID string
		if err := decodeTwo(values, &provider, &modelID); err != nil {
			return nil, err
		}
		model := images.GetImageModel(provider, modelID)
		return json.Marshal(encodeOptionalValue(model))

	case strings.HasSuffix(file, "image-models.ts") && fn == "getImageProviders":
		return json.Marshal(images.GetImageProviders())

	case strings.HasSuffix(file, "image-models.ts") && fn == "getImageModels":
		var provider types.ImagesProviderId
		if err := decodeOne(values, &provider); err != nil {
			return nil, err
		}
		return json.Marshal(images.GetImageModels(provider))

	default:
		return nil, fmt.Errorf("unsupported call %q", fn)
	}
}

func decodeOne(values []json.RawMessage, target any) error {
	if len(values) < 1 {
		return fmt.Errorf("missing argument")
	}
	return json.Unmarshal(values[0], target)
}

func decodeTwo(values []json.RawMessage, first, second any) error {
	if len(values) < 2 {
		return fmt.Errorf("missing arguments")
	}
	if err := json.Unmarshal(values[0], first); err != nil {
		return err
	}
	return json.Unmarshal(values[1], second)
}

// encodeOptionalValue renders a nil pointer as the explicit undefined marker and
// any other value as-is.
func encodeOptionalValue(value *types.ImagesModel) any {
	if value == nil {
		return map[string]bool{"$undefined": true}
	}
	return value
}
