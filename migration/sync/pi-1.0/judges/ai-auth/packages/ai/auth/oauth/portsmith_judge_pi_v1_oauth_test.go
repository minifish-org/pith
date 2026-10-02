package oauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	"net/url"
	"strings"
	"testing"
)

func TestPortsmithJudgePiV1PKCE(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		v, c, err := oauth.GeneratePKCE()
		if err != nil {
			t.Fatal(err)
		}
		if len(v) < 43 || len(v) > 128 || strings.ContainsAny(v, "+/=") {
			t.Fatal("invalid PKCE verifier")
		}
		sum := sha256.Sum256([]byte(v))
		if c != base64.RawURLEncoding.EncodeToString(sum[:]) {
			t.Fatal("not S256")
		}
		if seen[v] {
			t.Fatal("repeated verifier")
		}
		seen[v] = true
	}
}

func TestPortsmithJudgePiV1OAuthCallback(t *testing.T) {
	raw := "http://127.0.0.1:9876/callback?code=ok&state=nonce&iss=" + url.QueryEscape("https://issuer.example")
	code, err := oauth.ParseCallback(raw, "nonce", "https://issuer.example")
	if err != nil || code != "ok" {
		t.Fatalf("valid callback rejected %q %v", code, err)
	}
	for _, bad := range []string{strings.Replace(raw, "state=nonce", "state=wrong", 1), strings.Replace(raw, "issuer.example", "evil.example", 1), raw + "&code=other", raw + "&state=other", "http://127.0.0.1/callback?state=nonce", "http://127.0.0.1/callback?state=nonce&error=access_denied"} {
		if _, err := oauth.ParseCallback(bad, "nonce", "https://issuer.example"); err == nil {
			t.Error("unsafe callback accepted", bad)
		}
	}
}
