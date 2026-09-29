// Package search is the Go port of packages/agent/src/search/index.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package defines the session search service contract only. It deliberately
// contains no search implementation: products inject their own index. Cancellation
// and errors use the standard context and error contracts.
package search

import "context"

// SearchQuery is one search request. Limit uses a pointer so an absent limit
// stays distinct from an explicit zero.
type SearchQuery struct {
	Text  string `json:"text"`
	Limit *int   `json:"limit,omitempty"`
}

// SessionSearchTop is the best matching entry within a session hit.
type SessionSearchTop struct {
	EntryID   string  `json:"entryId"`
	Snippet   *string `json:"snippet,omitempty"`
	Timestamp float64 `json:"timestamp"`
}

// SessionSearchHit is one matching session. Score and Top are optional.
type SessionSearchHit struct {
	SessionID string            `json:"sessionId"`
	Score     *float64          `json:"score,omitempty"`
	Top       *SessionSearchTop `json:"top,omitempty"`
}

// EntrySearchHit is one matching transcript entry. Snippet and Score are
// optional.
type EntrySearchHit struct {
	SessionID string   `json:"sessionId"`
	EntryID   string   `json:"entryId"`
	Timestamp float64  `json:"timestamp"`
	Snippet   *string  `json:"snippet,omitempty"`
	Score     *float64 `json:"score,omitempty"`
}

// SessionSearchService is the session search contract.
type SessionSearchService interface {
	// SearchSessions returns the matching sessions for one query.
	SearchSessions(ctx context.Context, query SearchQuery) ([]SessionSearchHit, error)
	// Sync brings the index up to date with external storage.
	Sync(ctx context.Context) error
	// Notify announces that a session changed.
	Notify(sessionID string)
	// Remove drops a session from the index.
	Remove(ctx context.Context, sessionID string) error
	// Close releases service resources.
	Close(ctx context.Context) error
}

// EntrySearchService is the optional entry-level search capability. Upstream
// models it as an optional `searchEntries` method on SessionSearchService; Go
// expresses the optional capability as a separate interface so a session-only
// service remains valid.
type EntrySearchService interface {
	SessionSearchService
	// SearchEntries returns the matching entries for one query.
	SearchEntries(ctx context.Context, query SearchQuery) ([]EntrySearchHit, error)
}
