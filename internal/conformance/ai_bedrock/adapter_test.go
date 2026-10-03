package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only test bridge for the ai-bedrock batch. It translates input
// operations into calls to the real exported Go SDK. It does not reimplement
// SDK behavior, read fixtures or invoke Node.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Op      string                       `json:"op"`
		API     string                       `json:"api"`
		Mode    string                       `json:"mode"`
		Body    string                       `json:"body"`
		Status  int                          `json:"status"`
		Bedrock []map[string]json.RawMessage `json:"bedrock"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid adapter input: %w", err)
	}

	switch request.Op {
	case "protocol":
		return runBedrockProtocol(ctx, request.API, request.Mode, request.Body, request.Status, request.Bedrock)
	default:
		return nil, fmt.Errorf("unsupported ai-bedrock operation %q", request.Op)
	}
}

type bedrockRecordedRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body"`
}

type bedrockProtocolResult struct {
	Requests   []bedrockRecordedRequest `json:"requests"`
	Events     []string                 `json:"events"`
	Content    []types.ContentBlock     `json:"content"`
	Usage      bedrockUsage             `json:"usage"`
	StopReason types.StopReason         `json:"stopReason"`
	ResponseID *string                  `json:"responseId"`
}

// bedrockUsage mirrors the evaluator's usage shape, including the explicit
// undefined sentinel for cacheWrite1h that the JS oracle preserves.
type bedrockUsage struct {
	Input        float64         `json:"input"`
	Output       float64         `json:"output"`
	CacheRead    float64         `json:"cacheRead"`
	CacheWrite   float64         `json:"cacheWrite"`
	TotalTokens  float64         `json:"totalTokens"`
	Cost         types.UsageCost `json:"cost"`
	CacheWrite1h any             `json:"cacheWrite1h,omitempty"`
}

func runBedrockProtocol(ctx context.Context, apiName, mode, body string, status int, bedrockEvents []map[string]json.RawMessage) (json.RawMessage, error) {
	if apiName != "bedrock-converse-stream" {
		return nil, fmt.Errorf("unsupported protocol api %q", apiName)
	}

	responseBytes, err := encodeBedrockEventStream(bedrockEvents)
	if err != nil {
		return nil, err
	}

	var (
		mu       sync.Mutex
		requests []bedrockRecordedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &decoded)
		}
		mu.Lock()
		requests = append(requests, bedrockRecordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: decoded})
		mu.Unlock()

		contentType := "application/vnd.amazon.eventstream"
		if status != 200 {
			contentType = "application/json"
		}
		w.Header().Set("content-type", contentType)
		w.WriteHeader(status)
		if mode == "fragmented" {
			flusher, _ := w.(http.Flusher)
			for i := 0; i < len(responseBytes); i += 3 {
				end := i + 3
				if end > len(responseBytes) {
					end = len(responseBytes)
				}
				written, writeErr := w.Write(responseBytes[i:end])
				if writeErr != nil {
					fmt.Fprintf(os.Stderr, "Bedrock fragmented fixture write error: bytes=%d/%d offset=%d error=%v\n", written, end-i, i, writeErr)
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		if mode == "cancelled" {
			return
		}
		_, _ = w.Write(responseBytes)
	}))
	defer server.Close()

	model := &types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.ApiBedrockConverseStream,
		Provider:      types.ProviderId("fixture"),
		BaseUrl:       server.URL,
		Reasoning:     false,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 8192,
		MaxTokens:     1024,
		Cost:          types.ModelCost{},
	}

	apiKey := "fixture-key"
	maxTokens := 16
	temperature := 0.0
	region := "us-east-1"
	env := types.ProviderEnv{
		"AWS_BEDROCK_FORCE_HTTP1": stringPointer("1"),
		"NO_PROXY":                stringPointer("*"),
	}

	var signal <-chan struct{}
	if mode == "cancelled" {
		closed := make(chan struct{})
		close(closed)
		signal = closed
	}

	options := &api.BedrockOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{
				APIKey: &apiKey,
				Signal: signal,
				Env:    env,
			},
			MaxTokens:   &maxTokens,
			Temperature: &temperature,
		},
		BearerToken: &apiKey,
		Region:      &region,
	}

	transcript := types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	})

	stream := api.BedrockConverseStream(model, transcript, options)

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
	if status == http.StatusOK && mode == "fragmented" && result.ErrorMessage != nil {
		fmt.Fprintf(os.Stderr, "Bedrock fragmented SDK diagnostic: stopReason=%s error=%s\n", result.StopReason, *result.ErrorMessage)
	}
	if result.Content == nil {
		result.Content = []types.ContentBlock{}
	}

	mu.Lock()
	recorded := append([]bedrockRecordedRequest(nil), requests...)
	mu.Unlock()
	if recorded == nil {
		recorded = []bedrockRecordedRequest{}
	}

	usage := bedrockUsage{
		Input:       result.Usage.Input,
		Output:      result.Usage.Output,
		CacheRead:   result.Usage.CacheRead,
		CacheWrite:  result.Usage.CacheWrite,
		TotalTokens: result.Usage.TotalTokens,
		Cost:        result.Usage.Cost,
	}
	// The JS oracle's usage object carries cacheWrite1h only once the metadata
	// event has been handled; before that the property is absent. A zero usage
	// therefore omits the key entirely. When usage was populated but no 1h cache
	// details were reported, the property is present but undefined.
	if bedrockUsagePopulated(result.Usage) {
		if result.Usage.CacheWrite1h != nil {
			usage.CacheWrite1h = *result.Usage.CacheWrite1h
		} else {
			usage.CacheWrite1h = map[string]any{"$undefined": true}
		}
	}

	response := bedrockProtocolResult{
		Requests:   recorded,
		Events:     events,
		Content:    result.Content,
		Usage:      usage,
		StopReason: result.StopReason,
		ResponseID: result.ResponseId,
	}
	return json.Marshal(response)
}

func bedrockUsagePopulated(usage types.Usage) bool {
	return usage.Input != 0 || usage.Output != 0 || usage.CacheRead != 0 ||
		usage.CacheWrite != 0 || usage.TotalTokens != 0 || usage.CacheWrite1h != nil
}

func encodeBedrockEventStream(events []map[string]json.RawMessage) ([]byte, error) {
	encoder := eventstream.NewEncoder()
	var buffer bytes.Buffer
	for _, event := range events {
		for name, payload := range event {
			var headers eventstream.Headers
			headers.Set(":message-type", eventstream.StringValue("event"))
			headers.Set(":event-type", eventstream.StringValue(name))
			headers.Set(":content-type", eventstream.StringValue("application/json"))
			if err := encoder.Encode(&buffer, eventstream.Message{Headers: headers, Payload: payload}); err != nil {
				return nil, err
			}
		}
	}
	return buffer.Bytes(), nil
}

func stringPointer(value string) *string { return &value }
