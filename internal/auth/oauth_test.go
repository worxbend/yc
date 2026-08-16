package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testClientID     = "yc-test-client.apps.googleusercontent.com"
	testAccessToken  = FakeTokenMarker + "-access"
	testRefreshToken = FakeTokenMarker + "-refresh"
	testCode         = FakeTokenMarker + "-code"
	testState        = FakeTokenMarker + "-state"
	testVerifier     = "test-not-a-real-token-verifier-0123456789abcdefgh"
)

var testNow = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

// oauthTestServer is a deterministic stand-in for Google's auth endpoints.
type oauthTestServer struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenForms    []url.Values
	revokeForms   []url.Values
	tokenInfoAuth []url.Values

	tokenStatus   int
	tokenBody     string
	tokenInfoBody string
	channelsBody  string
	channelStatus int
}

func newOAuthTestServer(t *testing.T) *oauthTestServer {
	t.Helper()

	stub := &oauthTestServer{
		tokenStatus:   http.StatusOK,
		channelStatus: http.StatusOK,
		tokenBody: fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3599,"token_type":"Bearer","scope":%q}`,
			testAccessToken, testRefreshToken, string(ScopeYouTubeReadonly)+" "+string(ScopeYouTubeForceSSL)),
		tokenInfoBody: fmt.Sprintf(`{"azp":%q,"aud":%q,"scope":%q,"expires_in":"3599","exp":"1785340800"}`,
			testClientID, testClientID, string(ScopeYouTubeReadonly)+" "+string(ScopeYouTubeForceSSL)),
		channelsBody: `{"items":[{"id":"UCtest","snippet":{"title":"yc test channel","customUrl":"@yctest"}}]}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.mu.Lock()
		stub.tokenForms = append(stub.tokenForms, r.PostForm)
		status, body := stub.tokenStatus, stub.tokenBody
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.mu.Lock()
		stub.revokeForms = append(stub.revokeForms, r.PostForm)
		stub.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/tokeninfo", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.mu.Lock()
		stub.tokenInfoAuth = append(stub.tokenInfoAuth, r.PostForm)
		body := stub.tokenInfoBody
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if strings.TrimSpace(body) == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_token","error_description":"Invalid Value"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/channels", func(w http.ResponseWriter, _ *http.Request) {
		stub.mu.Lock()
		status, body := stub.channelStatus, stub.channelsBody
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *oauthTestServer) flow(t *testing.T, mutate func(*GoogleOAuthConfig)) *GoogleOAuthLoginFlow {
	t.Helper()

	cfg := GoogleOAuthConfig{
		ClientID:          testClientID,
		RedirectURI:       "http://127.0.0.1:65535/",
		Scopes:            LoginScopes(),
		TokenEndpoint:     s.server.URL + "/token",
		RevokeEndpoint:    s.server.URL + "/revoke",
		TokenInfoEndpoint: s.server.URL + "/tokeninfo",
		ChannelsEndpoint:  s.server.URL + "/channels",
		HTTPClient:        s.server.Client(),
		Now:               func() time.Time { return testNow },
		NewState:          func() (Secret, error) { return NewSecret(testState), nil },
		NewCodeVerifier:   func() (Secret, error) { return NewSecret(testVerifier), nil },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	flow := NewGoogleOAuthLoginFlow(cfg)
	t.Cleanup(func() { _ = flow.Close() })
	return flow
}

func (s *oauthTestServer) lastTokenForm(t *testing.T) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tokenForms) == 0 {
		t.Fatal("no token request was made")
	}
	return s.tokenForms[len(s.tokenForms)-1]
}

func (s *oauthTestServer) tokenRequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokenForms)
}

func TestBeginLoginBuildsAuthorizationURL(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{ForceConsent: true, LoginHint: "user@example.test"})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	parsed, err := url.Parse(challenge.AuthorizationURL.Reveal())
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := parsed.Query()

	want := map[string]string{
		"response_type":         "code",
		"client_id":             testClientID,
		"redirect_uri":          "http://127.0.0.1:65535/",
		"state":                 testState,
		"code_challenge":        CodeChallenge(NewSecret(testVerifier)),
		"code_challenge_method": "S256",
		"prompt":                "consent",
		"login_hint":            "user@example.test",
		"scope":                 string(ScopeYouTubeReadonly) + " " + string(ScopeYouTubeForceSSL),
	}
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Fatalf("authorization URL %s = %q, want %q", key, got, value)
		}
	}
	// access_type belongs to the web-server flow and must not be sent.
	if query.Has("access_type") {
		t.Fatal("the installed-app flow must not send access_type")
	}
	if challenge.RedirectURI != "http://127.0.0.1:65535/" {
		t.Fatalf("unexpected redirect URI %q", challenge.RedirectURI)
	}
	if !challenge.ExpiresAt.After(testNow) {
		t.Fatal("the challenge must carry a future expiry")
	}
}

