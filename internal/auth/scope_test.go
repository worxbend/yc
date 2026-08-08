package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMissingScopes(t *testing.T) {
	tests := []struct {
		name     string
		granted  []Scope
		required []Scope
		want     []Scope
	}{
		{
			name:     "nothing granted",
			required: SendScopes(),
			want:     []Scope{ScopeYouTubeForceSSL},
		},
		{
			name:     "exact match",
			granted:  LoginScopes(),
			required: ModerateScopes(),
		},
		{
			name:     "force-ssl subsumes readonly",
			granted:  []Scope{ScopeYouTubeForceSSL},
			required: ReadScopes(),
		},
		{
			name:     "broad youtube scope subsumes force-ssl",
			granted:  []Scope{ScopeYouTubeFull},
			required: SendScopes(),
		},
		{
			name:     "readonly does not subsume force-ssl",
			granted:  ReadScopes(),
			required: SendScopes(),
			want:     []Scope{ScopeYouTubeForceSSL},
		},
		{
			name:     "whitespace is trimmed before comparison",
			granted:  []Scope{Scope("  " + string(ScopeYouTubeReadonly) + "  ")},
			required: ReadScopes(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MissingScopes(test.granted, test.required)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("MissingScopes = %v, want %v", got, test.want)
			}
		})
	}
}

func TestScopesSplitsSpaceDelimitedValues(t *testing.T) {
	// Google returns granted scopes as one space-delimited string on the
	// token endpoint and as an array elsewhere; both have to parse.
	got := Scopes(string(ScopeYouTubeReadonly)+" "+string(ScopeYouTubeForceSSL), "", "   ", string(ScopeYouTubeReadonly))
	want := []Scope{ScopeYouTubeReadonly, ScopeYouTubeForceSSL}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scopes = %v, want %v", got, want)
	}
	if Scopes() != nil {
		t.Fatal("Scopes with no values must return nil")
	}
}

func TestScopeValuesRoundTrip(t *testing.T) {
	values := ScopeValues(LoginScopes())
	if !reflect.DeepEqual(Scopes(values...), LoginScopes()) {
		t.Fatalf("scope round trip lost values: %v", values)
	}
	if ScopeValues(nil) != nil {
		t.Fatal("ScopeValues(nil) must return nil")
	}
	sorted := SortedScopeValues([]Scope{ScopeYouTubeForceSSL, ScopeYouTubeReadonly})
	if sorted[0] != string(ScopeYouTubeForceSSL) {
		t.Fatalf("SortedScopeValues is not sorted: %v", sorted)
	}
}

func TestCredentialCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		credentials Credentials
		kind        CredentialKind
		read        bool
		send        bool
		moderate    bool
	}{
		{
			name: "nothing configured",
			kind: CredentialKindNone,
		},
		{
			name:        "api key reads only",
			credentials: NewAPIKeyCredentials(NewSecret(FakeTokenMarker)),
			kind:        CredentialKindAPIKey,
			read:        true,
		},
		{
			name: "readonly oauth cannot send",
			credentials: NewOAuthCredentials(TokenSet{
				AccessToken: NewSecret(FakeTokenMarker),
				Scopes:      ReadScopes(),
			}),
			kind: CredentialKindOAuth,
			read: true,
		},
		{
			name: "force-ssl oauth does everything",
			credentials: NewOAuthCredentials(TokenSet{
				AccessToken: NewSecret(FakeTokenMarker),
				Scopes:      LoginScopes(),
			}),
			kind:     CredentialKindOAuth,
			read:     true,
			send:     true,
			moderate: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials := test.credentials
			if got := credentials.Kind(); got != test.kind {
				t.Fatalf("Kind = %q, want %q", got, test.kind)
			}
			if got := credentials.CanRead(); got != test.read {
				t.Fatalf("CanRead = %v, want %v", got, test.read)
			}
			if got := credentials.CanSend(); got != test.send {
				t.Fatalf("CanSend = %v, want %v", got, test.send)
			}
			if got := credentials.CanModerate(); got != test.moderate {
				t.Fatalf("CanModerate = %v, want %v", got, test.moderate)
			}
		})
	}
}

// TestCredentialReason checks that a disabled control gets an explanation, and
// that the explanation never contains credential material.
func TestCredentialReason(t *testing.T) {
	credentials := NewAPIKeyCredentials(NewSecret(FakeTokenMarker))
	if reason := credentials.Reason(CapabilityRead); reason != "" {
		t.Fatalf("an available capability must have no reason, got %q", reason)
	}
	reason := credentials.Reason(CapabilitySend)
	if reason == "" {
		t.Fatal("an unavailable capability must explain itself")
	}
	if strings.Contains(reason, FakeTokenMarker) {
		t.Fatalf("reason leaked a credential: %s", reason)
	}

	readonly := NewOAuthCredentials(TokenSet{AccessToken: NewSecret(FakeTokenMarker), Scopes: ReadScopes()})
	if got := readonly.Reason(CapabilityModerate); !strings.Contains(got, string(ScopeYouTubeForceSSL)) {
		t.Fatalf("missing-scope reason should name the scope, got %q", got)
	}

	var none Credentials
	if got := none.Reason(CapabilityRead); !strings.Contains(got, "yc login") {
		t.Fatalf("unconfigured reason should point at login, got %q", got)
	}
}

func TestCredentialExpiry(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	credentials := Credentials{
		AccessToken: NewSecret(FakeTokenMarker),
		ExpiresAt:   now.Add(30 * time.Second),
	}

	if credentials.Expired(now, 0) {
		t.Fatal("a token 30s from expiry is not expired on its own terms")
	}
	if !credentials.Expired(now, DefaultExpirySkew) {
		t.Fatal("a token inside the 60s skew must be treated as expired")
	}
	fresh := Credentials{ExpiresAt: now.Add(2 * time.Minute)}
	if fresh.Expired(now, DefaultExpirySkew) {
		t.Fatal("a token well outside the skew must not be reported as expired")
	}

	var unknown Credentials
	if unknown.Expired(now, DefaultExpirySkew) {
		t.Fatal("an unknown expiry must not be reported as expired")
	}
	if credentials.Refreshable() {
		t.Fatal("no refresh token means not refreshable")
	}
	credentials.RefreshToken = NewSecret(FakeTokenMarker)
	if !credentials.Refreshable() {
		t.Fatal("a refresh token makes the credential refreshable")
	}
}

func TestScopesForCapability(t *testing.T) {
	if got := ScopesFor(CapabilityRead); !reflect.DeepEqual(got, ReadScopes()) {
		t.Fatalf("ScopesFor(read) = %v", got)
	}
	if got := ScopesFor(Capability("nonsense")); got != nil {
		t.Fatalf("an unknown capability has no scopes, got %v", got)
	}
	if !HasScope(LoginScopes(), ScopeYouTubeForceSSL) {
		t.Fatal("HasScope should find a granted scope")
	}
}
