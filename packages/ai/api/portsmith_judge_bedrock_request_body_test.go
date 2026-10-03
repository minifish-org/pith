package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// This fixes the race schedule at the boundary used by the real SDK: the
// request has been written, Do returns response headers, Smithy closes its
// request body, and net/http probes for bytes beyond ContentLength. The probe
// must still succeed while the response remains an unread stream.
func TestPortsmithJudgeBedrockRequestBodyLifetime(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"text":"fixture"}]}]}`)
	request := smithyhttp.NewStackRequest().(*smithyhttp.Request)
	var err error
	request, err = request.SetStream(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Method = http.MethodPost
	request.URL, err = url.Parse("https://bedrock-runtime.us-east-1.amazonaws.com/model/fixture/converse-stream")
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer fixture-only")

	responseBody := &portsmithJudgeBedrockUnreadBody{reader: strings.NewReader("first frame\nlast frame\n")}
	wantResponse := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/vnd.amazon.eventstream"}},
		Body:       responseBody,
	}
	var sentRequest *http.Request
	var sentPayload []byte
	doCalls := 0
	delegate := smithyhttp.ClientDoFunc(func(req *http.Request) (*http.Response, error) {
		doCalls++
		sentRequest = req
		if req.ContentLength != int64(len(payload)) {
			return nil, errors.New("declared request length changed")
		}
		var written bytes.Buffer
		if _, err := io.CopyN(&written, req.Body, req.ContentLength); err != nil {
			return nil, err
		}
		sentPayload = append([]byte(nil), written.Bytes()...)
		// Deliberately do not run the excess-byte probe yet. Smithy's real
		// ClientHandler closes its original request body after Do returns.
		return wantResponse, nil
	})
	handler := smithyhttp.NewClientHandler(newBedrockHTTPClient(delegate))
	result, _, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("request failed before response headers: %v", err)
	}
	response := result.(*smithyhttp.Response).Response
	if response != wantResponse || response.Body != responseBody {
		t.Fatal("HTTP response or streaming body was replaced")
	}
	if responseBody.reads.Load() != 0 {
		t.Fatal("HTTP client read or buffered the response body before returning headers")
	}
	if doCalls != 1 || !bytes.Equal(sentPayload, payload) {
		t.Fatalf("request was changed or retried: calls=%d payload=%q", doCalls, sentPayload)
	}
	if sentRequest.Header.Get("Authorization") != "Bearer fixture-only" || sentRequest.Header.Get("Content-Type") != "application/json" {
		t.Fatal("configured request headers changed")
	}
	// Exactly the post-ContentLength copy in net/http/transfer.go. The
	// original Smithy WriterTo returns EOF after Close; io.Copy treats that
	// as an error and net/http closes the active response connection. A
	// reader-only request wrapper converts ordinary Reader EOF to success.
	n, err := io.Copy(io.Discard, sentRequest.Body)
	if n != 0 || err != nil {
		t.Fatalf("closed request excess-byte probe must succeed: copied=%d err=%v", n, err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "first frame\nlast frame\n" {
		t.Fatalf("response stream was damaged: body=%q err=%v", body, err)
	}
	if err := response.Body.Close(); err != nil || !responseBody.closed.Load() {
		t.Fatalf("response close did not reach original body: %v", err)
	}
}

func TestPortsmithJudgeBedrockRequestBodyReplay(t *testing.T) {
	t.Run("body and replay preserve readers and close", func(t *testing.T) {
		closeError := errors.New("fixture close error")
		originalBody := &portsmithJudgeBedrockReplayBody{reader: strings.NewReader("payload"), closeError: closeError}
		replayBody := &portsmithJudgeBedrockReplayBody{reader: strings.NewReader("payload"), closeError: closeError}
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), portsmithJudgeBedrockContextKey{}, "fixture context"))
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid", originalBody)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = 7
		req.GetBody = func() (io.ReadCloser, error) { return replayBody, nil }
		req.Header.Set("X-Fixture", "preserved")
		response := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
		calls := 0
		delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
			calls++
			if sent == req || sent.Body == originalBody {
				t.Fatal("wrapper must clone request and leave caller's body intact")
			}
			if sent.Context().Value(portsmithJudgeBedrockContextKey{}) != "fixture context" || sent.ContentLength != 7 || sent.Header.Get("X-Fixture") != "preserved" {
				t.Fatal("request context, length, or headers changed")
			}
			cancel()
			if !errors.Is(sent.Context().Err(), context.Canceled) {
				t.Fatal("request cancellation was disconnected")
			}
			portsmithJudgeBedrockAssertReaderOnly(t, sent.Body)
			replay, err := sent.GetBody()
			if err != nil {
				t.Fatal(err)
			}
			portsmithJudgeBedrockAssertReaderOnly(t, replay)
			for _, body := range []io.ReadCloser{sent.Body, replay} {
				data, err := io.ReadAll(body)
				if err != nil || string(data) != "payload" {
					t.Fatalf("body or replay bytes changed: %q, %v", data, err)
				}
				if !errors.Is(body.Close(), closeError) {
					t.Fatal("body close error was not preserved")
				}
				if n, err := io.Copy(io.Discard, body); n != 0 || err != nil {
					t.Fatalf("closed body or replay excess probe failed: %d, %v", n, err)
				}
			}
			return response, nil
		})
		got, err := newBedrockHTTPClient(delegate).Do(req)
		if err != nil || got != response || calls != 1 {
			t.Fatalf("delegate behavior changed: response=%p err=%v calls=%d", got, err, calls)
		}
		if req.Body != originalBody || !originalBody.closed || !replayBody.closed {
			t.Fatal("caller body identity or delegated close changed")
		}
		if _, ok := req.Body.(io.WriterTo); !ok {
			t.Fatal("wrapper mutated caller body instead of cloning request")
		}
	})

	t.Run("empty bodies and delegate errors", func(t *testing.T) {
		for _, body := range []io.ReadCloser{nil, http.NoBody} {
			req, err := http.NewRequest(http.MethodGet, "https://fixture.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Body = body
			wantError := errors.New("fixture delegate error")
			delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
				if sent.Body != body || sent.GetBody != nil {
					t.Fatal("nil or NoBody sentinel changed")
				}
				return nil, wantError
			})
			if _, err := newBedrockHTTPClient(delegate).Do(req); err != wantError {
				t.Fatalf("delegate error changed: %v", err)
			}
		}
	})

	t.Run("replay error", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader("payload"))
		if err != nil {
			t.Fatal(err)
		}
		wantError := errors.New("fixture replay error")
		req.GetBody = func() (io.ReadCloser, error) { return nil, wantError }
		delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
			if body, err := sent.GetBody(); body != nil || err != wantError {
				t.Fatalf("GetBody error changed: body=%v err=%v", body, err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})
		if _, err := newBedrockHTTPClient(delegate).Do(req); err != nil {
			t.Fatal(err)
		}
	})
}

func portsmithJudgeBedrockAssertReaderOnly(t *testing.T, body io.ReadCloser) {
	t.Helper()
	if _, ok := body.(io.WriterTo); ok {
		t.Fatal("request body must expose Read and Close without WriterTo")
	}
}

type portsmithJudgeBedrockContextKey struct{}

type portsmithJudgeBedrockUnreadBody struct {
	reader io.Reader
	reads  atomic.Int64
	closed atomic.Bool
}

func (b *portsmithJudgeBedrockUnreadBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.reader.Read(p)
}

func (b *portsmithJudgeBedrockUnreadBody) Close() error {
	b.closed.Store(true)
	return nil
}

// This body reproduces the contract difference between an ordinary Reader and
// Smithy's safeWriteToReadCloser after Close without relying on private SDK APIs.
type portsmithJudgeBedrockReplayBody struct {
	reader     io.Reader
	closed     bool
	closeError error
}

func (b *portsmithJudgeBedrockReplayBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, io.EOF
	}
	return b.reader.Read(p)
}

func (b *portsmithJudgeBedrockReplayBody) WriteTo(w io.Writer) (int64, error) {
	if b.closed {
		return 0, io.EOF
	}
	return io.Copy(w, b.reader)
}

func (b *portsmithJudgeBedrockReplayBody) Close() error {
	b.closed = true
	return b.closeError
}