// TestBeginLoginDoesNotLeakURL asserts the authorization URL only escapes
// through the deliberate reveal path.
func TestBeginLoginDoesNotLeakURL(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if rendered := fmt.Sprintf(format, challenge); strings.Contains(rendered, testState) {
			t.Fatalf("challenge formatted with %s leaked state: %s", format, rendered)
		}
	}
}

func TestCompleteLoginExchangesCodeWithVerifier(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	result, err := flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         challenge.State,
		ExpectedState: challenge.State,
	})
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}

	form := stub.lastTokenForm(t)
	if got := form.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type = %q", got)
	}
	if got := form.Get("code_verifier"); got != testVerifier {
		t.Fatalf("code_verifier = %q, want the verifier BeginLogin generated", got)
	}
	if !VerifyCodeChallenge(NewSecret(form.Get("code_verifier")), CodeChallenge(NewSecret(testVerifier))) {
		t.Fatal("the exchanged verifier does not match the advertised challenge")
	}
	// An installed app has no secret to send unless one is configured.
	if form.Has("client_secret") {
		t.Fatal("client_secret must be omitted when none is configured")
	}

	if result.Tokens.AccessToken.Reveal() != testAccessToken {
		t.Fatal("access token was not carried through")
	}
	if result.Tokens.RefreshToken.Reveal() != testRefreshToken {
		t.Fatal("refresh token was not carried through")
	}
	if want := testNow.Add(3599 * time.Second); !result.Tokens.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", result.Tokens.ExpiresAt, want)
	}
	if result.Identity.ChannelID != "UCtest" || result.Identity.Handle != "@yctest" {
		t.Fatalf("identity was not resolved: %+v", result.Identity)
	}
	if len(result.MissingRequiredScopes()) != 0 {
		t.Fatalf("unexpected missing scopes: %v", result.MissingRequiredScopes())
	}
}

func TestCompleteLoginSendsConfiguredClientSecret(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, func(cfg *GoogleOAuthConfig) {
		cfg.ClientSecret = NewSecret(FakeTokenMarker + "-client-secret")
	})

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, err := flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         challenge.State,
		ExpectedState: challenge.State,
	}); err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if got := stub.lastTokenForm(t).Get("client_secret"); got != FakeTokenMarker+"-client-secret" {
		t.Fatalf("client_secret = %q", got)
	}
}

func TestCompleteLoginRejectsStateMismatch(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	if _, err := flow.BeginLogin(context.Background(), LoginRequest{}); err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err := flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         NewSecret("attacker-supplied-state"),
		ExpectedState: NewSecret(testState),
	})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("expected ErrStateMismatch, got %v", err)
	}
	if stub.tokenRequestCount() != 0 {
		t.Fatal("a state mismatch must not reach the token endpoint")
	}
	assertNoSecrets(t, err)
}

// A callback this flow never issued must not complete a login. Accepting one
// is login CSRF: an attacker who gets their own authorization code delivered
// to the victim's callback would sign the victim into the attacker's account,
// where the victim's next actions are visible to them.
func TestCompleteLoginRejectsAnUnknownStateByDefault(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	// No BeginLogin: nothing is pending, so the state below was never issued.
	_, err := flow.CompleteLogin(context.Background(), LoginCallback{
		Code:  NewSecret(testCode),
		State: NewSecret("attacker-supplied-state"),
	})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("expected ErrStateMismatch, got %v", err)
	}
	if stub.tokenRequestCount() != 0 {
		t.Fatal("an unknown state must not reach the token endpoint")
	}
	assertNoSecrets(t, err)
}

