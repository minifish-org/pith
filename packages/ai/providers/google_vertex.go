// This file is a Go port of packages/ai/src/providers/google-vertex.ts from Pi
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

// vertexADCPath is the default gcloud Application Default Credentials location.
const vertexADCPath = "~/.config/gcloud/application_default_credentials.json"

// vertexAuth accepts an explicit API key or Application Default Credentials.
// ADC additionally requires project and location env vars, read by the
// implementation itself.
var vertexAuth = authtypes.ApiKeyAuth{
	Name: "Google Cloud credentials",
	Login: func(ctx context.Context, interaction authtypes.ProviderAuthInteraction) (*authtypes.ApiKeyCredential, error) {
		if err := signalError(interaction.Signal()); err != nil {
			return nil, err
		}
		method, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
			Type:    authtypes.AuthPromptSelect,
			Message: "Select Google Vertex AI authentication method:",
			Options: []authtypes.AuthPromptOption{
				{ID: "api-key", Label: "Google Cloud API key"},
				{ID: "adc", Label: "Application Default Credentials"},
				{ID: "service-account", Label: "Service account credentials file"},
			},
		})
		if err != nil {
			return nil, err
		}
		if err := signalError(interaction.Signal()); err != nil {
			return nil, err
		}
		if method == "api-key" {
			key, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptSecret,
				Message: "Enter Google Cloud API key",
			})
			if err != nil {
				return nil, err
			}
			return authtypes.NewApiKeyCredential(key), nil
		}
		if method != "adc" && method != "service-account" {
			return nil, signalErrorf("Unknown Google Vertex AI auth method: %s", method)
		}
		message := "Run `gcloud auth application-default login`, then provide the project and location."
		if method == "service-account" {
			message = "Provide a service account credentials file, project, and location."
		}
		linkLabel := "Application Default Credentials"
		interaction.Notify(authtypes.AuthEvent{
			Type:    authtypes.AuthEventInfo,
			Message: &message,
			Links: []authtypes.AuthInfoLink{{
				Label: &linkLabel,
				URL:   "https://cloud.google.com/docs/authentication/provide-credentials-adc",
			}},
		})
		project, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
			Type:    authtypes.AuthPromptText,
			Message: "Enter Google Cloud project ID",
		})
		if err != nil {
			return nil, err
		}
		location, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
			Type:    authtypes.AuthPromptText,
			Message: "Enter Google Cloud location",
		})
		if err != nil {
			return nil, err
		}
		var credentialsPath *string
		if method == "service-account" {
			value, err := interaction.Prompt(interaction.Signal(), authtypes.AuthPrompt{
				Type:    authtypes.AuthPromptText,
				Message: "Enter service account credentials file path",
			})
			if err != nil {
				return nil, err
			}
			credentialsPath = &value
		}
		env := types.ProviderEnv{
			"GOOGLE_CLOUD_PROJECT":  &project,
			"GOOGLE_CLOUD_LOCATION": &location,
		}
		if credentialsPath != nil {
			env["GOOGLE_APPLICATION_CREDENTIALS"] = credentialsPath
		}
		return &authtypes.ApiKeyCredential{Type: authtypes.CredentialTypeAPIKey, Env: env}, nil
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
		var credentialKey *string
		if input.Credential != nil {
			credentialKey = input.Credential.Key
		}
		var key *string
		if credentialKey != nil {
			key = credentialKey
		} else {
			value, err := env("GOOGLE_CLOUD_API_KEY")
			if err != nil {
				return nil, err
			}
			key = value
		}
		if key != nil && *key != "" {
			source := "GOOGLE_CLOUD_API_KEY"
			if credentialKey != nil {
				source = "stored credential"
			}
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{APIKey: key}, Source: &source}, nil
		}

		var adcPath *string
		if input.Credential != nil && input.Credential.Env != nil {
			adcPath = input.Credential.Env["GOOGLE_APPLICATION_CREDENTIALS"]
		}
		if adcPath == nil {
			value, err := env("GOOGLE_APPLICATION_CREDENTIALS")
			if err != nil {
				return nil, err
			}
			adcPath = value
		}
		if err := signalError(input.Signal); err != nil {
			return nil, err
		}
		path := adcPath
		if path == nil {
			defaultPath := vertexADCPath
			path = &defaultPath
		}
		hasCredentials, err := input.Ctx.FileExists(*path)
		if err != nil {
			return nil, err
		}
		if err := signalError(input.Signal); err != nil {
			return nil, err
		}

		project := ""
		if input.Credential != nil && input.Credential.Env != nil && input.Credential.Env["GOOGLE_CLOUD_PROJECT"] != nil {
			project = *input.Credential.Env["GOOGLE_CLOUD_PROJECT"]
		} else if value, err := env("GOOGLE_CLOUD_PROJECT"); err != nil {
			return nil, err
		} else if value != nil && *value != "" {
			project = *value
		} else if value, err := env("GCLOUD_PROJECT"); err != nil {
			return nil, err
		} else if value != nil {
			project = *value
		}

		location := ""
		if input.Credential != nil && input.Credential.Env != nil && input.Credential.Env["GOOGLE_CLOUD_LOCATION"] != nil {
			location = *input.Credential.Env["GOOGLE_CLOUD_LOCATION"]
		} else if value, err := env("GOOGLE_CLOUD_LOCATION"); err != nil {
			return nil, err
		} else if value != nil {
			location = *value
		}

		if hasCredentials && project != "" && location != "" {
			source := "gcloud application default credentials"
			var credentialEnv types.ProviderEnv
			if input.Credential != nil {
				source = "stored credential"
				credentialEnv = input.Credential.Env
			}
			return &authtypes.AuthResult{Auth: authtypes.ModelAuth{}, Env: credentialEnv, Source: &source}, nil
		}
		return nil, nil
	},
}

// GoogleVertexProvider builds the Google Vertex AI provider.
func GoogleVertexProvider() ai.Provider {
	authValue := vertexAuth
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:     types.ProviderGoogleVertex,
		Name:   "Google Vertex AI",
		Auth:   authtypes.ProviderAuth{APIKey: &authValue},
		Models: catalogModelList(catalog.GOOGLE_VERTEX_MODELS),
		API:    api.GoogleVertexApi(),
	})
}
