package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func TestPromptSectionOrderAndReplacement(t *testing.T) {
	ctx := testContext(t)
	registry := h.CreateRegistry()
	zeta := h.PromptSection{Key: "zeta", Render: func(context.Context, h.PromptInput) (string, error) { return "Z-section", nil }}
	alpha := h.PromptSection{Key: "alpha", Render: func(context.Context, h.PromptInput) (string, error) { return "A-section", nil }}
	if err := registry.Install(h.Extension{Name: "ordered", Sections: []h.PromptSection{zeta, alpha}}); err != nil {
		t.Fatal(err)
	}
	harness, root := testOpen(t, ctx, registry, &testModels{})
	if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	first, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("one"), RequestID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := root.Entries(ctx, entryQueryAll(), 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var baseline []byte
	for _, entry := range page.Items {
		if entry.Kind == "pi.system" {
			baseline, _ = json.Marshal(entry.Model)
		}
	}
	zi, aiIndex := strings.Index(string(baseline), `"zeta"`), strings.Index(string(baseline), `"alpha"`)
	if zi < 0 || aiIndex <= zi {
		t.Fatalf("baseline order=%s", baseline)
	}
	// Reinstall with reversed order and run again: the replayed order differs, so
	// the planner emits a remove-all/re-add-all pair.
	if err := registry.Install(h.Extension{Name: "ordered", Sections: []h.PromptSection{alpha, zeta}}); err != nil {
		t.Fatal(err)
	}
	second, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("two"), RequestID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := root.Entries(ctx, entryQueryAll(), 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	var systems []string
	for _, entry := range all.Items {
		if entry.Kind == "pi.system" {
			encoded, _ := json.Marshal(entry.Model)
			systems = append(systems, string(encoded))
		}
	}
	// page.Items is newest-first; reverse to append order.
	for i, j := 0, len(systems)-1; i < j; i, j = i+1, j-1 {
		systems[i], systems[j] = systems[j], systems[i]
	}
	if len(systems) != 3 {
		t.Fatalf("system entries=%d: %v", len(systems), systems)
	}
	if !strings.Contains(systems[1], `"zeta":null`) || !strings.Contains(systems[1], `"alpha":null`) {
		t.Fatalf("removal patch=%s", systems[1])
	}
	if strings.Index(systems[2], `"alpha"`) > strings.Index(systems[2], `"zeta"`) {
		t.Fatalf("re-add order=%s", systems[2])
	}
	_ = harness
}
