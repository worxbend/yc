package auth

import (
	"strings"
	"testing"
)

// TestAmbiguousKeysAreRedactedInEveryParameterShape pins the credential half of
// the "state"/"code" split. Every shape here is one a real OAuth value arrives
// in, and a miss is a leaked authorization code or a leaked CSRF state.
func TestAmbiguousKeysAreRedactedInEveryParameterShape(t *testing.T) {
	t.Parallel()

	var redactor Redactor
	cases := []struct {
		name  string
		input string
		leak  string
	}{
		{"bare query assignment", "state=test-not-a-real-state", "test-not-a-real-state"},
		{"authorization url", "https://accounts.google.com/o/oauth2/v2/auth?response_type=code&client_id=x&state=test-not-a-real-state", "test-not-a-real-state"},
		{"form body after prose", "POST body: code=test-not-a-real-code&grant_type=authorization_code", "test-not-a-real-code"},
		{"query leading question mark", "?code=test-not-a-real-code", "test-not-a-real-code"},
		{"json object", `{"code":"test-not-a-real-code"}`, "test-not-a-real-code"},
		{"json object spaced", `{"state": "test-not-a-real-state"}`, "test-not-a-real-state"},
		{"json continuation", `"a":1,"state":"test-not-a-real-state"`, "test-not-a-real-state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redactor.Redact(tc.input)
			if strings.Contains(got, tc.leak) {
				t.Fatalf("Redact(%q) leaked %q: %q", tc.input, tc.leak, got)
			}
			if !strings.Contains(got, RedactedSecret) {
				t.Fatalf("Redact(%q) did not redact: %q", tc.input, got)
			}
		})
	}
}

// TestAmbiguousKeysLeaveProseAlone pins the other half. "state" and "code" are
// ordinary English, and Redact runs over every debug-log string attribute and
// every user-facing error, so over-matching silently destroys the diagnostics
// yc exists to show.
func TestAmbiguousKeysLeaveProseAlone(t *testing.T) {
	t.Parallel()

	var redactor Redactor
	for _, input := range []string{
		"connection state: connected",
		"poller state: streaming",
		"error code: 403",
		"country code: US",
		"http status code: 500",
		"the chat ended; reason code: liveChatEnded",
	} {
		if got := redactor.Redact(input); got != input {
			t.Errorf("Redact(%q) over-redacted to %q", input, got)
		}
	}
}

// TestAmbiguousKeysDoNotShadowTheExplicitKeys guards the boundary between the
// two patterns: a longer identifier that merely ends in "code" must still be
// matched by name, not truncated into a bare "code" match.
func TestAmbiguousKeysDoNotShadowTheExplicitKeys(t *testing.T) {
	t.Parallel()

	var redactor Redactor
	got := redactor.Redact("code_verifier=test-not-a-real-verifier&code_challenge=test-not-a-real-challenge")
	if strings.Contains(got, "test-not-a-real-verifier") || strings.Contains(got, "test-not-a-real-challenge") {
		t.Fatalf("PKCE values survived redaction: %q", got)
	}
}
