// This file is a Go port of packages/ai/src/providers/amazon-bedrock.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"context"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// bedrockAuth accepts a bearer token or the AWS SDK's default credential chain.
// The login flow can store a token/profile choice; resolve also detects ambient
// AWS credentials without copying them into the credential store.
var bedrockAuth = authtypes.ApiKeyAuth{
	Name: "AWS credentials or bearer token",
	Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.ApiKeyCredential, error) {
		if err := signalError(interaction.Signal()); err != nil {
			return nil, err
		}
		method, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
			Type:    authtypes.AuthPromptSelect,
			Message: "Select Amazon Bedrock authentication method:",
			Options: []authtypes.AuthPromptOption{
				{ID: "bearer-token", Label: "Bearer token"},
				{ID: "aws-profile", Label: "AWS profile"},
				{ID: "credential-chain", Label: "Existing AWS credential chain"},
			},
		})
		if err != nil {
			return nil, err
		}
		if err := signalError(interaction.Signal()); err != nil {
			return nil, err
		}
		if method == "bearer-token" {
			key, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSecret,
				Message: "Enter Amazon Bedrock bearer token",
			})
			if err != nil {
				return nil, err
			}
			return authtypes.NewApiKeyCredential(key), nil
		}
		message := "Amazon Bedrock supports AWS profiles, IAM credentials, and role-based credentials."
		linkLabel := "AWS credential provider chain"
		interaction.Notify(authtypes.AuthEvent{
			Type:    authtypes.AuthEventInfo,
			Message: &message,
			Links: []authtypes.AuthInfoLink{{
				Label: &linkLabel,
				URL:   "https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html",
			}},
		})
		if method == "aws-profile" {
			profile, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptText,
				Message: "Enter AWS profile name",
			})
			if err != nil {
				return nil, err
			}
			return &authtypes.ApiKeyCredential{
				Type: authtypes.CredentialTypeAPIKey,
				Env:  types.ProviderEnv{"AWS_PROFILE": &profile},
			}, nil
		}
		if method != "credential-chain" {
			return nil, signalErrorf("Unknown Amazon Bedrock auth method: %s", method)
		}
		if _, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
			Type:    authtypes.AuthPromptText,
			Message: "Configure AWS credentials, then press Enter to continue",
		}); err != nil {
			return nil, err
		}
		return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey}, nil
	},
	Resolve: func(_ context.Context, input authtypes.ApiKeyResolveInput) (*authtypes.AuthResult, error) {
		env := func(name string) (*string, error) {
			if err := signalError(input.Signal); err != nil {
				return nil, err
			}
			value, err := input.Ctx.Env(name)
			if err != nil {
				return nil, err
			}
			if err := signalError(input.Signal); err != nil {
				return nil, err
			}
			return value, nil
		}
		if input.Credential != nil && input.Credential.Key != nil && *input.Credential.Key != "" {
			source := "stored credential"
			return &authtypes.AuthResult{
				Auth:   authtypes.ModelAuth{APIKey: input.Credential.Key},
				Env:    input.Credential.Env,
				Source: &source,
			}, nil
		}
		if value, err := env("AWS_BEARER_TOKEN_BEDROCK"); err != nil {
			return nil, err
		} else if value != nil && *value != "" {
			source := "AWS_BEARER_TOKEN_BEDROCK"
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{}, Source: &source}, nil
		}

		var credentialProfile *string
		if input.Credential != nil && input.Credential.Env != nil {
			credentialProfile = input.Credential.Env["AWS_PROFILE"]
		}
		ambientProfile, err := env("AWS_PROFILE")
		if err != nil {
			return nil, err
		}
		if credentialProfile != nil || (ambientProfile != nil && *ambientProfile != "") {
			source := "AWS_PROFILE"
			var credentialEnv types.ProviderEnv
			if credentialProfile != nil {
				source = "stored credential"
				credentialEnv = input.Credential.Env
			}
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{}, Env: credentialEnv, Source: &source}, nil
		}

		accessKey, err := env("AWS_ACCESS_KEY_ID")
		if err != nil {
			return nil, err
		}
		secretKey, err := env("AWS_SECRET_ACCESS_KEY")
		if err != nil {
			return nil, err
		}
		if accessKey != nil && *accessKey != "" && secretKey != nil && *secretKey != "" {
			source := "AWS access keys"
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{}, Source: &source}, nil
		}
		for _, name := range []string{
			"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
			"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		} {
			value, err := env(name)
			if err != nil {
				return nil, err
			}
			if value != nil && *value != "" {
				source := "ECS task role"
				return &authtypes.AuthResult{Auth: authtypes.ModelAuth{}, Source: &source}, nil
			}
		}
		webIdentity, err := env("AWS_WEB_IDENTITY_TOKEN_FILE")
		if err != nil {
			return nil, err
		}
		if webIdentity != nil && *webIdentity != "" {
			source := "web identity token"
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{}, Source: &source}, nil
		}
		return nil, nil
	},
}

// AmazonBedrockProvider builds the Amazon Bedrock provider.
func AmazonBedrockProvider() ai.Provider {
	authValue := bedrockAuth
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:     types.ProviderAmazonBedrock,
		Name:   "Amazon Bedrock",
		Auth:   authtypes.ProviderAuth{APIKey: &authValue},
		Models: catalogModelList(catalog.AMAZON_BEDROCK_MODELS),
		API:    api.BedrockConverseStreamApi(),
	})
}
