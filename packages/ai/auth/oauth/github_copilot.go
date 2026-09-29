// This file is a Go port of packages/ai/src/auth/oauth/github-copilot.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/utils"
)

var githubCopilotClientID = func() string {
	decoded, err := base64Decode("SXYxLmI1MDdhMDhjODdlY2ZlOTg=")
	if err != nil {
		return ""
	}
	return decoded
}()

var copilotHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

const copilotAPIVersion = "2026-06-01"

var githubCopilotHTTPClient = &http.Client{Timeout: 60 * time.Second}

type copilotDeviceCodeResponse struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        *float64
	ExpiresIn       float64
}

func normalizeDomain(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}
	candidate := trimmed
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	return parsed.Hostname(), true
}

func copilotURLs(domain string) (deviceCodeURL, accessTokenURL, copilotTokenURL string) {
	return "https://" + domain + "/login/device/code",
		"https://" + domain + "/login/oauth/access_token",
		"https://api." + domain + "/copilot_internal/v2/token"
}

var proxyEpPattern = regexp.MustCompile(`proxy-ep=([^;]+)`)

func getBaseURLFromToken(token string) (string, bool) {
	match := proxyEpPattern.FindStringSubmatch(token)
	if len(match) < 2 {
		return "", false
	}
	proxyHost := match[1]
	apiHost := strings.TrimPrefix(proxyHost, "proxy.")
	if strings.HasPrefix(proxyHost, "proxy.") {
		apiHost = "api." + strings.TrimPrefix(proxyHost, "proxy.")
	}
	return "https://" + apiHost, true
}

func getGitHubCopilotBaseURL(token string, enterpriseDomain string) string {
	if token != "" {
		if fromToken, ok := getBaseURLFromToken(token); ok {
			return fromToken
		}
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}

func asRecord(value any) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	return record, ok
}

type copilotModelCatalog struct {
	AvailableModelIDs []string
	PolicyModelIDs    []string
}

func parseGitHubCopilotModelCatalog(raw any, allowPolicyFallback bool) (*copilotModelCatalog, error) {
	root, ok := asRecord(raw)
	if !ok {
		return nil, errors.New("Invalid Copilot models response")
	}
	data, ok := root["data"].([]any)
	if !ok {
		return nil, errors.New("Invalid Copilot models response")
	}

	type accountModel struct {
		id            string
		pickerEnabled bool
		policyState   any
	}
	accountModels := make([]accountModel, 0, len(data))
	for _, rawItem := range data {
		item, ok := asRecord(rawItem)
		if !ok {
			continue
		}
		id, ok := item["id"].(string)
		if !ok {
			continue
		}
		if capabilities, ok := asRecord(item["capabilities"]); ok {
			if supports, ok := asRecord(capabilities["supports"]); ok {
				if toolCalls, ok := supports["tool_calls"].(bool); ok && !toolCalls {
					continue
				}
			}
		}
		var policyState any
		if policy, ok := asRecord(item["policy"]); ok {
			policyState = policy["state"]
		}
		accountModels = append(accountModels, accountModel{
			id:            id,
			pickerEnabled: item["model_picker_enabled"] == true,
			policyState:   policyState,
		})
	}

	pickerModelIDs := make([]string, 0, len(accountModels))
	for _, model := range accountModels {
		if model.pickerEnabled && model.policyState != "disabled" {
			pickerModelIDs = append(pickerModelIDs, model.id)
		}
	}
	usePolicyFallback := allowPolicyFallback && len(pickerModelIDs) == 0
	var availableModelIDs []string
	if len(pickerModelIDs) > 0 || !allowPolicyFallback {
		availableModelIDs = pickerModelIDs
	} else {
		for _, model := range accountModels {
			if model.policyState == "enabled" {
				availableModelIDs = append(availableModelIDs, model.id)
			}
		}
	}
	policyModelIDs := make([]string, 0)
	for _, model := range accountModels {
		if model.policyState == "unconfigured" && (model.pickerEnabled || usePolicyFallback) {
			if _, ok := catalog.GITHUB_COPILOT_MODELS[model.id]; ok {
				policyModelIDs = append(policyModelIDs, model.id)
			}
		}
	}
	return &copilotModelCatalog{AvailableModelIDs: availableModelIDs, PolicyModelIDs: policyModelIDs}, nil
}

type copilotRetryPolicy struct {
	MaxRetries   int
	MaxElapsedMs float64
}

