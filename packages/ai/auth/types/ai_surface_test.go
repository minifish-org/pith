package authtypes

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtensionOAuthPromptCallbackSurface(t *testing.T) {
	placeholder := "sk-..."
	prompt := OAuthPrompt{Message: "Paste key", Placeholder: &placeholder, AllowEmpty: true}
	if prompt.Message != "Paste key" || prompt.Placeholder == nil || *prompt.Placeholder != "sk-..." {
		t.Fatalf("unexpected prompt %#v", prompt)
	}
	auth := OAuthAuthInfo{URL: "https://example.test/auth"}
	if auth.URL != "https://example.test/auth" || auth.Instructions != nil {
		t.Fatalf("unexpected auth info %#v", auth)
	}
	device := OAuthDeviceCodeInfo{UserCode: "ABCD", VerificationURI: "https://example.test/device"}
	if device.IntervalSeconds != nil || device.ExpiresInSeconds != nil {
		t.Fatalf("optional device fields must be absent: %#v", device)
	}
	selected, called := "", false
	callbacks := OAuthLoginCallbacks{
		OnSelect: func(_ context.Context, p OAuthSelectPrompt) (string, bool, error) {
			called = true
			return p.Options[0].ID, true, nil
		},
	}
	options := []OAuthSelectOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}
	selected, ok, err := callbacks.OnSelect(context.Background(), OAuthSelectPrompt{Message: "pick", Options: options})
	if err != nil || !ok || !called || selected != "a" {
		t.Fatalf("unexpected select result %q %v %v", selected, ok, err)
	}
	if callbacks.OnAuth != nil || callbacks.OnDeviceCode != nil || callbacks.Signal != nil {
		t.Fatal("absent callbacks must stay nil")
	}
}

func TestOAuthCredentialsExtensionFieldsRoundTrip(t *testing.T) {
	input := []byte(`{"refresh":"r","access":"a","expires":1,"enterpriseUrl":"https://ent","scope":"read"}`)
	var credentials OAuthCredentials
	if err := json.Unmarshal(input, &credentials); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if credentials.Refresh != "r" || credentials.Access != "a" || credentials.Expires != 1 {
		t.Fatalf("token fields lost: %#v", credentials)
	}
	if value, ok := credentials.Extra["enterpriseUrl"]; !ok || string(value) != `"https://ent"` {
		t.Fatalf("extension field lost: %v", credentials.Extra)
	}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	for _, key := range []string{"refresh", "access", "expires", "enterpriseUrl", "scope"} {
		if _, ok := roundTrip[key]; !ok {
			t.Fatalf("missing %q in %v", key, roundTrip)
		}
	}
}

func TestOAuthCredentialExtraAccessors(t *testing.T) {
	credential := NewOAuthCredential("r", "a", 10)
	if err := credential.SetExtra("availableModelIds", []string{"m1", "m2"}); err != nil {
		t.Fatalf("set extra: %v", err)
	}
	models, ok := credential.ExtraStringSlice("availableModelIds")
	if !ok || !reflect.DeepEqual(models, []string{"m1", "m2"}) {
		t.Fatalf("unexpected slice %v %v", models, ok)
	}
	if _, ok := credential.ExtraString("missing"); ok {
		t.Fatal("missing field must report absent")
	}
	if got := credential.SortedExtraKeys(); !reflect.DeepEqual(got, []string{"availableModelIds"}) {
		t.Fatalf("unexpected sorted keys %v", got)
	}
	if credential.CredentialType() != CredentialTypeOAuth {
		t.Fatalf("unexpected credential type %q", credential.CredentialType())
	}
}
