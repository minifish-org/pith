package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	h "github.com/minifish-org/pith/packages/durable/harness"
)

func TestPortsmithJudgeDurableRegistryReplacementAndPromptOrder(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	z := h.PromptSection{Key: "zeta", Render: func(context.Context, h.PromptInput) (string, error) { return "Z-section", nil }}
	a := h.PromptSection{Key: "alpha", Render: func(context.Context, h.PromptInput) (string, error) { return "A-section", nil }}
	djCheck(t, reg.Install(h.Extension{Name: "ordered", Sections: []h.PromptSection{z, a}}))
	djCheck(t, reg.Install(h.Extension{Name: "other"}))
	old := reg.Snapshot()
	models := &djModels{}
	_, root := djOpen(t, ctx, reg, models)
	djConfigure(t, ctx, root)
	djSettled(t, ctx, djSubmit(t, ctx, root, djInput("one", "one")), "done")
	first := djHistory(t, ctx, root)
	var firstWire []byte
	for _, entry := range first {
		if entry.Kind == "pi.system" {
			firstWire, _ = json.Marshal(entry.Model)
		}
	}
	zi, ai := strings.Index(string(firstWire), `"zeta"`), strings.Index(string(firstWire), `"alpha"`)
	if zi < 0 || ai <= zi {
		t.Fatalf("selected section order lost in stored baseline: %s", firstWire)
	}
	djCheck(t, reg.Install(h.Extension{Name: "ordered", Sections: []h.PromptSection{a, z}}))
	now := reg.Snapshot()
	if len(now.Installed()) < 2 || now.Installed()[0].Name != "ordered" || old.Installed()[0].Sections[0].Key != "zeta" {
		t.Fatal("replacement moved installation or mutated phase snapshot")
	}
	djSettled(t, ctx, djSubmit(t, ctx, root, djInput("two", "two")), "done")
	history := djHistory(t, ctx, root)
	var systems []string
	for _, entry := range history {
		if entry.Kind == "pi.system" {
			encoded, _ := json.Marshal(entry.Model)
			systems = append(systems, string(encoded))
		}
	}
	if len(systems) != 3 {
		t.Fatalf("order-only change needs remove-all/re-add-all system entries, got %d: %v", len(systems), systems)
	}
	if !strings.Contains(systems[1], `"zeta":null`) || !strings.Contains(systems[1], `"alpha":null`) {
		t.Fatalf("section removal patch missing: %s", systems[1])
	}
	ai, zi = strings.Index(systems[2], `"alpha"`), strings.Index(systems[2], `"zeta"`)
	if ai < 0 || zi <= ai {
		t.Fatalf("re-add order changed: %s", systems[2])
	}
	djCheck(t, reg.Uninstall("ordered"))
	djCheck(t, reg.Install(h.Extension{Name: "ordered", Sections: []h.PromptSection{z}}))
	installed := reg.Snapshot().Installed()
	if installed[len(installed)-1].Name != "ordered" {
		t.Fatal("uninstall/reinstall did not append extension")
	}
	djCheck(t, root.Configure(ctx, json.RawMessage(`{"extensions":[]}`)))
	agent, e := root.Agent(ctx)
	djCheck(t, e)
	if len(agent.Extensions) != 0 || len(agent.Sections) != 0 {
		t.Fatalf("explicit empty selection fell back to host defaults: %+v", agent)
	}
	djCheck(t, root.Configure(ctx, json.RawMessage(`{"extensions":null}`)))
	agent, e = root.Agent(ctx)
	djCheck(t, e)
	if len(agent.Extensions) == 0 {
		t.Fatal("null failed to clear stored extension override")
	}
}
