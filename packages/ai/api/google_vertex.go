// This file is a Go port of packages/ai/src/api/google-vertex.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// GoogleVertexOptions are the Google Vertex stream options.
type GoogleVertexOptions struct {
	types.StreamOptions
	ToolChoice *GoogleToolChoice
	Thinking   *GoogleThinkingOptions
	Project    *string
	Location   *string
}

const googleVertexAPIVersion = "v1"
const googleVertexCredentialsMarker = "gcp-vertex-credentials"

var googleVertexPlaceholderKeyPattern = regexp.MustCompile(`^<[^>]+>$`)
var googleVertexVersionSegmentPattern = regexp.MustCompile(`^v[0-9]+(beta[0-9]*)?$`)

// GoogleVertexStream is the Google Vertex streaming entry point.
func GoogleVertexStream(model *types.Model, context *types.TranscriptContext, options *GoogleVertexOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	normalizedContext := utils.CollapseSystemMessages(*context)

	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		var signal <-chan struct{}
		if options != nil {
			signal = options.Signal
		}
		fail := func(err error) { failGoogle(stream, &output, err, aborted(signal)) }

		if options != nil && options.Fetch != nil {
			fail(fmt.Errorf("Custom fetch is not supported by the Google Vertex adapter"))
			return
		}

		apiKey := resolveGoogleVertexAPIKey(options)

		buildInput := googleBuildInput{Signal: signal}
		if options != nil {
			buildInput.Temperature = options.Temperature
			buildInput.MaxTokens = options.MaxTokens
			buildInput.Thinking = options.Thinking
			if options.ToolChoice != nil {
				choice := string(*options.ToolChoice)
				buildInput.ToolChoice = &choice
			}
		}

		request, err := buildGoogleGenerateRequest(model, &normalizedContext, buildInput)
		if err != nil {
			fail(err)
			return
		}

		if options.OnPayload != nil {
			next, payloadErr := options.OnPayload(request, model)
			if payloadErr != nil {
				fail(payloadErr)
				return
			}
			if next != nil {
				switch replacement := next.(type) {
				case *GoogleGenerateContentRequest:
					request = replacement
				case GoogleGenerateContentRequest:
					request = &replacement
				default:
					fail(fmt.Errorf("Unsupported Google onPayload replacement type %T", next))
					return
				}
			}
		}

		requestContext, cancel := contextForSignal(contextBackground(), signal)
		defer cancel()

		location := ""
		var requestHeaders map[string]string
		if apiKey != nil {
			requestHeaders = googleRequestHeaders(model, optionsHeadersOf(options), apiKey)
		} else {
			project, err := resolveGoogleVertexProject(options)
			if err != nil {
				fail(err)
				return
			}
			location, err = resolveGoogleVertexLocation(options)
			if err != nil {
				fail(err)
				return
			}
			_ = project
			token, err := resolveGoogleVertexAccessToken(requestContext, optionsEnvOf(options))
			if err != nil {
				fail(err)
				return
			}
			requestHeaders = googleVertexHeaders(model, optionsHeadersOf(options), token)
		}

		body, err := json.Marshal(request)
		if err != nil {
			fail(err)
			return
		}

		var requestOptions *types.ProviderRequestOptions
		if options != nil {
			requestOptions = &options.ProviderRequestOptions
		}
		response, err := googlePerformRequest(requestContext, requestOptions, http.MethodPost, googleVertexURL(model, location), requestHeaders, body)
		if err != nil {
			fail(err)
			return
		}
		if response == nil {
			fail(fmt.Errorf("Google Vertex request returned no response"))
			return
		}
		defer response.Body.Close()

		if options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		stream.Push(types.NewStartEvent(output))
		if err := consumeGoogleStream(requestContext, model, stream, &output, response.Body, signal, googleVertexOptionObserver(options)); err != nil {
			fail(err)
			return
		}
		finalizeGoogleStream(stream, &output, signal, "Google Vertex stream ended without a finish reason")
	}()

	return stream
}

