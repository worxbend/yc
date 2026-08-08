package auth

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestSecretNeverFormatsRaw is the load-bearing redaction test: every formatting
// verb and every encoder yc might reach for has to produce a placeholder.
func TestSecretNeverFormatsRaw(t *testing.T) {
	secret := NewSecret(FakeTokenMarker)

	rendered := []string{
		secret.String(),
		secret.GoString(),
		secret.Redacted(),
		fmt.Sprint(secret),
	}
	// Every verb a caller might reach for, including the ones a linter would
	// rewrite away: the whole point is that none of them can print the raw
	// value.
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%10s"} {
		rendered = append(rendered, fmt.Sprintf(format, secret))
	}
	for _, value := range rendered {
		if strings.Contains(value, FakeTokenMarker) {
			t.Fatalf("formatted secret leaked the raw value: %s", value)
		}
	}

	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	if strings.Contains(string(encoded), FakeTokenMarker) {
		t.Fatalf("json encoding leaked the raw value: %s", encoded)
	}

	text, err := secret.MarshalText()
	if err != nil {
		t.Fatalf("marshal secret text: %v", err)
	}
	if strings.Contains(string(text), FakeTokenMarker) {
		t.Fatalf("text encoding leaked the raw value: %s", text)
	}

	if secret.Reveal() != FakeTokenMarker {
		t.Fatalf("Reveal must return the raw value, got %q", secret.Reveal())
	}
}

// TestSecretEmptyPrintsEmpty keeps "not configured" distinguishable from
// "configured but hidden" in diagnostics.
func TestSecretEmptyPrintsEmpty(t *testing.T) {
	var secret Secret
	if secret.Present() {
		t.Fatal("zero secret must not be present")
	}
	if got := secret.String(); got != "" {
		t.Fatalf("empty secret should print empty, got %q", got)
	}
	if got := NewSecret("   ").String(); got != "" {
		t.Fatalf("whitespace-only secret should print empty, got %q", got)
	}
	if got := fmt.Sprintf("%#v", secret); got != `auth.Secret("")` {
		t.Fatalf("unexpected GoString for empty secret: %s", got)
	}
}

// TestAuthTypesNeverFormatRaw walks every exported type in the package that
// carries credential material and asserts that no formatting verb and no JSON
// encoding of it can emit the fake marker.
func TestAuthTypesNeverFormatRaw(t *testing.T) {
	secret := NewSecret(FakeTokenMarker)
	tokens := TokenSet{
		AccessToken:  secret,
		RefreshToken: secret,
		TokenType:    "Bearer",
		Scopes:       LoginScopes(),
	}

	values := []any{
		LoginRequest{ClientID: "client", ClientSecret: secret, State: secret, CodeVerifier: secret},
		LoginChallenge{AuthorizationURL: secret, State: secret},
		LoginCallback{Code: secret, State: secret, ExpectedState: secret, CodeVerifier: secret},
		tokens,
		LoginResult{Tokens: tokens, Scopes: LoginScopes()},
		Credentials{AccessToken: secret, RefreshToken: secret, APIKey: secret},
		NewFakeLoginFlow().Challenge,
	}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			rendered := fmt.Sprintf(format, value)
			if strings.Contains(rendered, FakeTokenMarker) {
				t.Fatalf("%T formatted with %s leaked a credential: %s", value, format, rendered)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %T: %v", value, err)
		}
		if strings.Contains(string(encoded), FakeTokenMarker) {
			t.Fatalf("%T json encoding leaked a credential: %s", value, encoded)
		}
	}
}

func TestRedactorRemovesExplicitSecrets(t *testing.T) {
	redactor := NewRedactor(NewSecret(FakeTokenMarker), NewSecret(""))
	got := redactor.Redact("failed with token " + FakeTokenMarker + " twice: " + FakeTokenMarker)
	if strings.Contains(got, FakeTokenMarker) {
		t.Fatalf("explicit secret survived redaction: %s", got)
	}
	if strings.Count(got, RedactedSecret) != 2 {
		t.Fatalf("expected two placeholders, got %q", got)
	}
}

func TestRedactorPatterns(t *testing.T) {
	tests := []struct {
		name  string
		value string
		leak  string
	}{
		{"access token", "access_token=ya29.super-secret-value", "ya29.super-secret-value"},
		{"refresh token", "refresh_token: 1//0gsecretvalue", "1//0gsecretvalue"},
		{"id token", "id_token=eyJhbGciOiJSUzI1NiJ9.payload", "eyJhbGciOiJSUzI1NiJ9.payload"},
		{"client secret", "client_secret=GOCSPX-abcdefghijklmnop", "GOCSPX-abcdefghijklmnop"},
		{"api key snake", "api_key=notarealkeyvalue", "notarealkeyvalue"},
		{"api key dashed", "api-key=notarealkeyvalue", "notarealkeyvalue"},
		{"bearer header", "Authorization: Bearer ya29.header-token", "ya29.header-token"},
		{"oauth code", "code=4/0Aeaf-authorization-code", "4/0Aeaf-authorization-code"},
		{"oauth state", "state=csrfstatevalue", "csrfstatevalue"},
		{"pkce verifier", "code_verifier=verifiervaluehere", "verifiervaluehere"},
		{"pkce challenge", "code_challenge=challengevaluehere", "challengevaluehere"},
		{"bare google api key", "https://example.test/x?k=AIzaSyA1234567890abcdefghijklmnopqrstuv", "AIzaSyA1234567890abcdefghijklmnopqrstuv"},
	}

	redactor := NewRedactor()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := redactor.Redact(test.value)
			if strings.Contains(got, test.leak) {
				t.Fatalf("pattern redaction missed %q: %s", test.leak, got)
			}
			if !strings.Contains(got, RedactedSecret) {
				t.Fatalf("expected a placeholder in %q", got)
			}
		})
	}
}