func fetchWithRateLimitRetry(ctx context.Context, rawURL string, init func(context.Context) (*http.Request, error), signal context.Context, policy copilotRetryPolicy) (*http.Response, error) {
	var retryBudgetCtx context.Context
	var cancelBudget context.CancelFunc
	requestSignal := signal
	retryDeadline := float64(0)
	if policy.MaxRetries > 0 && policy.MaxElapsedMs > 0 {
		retryBudgetCtx, cancelBudget = context.WithTimeout(ctx, time.Duration(policy.MaxElapsedMs)*time.Millisecond)
		defer cancelBudget()
		requestSignal = retryBudgetCtx
		retryDeadline = float64(time.Now().UnixMilli()) + policy.MaxElapsedMs
	}

	for retry := 0; ; retry++ {
		request, err := init(requestSignal)
		if err != nil {
			return nil, err
		}
		requestCtx, cancelRequest := context.WithTimeout(request.Context(), 5*time.Second)
		response, err := githubCopilotHTTPClient.Do(request.WithContext(requestCtx))
		cancelRequest()
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusTooManyRequests || retry == policy.MaxRetries {
			return response, nil
		}

		delayMs := 500 * math.Pow(2, float64(retry))
		if retryAfter := response.Header.Get("retry-after"); retryAfter != "" {
			if seconds, err := parseFloat(retryAfter); err == nil {
				delayMs = seconds * 1000
			} else if when, err := http.ParseTime(retryAfter); err == nil {
				delayMs = float64(when.UnixMilli() - time.Now().UnixMilli())
			} else {
				return response, nil
			}
			if math.IsNaN(delayMs) || math.IsInf(delayMs, 0) {
				return response, nil
			}
		}
		if delayMs < 0 {
			delayMs = 0
		}
		if retryDeadline != 0 && delayMs >= retryDeadline-float64(time.Now().UnixMilli()) {
			return response, nil
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if err := utils.Sleep(requestSignal, delayMs); err != nil {
			return nil, err
		}
	}
}

func parseFloat(value string) (float64, error) {
	var result float64
	_, err := fmt.Sscanf(strings.TrimSpace(value), "%g", &result)
	if err != nil {
		return 0, err
	}
	return result, nil
}

func fetchGitHubCopilotModels(ctx context.Context, copilotToken, enterpriseDomain string, signal context.Context, policy copilotRetryPolicy) (*copilotModelCatalog, error) {
	baseURL := getGitHubCopilotBaseURL(copilotToken, enterpriseDomain)
	allowPolicyFallback := baseURL == "https://api.individual.githubcopilot.com"
	response, err := fetchWithRateLimitRetry(ctx, baseURL+"/models", func(requestCtx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+copilotToken)
		for key, value := range copilotHeaders {
			request.Header.Set(key, value)
		}
		request.Header.Set("X-GitHub-Api-Version", copilotAPIVersion)
		return request, nil
	}, signal, policy)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%d %s: %s", response.StatusCode, response.Status, string(body))
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return parseGitHubCopilotModelCatalog(raw, allowPolicyFallback)
}