// GoogleVertexStreamSimple is the unified-reasoning entry point for the Google
// Vertex API.
func GoogleVertexStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	base := BuildBaseOptions(model, context, options, nil)
	typed := &GoogleVertexOptions{StreamOptions: base}
	if options != nil && options.ToolChoice != nil {
		choice := GoogleToolChoice(*options.ToolChoice)
		typed.ToolChoice = &choice
	}

	if options == nil || options.Reasoning == nil {
		typed.Thinking = &GoogleThinkingOptions{Enabled: false}
		return GoogleVertexStream(model, context, typed)
	}

	clampedReasoning := clampThinkingLevel(model, *options.Reasoning)
	if clampedReasoning == types.ThinkingOff {
		typed.Thinking = &GoogleThinkingOptions{Enabled: false}
		return GoogleVertexStream(model, context, typed)
	}

	resolvedLevel, err := ResolveGoogleThinkingLevel(model, clampedReasoning)
	if err != nil {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			failGoogle(stream, &output, err, false)
		}()
		return stream
	}

	if UsesGoogleThinkingLevel(model) {
		level := ToGoogleThinkingLevel(resolvedLevel)
		typed.Thinking = &GoogleThinkingOptions{Enabled: true, Level: &level}
		return GoogleVertexStream(model, context, typed)
	}

	budget := googleVertexBudget(model, resolvedLevel, options.ThinkingBudgets)
	typed.Thinking = &GoogleThinkingOptions{Enabled: true, BudgetTokens: &budget}
	return GoogleVertexStream(model, context, typed)
}

func optionsHeadersOf(options *GoogleVertexOptions) types.ProviderHeaders {
	if options == nil {
		return nil
	}
	return options.Headers
}

func optionsEnvOf(options *GoogleVertexOptions) types.ProviderEnv {
	if options == nil {
		return nil
	}
	return options.Env
}

func resolveGoogleVertexAPIKey(options *GoogleVertexOptions) *string {
	if options == nil || options.APIKey == nil {
		return nil
	}
	apiKey := strings.TrimSpace(*options.APIKey)
	if apiKey == "" || apiKey == googleVertexCredentialsMarker || googleVertexPlaceholderKeyPattern.MatchString(apiKey) {
		return nil
	}
	return &apiKey
}

func resolveGoogleVertexProject(options *GoogleVertexOptions) (string, error) {
	if options != nil && options.Project != nil && *options.Project != "" {
		return *options.Project, nil
	}
	if value := utils.GetProviderEnvValue("GOOGLE_CLOUD_PROJECT", optionsEnvOf(options)); value != nil && *value != "" {
		return *value, nil
	}
	if value := utils.GetProviderEnvValue("GCLOUD_PROJECT", optionsEnvOf(options)); value != nil && *value != "" {
		return *value, nil
	}
	return "", fmt.Errorf("Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT or pass project in options.")
}

func resolveGoogleVertexLocation(options *GoogleVertexOptions) (string, error) {
	if options != nil && options.Location != nil && *options.Location != "" {
		return *options.Location, nil
	}
	if value := utils.GetProviderEnvValue("GOOGLE_CLOUD_LOCATION", optionsEnvOf(options)); value != nil && *value != "" {
		return *value, nil
	}
	return "", fmt.Errorf("Vertex AI requires a location. Set GOOGLE_CLOUD_LOCATION or pass location in options.")
}

func googleVertexHeaders(model *types.Model, optionsHeaders types.ProviderHeaders, token string) map[string]string {
	headers := map[string]string{"User-Agent": utils.GetPiUserAgent()}
	for key, value := range model.Headers {
		headers[key] = value
	}
	for key, value := range optionsHeaders {
		if value == nil {
			delete(headers, key)
			continue
		}
		headers[key] = *value
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	headers["content-type"] = "application/json"
	return headers
}

func googleVertexURL(model *types.Model, location string) string {
	base := resolveGoogleVertexCustomBaseURL(model.BaseUrl)
	if base == "" {
		host := location
		if host == "" {
			host = "us-central1"
		}
		return "https://" + host + "-aiplatform.googleapis.com/" + googleVertexAPIVersion +
			"/publishers/google/models/" + model.Id + ":streamGenerateContent?alt=sse"
	}
	apiVersion := googleVertexAPIVersion
	if googleVertexBaseURLIncludesAPIVersion(base) {
		apiVersion = ""
	}
	prefix := base
	if apiVersion != "" {
		prefix = base + "/" + apiVersion
	}
	return prefix + "/publishers/google/models/" + model.Id + ":streamGenerateContent?alt=sse"
}

func resolveGoogleVertexCustomBaseURL(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" || strings.Contains(trimmed, "{location}") {
		return ""
	}
	return strings.TrimRight(trimmed, "/")
}

