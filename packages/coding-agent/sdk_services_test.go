package codingagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestFromServicesPreservesPreparedResourcesAndObserver(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "prepared.md")
	if err := os.WriteFile(file, []byte("Prepared template $1"), 0600); err != nil {
		t.Fatal(err)
	}
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{
		Cwd:       dir,
		Resources: ResourceOptions{SystemPrompt: "Host-supplied instructions", TemplatePaths: []string{file}},
		Settings:  Settings{"custom": []byte(`"host setting"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer services.Manager.Close()
	defer services.Tools.CloseTools()
	// Services represent a resolved snapshot, not an instruction to reload files.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	observed := 0
	session, err := CreateAgentSessionFromServices(CreateAgentSessionFromServicesOptions{
		Services: services,
		Model: ModelOptions{Model: sessionTestModel(), StreamFn: func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
			text := transcriptJSON(transcript)
			if !strings.Contains(text, "Host-supplied instructions") || !strings.Contains(text, "Prepared template argument") {
				t.Errorf("prepared resources lost: %s", text)
			}
			if err := options.OnProviderStreamEvent("provider event", model); err != nil {
				t.Error(err)
			}
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("ok"))
		}},
		OnProviderStreamEvent: func(data any, _ *aitypes.Model) error {
			observed++
			if data != "provider event" {
				t.Error("observer data changed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	services.Resources.Templates[0].Body = "changed by host"
	services.Settings["custom"][1] = 'X'
	if _, err := session.Prompt(context.Background(), "/prepared argument", PromptOptions{}); err != nil {
		t.Fatal(err)
	}
	if observed != 1 {
		t.Fatalf("provider observer calls = %d", observed)
	}
	if string(session.settings["custom"]) != `"host setting"` {
		t.Fatal("services settings discarded")
	}
}

func TestFromServicesPreservesVirtualModelRoutes(t *testing.T) {
	dir := t.TempDir()
	services, err := CreateAgentSessionServices(CreateAgentSessionServicesOptions{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer services.Manager.Close()
	defer services.Tools.CloseTools()
	routes := 0
	definition := VirtualModelDefinition{
		Provider: "test-router", ID: "router", Name: "Router",
		ContextWindow: 1000000, MaxTokens: 384000,
		Route: func(context.Context, ModelRouteRequest) (ModelRoute, error) {
			routes++
			return ModelRoute{Model: *sessionTestModel()}, nil
		},
	}
	model := CreateVirtualModel(definition)
	session, err := CreateAgentSessionFromServices(CreateAgentSessionFromServicesOptions{
		Services: services, VirtualModels: []VirtualModelDefinition{definition},
		Model: ModelOptions{Model: &model, StreamFn: func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
			if model.Id != "test-model" {
				t.Errorf("request did not use routed model: %s", model.Id)
			}
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("ok"))
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Prompt(context.Background(), "route"); err != nil {
		t.Fatal(err)
	}
	if routes != 1 {
		t.Fatalf("route calls = %d", routes)
	}
}
