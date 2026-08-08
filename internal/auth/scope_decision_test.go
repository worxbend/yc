package auth

import (
	"strings"
	"testing"
	"time"
)

// yc requests the narrowest scopes that cover what it does. Widening the set is
// a decision, not an implementation detail: every extra scope is one more thing
// the consent screen asks a user to grant, and one more thing a leaked token
// could do.
func TestScopeSetsAreTheNarrowestThatCoverTheirCapability(t *testing.T) {
	if got := ScopeValues(ReadScopes()); len(got) != 1 || got[0] != string(ScopeYouTubeReadonly) {
		t.Errorf("ReadScopes() = %v, want only youtube.readonly", got)
	}
	for name, scopes := range map[string][]Scope{
		"SendScopes":         SendScopes(),
		"ModerateScopes":     ModerateScopes(),
		"StreamManageScopes": StreamManageScopes(),
	} {
		got := ScopeValues(scopes)
		if len(got) != 1 || got[0] != string(ScopeYouTubeForceSSL) {
			t.Errorf("%s() = %v, want only youtube.force-ssl", name, got)
		}
	}

	// Login asks for exactly the union, and never for the broad youtube scope.
	login := ScopeValues(LoginScopes())
	if len(login) != 2 {
		t.Errorf("LoginScopes() = %v, want exactly readonly plus force-ssl", login)
	}
	for _, scope := range login {
		if scope == string(ScopeYouTubeFull) {
			t.Errorf("LoginScopes() requests the broad youtube scope: %v", login)
		}
	}
	// Every capability's requirement must be inside what login requests, or a
	// successful login would still leave a feature unusable.
	for name, required := range map[string][]Scope{
		"read": ReadScopes(), "send": SendScopes(),
		"moderate": ModerateScopes(), "stream": StreamManageScopes(),
	} {
		if missing := MissingScopes(LoginScopes(), required); len(missing) > 0 {
			t.Errorf("a completed login cannot %s: missing %v", name, ScopeValues(missing))
		}
	}
}

// force-ssl and the broad youtube scope both subsume readonly, so a credential
// granted only force-ssl must not be reported as missing read access - that
// would send a user back to the consent screen to grant something they already
// effectively have.
func TestMissingScopesHonorsSubsumption(t *testing.T) {
	cases := []struct {
		name     string
		granted  []Scope
		required []Scope
		want     []string
	}{
		{"exact match", LoginScopes(), LoginScopes(), nil},
		{"force-ssl subsumes readonly", []Scope{ScopeYouTubeForceSSL}, ReadScopes(), nil},
		{"broad youtube subsumes readonly", []Scope{ScopeYouTubeFull}, ReadScopes(), nil},
		{"broad youtube subsumes force-ssl", []Scope{ScopeYouTubeFull}, SendScopes(), nil},
		{"readonly does not subsume force-ssl", ReadScopes(), SendScopes(), []string{string(ScopeYouTubeForceSSL)}},
		{"nothing granted", nil, LoginScopes(), ScopeValues(LoginScopes())},
		{"nothing required", LoginScopes(), nil, nil},
		{"blank entries ignored", []Scope{"", "  ", ScopeYouTubeForceSSL}, SendScopes(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeValues(MissingScopes(tc.granted, tc.required))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("MissingScopes = %v, want %v", got, tc.want)
			}
		})
	}
}

// The capability answer is what enables or disables the composer and the
// moderation keys, so each credential kind has to resolve to the honest answer.
func TestCredentialCapabilityDecisions(t *testing.T) {
	cases := []struct {
		name        string
		credentials Credentials
		read        bool
		send        bool
		moderate    bool
		stream      bool
	}{
		{
			name:        "nothing configured",
			credentials: Credentials{},
		},
		{
			name:        "api key reads only",
			credentials: Credentials{APIKey: NewSecret("AIza-" + FakeTokenMarker)},
			read:        true,
		},
		{
			name: "readonly token cannot write",
			credentials: Credentials{
				AccessToken: NewSecret(FakeTokenMarker),
				Scopes:      ReadScopes(),
			},
			read: true,
		},
		{
			name: "force-ssl token can do everything yc offers",
			credentials: Credentials{
				AccessToken: NewSecret(FakeTokenMarker),
				Scopes:      []Scope{ScopeYouTubeForceSSL},
			},
			read: true, send: true, moderate: true, stream: true,
		},
		{
			name: "broad youtube scope is accepted even though yc never asks for it",
			credentials: Credentials{
				AccessToken: NewSecret(FakeTokenMarker),
				Scopes:      []Scope{ScopeYouTubeFull},
			},
			read: true, send: true, moderate: true, stream: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]bool{
				"read":     tc.credentials.Can(CapabilityRead),
				"send":     tc.credentials.CanSend(),
				"moderate": tc.credentials.CanModerate(),
				"stream":   tc.credentials.CanManageStream(),
			}
			want := map[string]bool{
				"read": tc.read, "send": tc.send, "moderate": tc.moderate, "stream": tc.stream,
			}
			for capability, wanted := range want {
				if got[capability] != wanted {
					t.Errorf("can %s = %v, want %v", capability, got[capability], wanted)
				}
			}

			// A control yc cannot offer must explain itself, and the
			// explanation must never carry credential material.
			for _, capability := range []Capability{
				CapabilityRead, CapabilitySend, CapabilityModerate, CapabilityManageStream,
			} {
				reason := tc.credentials.Reason(capability)
				if tc.credentials.Can(capability) {
					if reason != "" {
						t.Errorf("an available %v capability carried the reason %q", capability, reason)
					}
					continue
				}
				if reason == "" {
					t.Errorf("the unavailable %v capability carried no reason", capability)
				}
				if strings.Contains(reason, FakeTokenMarker) || strings.Contains(reason, "AIza-") {
					t.Errorf("the %v reason leaked a credential: %q", capability, reason)
				}
			}
		})
	}
}

