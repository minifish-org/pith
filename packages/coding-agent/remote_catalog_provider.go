// Remote model catalog for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/remote-catalog-provider.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe. It fetches a
// provider's pi.dev catalog overlay, revalidates it with an ETag, and persists a
// freshness stamp. Network access is explicit: a caller injects an HTTP client
// and the base URL, so the SDK never reaches the network implicitly.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// DefaultCatalogBaseURL is the pi.dev catalog origin.
const DefaultCatalogBaseURL = "https://pi.dev"

// RemoteCatalogRefreshInterval is the minimum age before a checked catalog is
// fetched again.
const RemoteCatalogRefreshInterval = 4 * time.Hour

// RemoteCatalogAttemptTimeout bounds one catalog request attempt.
const RemoteCatalogAttemptTimeout = 4 * time.Second

// RemoteCatalogModelTypes are the model types the client can consume.
var RemoteCatalogModelTypes = []string{"chat", "image", "classifier"}

// RemoteCatalogState is the persisted catalog overlay for one provider.
type RemoteCatalogState struct {
	Models       []json.RawMessage `json:"models"`
	CheckedAt    int64             `json:"checkedAt"`
	LastModified int64             `json:"lastModified"`
	ETag         string            `json:"etag,omitempty"`
}

// RemoteCatalogResult is the outcome of one refresh.
type RemoteCatalogResult struct {
	// State is the entry to persist. A zero State means nothing changed.
	State RemoteCatalogState
	// Models is the overlay to apply, in stable API/id order.
	Models []json.RawMessage
	// NotModified reports a 304 revalidation.
	NotModified bool
	// NotSupported reports a 404/501 response (the provider has no remote
	// catalog).
	NotSupported bool
	// Persist reports whether State should be written.
	Persist bool
}

// RemoteCatalogClient fetches provider catalogs.
type RemoteCatalogClient struct {
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
}

// ParseRemoteCatalog parses a catalog body for a provider. Entries without an
// id or with an unsupported type are dropped, and the provider id is stamped
// onto every model.
func ParseRemoteCatalog(providerID string, body []byte) ([]json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid model catalog for provider %q: %w", providerID, err)
	}
	var entries []any
	switch typed := value.(type) {
	case []any:
		entries = typed
	case map[string]any:
		if models, ok := typed["models"].([]any); ok {
			entries = models
		} else {
			for _, entry := range typed {
				entries = append(entries, entry)
			}
		}
	default:
		return nil, fmt.Errorf("invalid model catalog for provider %q", providerID)
	}
	out := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := object["id"]; !ok {
			continue
		}
		if modelType, ok := object["type"].(string); ok && modelType != "" && !remoteCatalogSupportsType(modelType) {
			continue
		}
		object["provider"] = providerID
		encoded, err := json.Marshal(object)
		if err != nil {
			continue
		}
		out = append(out, encoded)
	}
	sortRemoteModels(out)
	return out, nil
}

func remoteCatalogSupportsType(modelType string) bool {
	for _, supported := range RemoteCatalogModelTypes {
		if modelType == supported {
			return true
		}
	}
	return false
}

// MergeRemoteModels merges baseline and dynamic catalogs. A dynamic entry
// replaces a baseline entry with the same type and id.
func MergeRemoteModels(baseline, dynamic []json.RawMessage) []json.RawMessage {
	merged := map[string]json.RawMessage{}
	order := []string{}
	add := func(model json.RawMessage) {
		key := remoteModelKey(model)
		if _, exists := merged[key]; !exists {
			order = append(order, key)
		}
		merged[key] = model
	}
	for _, model := range baseline {
		add(model)
	}
	for _, model := range dynamic {
		add(model)
	}
	out := make([]json.RawMessage, 0, len(order))
	for _, key := range order {
		out = append(out, merged[key])
	}
	return out
}

func remoteModelKey(model json.RawMessage) string {
	var object map[string]any
	if err := json.Unmarshal(model, &object); err != nil {
		return string(model)
	}
	modelType, _ := object["type"].(string)
	id, _ := object["id"].(string)
	return modelType + "\x00" + id
}

