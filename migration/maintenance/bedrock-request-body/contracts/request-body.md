# Bedrock request-body lifetime contract

## Pinned source and observed failure

Pi source commit a13d35a742c6ef8462812a28fbe1d8c8b7431c32 defines the
Bedrock provider in packages/ai/src/api/bedrock-converse-stream.ts. Its pinned
tests cover credentials, endpoint selection, headers, response metadata, and
stop reasons; the proxy utility defines scoped proxy and bypass behavior. These
behaviors remain required. This repair addresses a Go dependency interaction
that is absent from the JavaScript transport.

Pith main CI run 37093115103 failed the unchanged fragmented Bedrock fixture
under race detection and again under ordinary testing. A separate diagnostic
branch reproduced the failure during concurrent sandbox load in run
37094412360: the SDK reported `decode event: read tcp ...: use of closed network
connection`, and the server's next three-byte write failed with `broken pipe`
at response offset 435. Focused repetitions without that load sometimes pass,
so a green rerun does not establish correctness.

## Isolated mechanism and primary source pointers

The relevant installed source versions and functions are:

- github.com/aws/smithy-go v1.28.1,
  transport/http/internal/io/safe.go: NewSafeReadCloser,
  safeWriteToReadCloser.WriteTo, safeReadCloser.Read, and safeReadCloser.Close.
  A closed Read returns EOF normally; a closed WriteTo returns `(0, io.EOF)`.
- The same module, transport/http/client.go: ClientHandler.Handle closes
  builtRequest.Body immediately after HTTPClient.Do returns response headers.
- Go 1.24.0, src/net/http/transfer.go: transferWriter.writeBody copies the exact
  Content-Length through a LimitedReader and then probes the original body for
  excess bytes with io.Copy. A Reader returning EOF ends that copy successfully;
  an exposed WriterTo returning EOF makes the copy fail instead.
- Go 1.24.0, src/net/http/transport.go: persistConn.writeLoop closes its
  connection after a request write error, which can interrupt an active streamed
  response.
- github.com/aws/aws-sdk-go-v2/service/bedrockruntime v1.63.1,
  api_client.go: New resolves HTTP client defaults before its option callbacks.
  NewFromConfig passes its callbacks to that point; resolveHTTPClient recognizes
  BuildableClient to apply dialer, TLS, and read timeout defaults.

An isolated counterexample builds a real Smithy request from bytes.NewReader,
copies its declared Content-Length, closes its request body, and probes for
excess bytes. The exposed WriterTo produces zero bytes plus io.EOF. Wrapping
the same body as ReadCloser alone makes the excess-byte probe return zero bytes
with nil error. That demonstrates the mechanism without relying on scheduler
timing. The independent regression must reject the unmodified Pith HTTP
boundary and exercise the repaired boundary, not merely retest the dependency.

## Required implementation

1. Retain the actual AWS Bedrock SDK. For the SDK client constructed by Pith,
   wrap its resolved HTTP client using a bedrockruntime.NewFromConfig option
   callback. Install the wrapper after AWS default resolution; wrapping
   cfg.HTTPClient earlier hides its BuildableClient type and can bypass SDK
   timeout and dialer initialization. Preserve an explicitly configured proxy
   client by delegating to that same resolved client.
2. The wrapper's Do must clone the incoming request using its existing context.
   Preserve method, URL, headers, body bytes, ContentLength, transfer encoding,
   trailers, and all other request semantics. Do not mutate the caller's
   request or its GetBody function.
3. Present non-nil, non-http.NoBody request bodies to the delegate as an
   io.ReadCloser whose method set does not expose io.WriterTo. Read and Close
   delegate to the original body. Preserve nil and http.NoBody unchanged; do
   not buffer or reread the body as part of wrapping it.
4. If GetBody is present, preserve its replay behavior and errors, wrapping each
   returned real body in the same Reader/Closer boundary. Preserve nil or
   http.NoBody replay results unchanged. This keeps SDK retry and HTTP redirect
   request reconstruction intact.
5. Return the delegate's response and error unchanged. Do not read, buffer,
   replace, or close the response body. Keep event streaming incremental and
   preserve cancellation, genuine decode/transport errors, and response header
   callbacks. Do not suppress an EOF stream error or retry a failed stream.
6. Keep an injected BedrockOptions.Client untouched. Preserve endpoint and
   region resolution, profile/credential precedence, bearer and SigV4 signing,
   custom headers, proxy selection/bypass, retry settings, serialization,
   content conversion, usage, and stop reasons.
7. Provide the small unexported constructor
   `newBedrockHTTPClient(client bedrockruntime.HTTPClient) bedrockruntime.HTTPClient`
   in packages/ai/api/bedrock_http.go. The SDK construction callback must use it
   on the already resolved options.HTTPClient. This private seam supports the
   independent judge without adding a public SDK API. Change only the SDK
   construction site in bedrock_converse_stream.go. Keep
   state per request or immutable so concurrent requests remain race-safe. Add
   focused model-owned tests in bedrock_http_test.go. Do not introduce new
   dependencies, exported APIs, or unrelated stream-lifecycle changes.

## Independent acceptance and integration

The independent judge must demonstrate the closed request-body excess-probe
counterexample at Pith's HTTP boundary and protect request cloning, Body/GetBody
cleanup and replay, nil/http.NoBody, error propagation, response streaming, and
concurrent request safety as applicable. It must also verify that the installed
wrapper reaches the real SDK construction path without altering the injected
client seam or configured client defaults.

Existing Bedrock conformance and Pi 1.0 behavior must remain unchanged. The
candidate must pass compilation and vet with CGO disabled, cumulative independent
acceptance, candidate race checks, and complete project integration including
race checks. The frozen fragmented fixture must pass under the previously
failing concurrent sandbox load, and the actual main CI must pass on Linux and
macOS before the repair is reported complete. Preserve preparation, candidate,
verification, and final commit evidence in a new maintenance journal.