func googleVertexBaseURLIncludesAPIVersion(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err == nil {
		for _, part := range strings.Split(parsed.Path, "/") {
			if googleVertexVersionSegmentPattern.MatchString(part) {
				return true
			}
		}
		return false
	}
	for _, part := range strings.Split(baseURL, "/") {
		if googleVertexVersionSegmentPattern.MatchString(part) {
			return true
		}
	}
	return false
}

func googleVertexBudget(model *types.Model, level ResolvedGoogleThinkingLevel, customBudgets *types.ThinkingBudgets) int {
	if customBudgets != nil {
		switch level {
		case "minimal":
			if customBudgets.Minimal != nil {
				return *customBudgets.Minimal
			}
		case "low":
			if customBudgets.Low != nil {
				return *customBudgets.Low
			}
		case "medium":
			if customBudgets.Medium != nil {
				return *customBudgets.Medium
			}
		case "high":
			if customBudgets.High != nil {
				return *customBudgets.High
			}
		}
	}
	switch {
	case strings.Contains(model.Id, "2.5-pro"):
		return map[ResolvedGoogleThinkingLevel]int{"minimal": 128, "low": 2048, "medium": 8192, "high": 32768}[level]
	case strings.Contains(model.Id, "2.5-flash"):
		return map[ResolvedGoogleThinkingLevel]int{"minimal": 128, "low": 2048, "medium": 8192, "high": 24576}[level]
	default:
		return -1
	}
}

// =============================================================================
// Application Default Credentials (Go runtime adaptation)
// =============================================================================

type googleServiceAccountCredentials struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// resolveGoogleVertexAccessToken obtains an OAuth access token for the Vertex
// ADC path: a service-account key file when GOOGLE_APPLICATION_CREDENTIALS is
// set, otherwise the GCE metadata server.
func resolveGoogleVertexAccessToken(ctx context.Context, env types.ProviderEnv) (string, error) {
	if keyFile := utils.GetProviderEnvValue("GOOGLE_APPLICATION_CREDENTIALS", env); keyFile != nil && *keyFile != "" {
		if token, err := googleServiceAccountToken(ctx, *keyFile); err == nil {
			return token, nil
		}
	}
	return googleMetadataAccessToken(ctx)
}

func googleServiceAccountToken(ctx context.Context, keyFile string) (string, error) {
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return "", err
	}
	var credentials googleServiceAccountCredentials
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return "", err
	}
	tokenURI := credentials.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(credentials.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("invalid Google service account private key")
	}
	signingKey, err := parseGooglePrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}

	now := time.Now()
	headerJSON, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claimsJSON, _ := json.Marshal(map[string]any{
		"iss":   credentials.ClientEmail,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	hashed := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, signingKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}
	assertion := signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Google token exchange failed: %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("Google token exchange returned no access token")
	}
	return payload.AccessToken, nil
}

func parseGooglePrivateKey(der []byte) (*rsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, fmt.Errorf("Google service account key is not RSA")
	}
	if rsaKey, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return rsaKey, nil
	}
	return nil, fmt.Errorf("unable to parse Google service account private key")
}

func googleMetadataAccessToken(ctx context.Context) (string, error) {
	const metadataURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Google metadata token request failed: %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("Google metadata token response contained no access token")
	}
	return payload.AccessToken, nil
}

// googleVertexOptionObserver returns the optional provider-stream-event observer.
func googleVertexOptionObserver(options *GoogleVertexOptions) func(data any, model *types.Model) error {
	if options == nil {
		return nil
	}
	return options.OnProviderStreamEvent
}
