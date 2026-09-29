// This file is a Go port of packages/ai/src/auth/oauth/pkce.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func base64Decode(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// PKCE is a generated code verifier and its S256 challenge.
type PKCE struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE generates a PKCE code verifier and challenge. The 32 random
// bytes encode as a 43-character base64url verifier; the challenge is the
// base64url SHA-256 digest of the verifier.
//
// Ports `generatePKCE` from packages/ai/src/auth/oauth/pkce.ts.
func GeneratePKCE() (PKCE, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}