// The resumed / externally-driven login still works, but only for a caller
// that opted in and supplied the PKCE verifier itself.
func TestCompleteLoginAllowsAnUnknownStateWhenExplicitlyEnabled(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, func(cfg *GoogleOAuthConfig) {
		cfg.AllowExternalCallbacks = true
	})

	if _, err := flow.CompleteLogin(context.Background(), LoginCallback{
		Code:         NewSecret(testCode),
		State:        NewSecret("externally-driven-state"),
		CodeVerifier: NewSecret(testVerifier),
	}); err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if stub.tokenRequestCount() != 1 {
		t.Fatalf("token requests = %d, want 1", stub.tokenRequestCount())
	}
}

// Google rotates the refresh token, so a caller that was still holding the old
// one when the rotation landed would spend a dead token and be told to log in
// again. The memo hands it the rotated set instead, without a second exchange.
func TestRefreshServesAStaleTokenHolderFromTheMemo(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	first, err := flow.Refresh(context.Background(), NewSecret(testRefreshToken))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// The in-flight call is gone by now; this caller arrives late with the
	// pre-rotation token.
	second, err := flow.Refresh(context.Background(), NewSecret(testRefreshToken))
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if second.AccessToken.Reveal() != first.AccessToken.Reveal() {
		t.Fatal("the late caller did not receive the rotated token set")
	}
	if stub.tokenRequestCount() != 1 {
		t.Fatalf("token requests = %d, want 1: the memo must not re-exchange", stub.tokenRequestCount())
	}
}

// The memo is keyed by digest so a raw refresh token never sits in an ordinary
// map key, where a debug dump of the flow would expose it.
func TestRefreshKeyDoesNotContainTheToken(t *testing.T) {
	key := refreshKey(NewSecret(testRefreshToken))
	if strings.Contains(key, testRefreshToken) {
		t.Fatalf("refresh key leaks the token: %q", key)
	}
	if key == "" {
		t.Fatal("refresh key is empty")
	}
}

func TestCompleteLoginReportsDenial(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	_, err := flow.CompleteLogin(context.Background(), LoginCallback{
		State:            NewSecret(testState),
		Error:            "access_denied",
		ErrorDescription: "The user denied the request",
	})
	if !errors.Is(err, ErrLoginDenied) {
		t.Fatalf("expected ErrLoginDenied, got %v", err)
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("the denial reason should be visible: %v", err)
	}
	if stub.tokenRequestCount() != 0 {
		t.Fatal("a denied callback must not reach the token endpoint")
	}
}

func TestCompleteLoginRejectsMissingScope(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.tokenBody = fmt.Sprintf(`{"access_token":%q,"expires_in":3599,"token_type":"Bearer","scope":%q}`,
		testAccessToken, string(ScopeYouTubeReadonly))
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         challenge.State,
		ExpectedState: challenge.State,
	})
	if !errors.Is(err, ErrMissingScope) {
		t.Fatalf("expected ErrMissingScope, got %v", err)
	}
	if !strings.Contains(err.Error(), string(ScopeYouTubeForceSSL)) {
		t.Fatalf("the missing scope should be named: %v", err)
	}
}

func TestCompleteLoginRedactsTokenEndpointFailure(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.tokenStatus = http.StatusBadRequest
	stub.tokenBody = fmt.Sprintf(`{"error":"invalid_request","error_description":"code %s was rejected"}`, testCode)
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         challenge.State,
		ExpectedState: challenge.State,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoSecrets(t, err)
}

func TestRefreshKeepsExistingRefreshToken(t *testing.T) {
	stub := newOAuthTestServer(t)
	// Google omits refresh_token on a refresh response.
	stub.tokenBody = fmt.Sprintf(`{"access_token":%q,"expires_in":3599,"token_type":"Bearer"}`, testAccessToken+"-2")
	flow := stub.flow(t, nil)

	tokens, err := flow.Refresh(context.Background(), NewSecret(testRefreshToken))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tokens.AccessToken.Reveal() != testAccessToken+"-2" {
		t.Fatal("the rotated access token was not returned")
	}
	if tokens.RefreshToken.Reveal() != testRefreshToken {
		t.Fatal("an absent refresh token means keep the one you have")
	}
	form := stub.lastTokenForm(t)
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != testRefreshToken {
		t.Fatalf("unexpected refresh form: %v", form)
	}
}

