package search

import (
	"context"
	"testing"
)

// fakeSearchService verifies that the exported interfaces describe a usable
// session-only service and a richer entry-search service.
type fakeSearchService struct {
	notified []string
}

func (f *fakeSearchService) SearchSessions(ctx context.Context, query SearchQuery) ([]SessionSearchHit, error) {
	score := 0.5
	return []SessionSearchHit{{SessionID: "s1", Score: &score}}, nil
}

func (f *fakeSearchService) Sync(ctx context.Context) error { return nil }

func (f *fakeSearchService) Notify(sessionID string) { f.notified = append(f.notified, sessionID) }

func (f *fakeSearchService) Remove(ctx context.Context, sessionID string) error { return nil }

func (f *fakeSearchService) Close(ctx context.Context) error { return nil }

type fakeEntrySearchService struct {
	fakeSearchService
}

func (f *fakeEntrySearchService) SearchEntries(ctx context.Context, query SearchQuery) ([]EntrySearchHit, error) {
	return []EntrySearchHit{{SessionID: "s1", EntryID: "e1", Timestamp: 1}}, nil
}

func TestSearchServiceContracts(t *testing.T) {
	var _ SessionSearchService = (*fakeSearchService)(nil)
	var _ EntrySearchService = (*fakeEntrySearchService)(nil)

	service := &fakeSearchService{}
	service.Notify("s1")
	if len(service.notified) != 1 || service.notified[0] != "s1" {
		t.Fatalf("notify mismatch: %#v", service.notified)
	}
	hits, err := service.SearchSessions(context.Background(), SearchQuery{Text: "x"})
	if err != nil || len(hits) != 1 || hits[0].Score == nil || *hits[0].Score != 0.5 {
		t.Fatalf("search mismatch: %#v %v", hits, err)
	}
}

func TestSearchQueryLimitPresence(t *testing.T) {
	limit := 0
	query := SearchQuery{Text: "x", Limit: &limit}
	if query.Limit == nil || *query.Limit != 0 {
		t.Fatalf("explicit zero limit must be preserved: %#v", query)
	}
	absent := SearchQuery{Text: "x"}
	if absent.Limit != nil {
		t.Fatalf("absent limit must stay nil: %#v", absent)
	}
}
