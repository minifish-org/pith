// This file is a Go port of the shared helpers of packages/ai/src/auth/oauth.
// It contains source-derived helpers used by several OAuth flows: callback URL
// parsing (state/issuer boundary checks) and cryptographic random values.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ParseCallback validates a provider OAuth redirect URL and returns the
// authorization code. It rejects a provider error, a missing or duplicated
// code/state, a state mismatch against expectedState, and an issuer mismatch
// against expectedIssuer (RFC 9207 `iss`). expectedState and expectedIssuer may
// be empty to skip that check.
//
// The helper is stateless; the shared callback server owns the
// "accept a valid code exactly once per flow" guarantee.
func ParseCallback(rawURL, expectedState, expectedIssuer string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", errors.New("oauth: invalid callback URL")
	}

	values := parsed.Query()
	if values == nil {
		values = url.Values{}
	}
	// Some providers deliver the code in the fragment (Anthropic's copy-code
	// page). Use it only when the query carries no code.
	if len(values["code"]) == 0 && parsed.Fragment != "" {
		if fragment, ferr := url.ParseQuery(parsed.Fragment); ferr == nil {
			for key, value := range fragment {
				values[key] = value
			}
		}
	}

	if providerError := values.Get("error"); providerError != "" {
		description := values.Get("error_description")
		if description == "" {
			description = providerError
		}
		return "", fmt.Errorf("oauth: authorization failed: %s", description)
	}

	codes := values["code"]
	if len(codes) != 1 || strings.TrimSpace(codes[0]) == "" {
		if len(codes) > 1 {
			return "", errors.New("oauth: callback contained more than one authorization code")
		}
		return "", errors.New("oauth: callback is missing the authorization code")
	}

	states := values["state"]
	if expectedState != "" {
		if len(states) != 1 || states[0] != expectedState {
			return "", errors.New("oauth: state mismatch")
		}
	} else if len(states) > 1 {
		return "", errors.New("oauth: callback contained more than one state")
	}

	if expectedIssuer != "" {
		issuers := values["iss"]
		if len(issuers) != 1 || issuers[0] != expectedIssuer {
			return "", errors.New("oauth: issuer mismatch")
		}
	}

	return codes[0], nil
}

// randomBase64URL returns n cryptographically random bytes encoded as an
// unpadded base64url string.
func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