func fetchCopilotJSON(ctx context.Context, rawURL string, init func(context.Context) (*http.Request, error)) (any, error) {
	request, err := init(ctx)
	if err != nil {
		return nil, err
	}
	response, err := githubCopilotHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%d %s: %s", response.StatusCode, response.Status, string(body))
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func startCopilotDeviceFlow(ctx context.Context, domain string, signal context.Context) (*copilotDeviceCodeResponse, error) {
	deviceCodeURL, _, _ := copilotURLs(domain)
	form := url.Values{}
	form.Set("client_id", githubCopilotClientID)
	form.Set("scope", "read:user")
	data, err := fetchCopilotJSON(ctx, deviceCodeURL, func(requestCtx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, deviceCodeURL, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
		return request, nil
	})
	if err != nil {
		return nil, err
	}
	record, ok := asRecord(data)
	if !ok {
		return nil, errors.New("Invalid device code response")
	}
	deviceCode, _ := record["device_code"].(string)
	userCode, _ := record["user_code"].(string)
	verificationURI, _ := record["verification_uri"].(string)
	var interval *float64
	if rawInterval, ok := record["interval"].(float64); ok {
		interval = &rawInterval
	}
	expiresIn, expiresOK := record["expires_in"].(float64)
	if deviceCode == "" || userCode == "" || verificationURI == "" || !expiresOK {
		return nil, errors.New("Invalid device code response fields")
	}
	parsedURI, err := url.Parse(verificationURI)
	if err != nil || (parsedURI.Scheme != "https" && parsedURI.Scheme != "http") {
		return nil, errors.New("Untrusted verification_uri in device code response")
	}
	return &copilotDeviceCodeResponse{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		VerificationURI: parsedURI.String(),
		Interval:        interval,
		ExpiresIn:       expiresIn,
	}, nil
}

func pollForGitHubAccessToken(ctx context.Context, domain string, device *copilotDeviceCodeResponse, signal context.Context) (string, error) {
	_, accessTokenURL, _ := copilotURLs(domain)
	expires := device.ExpiresIn
	result, err := PollOAuthDeviceCodeFlow(ctx, OAuthDeviceCodePollOptions[string]{
		IntervalSeconds:     device.Interval,
		ExpiresInSeconds:    &expires,
		WaitBeforeFirstPoll: true,
		Signal:              signal,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			form := url.Values{}
			form.Set("client_id", githubCopilotClientID)
			form.Set("device_code", device.DeviceCode)
			form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
			raw, err := fetchCopilotJSON(ctx, accessTokenURL, func(requestCtx context.Context) (*http.Request, error) {
				request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, accessTokenURL, strings.NewReader(form.Encode()))
				if err != nil {
					return nil, err
				}
				request.Header.Set("Accept", "application/json")
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
				return request, nil
			})
			if err != nil {
				return DeviceCodePollResultWithMessage[string](err), nil
			}
			record, ok := asRecord(raw)
			if !ok {
				return DeviceCodeFailed[string]("Invalid device token response"), nil
			}
			if token, ok := record["access_token"].(string); ok {
				return DeviceCodeComplete(token), nil
			}
			if errorCode, ok := record["error"].(string); ok {
				if errorCode == "authorization_pending" {
					return DeviceCodePending[string](), nil
				}
				if errorCode == "slow_down" {
					var interval *float64
					if rawInterval, ok := record["interval"].(float64); ok {
						interval = &rawInterval
					}
					return DeviceCodeSlowDown[string](interval), nil
				}
				description := ""
				if text, ok := record["error_description"].(string); ok && text != "" {
					description = ": " + text
				}
				return DeviceCodeFailed[string](fmt.Sprintf("Device flow failed: %s%s", errorCode, description)), nil
			}
			return DeviceCodeFailed[string]("Invalid device token response"), nil
		},
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

// DeviceCodePollResultWithMessage converts a fetch error into a failed poll
// result.
func DeviceCodePollResultWithMessage[T any](err error) OAuthDeviceCodePollResult[T] {
	return DeviceCodeFailed[T](err.Error())
}

func refreshGitHubCopilotAccessToken(ctx context.Context, refreshToken, enterpriseDomain string, signal context.Context) (*authtypes.OAuthCredential, error) {
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	_, _, copilotTokenURL := copilotURLs(domain)
	raw, err := fetchCopilotJSON(ctx, copilotTokenURL, func(requestCtx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, copilotTokenURL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+refreshToken)
		for key, value := range copilotHeaders {
			request.Header.Set(key, value)
		}
		return request, nil
	})
	if err != nil {
		return nil, err
	}
	record, ok := asRecord(raw)
	if !ok {
		return nil, errors.New("Invalid Copilot token response")
	}
	token, tokenOK := record["token"].(string)
	expiresAt, expiresOK := record["expires_at"].(float64)
	if !tokenOK || !expiresOK {
		return nil, errors.New("Invalid Copilot token response fields")
	}
	credential := &authtypes.OAuthCredential{
		OAuthCredentials: authtypes.OAuthCredentials{
			Refresh: refreshToken,
			Access:  token,
			Expires: expiresAt*1000 - 5*60*1000,
		},
		Type: authtypes.CredentialTypeOAuth,
	}
	if enterpriseDomain != "" {
		_ = credential.SetExtra("enterpriseUrl", enterpriseDomain)
	}
	return credential, nil
}

func refreshGitHubCopilotToken(ctx context.Context, refreshToken, enterpriseDomain string, signal context.Context) (*authtypes.OAuthCredential, error) {
	credentials, err := refreshGitHubCopilotAccessToken(ctx, refreshToken, enterpriseDomain, signal)
	if err != nil {
		return nil, err
	}
	models, err := fetchGitHubCopilotModels(ctx, credentials.Access, enterpriseDomain, signal, copilotRetryPolicy{})
	if err != nil {
		return nil, err
	}
	_ = credentials.SetExtra("availableModelIds", models.AvailableModelIDs)
	return credentials, nil
}

func enableGitHubCopilotModel(ctx context.Context, token, modelID, enterpriseDomain string, signal context.Context) (bool, error) {
	baseURL := getGitHubCopilotBaseURL(token, enterpriseDomain)
	requestURL := baseURL + "/models/" + url.PathEscape(modelID) + "/policy"
	response, err := fetchWithRateLimitRetry(ctx, requestURL, func(requestCtx context.Context) (*http.Request, error) {
		body, _ := json.Marshal(map[string]any{"state": "enabled"})
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		for key, value := range copilotHeaders {
			request.Header.Set(key, value)
		}
		request.Header.Set("openai-intent", "chat-policy")
		request.Header.Set("x-interaction-type", "chat-policy")
		return request, nil
	}, signal, copilotRetryPolicy{MaxRetries: 2, MaxElapsedMs: 5000})
	if err != nil {
		if signal != nil && signal.Err() != nil {
			return false, err
		}
		return false, nil
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(response.Body)
		return false, fmt.Errorf("%d %s: %s", response.StatusCode, response.Status, string(body))
	}
	return response.StatusCode >= 200 && response.StatusCode < 300, nil
}

func enableGitHubCopilotModels(ctx context.Context, token string, modelIDs []string, enterpriseDomain string, signal context.Context) ([]string, error) {
	enabled := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		ok, err := enableGitHubCopilotModel(ctx, token, modelID, enterpriseDomain, signal)
		if err != nil {
			if signal != nil && signal.Err() != nil {
				return enabled, err
			}
			break
		}
		if ok {
			enabled = append(enabled, modelID)
		}
	}
	return enabled, nil
}

