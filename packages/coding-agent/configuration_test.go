package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/minifish-org/pith/packages/ai/auth/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func testModel() *aitypes.Model {
	return &aitypes.Model{
		Id:               "test-model",
		Name:             "Test Model",
		Api:              aitypes.ApiOpenAICompletions,
		Provider:         aitypes.ProviderOpenAI,
		BaseUrl:          "http://localhost.invalid/v1",
		Reasoning:        true,
		ThinkingLevelMap: aitypes.ThinkingLevelMap{aitypes.ThinkingOff: nil, aitypes.ThinkingHigh: stringPtr("high")},
		Input:            []aitypes.ModelInputModality{aitypes.ModelInputText, aitypes.ModelInputImage},
		Cost:             aitypes.ModelCost{ModelCostRates: aitypes.ModelCostRates{Input: 1, Output: 2}},
		ContextWindow:    1_000_000,
		MaxTokens:        384_000,
		Headers:          map[string]string{"x-test": "1"},
	}
}

func stringPtr(value string) *string { return &value }

func TestLoadSettingsMergePrecedence(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.json")
	project := filepath.Join(dir, "project.json")
	if err := os.WriteFile(global, []byte(`{"flag":true,"nested":{"a":1,"b":2},"unknown":"preserve"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"flag":false,"nested":{"b":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(global, project, Settings{"zero": json.RawMessage(`0`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(settings["flag"]) != "false" {
		t.Fatalf("project must override global: %s", settings["flag"])
	}
	if string(settings["unknown"]) != `"preserve"` {
		t.Fatalf("unknown key lost: %s", settings["unknown"])
	}
	if string(settings["zero"]) != "0" {
		t.Fatalf("zero override lost: %s", settings["zero"])
	}
	var nested map[string]int
	if err := json.Unmarshal(settings["nested"], &nested); err != nil {
		t.Fatal(err)
	}
	if nested["a"] != 1 || nested["b"] != 3 {
		t.Fatalf("nested merge wrong: %#v", nested)
	}
}

func TestLoadSettingsNestedNullAndArrayReplacement(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.json")
	project := filepath.Join(dir, "project.json")
	if err := os.WriteFile(global, []byte(`{"obj":{"keep":1,"drop":2},"list":[1,2,3],"scalar":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"obj":{"drop":null},"list":[9],"scalar":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(global, project, nil)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(settings["obj"], &object); err != nil {
		t.Fatal(err)
	}
	if string(object["keep"]) != "1" {
		t.Fatalf("nested object should keep unmentioned key: %s", object["keep"])
	}
	if string(object["drop"]) != "null" {
		t.Fatalf("nested null must be preserved: %s", object["drop"])
	}
	if string(settings["list"]) != "[9]" {
		t.Fatalf("arrays must replace, not concatenate: %s", settings["list"])
	}
	if string(settings["scalar"]) != "0" {
		t.Fatalf("zero must override: %s", settings["scalar"])
	}
}

func TestLoadSettingsOverridesTopmost(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.json")
	project := filepath.Join(dir, "project.json")
	if err := os.WriteFile(global, []byte(`{"a":1,"b":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"a":2,"b":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(global, project, Settings{"a": json.RawMessage(`3`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(settings["a"]) != "3" {
		t.Fatalf("caller override must win: %s", settings["a"])
	}
	if string(settings["b"]) != "2" {
		t.Fatalf("project must beat global: %s", settings["b"])
	}
}

func TestLoadSettingsMissingFilesAreEmpty(t *testing.T) {
	dir := t.TempDir()
	settings, err := LoadSettings(filepath.Join(dir, "missing-global.json"), filepath.Join(dir, "missing-project.json"), nil)
	if err != nil {
		t.Fatalf("missing files must not error: %v", err)
	}
	if len(settings) != 0 {
		t.Fatalf("missing files must be empty settings, got %#v", settings)
	}
}

func TestLoadSettingsMalformedExistingFileIsError(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.json")
	project := filepath.Join(dir, "project.json")
	if err := os.WriteFile(global, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(global, project, nil); err == nil {
		t.Fatal("malformed existing settings must be reported")
	}
}

func TestSaveSettingsRoundTripAndPermissions(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "nested", "saved.json")
	settings := Settings{
		"unknown": json.RawMessage(`"preserve"`),
		"zero":    json.RawMessage(`0`),
		"nested":  json.RawMessage(`{"a":null}`),
	}
	if err := SaveSettings(output, settings); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != SettingsFileMode {
		t.Fatalf("settings must be private (0600), got %o", info.Mode().Perm())
	}
	back, err := LoadSettings(output, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(back["unknown"]) != `"preserve"` || string(back["zero"]) != "0" {
		t.Fatalf("round trip lost values: %#v", back)
	}
}

func TestSaveSettingsInterruptedWriteKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "saved.json")
	if err := SaveSettings(output, Settings{"a": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	// An invalid raw value fails encoding before the destination is touched.
	if err := SaveSettings(output, Settings{"a": json.RawMessage(`{`)}); err == nil {
		t.Fatal("invalid settings must fail before writing")
	}
	back, err := LoadSettings(output, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(back["a"]) != "1" {
		t.Fatalf("interrupted save must keep the original file: %s", back["a"])
	}
}

func TestResolveModelCopiesAndPreserves(t *testing.T) {
	model := testModel()
	resolved, err := ResolveModel(ModelOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ContextWindow != 1_000_000 || resolved.MaxTokens != 384_000 {
		t.Fatalf("capacity silently reduced: %#v", resolved)
	}
	if resolved.Provider != model.Provider || resolved.Api != model.Api || resolved.BaseUrl != model.BaseUrl {
		t.Fatal("provider/api/base URL not preserved")
	}
	if resolved.Headers["x-test"] != "1" {
		t.Fatal("headers not preserved")
	}
	if got := resolved.ThinkingLevelMap[aitypes.ThinkingHigh]; got == nil || *got != "high" {
		t.Fatal("thinking capabilities not preserved")
	}
	resolved.Id = "changed"
	resolved.Headers["x-test"] = "mutated"
	if model.Id != "test-model" || model.Headers["x-test"] != "1" {
		t.Fatal("resolved model aliases caller data")
	}
}

func TestResolveModelExplicitLimits(t *testing.T) {
	model := testModel()
	max := 8192
	resolved, err := ResolveModel(ModelOptions{Model: model, MaxTokens: &max})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MaxTokens != 8192 {
		t.Fatalf("explicit max tokens ignored: %g", resolved.MaxTokens)
	}
	contextWindow := 500_000
	resolved, err = ResolveModel(ModelOptions{Model: model, ContextWindow: &contextWindow})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ContextWindow != 500_000 {
		t.Fatalf("explicit context window ignored: %g", resolved.ContextWindow)
	}
	if model.MaxTokens != 384_000 || model.ContextWindow != 1_000_000 {
		t.Fatal("caller model mutated by overrides")
	}
}

func TestResolveModelRejectsInvalidOverrides(t *testing.T) {
	model := testModel()
	for name, options := range map[string]ModelOptions{
		"over-output":  {Model: model, MaxTokens: intPtr(400_000)},
		"zero-output":  {Model: model, MaxTokens: intPtr(0)},
		"negative":     {Model: model, MaxTokens: intPtr(-1)},
		"over-context": {Model: model, ContextWindow: intPtr(2_000_000)},
		"zero-context": {Model: model, ContextWindow: intPtr(0)},
	} {
		if _, err := ResolveModel(options); err == nil {
			t.Fatalf("%s override must be rejected", name)
		}
	}
	if _, err := ResolveModel(ModelOptions{}); err == nil {
		t.Fatal("missing model must be rejected")
	}
}

func TestResolveModelAllowsLimitsWhenCapacityUnknown(t *testing.T) {
	model := &aitypes.Model{Id: "custom", Provider: "custom", Api: aitypes.ApiOpenAICompletions}
	resolved, err := ResolveModel(ModelOptions{Model: model, MaxTokens: intPtr(128), ContextWindow: intPtr(2048)})
	if err != nil {
		t.Fatalf("positive limits must be accepted when capacity is unknown: %v", err)
	}
	if resolved.MaxTokens != 128 || resolved.ContextWindow != 2048 {
		t.Fatalf("limits not applied: %#v", resolved)
	}
}

func intPtr(value int) *int { return &value }

func TestSettingsManagerScopesAndFlush(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ConfigDirName, "settings.json")
	manager := SettingsManagerCreate(dir, filepath.Join(dir, "agent"), SettingsManagerCreateOptions{})
	manager.SetProject("theme", "dark")
	manager.SetGlobal("defaultThinkingLevel", "low")
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	project, err := LoadSettings(projectPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(project["theme"]) != `"dark"` {
		t.Fatalf("project setting not persisted: %#v", project)
	}
	merged := manager.GetSettings()
	if string(merged["defaultThinkingLevel"]) != `"low"` {
		t.Fatalf("global setting not merged: %#v", merged)
	}
	manager.ApplyOverrides(Settings{"theme": json.RawMessage(`"light"`)})
	if string(manager.GetSettings()["theme"]) != `"light"` {
		t.Fatal("caller override must win over project")
	}
}

func TestSettingsManagerUntrustedProjectWritesRefused(t *testing.T) {
	dir := t.TempDir()
	trusted := false
	manager := SettingsManagerCreate(dir, filepath.Join(dir, "agent"), SettingsManagerCreateOptions{ProjectTrusted: &trusted})
	manager.SetProject("theme", "dark")
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ConfigDirName, "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("untrusted project settings must not be written")
	}
}

func TestConcurrentIndependentConfiguration(t *testing.T) {
	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			manager := SettingsManagerInMemory(Settings{"worker": json.RawMessage([]byte(`0`))}, SettingsManagerCreateOptions{})
			value := json.RawMessage([]byte(`"` + string(rune('a'+index)) + `"`))
			manager.ApplyOverrides(Settings{"worker": value})
			if err := manager.Flush(); err != nil {
				errs <- err
				return
			}
			if got := string(manager.GetSettings()["worker"]); got != string(value) {
				errs <- errors.New("worker state leaked: got " + got)
			}
		}(i)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestResolveAPIKeyCallback(t *testing.T) {
	runtime := &ModelRuntime{credentials: NewRuntimeCredentials(nil)}
	if _, err := runtime.ResolveAPIKey(context.Background(), "openai"); err == nil {
		t.Fatal("no credential must error")
	}

	runtime.apiKey = func(context.Context, string) (string, error) {
		return "", nil
	}
	if _, err := runtime.ResolveAPIKey(context.Background(), "openai"); err == nil {
		t.Fatal("empty credential callback must error")
	}

	boom := errors.New("provider unavailable")
	runtime.apiKey = func(context.Context, string) (string, error) { return "", boom }
	if _, err := runtime.ResolveAPIKey(context.Background(), "openai"); !errors.Is(err, boom) {
		t.Fatalf("failed credential callback must propagate: %v", err)
	}

	runtime.apiKey = func(_ context.Context, provider string) (string, error) {
		return "key-for-" + provider, nil
	}
	key, err := runtime.ResolveAPIKey(context.Background(), "openai")
	if err != nil || key != "key-for-openai" {
		t.Fatalf("credential callback not used: %q %v", key, err)
	}
}

func TestRuntimeCredentialsOverlay(t *testing.T) {
	credentials := NewRuntimeCredentials(nil)
	if credentials.HasRuntimeAPIKey("openai") {
		t.Fatal("unexpected runtime key")
	}
	credentials.SetRuntimeAPIKey("openai", "runtime-key")
	credential, err := credentials.Read(context.Background(), "openai", nil)
	if err != nil {
		t.Fatal(err)
	}
	apiKey, ok := credential.(*authtypes.ApiKeyCredential)
	if !ok || apiKey.Key == nil || *apiKey.Key != "runtime-key" {
		t.Fatalf("runtime credential not returned: %#v", credential)
	}
	credentials.RemoveRuntimeAPIKey("openai")
	if credentials.HasRuntimeAPIKey("openai") {
		t.Fatal("runtime key not removed")
	}
}

func TestConfigValueNoShellByDefault(t *testing.T) {
	ClearConfigValueCache()
	env := map[string]string{"TOKEN": "secret"}
	if got := ResolveConfigValue("$TOKEN", env); got == nil || *got != "secret" {
		t.Fatalf("env template not resolved: %v", got)
	}
	if got := ResolveConfigValue("${MISSING}", env); got != nil {
		t.Fatalf("missing env must not resolve: %v", *got)
	}
	if got := ResolveConfigValue("literal", env); got == nil || *got != "literal" {
		t.Fatalf("literal not resolved: %v", got)
	}
	// Command config values must not run by default.
	executed := false
	ConfigValueCommandExecutor = nil
	if got := ResolveConfigValue("!echo pwned", env); got != nil {
		t.Fatalf("shell command executed by default: %v", *got)
	}
	if _, err := ResolveConfigValueOrThrow("!echo pwned", "API key", env); err == nil {
		t.Fatal("command config value must fail when no executor is installed")
	}
	if executed {
		t.Fatal("command executor ran despite being disabled")
	}
	// Opt-in executor is honored and cached.
	ConfigValueCommandExecutor = func(command string) (string, bool) { return "resolved:" + command, true }
	defer func() { ConfigValueCommandExecutor = nil; ClearConfigValueCache() }()
	if got := ResolveConfigValue("!echo hi", env); got == nil || *got != "resolved:echo hi" {
		t.Fatalf("opt-in executor not used: %v", got)
	}
}

func TestMergeSettingsDoesNotMutateInputs(t *testing.T) {
	base := Settings{"a": json.RawMessage(`{"x":1}`)}
	override := Settings{"a": json.RawMessage(`{"y":2}`)}
	merged := MergeSettings(base, override)
	var value map[string]int
	if err := json.Unmarshal(merged["a"], &value); err != nil {
		t.Fatal(err)
	}
	if value["x"] != 1 || value["y"] != 2 {
		t.Fatalf("merge lost keys: %#v", value)
	}
	if string(base["a"]) != `{"x":1}` || string(override["a"]) != `{"y":2}` {
		t.Fatal("merge mutated its inputs")
	}
}

func TestParseModelPatternThinkingLevel(t *testing.T) {
	models := []aitypes.Model{*testModel()}
	parsed := ParseModelPattern("test-model:high", models, nil)
	if parsed.Model == nil || parsed.ThinkingLevel != aitypes.ThinkingHigh {
		t.Fatalf("thinking level not parsed: %#v", parsed)
	}
	parsed = ParseModelPattern("test-model:bogus", models, nil)
	if parsed.Model == nil || parsed.Warning == "" {
		t.Fatalf("invalid thinking level should warn and fall back: %#v", parsed)
	}
	if got := ParseModelPattern("unknown", models, nil); got.Model != nil {
		t.Fatal("unknown pattern must not resolve")
	}
}

func TestGlobMatch(t *testing.T) {
	if !globMatch("openai/*", "openai/gpt-5") {
		t.Fatal("simple glob should match")
	}
	if globMatch("openai/*", "anthropic/claude") {
		t.Fatal("simple glob must respect prefix")
	}
	if !globMatch("*sonnet*", "claude-sonnet-4") {
		t.Fatal("substring glob should match a bare model id")
	}
	if globMatch("*sonnet*", "anthropic/claude-sonnet-4") {
		t.Fatal("single star must not cross a path separator")
	}
}

func TestLoadSettingsStripsBOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.json")
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"a":1}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadSettings(path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(settings["a"]) != "1" {
		t.Fatalf("BOM not stripped: %#v", settings)
	}
}

func TestTopLevelNullReplacesObject(t *testing.T) {
	merged := MergeSettings(Settings{"a": json.RawMessage(`{"x":1}`)}, Settings{"a": json.RawMessage(`null`)})
	if string(merged["a"]) != "null" {
		t.Fatalf("null must replace an object: %s", merged["a"])
	}
	if !strings.Contains(string(merged["a"]), "null") {
		t.Fatal("unreachable")
	}
}
