package sdk

import (
	"encoding/json"
	"testing"

	"github.com/minifish-org/pith/packages/ai/auth/oauth"
)

func TestTypeBuilderBuildsJSONSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema TSchema
		want   map[string]any
	}{
		{name: "string", schema: Type.String(), want: map[string]any{"type": "string"}},
		{name: "integer", schema: Type.Integer(), want: map[string]any{"type": "integer"}},
		{name: "boolean", schema: Type.Boolean(), want: map[string]any{"type": "boolean"}},
		{name: "literal", schema: Type.Literal("x"), want: map[string]any{"const": "x"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got map[string]any
			if err := json.Unmarshal(testCase.schema, &got); err != nil {
				t.Fatalf("invalid schema JSON: %v", err)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("schema mismatch: want %v got %v", testCase.want, got)
			}
			for key, value := range testCase.want {
				if got[key] != value {
					t.Fatalf("schema[%q]: want %v got %v", key, value, got[key])
				}
			}
		})
	}
}

func TestTypeBuilderObjectAndArray(t *testing.T) {
	schema := Type.Object(map[string]TSchema{"n": Type.Number()}, "n")
	var object map[string]any
	if err := json.Unmarshal(schema, &object); err != nil {
		t.Fatalf("object schema: %v", err)
	}
	if object["type"] != "object" {
		t.Fatalf("unexpected object type %v", object["type"])
	}
	required, ok := object["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "n" {
		t.Fatalf("unexpected required %v", object["required"])
	}
	array := Type.Array(Type.String())
	var arraySchema map[string]any
	if err := json.Unmarshal(array, &arraySchema); err != nil {
		t.Fatalf("array schema: %v", err)
	}
	if arraySchema["type"] != "array" {
		t.Fatalf("unexpected array type %v", arraySchema["type"])
	}
	union := Type.Union(Type.String(), Type.Number())
	var unionSchema map[string]any
	if err := json.Unmarshal(union, &unionSchema); err != nil {
		t.Fatalf("union schema: %v", err)
	}
	if members, ok := unionSchema["anyOf"].([]any); !ok || len(members) != 2 {
		t.Fatalf("unexpected union %v", unionSchema["anyOf"])
	}
	enum := Type.Enum("a", "b")
	var enumSchema map[string]any
	if err := json.Unmarshal(enum, &enumSchema); err != nil {
		t.Fatalf("enum schema: %v", err)
	}
	if enumSchema["type"] != "string" {
		t.Fatalf("unexpected enum type %v", enumSchema["type"])
	}
	unsafe := Type.Unsafe(TSchema(`{"$ref":"#/definitions/x"}`))
	if string(unsafe) != `{"$ref":"#/definitions/x"}` {
		t.Fatalf("unsafe must pass the schema through: %s", unsafe)
	}
}

func TestStaticProjectsThePairedValue(t *testing.T) {
	if got := Static(7); got != 7 {
		t.Fatalf("Static must project the value, got %v", got)
	}
	type payload struct{ Name string }
	value := payload{Name: "schema"}
	if got := Static(value); got != value {
		t.Fatalf("Static must preserve the value, got %v", got)
	}
}

func TestBedrockProviderModuleIsLinked(t *testing.T) {
	if BedrockProviderModule == nil {
		t.Fatal("bedrock provider module must be linked")
	}
}

func TestRegisterBunOAuthFlowsUsesBundledFlows(t *testing.T) {
	RegisterBunOAuthFlows()
	loaded, err := oauth.LoadAnthropicOAuth()
	if err != nil {
		t.Fatalf("load anthropic: %v", err)
	}
	if loaded != oauth.AnthropicOAuth {
		t.Fatal("bundled loader must resolve the statically linked Anthropic flow")
	}
	radius, err := oauth.LoadRadiusOAuth(oauth.RadiusOAuthOptions{})
	if err != nil {
		t.Fatalf("load radius: %v", err)
	}
	if radius == nil {
		t.Fatal("radius flow must be constructible through the bundled loader")
	}
}

func TestOAuthAliasesResolveToAuthTypes(t *testing.T) {
	prompt := OAuthPrompt{Message: "m"}
	if prompt.Message != "m" {
		t.Fatal("OAuthPrompt alias is not the auth types DTO")
	}
	var callbacks OAuthLoginCallbacks
	if callbacks.OnAuth != nil || callbacks.Signal != nil {
		t.Fatal("optional callbacks must default to absent")
	}
	credentials := OAuthCredentials{Refresh: "r", Access: "a", Expires: 1}
	if credentials.Refresh != "r" {
		t.Fatal("OAuthCredentials alias is not the auth types DTO")
	}
}