// A zero expiry is unknown, not expired: treating it as expired would refuse a
// token supplied through the environment, which never carries one.
func TestCredentialExpiryAndRefreshability(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	unknown := Credentials{AccessToken: NewSecret(FakeTokenMarker)}
	if unknown.Expired(now, time.Minute) {
		t.Error("a token with no stated expiry was reported as expired")
	}
	if unknown.Refreshable() {
		t.Error("a credential with no refresh token claimed to be refreshable")
	}

	live := Credentials{AccessToken: NewSecret(FakeTokenMarker), ExpiresAt: now.Add(time.Hour)}
	if live.Expired(now, time.Minute) {
		t.Error("a token valid for another hour was reported as expired")
	}

	// The skew is what refreshes before a request fails rather than after.
	nearly := Credentials{AccessToken: NewSecret(FakeTokenMarker), ExpiresAt: now.Add(30 * time.Second)}
	if !nearly.Expired(now, time.Minute) {
		t.Error("a token inside the skew window was not reported as expired")
	}
	if nearly.Expired(now, 0) {
		t.Error("a token still valid was reported as expired with no skew")
	}
	if nearly.Expired(now, -time.Hour) {
		t.Error("a negative skew was not clamped to zero")
	}

	refreshable := Credentials{RefreshToken: NewSecret("refresh-" + FakeTokenMarker)}
	if !refreshable.Refreshable() {
		t.Error("a credential holding a refresh token did not report as refreshable")
	}
	// The redactor must carry every secret the credential holds.
	redacted := refreshable.Redactor().Redact("refresh-" + FakeTokenMarker)
	if strings.Contains(redacted, FakeTokenMarker) {
		t.Errorf("the credential redactor missed its own refresh token: %q", redacted)
	}
}

// An empty scope list means "everything yc can use" rather than "nothing",
// because a request that asked for no scopes would produce a token that cannot
// read chat.
func TestLoginRequestDefaultsToTheFullScopeSet(t *testing.T) {
	if got := ScopeValues(LoginRequest{}.RequiredScopes()); strings.Join(got, ",") != strings.Join(ScopeValues(LoginScopes()), ",") {
		t.Errorf("RequiredScopes() = %v, want the full login set", got)
	}

	explicit := LoginRequest{Scopes: ReadScopes()}
	got := explicit.RequiredScopes()
	if len(got) != 1 || got[0] != ScopeYouTubeReadonly {
		t.Errorf("RequiredScopes() = %v, want the requested subset", ScopeValues(got))
	}
	// The returned slice must be a copy: a caller mutating it would rewrite
	// the request's own scopes.
	got[0] = Scope("mutated")
	if explicit.Scopes[0] != ScopeYouTubeReadonly {
		t.Error("RequiredScopes() handed back the request's own slice")
	}
}

// Every redactor a login-flow type exposes has to carry that type's secrets,
// because those are exactly the redactors the error paths use.
func TestEveryLoginTypeRedactorCarriesItsOwnSecrets(t *testing.T) {
	const (
		code     = "code-" + FakeTokenMarker
		state    = "state-" + FakeTokenMarker
		verifier = "verifier-" + FakeTokenMarker
		access   = "access-" + FakeTokenMarker
		refresh  = "refresh-" + FakeTokenMarker
		authURL  = "https://accounts.google.com/o/oauth2/v2/auth?state=" + state
	)

	cases := map[string]struct {
		redactor Redactor
		secrets  []string
	}{
		"LoginRequest": {
			redactor: LoginRequest{
				ClientSecret: NewSecret(access),
				State:        NewSecret(state),
				CodeVerifier: NewSecret(verifier),
			}.Redactor(),
			secrets: []string{access, state, verifier},
		},
		"LoginChallenge": {
			redactor: LoginChallenge{
				AuthorizationURL: NewSecret(authURL),
				State:            NewSecret(state),
			}.Redactor(),
			secrets: []string{authURL, state},
		},
		"LoginCallback": {
			redactor: LoginCallback{
				Code:         NewSecret(code),
				State:        NewSecret(state),
				CodeVerifier: NewSecret(verifier),
			}.Redactor(),
			secrets: []string{code, state, verifier},
		},
		"TokenSet": {
			redactor: TokenSet{
				AccessToken:  NewSecret(access),
				RefreshToken: NewSecret(refresh),
			}.Redactor(),
			secrets: []string{access, refresh},
		},
		"LoginResult": {
			redactor: LoginResult{Tokens: TokenSet{
				AccessToken:  NewSecret(access),
				RefreshToken: NewSecret(refresh),
			}}.Redactor(),
			secrets: []string{access, refresh},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			message := "failure involving " + strings.Join(tc.secrets, " and ")
			got := tc.redactor.Redact(message)
			for _, secret := range tc.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("%s's redactor missed %q:\n%s", name, secret, got)
				}
			}
		})
	}
}
