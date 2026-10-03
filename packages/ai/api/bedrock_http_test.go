package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// These tests own the Go-specific request-body adaptation described in
// bedrock_http.go. They intentionally exercise the unexported seam so they stay
// focused on the reader-only boundary rather than re-testing the AWS SDK.

// bedrockHTTPTestWriterToBody mimics the closed-body behavior of Smithy's
// safeWriteToReadCloser: after Close, Read reports an ordinary io.EOF while
// WriteTo reports io.EOF as an error. net/http treats the latter as a failed
// excess-byte probe.
type bedrockHTTPTestWriterToBody struct {
	reader     io.Reader
	closed     bool
	closeError error
}

func (b *bedrockHTTPTestWriterToBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, io.EOF
	}
	return b.reader.Read(p)
}

func (b *bedrockHTTPTestWriterToBody) WriteTo(w io.Writer) (int64, error) {
	if b.closed {
		return 0, io.EOF
	}
	return io.Copy(w, b.reader)
}

func (b *bedrockHTTPTestWriterToBody) Close() error {
	b.closed = true
	return b.closeError
}

// TestBedrockHTTPClientHidesWriterToProbe documents the counterexample and the
// repaired boundary without relying on scheduler timing: an exposed WriterTo
// fails the post-Content-Length probe after close, while the wrapper's
// reader-only body reports EOF normally.
func TestBedrockHTTPClientHidesWriterToProbe(t *testing.T) {
	payload := []byte("fixture request payload")

	// Unmodified boundary: the closed WriterTo fails the probe.
	exposed := &bedrockHTTPTestWriterToBody{reader: bytes.NewReader(payload)}
	if err := exposed.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, exposed); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("exposed WriterTo should fail the closed-body probe, got n=%d err=%v", n, err)
	}

	original := &bedrockHTTPTestWriterToBody{reader: bytes.NewReader(payload)}
	req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", original)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(payload))

	var sentBody io.ReadCloser
	delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
		if sent == req || sent.Body == original {
			t.Fatal("wrapper must clone the request and body")
		}
		if _, ok := sent.Body.(io.WriterTo); ok {
			t.Fatal("wrapper must not expose io.WriterTo")
		}
		sentBody = sent.Body
		written, err := io.ReadAll(sent.Body)
		if err != nil || !bytes.Equal(written, payload) {
			t.Fatalf("request body changed: %q %v", written, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	if _, err := newBedrockHTTPClient(delegate).Do(req); err != nil {
		t.Fatalf("wrapper Do failed: %v", err)
	}
	// net/http closes the request body after the response headers arrive and
	// then probes the original body for excess bytes. The wrapper must make
	// that probe succeed.
	if err := sentBody.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, sentBody); n != 0 || err != nil {
		t.Fatalf("repaired closed-body probe must succeed, got n=%d err=%v", n, err)
	}
	if !original.closed {
		t.Fatal("wrapper close did not reach the original body")
	}
}

