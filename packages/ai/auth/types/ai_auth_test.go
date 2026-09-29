package authtypes

import (
	"encoding/json"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

// Mirrors the auth credential wire shape: type discriminator, token fields and
// preserved extension fields such as enterpriseUrl and accountId.
func TestOAuthCredentialJSONRoundTripPreservesExtras(t *testing.T) {
	credential := NewOAuthCredential("refresh-token", "access-token", 1234)
	credential.SetExtra("enterpriseUrl", "company.ghe.com")
	credential.SetExtra("accountId", "acct_123")
	credential.SetExtra("availableModelIds", []string{"gpt-4o", "claude"})

	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "oauth" || decoded["refresh"] != "refresh-token" || decoded["access"] != "access-token" {
		t.Fatalf("unexpected credential fields: %v", decoded)
	}
	if decoded["expires"].(float64) != 1234 {
		t.Fatalf("expires = %v; want 1234", decoded["expires"])
	}
	if decoded["enterpriseUrl"] != "company.ghe.com" || decoded["accountId"] != "acct_123" {
		t.Fatalf("extension fields lost: %v", decoded)
	}

	restored := &OAuthCredential{}
	if err := json.Unmarshal(raw, restored); err != nil {
		t.Fatal(err)
	}
	if enterprise, ok := restored.ExtraString("enterpriseUrl"); !ok || enterprise != "company.ghe.com" {
		t.Fatalf("enterpriseUrl = %q, %v", enterprise, ok)
	}
	models, ok := restored.ExtraStringSlice("availableModelIds")
	if !ok || len(models) != 2 || models[0] != "gpt-4o" {
		t.Fatalf("availableModelIds = %v, %v", models, ok)
	}
	if restored.CredentialType() != CredentialTypeOAuth {
		t.Fatalf("credential type = %q", restored.CredentialType())
	}
}

// A stored api-key credential keeps an optional key and provider env.
func TestApiKeyCredentialShape(t *testing.T) {
	accountID := "acct"
	credential := &ApiKeyCredential{
		Type: CredentialTypeAPIKey,
		Key:  stringPointer("secret"),
		Env:  types.ProviderEnv{"CLOUDFLARE_ACCOUNT_ID": &accountID},
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "api_key" || decoded["key"] != "secret" {
		t.Fatalf("unexpected api key credential: %v", decoded)
	}
	if credential.CredentialType() != CredentialTypeAPIKey {
		t.Fatalf("credential type = %q", credential.CredentialType())
	}

	empty := &ApiKeyCredential{Type: CredentialTypeAPIKey}
	rawEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var decodedEmpty map[string]any
	if err := json.Unmarshal(rawEmpty, &decodedEmpty); err != nil {
		t.Fatal(err)
	}
	if _, present := decodedEmpty["key"]; present {
		t.Fatalf("absent key should be omitted: %v", decodedEmpty)
	}
}

// CredentialInfo never carries the secret.
func TestCredentialInfoOnlyMetadata(t *testing.T) {
	info := CredentialInfo{ProviderID: "anthropic", Type: CredentialTypeOAuth}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["providerId"] != "anthropic" || decoded["type"] != "oauth" {
		t.Fatalf("unexpected info: %v", decoded)
	}
	if len(decoded) != 2 {
		t.Fatalf("credential info leaked fields: %v", decoded)
	}
}

func stringPointer(value string) *string { return &value }
