// Radius gateway configuration.
//
// Ports packages/ai/src/providers/radius-config.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
//
// PROVENANCE: the upstream file imports `OAuthCredential` from
// ../auth/types.ts. The auth types package is outside this batch's source set, so
// this file declares the minimal `OAuthCredential` interchange it reads instead
// of importing a package that does not exist yet. When the auth module lands it
// must keep the `gatewayConfig` field and this alias must be reconciled.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
)

// DEFAULT_RADIUS_GATEWAY is the default Radius gateway origin.
//
// Ports `DEFAULT_RADIUS_GATEWAY` from
// packages/ai/src/providers/radius-config.ts.
const DEFAULT_RADIUS_GATEWAY = "https://radius.pi.dev"

// OAuthCredential is the small credential interchange radius-config.ts reads.
//
// Only the gatewayConfig extension is consumed here, so the surrounding
// credential fields are carried opaquely; the auth module owns the full shape.
type OAuthCredential struct {
	// Type is the credential discriminator (for example "oauth").
	Type string `json:"type,omitempty"`
}

// RadiusGatewayModel is one model advertised by a Radius gateway.
//
// Ports `RadiusGatewayModel` from packages/ai/src/providers/radius-config.ts.
type RadiusGatewayModel struct {
	Id               string                     `json:"id"`
	Name             string                     `json:"name"`
	Reasoning        bool                       `json:"reasoning"`
	ThinkingLevelMap types.ThinkingLevelMap     `json:"thinkingLevelMap,omitempty"`
	Input            []types.ModelInputModality `json:"input"`
	Cost             types.ModelCost            `json:"cost"`
	ContextWindow    float64                    `json:"contextWindow"`
	MaxTokens        float64                    `json:"maxTokens"`
}

// RadiusGatewayConfig is the configuration served by a Radius gateway.
//
// Ports `RadiusGatewayConfig` from packages/ai/src/providers/radius-config.ts.
type RadiusGatewayConfig struct {
	BaseUrl string               `json:"baseUrl"`
	Models  []RadiusGatewayModel `json:"models"`
}

// RadiusOAuthCredential is an OAuth credential with the optional Radius gateway
// configuration attached.
//
// Ports `RadiusOAuthCredential` from packages/ai/src/providers/radius-config.ts.
type RadiusOAuthCredential struct {
	OAuthCredential
	GatewayConfig *RadiusGatewayConfig `json:"gatewayConfig,omitempty"`
}

// isRadiusGatewayModel reports whether a decoded JSON value is a valid
// RadiusGatewayModel.
func isRadiusGatewayModel(value any) bool {
	model, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := model["id"].(string); !ok {
		return false
	}
	if _, ok := model["name"].(string); !ok {
		return false
	}
	if _, ok := model["reasoning"].(bool); !ok {
		return false
	}
	if _, ok := model["input"].([]any); !ok {
		return false
	}
	cost, ok := model["cost"].(map[string]any)
	if !ok || cost == nil {
		return false
	}
	if _, ok := model["contextWindow"].(float64); !ok {
		return false
	}
	if _, ok := model["maxTokens"].(float64); !ok {
		return false
	}
	return true
}

// SanitizeRadiusGatewayConfig validates and copies an unknown JSON value into a
// RadiusGatewayConfig, returning nil when the shape is not a valid config.
//
// This is the port of the upstream private `sanitizeRadiusGatewayConfig`.
func SanitizeRadiusGatewayConfig(config *RadiusGatewayConfig) *RadiusGatewayConfig {
	if config == nil {
		return nil
	}
	models := make([]RadiusGatewayModel, 0, len(config.Models))
	for _, model := range config.Models {
		models = append(models, model)
	}
	return &RadiusGatewayConfig{BaseUrl: config.BaseUrl, Models: models}
}

// sanitizeRadiusGatewayValue validates a raw JSON value the way the upstream
// sanitizer does, filtering entries with the same predicate.
func sanitizeRadiusGatewayValue(config any) *RadiusGatewayConfig {
	object, ok := config.(map[string]any)
	if !ok || object == nil {
		return nil
	}
	baseUrl, ok := object["baseUrl"].(string)
	if !ok {
		return nil
	}
	rawModels, ok := object["models"].([]any)
	if !ok {
		return nil
	}
	models := make([]RadiusGatewayModel, 0, len(rawModels))
	for _, rawModel := range rawModels {
		if !isRadiusGatewayModel(rawModel) {
			continue
		}
		encoded, err := json.Marshal(rawModel)
		if err != nil {
			continue
		}
		var model RadiusGatewayModel
		if err := json.Unmarshal(encoded, &model); err != nil {
			continue
		}
		models = append(models, model)
	}
	return &RadiusGatewayConfig{BaseUrl: baseUrl, Models: models}
}

