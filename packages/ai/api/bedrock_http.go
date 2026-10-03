// This file contains the local Go transport adaptation for the Bedrock
// ConverseStream provider. The adaptation is specific to the Go AWS SDK and
// has no counterpart in the upstream TypeScript implementation.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The AWS SDK for Go v2 writes a request body through
// github.com/aws/smithy-go/transport/http/internal/io. That wrapper exposes
// io.WriterTo and reports io.EOF as an error once the request body has been
// closed. net/http copies the exact Content-Length through a LimitedReader and
// then probes the original body for excess bytes with io.Copy. When Smithy has
// already closed the request body (it does so immediately after the response
// headers arrive), a WriterTo returning io.EOF makes that probe fail, and
// net/http/transport.go closes the connection, interrupting an in-flight
// streamed response.
//
// newBedrockHTTPClient hides io.WriterTo behind a plain io.ReadCloser so the
// excess-byte probe observes an ordinary EOF. The wrapper clones each request,
// delegates Read/Close and GetBody replay to the original body, and otherwise
// leaves the request, its context, the response, and the caller's request
// untouched. The wrapper holds no mutable state, so it is safe to share across
// concurrent requests.
package api

import (
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// bedrockReaderOnlyBody presents a request body as a plain io.ReadCloser.
// Because the type does not implement io.WriterTo, net/http reads the body
// through Read, which reports io.EOF normally instead of surfacing the closed
// Smithy WriterTo error. Read and Close delegate to the original body, and the
// body is never buffered or reread.
type bedrockReaderOnlyBody struct {
	body io.ReadCloser
}

// Read delegates to the wrapped request body.
func (b *bedrockReaderOnlyBody) Read(p []byte) (int, error) {
	return b.body.Read(p)
}

// Close delegates to the wrapped request body.
func (b *bedrockReaderOnlyBody) Close() error {
	return b.body.Close()
}

// bedrockHTTPClient wraps the resolved AWS SDK HTTP client. It exists solely to
// install the reader-only request-body boundary described above; all other
// transport behavior (dialer defaults, timeouts, proxy, TLS, connection reuse)
// remains owned by the delegate returned by the SDK.
type bedrockHTTPClient struct {
	client bedrockruntime.HTTPClient
}

// newBedrockHTTPClient returns a Bedrock HTTP client that delegates to client
// while hiding io.WriterTo on request bodies. It is unexported because it is a
// Pith-internal transport seam: the BedrockOptions.Client injection point and
// the public SDK surface are unchanged.
func newBedrockHTTPClient(client bedrockruntime.HTTPClient) bedrockruntime.HTTPClient {
	return &bedrockHTTPClient{client: client}
}

// Do clones the incoming request, converts real request bodies to a
// reader-only boundary, and delegates to the resolved AWS HTTP client. The
// caller's request, its Body, and its GetBody function are never mutated, and
// the delegate's response and error are returned unchanged.
func (c *bedrockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if cloned.Body != nil && cloned.Body != http.NoBody {
		cloned.Body = wrapBedrockRequestBody(cloned.Body)
	}
	if cloned.GetBody != nil {
		original := cloned.GetBody
		cloned.GetBody = func() (io.ReadCloser, error) {
			body, err := original()
			if err != nil {
				return body, err
			}
			if body == nil || body == http.NoBody {
				return body, nil
			}
			return wrapBedrockRequestBody(body), nil
		}
	}
	return c.client.Do(cloned)
}

// wrapBedrockRequestBody hides io.WriterTo while preserving Read and Close.
func wrapBedrockRequestBody(body io.ReadCloser) io.ReadCloser {
	return &bedrockReaderOnlyBody{body: body}
}