// TestBedrockHTTPClientClonesRequestAndReplay verifies request semantics are
// preserved: context cancellation, method, URL, headers, ContentLength, body
// bytes, body close errors, and GetBody replay.
func TestBedrockHTTPClientClonesRequestAndReplay(t *testing.T) {
	closeError := errors.New("fixture close error")
	original := &bedrockHTTPTestWriterToBody{reader: strings.NewReader("payload"), closeError: closeError}
	replay := &bedrockHTTPTestWriterToBody{reader: strings.NewReader("payload"), closeError: closeError}

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), bedrockHTTPTestContextKey{}, "fixture"))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/path?query=1", original)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 7
	req.Header.Set("X-Fixture", "preserved")
	req.GetBody = func() (io.ReadCloser, error) { return replay, nil }

	delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
		if sent == req || sent.Body == original {
			t.Fatal("caller request or body was mutated")
		}
		if sent.Method != http.MethodPost || sent.URL.String() != "https://fixture.invalid/path?query=1" {
			t.Fatalf("method or URL changed: %s %s", sent.Method, sent.URL)
		}
		if sent.ContentLength != 7 || sent.Header.Get("X-Fixture") != "preserved" {
			t.Fatal("ContentLength or headers changed")
		}
		if sent.Context().Value(bedrockHTTPTestContextKey{}) != "fixture" {
			t.Fatal("request context value changed")
		}
		cancel()
		if !errors.Is(sent.Context().Err(), context.Canceled) {
			t.Fatal("request cancellation was disconnected")
		}
		if _, ok := sent.Body.(io.WriterTo); ok {
			t.Fatal("body must not expose io.WriterTo")
		}
		replayBody, err := sent.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := replayBody.(io.WriterTo); ok {
			t.Fatal("replay body must not expose io.WriterTo")
		}
		for _, body := range []io.ReadCloser{sent.Body, replayBody} {
			data, err := io.ReadAll(body)
			if err != nil || string(data) != "payload" {
				t.Fatalf("body bytes changed: %q %v", data, err)
			}
			if !errors.Is(body.Close(), closeError) {
				t.Fatal("close error was not preserved")
			}
			if n, err := io.Copy(io.Discard, body); n != 0 || err != nil {
				t.Fatalf("closed body probe failed: %d %v", n, err)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	if _, err := newBedrockHTTPClient(delegate).Do(req); err != nil {
		t.Fatalf("wrapper Do failed: %v", err)
	}
	if req.Body != original || !original.closed || !replay.closed {
		t.Fatal("caller body identity or delegated close changed")
	}
	if _, ok := req.Body.(io.WriterTo); !ok {
		t.Fatal("caller body must remain untouched")
	}
	if req.GetBody == nil {
		t.Fatal("caller GetBody must remain set")
	}
}

// TestBedrockHTTPClientPreservesEmptyBodies checks that nil and http.NoBody are
// forwarded unchanged and never wrapped.
func TestBedrockHTTPClientPreservesEmptyBodies(t *testing.T) {
	for _, body := range []io.ReadCloser{nil, http.NoBody} {
		req, err := http.NewRequest(http.MethodGet, "https://fixture.invalid", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Body = body
		delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
			if sent.Body != body || sent.GetBody != nil {
				t.Fatalf("empty body sentinel changed: %v", sent.Body)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})
		if _, err := newBedrockHTTPClient(delegate).Do(req); err != nil {
			t.Fatalf("wrapper Do failed: %v", err)
		}
	}
}

// TestBedrockHTTPClientPreservesReplayErrors checks that GetBody errors pass
// through unchanged.
func TestBedrockHTTPClientPreservesReplayErrors(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	wantError := errors.New("fixture replay error")
	req.GetBody = func() (io.ReadCloser, error) { return nil, wantError }
	delegate := smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
		body, err := sent.GetBody()
		if body != nil || err != wantError {
			t.Fatalf("GetBody error changed: body=%v err=%v", body, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	if _, err := newBedrockHTTPClient(delegate).Do(req); err != nil {
		t.Fatalf("wrapper Do failed: %v", err)
	}
}

// TestBedrockHTTPClientReturnsDelegateResult checks that the delegate's
// response and error are returned unchanged and that the response body is not
// read, buffered, or closed by the wrapper.
func TestBedrockHTTPClientReturnsDelegateResult(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusOK, Body: &bedrockHTTPTestUntouchedBody{reader: strings.NewReader("stream")}}
	delegate := smithyhttp.ClientDoFunc(func(*http.Request) (*http.Response, error) {
		return response, nil
	})
	req, err := http.NewRequest(http.MethodGet, "https://fixture.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := newBedrockHTTPClient(delegate).Do(req)
	if err != nil || got != response {
		t.Fatalf("response changed: %p %v", got, err)
	}
	tracked := response.Body.(*bedrockHTTPTestUntouchedBody)
	if tracked.reads != 0 || tracked.closed {
		t.Fatal("wrapper read or closed the response body")
	}

	wantError := errors.New("fixture delegate error")
	failing := smithyhttp.ClientDoFunc(func(*http.Request) (*http.Response, error) { return nil, wantError })
	if _, err := newBedrockHTTPClient(failing).Do(req); err != wantError {
		t.Fatalf("delegate error changed: %v", err)
	}
}

// TestBedrockHTTPClientConcurrentRequests exercises the shared wrapper from many
// goroutines. The wrapper is stateless, so concurrent use must be race-free.
func TestBedrockHTTPClientConcurrentRequests(t *testing.T) {
	client := newBedrockHTTPClient(smithyhttp.ClientDoFunc(func(sent *http.Request) (*http.Response, error) {
		if _, ok := sent.Body.(io.WriterTo); ok {
			return nil, errors.New("body exposed WriterTo")
		}
		data, err := io.ReadAll(sent.Body)
		if err != nil {
			return nil, err
		}
		if string(data) != "payload" {
			return nil, errors.New("body bytes changed")
		}
		body := &bedrockHTTPTestUntouchedBody{reader: strings.NewReader("stream")}
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	}))

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader("payload"))
			if err != nil {
				errs <- err
				return
			}
			response, err := client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			if _, err := io.ReadAll(response.Body); err != nil {
				errs <- err
				return
			}
			_ = response.Body.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// bedrockHTTPTestUntouchedBody tracks whether a response body was read or
// closed by the wrapper.
type bedrockHTTPTestUntouchedBody struct {
	reader io.Reader
	reads  int
	closed bool
}

func (b *bedrockHTTPTestUntouchedBody) Read(p []byte) (int, error) {
	b.reads++
	return b.reader.Read(p)
}

func (b *bedrockHTTPTestUntouchedBody) Close() error {
	b.closed = true
	return nil
}

type bedrockHTTPTestContextKey struct{}