func loginGitHubCopilot(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.OAuthCredential, error) {
	placeholder := "company.ghe.com"
	input, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
		Type:        authtypes.AuthPromptText,
		Message:     "GitHub Enterprise URL/domain (blank for github.com)",
		Placeholder: &placeholder,
	})
	if err != nil {
		return nil, err
	}
	if interaction.Signal() != nil && interaction.Signal().Err() != nil {
		return nil, errors.New("Login cancelled")
	}
	trimmed := strings.TrimSpace(input)
	enterpriseDomain, valid := normalizeDomain(input)
	if trimmed != "" && !valid {
		return nil, errors.New("Invalid GitHub Enterprise URL/domain")
	}
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	device, err := startCopilotDeviceFlow(ctx, domain, interaction.Signal())
	if err != nil {
		return nil, err
	}
	interaction.Notify(authtypes.AuthEvent{
		Type:             authtypes.AuthEventDeviceCode,
		UserCode:         &device.UserCode,
		VerificationURI:  &device.VerificationURI,
		IntervalSeconds:  device.Interval,
		ExpiresInSeconds: &device.ExpiresIn,
	})
	githubAccessToken, err := pollForGitHubAccessToken(ctx, domain, device, interaction.Signal())
	if err != nil {
		return nil, err
	}
	credentials, err := refreshGitHubCopilotAccessToken(ctx, githubAccessToken, enterpriseDomain, interaction.Signal())
	if err != nil {
		return nil, err
	}
	models, err := fetchGitHubCopilotModels(ctx, credentials.Access, enterpriseDomain, interaction.Signal(), copilotRetryPolicy{MaxRetries: 2, MaxElapsedMs: 5000})
	if err != nil {
		return nil, err
	}
	var enabledModelIDs []string
	if len(models.PolicyModelIDs) > 0 {
		interaction.Notify(authtypes.AuthEvent{Type: authtypes.AuthEventProgress, Message: stringPtr("Enabling models...")})
		enabledModelIDs, err = enableGitHubCopilotModels(ctx, credentials.Access, models.PolicyModelIDs, enterpriseDomain, interaction.Signal())
		if err != nil {
			return nil, err
		}
	}
	combined := append([]string(nil), models.AvailableModelIDs...)
	combined = append(combined, enabledModelIDs...)
	_ = credentials.SetExtra("availableModelIds", uniqueStrings(combined))
	return credentials, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func copilotEnterpriseDomain(credential *authtypes.OAuthCredential) string {
	enterpriseURL, ok := credential.ExtraString("enterpriseUrl")
	if !ok || enterpriseURL == "" {
		return ""
	}
	if domain, valid := normalizeDomain(enterpriseURL); valid {
		return domain
	}
	return ""
}

// GitHubCopilotOAuth is the GitHub Copilot OAuth flow.
//
// Ports `githubCopilotOAuth` from
// packages/ai/src/auth/oauth/github-copilot.ts.
var GitHubCopilotOAuth = &authtypes.OAuthAuth{
	Name:           "GitHub Copilot",
	IsSubscription: true,
	Login:          loginGitHubCopilot,
	Refresh: func(ctx context.Context, credential *authtypes.OAuthCredential) (*authtypes.OAuthCredential, error) {
		return refreshGitHubCopilotToken(ctx, credential.Refresh, copilotEnterpriseDomain(credential), ctx)
	},
	ToAuth: func(ctx context.Context, credential *authtypes.OAuthCredential) (authtypes.ModelAuth, error) {
		baseURL := getGitHubCopilotBaseURL(credential.Access, copilotEnterpriseDomain(credential))
		return authtypes.ModelAuth{APIKey: stringPtr(credential.Access), BaseURL: &baseURL}, nil
	},
}
