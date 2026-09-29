package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only test bridge for the ai-google batch. It translates input
// operations into calls to the real exported Go SDK. It does not reimplement
// SDK behavior, read fixtures or invoke Node.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Op     string `json:"op"`
		File   string `json:"file"`
		API    string `json:"api"`
		Mode   string `json:"mode"`
		Body   string `json:"body"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid adapter input: %w", err)
	}

	switch request.Op {
	case "protocol":
		return runProtocol(ctx, request.API, request.Mode, request.Body, request.Status)
	default:
		return nil, fmt.Errorf("unsupported ai-google operation %q", request.Op)
	}
}

type recordedRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body"`
}

type protocolResult struct {
	Requests   []recordedRequest `json:"requests"`
	Events     []string          `json:"events"`
	Content    []any             `json:"content"`
	Usage      types.Usage       `json:"usage"`
	StopReason types.StopReason  `json:"stopReason"`
	ResponseID *string           `json:"responseId"`
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
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			_ = decoder.Decode(&decoded)
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

	provider := types.ProviderId("fixture")
	if apiName == "google-vertex" {
		provider = types.ProviderGoogleVertex
	}
	model := types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.Api(apiName),
		Provider:      provider,
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
	base := types.StreamOptions{
		ProviderRequestOptions: types.ProviderRequestOptions{
			APIKey: &apiKey,
			Signal: signal,
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
	}

	transcript := types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	})

	var stream *types.AssistantMessageEventStream
	switch types.Api(apiName) {
	case types.ApiGoogleGenerativeAI:
		stream = api.GoogleGenerativeAIStream(&model, transcript, &api.GoogleOptions{StreamOptions: base})
	case types.ApiGoogleVertex:
		project := "fixture"
		location := "us-central1"
		stream = api.GoogleVertexStream(&model, transcript, &api.GoogleVertexOptions{
			StreamOptions: base,
			Project:       &project,
			Location:      &location,
		})
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

	mu.Lock()
	recorded := append([]recordedRequest(nil), requests...)
	mu.Unlock()
	if recorded == nil {
		recorded = []recordedRequest{}
	}

	response := protocolResult{
		Requests:   recorded,
		Events:     events,
		Content:    normalizeGoogleContent(result.Content),
		Usage:      result.Usage,
		StopReason: result.StopReason,
		ResponseID: result.ResponseId,
	}
	return json.Marshal(response)
}

// normalizeGoogleContent mirrors how the upstream streaming protocol shapes the
// optional content fields: textSignature and thinkingSignature are always own
// properties (possibly undefined), while tool call thoughtSignature is added
// only when present.
func normalizeGoogleContent(blocks []types.ContentBlock) []any {
	out := []any{}
	for _, block := range blocks {
		switch block.Type {
		case types.ContentTypeText:
			text := ""
			var signature any = undefinedValue()
			if block.Text != nil {
				text = block.Text.Text
				if block.Text.TextSignature != nil {
					signature = *block.Text.TextSignature
				}
			}
			out = append(out, map[string]any{
				"type":          "text",
				"text":          text,
				"textSignature": signature,
			})
		case types.ContentTypeThinking:
			thinking := ""
			var signature any = undefinedValue()
			if block.Thinking != nil {
				thinking = block.Thinking.Thinking
				if block.Thinking.ThinkingSignature != nil {
					signature = *block.Thinking.ThinkingSignature
				}
			}
			entry := map[string]any{
				"type":              "thinking",
				"thinking":          thinking,
				"thinkingSignature": signature,
			}
			if block.Thinking != nil && block.Thinking.Redacted != nil {
				entry["redacted"] = *block.Thinking.Redacted
			}
			out = append(out, entry)
		case types.ContentTypeImage:
			data := ""
			mimeType := ""
			if block.Image != nil {
				data = block.Image.Data
				mimeType = block.Image.MimeType
			}
			out = append(out, map[string]any{"type": "image", "data": data, "mimeType": mimeType})
		case types.ContentTypeToolCall:
			if block.ToolCall == nil {
				continue
			}
			entry := map[string]any{
				"type":      "toolCall",
				"id":        block.ToolCall.Id,
				"name":      block.ToolCall.Name,
				"arguments": block.ToolCall.Arguments,
			}
			if block.ToolCall.ThoughtSignature != nil {
				entry["thoughtSignature"] = *block.ToolCall.ThoughtSignature
			}
			out = append(out, entry)
		}
	}
	return out
}

func undefinedValue() map[string]bool {
	return map[string]bool{"$undefined": true}
}