// radiusSchemePattern matches an http(s) scheme, mirroring the upstream
// /^https?:\/\//iu test.
var radiusSchemePattern = regexp.MustCompile(`(?i)^https?://`)

// radiusTrailingSlashPattern mirrors the upstream /\/+$/u replacement.
var radiusTrailingSlashPattern = regexp.MustCompile(`/+$`)

// NormalizeRadiusGatewayUrl prepends https:// when the value has no http(s)
// scheme and strips trailing slashes.
//
// Ports `normalizeRadiusGatewayUrl` from
// packages/ai/src/providers/radius-config.ts.
func NormalizeRadiusGatewayUrl(value string) string {
	withScheme := value
	if !radiusSchemePattern.MatchString(value) {
		withScheme = "https://" + value
	}
	return radiusTrailingSlashPattern.ReplaceAllString(withScheme, "")
}

// GetRadiusCredentialConfig extracts and sanitizes the gateway configuration of a
// credential.
//
// Ports `getRadiusCredentialConfig` from
// packages/ai/src/providers/radius-config.ts.
func GetRadiusCredentialConfig(credential *RadiusOAuthCredential) *RadiusGatewayConfig {
	if credential == nil {
		return nil
	}
	return SanitizeRadiusGatewayConfig(credential.GatewayConfig)
}

// GetRadiusModelsFromConfig expands a gateway configuration into models bound to
// the given provider id.
//
// Ports `getRadiusModelsFromConfig` from
// packages/ai/src/providers/radius-config.ts.
func GetRadiusModelsFromConfig(providerId string, config RadiusGatewayConfig) []types.Model {
	models := make([]types.Model, 0, len(config.Models))
	for _, model := range config.Models {
		models = append(models, types.Model{
			Id:               model.Id,
			Name:             model.Name,
			Api:              types.ApiPiMessages,
			Provider:         types.ProviderId(providerId),
			BaseUrl:          config.BaseUrl,
			Reasoning:        model.Reasoning,
			ThinkingLevelMap: model.ThinkingLevelMap,
			Input:            model.Input,
			Cost:             model.Cost,
			ContextWindow:    model.ContextWindow,
			MaxTokens:        model.MaxTokens,
		})
	}
	return models
}

// GetRadiusModels returns the Radius models for a credential, or an empty slice
// when the credential has no valid gateway configuration.
//
// Ports `getRadiusModels` from packages/ai/src/providers/radius-config.ts.
func GetRadiusModels(providerId string, credential *RadiusOAuthCredential) []types.Model {
	config := GetRadiusCredentialConfig(credential)
	if config == nil {
		return []types.Model{}
	}
	return GetRadiusModelsFromConfig(providerId, *config)
}

// truncateHttpBody trims a response body and bounds its length.
func truncateHttpBody(body string) string {
	trimmed := strings.TrimSpace(body)
	runes := []rune(trimmed)
	if len(runes) > 512 {
		return string(runes[:512]) + "…"
	}
	return trimmed
}

// LoadRadiusGatewayConfig fetches the /v1/config document from a Radius gateway.
//
// Ports `loadRadiusGatewayConfig` from
// packages/ai/src/providers/radius-config.ts.
func LoadRadiusGatewayConfig(ctx context.Context, gateway string, apiKey string, client *http.Client) (RadiusGatewayConfig, error) {
	if client == nil {
		client = http.DefaultClient
	}
	endpoint, err := url.JoinPath(gateway, "/v1/config")
	if err != nil {
		return RadiusGatewayConfig{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return RadiusGatewayConfig{}, err
	}
	request.Header.Set("accept", "application/json")
	if apiKey != "" {
		request.Header.Set("authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return RadiusGatewayConfig{}, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return RadiusGatewayConfig{}, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return RadiusGatewayConfig{}, fmt.Errorf(
			"Could not load Radius config from %s: %d: %s",
			gateway, response.StatusCode, truncateHttpBody(string(body)),
		)
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return RadiusGatewayConfig{}, fmt.Errorf("Invalid Radius config from %s", gateway)
	}
	config := sanitizeRadiusGatewayValue(decoded)
	if config == nil {
		return RadiusGatewayConfig{}, fmt.Errorf("Invalid Radius config from %s", gateway)
	}
	return *config, nil
}