func TestRefreshInvalidGrantIsTerminal(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.tokenStatus = http.StatusBadRequest
	stub.tokenBody = `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
	flow := stub.flow(t, nil)

	_, err := flow.Refresh(context.Background(), NewSecret(testRefreshToken))
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expected ErrLoginRequired, got %v", err)
	}
	assertNoSecrets(t, err)
}

func TestRefreshWithoutTokenIsTerminal(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	if _, err := flow.Refresh(context.Background(), Secret("")); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expected ErrLoginRequired, got %v", err)
	}
	if stub.tokenRequestCount() != 0 {
		t.Fatal("an absent refresh token must not reach the network")
	}
}

// TestRefreshIsSingleFlight is the concurrency invariant: N simultaneous 401s
// must cause one exchange, not N, or the refresh token races itself into
// revocation.
func TestRefreshIsSingleFlight(t *testing.T) {
	stub := newOAuthTestServer(t)

	release := make(chan struct{})
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		stub.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(gate.Close)

	flow := stub.flow(t, func(cfg *GoogleOAuthConfig) {
		cfg.TokenEndpoint = gate.URL + "/token"
		cfg.HTTPClient = gate.Client()
	})

	const callers = 8
	var wg sync.WaitGroup
	results := make([]TokenSet, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = flow.Refresh(context.Background(), NewSecret(testRefreshToken))
		}()
	}

	// Give every goroutine a chance to reach the shared call before the
	// single exchange is allowed to complete.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if results[i].AccessToken.Reveal() != testAccessToken {
			t.Fatalf("caller %d did not receive the shared token", i)
		}
	}
	if got := stub.tokenRequestCount(); got != 1 {
		t.Fatalf("refresh made %d token requests, want exactly 1", got)
	}
}

func TestTokenInfoReportsScopesWithoutReturningTheToken(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	tokens, err := flow.TokenInfo(context.Background(), NewSecret(testAccessToken))
	if err != nil {
		t.Fatalf("TokenInfo: %v", err)
	}
	if tokens.AccessToken.Present() {
		t.Fatal("TokenInfo must never hand a credential back")
	}
	if len(MissingScopes(tokens.Scopes, LoginScopes())) != 0 {
		t.Fatalf("granted scopes were not reported: %v", tokens.Scopes)
	}
	if want := testNow.Add(3599 * time.Second); !tokens.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", tokens.ExpiresAt, want)
	}

	stub.mu.Lock()
	forms := stub.tokenInfoAuth
	stub.mu.Unlock()
	if len(forms) != 1 || forms[0].Get("access_token") != testAccessToken {
		t.Fatal("the token must be sent in the POST body, not the query string")
	}
}

func TestTokenInfoRejectsForeignClient(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.tokenInfoBody = `{"azp":"someone-else.apps.googleusercontent.com","aud":"someone-else.apps.googleusercontent.com","scope":"","expires_in":"3599"}`
	flow := stub.flow(t, nil)

	_, err := flow.TokenInfo(context.Background(), NewSecret(testAccessToken))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestTokenInfoRejectsInvalidToken(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.tokenInfoBody = ""
	flow := stub.flow(t, nil)

	_, err := flow.TokenInfo(context.Background(), NewSecret(testAccessToken))
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	assertNoSecrets(t, err)
}

func TestRevokePostsTheToken(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	if err := flow.Revoke(context.Background(), NewSecret(testAccessToken)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	stub.mu.Lock()
	forms := stub.revokeForms
	stub.mu.Unlock()
	if len(forms) != 1 || forms[0].Get("token") != testAccessToken {
		t.Fatalf("unexpected revoke forms: %v", forms)
	}

	// Revoking nothing is a no-op, not a network call.
	if err := flow.Revoke(context.Background(), Secret("")); err != nil {
		t.Fatalf("Revoke of an absent token: %v", err)
	}
	stub.mu.Lock()
	count := len(stub.revokeForms)
	stub.mu.Unlock()
	if count != 1 {
		t.Fatal("an absent token must not reach the revoke endpoint")
	}
}

func TestCompleteLoginSurfacesRejectedTokenFromIdentityLookup(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.channelStatus = http.StatusUnauthorized
	stub.channelsBody = `{"error":{"code":401,"message":"Invalid Credentials"}}`
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	_, err = flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         challenge.State,
		ExpectedState: challenge.State,
	})
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	assertNoSecrets(t, err)
}

// TestCompleteLoginToleratesChannellessAccount covers a Google account with no
// YouTube channel: reading chat still works, so login must not fail.
func TestCompleteLoginToleratesChannellessAccount(t *testing.T) {
	stub := newOAuthTestServer(t)
	stub.channelsBody = `{"items":[]}`
	flow := stub.flow(t, nil)

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	result, err := flow.CompleteLogin(context.Background(), LoginCallback{
		Code:          NewSecret(testCode),
		State:         challenge.State,
		ExpectedState: challenge.State,
	})
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if result.Identity != (Identity{}) {
		t.Fatalf("expected an empty identity, got %+v", result.Identity)
	}
}

func TestCodeVerifierAndChallenge(t *testing.T) {
	verifier, err := NewCodeVerifier()
	if err != nil {
		t.Fatalf("NewCodeVerifier: %v", err)
	}
	raw := verifier.Reveal()
	if len(raw) != MaxCodeVerifierLength {
		t.Fatalf("verifier length = %d, want %d", len(raw), MaxCodeVerifierLength)
	}
	if err := ValidateCodeVerifier(verifier); err != nil {
		t.Fatalf("generated verifier failed validation: %v", err)
	}
	for _, r := range raw {
		if !strings.ContainsRune(codeVerifierAlphabet, r) {
			t.Fatalf("verifier contains a reserved character %q", r)
		}
	}

	// RFC 7636 A.1: the documented verifier/challenge pair.
	const rfcVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const rfcChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := CodeChallenge(NewSecret(rfcVerifier)); got != rfcChallenge {
		t.Fatalf("CodeChallenge = %q, want %q", got, rfcChallenge)
	}
	if !VerifyCodeChallenge(NewSecret(rfcVerifier), rfcChallenge) {
		t.Fatal("VerifyCodeChallenge rejected a matching pair")
	}
	if VerifyCodeChallenge(NewSecret(rfcVerifier), "not-the-challenge") {
		t.Fatal("VerifyCodeChallenge accepted a mismatched pair")
	}

	another, err := NewCodeVerifier()
	if err != nil {
		t.Fatalf("NewCodeVerifier: %v", err)
	}
	if another.Reveal() == raw {
		t.Fatal("two verifiers must not be identical")
	}
}

func TestValidateCodeVerifierRejectsBadInput(t *testing.T) {
	tests := map[string]Secret{
		"too short":           NewSecret(strings.Repeat("a", MinCodeVerifierLength-1)),
		"too long":            NewSecret(strings.Repeat("a", MaxCodeVerifierLength+1)),
		"reserved characters": NewSecret(strings.Repeat("a", MinCodeVerifierLength-1) + "/"),
		"empty":               Secret(""),
		"whitespace embedded": NewSecret(strings.Repeat("a", MinCodeVerifierLength-1) + " "),
	}
	for name, verifier := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateCodeVerifier(verifier)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if strings.Contains(err.Error(), verifier.Reveal()) && verifier.Present() {
				t.Fatalf("the verifier leaked into the error: %v", err)
			}
		})
	}
}

func TestNewStateIsUnguessableAndUnique(t *testing.T) {
	first, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	second, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if first.Reveal() == second.Reveal() {
		t.Fatal("two states must not be identical")
	}
	if len(first.Reveal()) < 32 {
		t.Fatalf("state is too short to be unguessable: %d characters", len(first.Reveal()))
	}
}

func TestBeginLoginRequiresClientID(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, func(cfg *GoogleOAuthConfig) { cfg.ClientID = "" })

	_, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err == nil {
		t.Fatal("expected an error for a missing client ID")
	}
	if !strings.Contains(err.Error(), "client ID") {
		t.Fatalf("the error should name the missing value: %v", err)
	}
}

func TestBeginLoginRejectsDuplicateState(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	if _, err := flow.BeginLogin(context.Background(), LoginRequest{}); err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, err := flow.BeginLogin(context.Background(), LoginRequest{}); err == nil {
		t.Fatal("a second attempt with the same state must be rejected")
	}
}

func TestBeginLoginHonorsCanceledContext(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := flow.BeginLogin(ctx, LoginRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, err := flow.CompleteLogin(ctx, LoginCallback{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// assertNoSecrets fails when an error message carries any test credential.
func assertNoSecrets(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		rendered := fmt.Sprintf(format, err)
		for _, secret := range []string{testAccessToken, testRefreshToken, testCode, testVerifier, testState, FakeTokenMarker} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("error formatted with %s leaked %q: %s", format, secret, rendered)
			}
		}
	}
}