func sortRemoteModels(models []json.RawMessage) {
	sort.SliceStable(models, func(i, j int) bool { return string(models[i]) < string(models[j]) })
}

// RemoteModels returns the overlay models of a state, filtered by provider.
func RemoteCatalogOverlay(state *RemoteCatalogState, providerID string, localGeneratedAt int64) []json.RawMessage {
	if state == nil || len(state.Models) == 0 {
		return nil
	}
	if localGeneratedAt != 0 && state.LastModified <= localGeneratedAt {
		return nil
	}
	out := make([]json.RawMessage, 0, len(state.Models))
	for _, model := range state.Models {
		var object map[string]any
		if err := json.Unmarshal(model, &object); err != nil {
			continue
		}
		if provider, _ := object["provider"].(string); provider != "" && provider != providerID {
			continue
		}
		out = append(out, model)
	}
	return out
}

// Refresh fetches and revalidates a provider's catalog. The caller supplies the
// stored state (may be nil). allowNetwork false only restores the stored overlay
// and reports no persist. A transient failure returns the overlay from the
// stored state and an error.
func (c *RemoteCatalogClient) Refresh(ctx context.Context, providerID string, stored *RemoteCatalogState, options RemoteCatalogRefreshOptions) (RemoteCatalogResult, error) {
	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		baseURL = DefaultCatalogBaseURL
	}
	restored := RemoteCatalogOverlay(stored, providerID, options.LocalGeneratedAt)
	result := RemoteCatalogResult{Models: restored}
	if !options.AllowNetwork || ctx.Err() != nil {
		return result, nil
	}
	now := time.Now().UnixMilli()
	if !options.Force && stored != nil && stored.CheckedAt != 0 && stored.LastModified != 0 && now-stored.CheckedAt < RemoteCatalogRefreshInterval.Milliseconds() {
		return result, nil
	}

	validator := ""
	if stored != nil && len(stored.Models) > 0 {
		validator = stored.ETag
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/api/models/providers/" + providerID
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("accept", "application/json")
	if c.UserAgent != "" {
		request.Header.Set("User-Agent", c.UserAgent)
	}
	if validator != "" {
		request.Header.Set("if-none-match", validator)
	}

	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: RemoteCatalogAttemptTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		result.State = RemoteCatalogState{CheckedAt: now}
		if stored != nil {
			result.State.Models = stored.Models
			result.State.LastModified = stored.LastModified
			result.State.ETag = stored.ETag
		}
		result.Persist = true
		return result, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified && stored != nil {
		updated := *stored
		updated.CheckedAt = now
		result.State = updated
		result.NotModified = true
		result.Persist = true
		return result, nil
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented {
		result.State = RemoteCatalogState{CheckedAt: now}
		result.NotSupported = true
		result.Persist = true
		result.Models = nil
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.State = RemoteCatalogState{CheckedAt: now}
		if stored != nil {
			result.State.Models = stored.Models
			result.State.LastModified = stored.LastModified
			result.State.ETag = stored.ETag
		}
		result.Persist = true
		return result, fmt.Errorf("model catalog request failed for %s: %d", providerID, response.StatusCode)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return result, err
	}
	models, err := ParseRemoteCatalog(providerID, body)
	if err != nil {
		return result, err
	}
	lastModified := int64(0)
	if raw := response.Header.Get("last-modified"); raw != "" {
		if parsed, err := http.ParseTime(raw); err == nil {
			lastModified = parsed.UnixMilli()
		}
	}
	state := RemoteCatalogState{
		Models:       models,
		CheckedAt:    now,
		LastModified: lastModified,
		ETag:         response.Header.Get("etag"),
	}
	result.State = state
	result.Models = RemoteCatalogOverlay(&state, providerID, options.LocalGeneratedAt)
	result.Persist = true
	return result, nil
}

// RemoteCatalogRefreshOptions configures one refresh.
type RemoteCatalogRefreshOptions struct {
	// AllowNetwork false restores only the stored overlay.
	AllowNetwork bool
	// Force ignores the freshness interval.
	Force bool
	// LocalGeneratedAt is the build time of the embedded catalog; an overlay not
	// newer than it is ignored.
	LocalGeneratedAt int64
}
