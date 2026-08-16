package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Google installed-app OAuth endpoints.
//
//nolint:gosec // gosec's G101 pattern-matches "oauth2"/"token" in the URLs; these are public endpoint addresses, not credentials
const (
	GoogleAuthEndpoint      = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenEndpoint     = "https://oauth2.googleapis.com/token"
	GoogleRevokeEndpoint    = "https://oauth2.googleapis.com/revoke"
	GoogleTokenInfoEndpoint = "https://oauth2.googleapis.com/tokeninfo"
)

// GoogleChannelsEndpoint resolves the authorized channel after a login.
//
// internal/auth cannot import internal/youtube - the dependency runs the other
// way - so the one identity call login needs is made here, against a single
// documented URL, rather than by taking on the whole transport package.
const GoogleChannelsEndpoint = "https://www.googleapis.com/youtube/v3/channels"

// DefaultExpirySkew is how far before a token's stated expiry it is treated as
// expired. Refreshing proactively inside this window is what keeps a 401 from
// being the trigger for refresh.
const DefaultExpirySkew = 60 * time.Second

// PKCE verifier bounds, per RFC 7636: 43-128 characters drawn from
// [A-Za-z0-9-._~].
const (
	MinCodeVerifierLength = 43
	MaxCodeVerifierLength = 128
)

const (
	// defaultOAuthTimeout bounds every token, revoke, and tokeninfo call.
	defaultOAuthTimeout = 15 * time.Second
	// defaultLoginTTL is how long a pending authorization attempt stays
	// valid before its state is discarded.
	defaultLoginTTL = 10 * time.Minute
	// maxOAuthResponseBytes bounds every response yc decodes, so a hostile
	// or broken endpoint cannot make yc allocate without limit.
	maxOAuthResponseBytes = 1 << 16
)

// codeVerifierAlphabet is RFC 7636's unreserved set. Drawing from it directly
// avoids base64 padding questions and keeps the verifier URL-safe by
// construction.
const codeVerifierAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// GoogleOAuthConfig configures the installed-app login flow.
//
// Endpoint and HTTPClient are injectable so tests can drive the whole flow
// against a deterministic in-process server with no network access.
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret Secret
	// RedirectURI overrides the loopback listener. Empty means "bind
	// 127.0.0.1 on an ephemeral port", which is the recommended desktop
	// mechanism; the loopback deprecation applies to mobile apps only.
	RedirectURI string
	Scopes      []Scope

	AuthEndpoint      string
	TokenEndpoint     string
	RevokeEndpoint    string
	TokenInfoEndpoint string
	// ChannelsEndpoint backs identity resolution after a completed login.
	ChannelsEndpoint string

	HTTPClient *http.Client
	Timeout    time.Duration
	// Now is injectable for deterministic expiry tests.
	Now func() time.Time

	// LoopbackAddress overrides the loopback bind address, and
	// CallbackTimeout bounds how long BeginLogin's listener waits.
	LoopbackAddress string
	CallbackTimeout time.Duration
	// LoginTTL bounds how long a pending attempt's state stays valid.
	LoginTTL time.Duration

	// NewCodeVerifier and NewState are injectable so tests can pin the PKCE
	// material without reading it back out of a Secret.
	NewCodeVerifier func() (Secret, error)
	NewState        func() (Secret, error)

	// ResolveIdentity overrides the built-in channels.list?mine=true lookup.
	// Tests inject a stub; production leaves it nil.
	ResolveIdentity func(ctx context.Context, accessToken Secret) (Identity, error)

	// AllowExternalCallbacks lets CompleteLogin accept a callback whose state
	// this flow instance never issued, provided the caller supplies the PKCE
	// verifier itself. That is how a resumed or externally-driven login works,
	// but it also disables the pending-attempt half of the CSRF check, so it
	// is off by default: an unknown or expired state is then rejected with
	// ErrStateMismatch instead of being quietly completed.
	AllowExternalCallbacks bool
}

// ErrUnexpectedRedirect reports that a credential-bearing request was answered
// with a redirect. It is a sentinel so a misconfigured proxy reads as itself
// rather than as a rejected credential.
var ErrUnexpectedRedirect = errors.New("the authorization endpoint answered with a redirect")

// NewOAuthHTTPClient returns the HTTP client the installed-app flow must use.
//
// It refuses to follow redirects. Every call this package makes carries a
// credential in the request body - the authorization code and PKCE verifier on
// exchange, the refresh token on refresh and revoke, the access token on
// tokeninfo - and Go re-sends the body verbatim when it follows a 307 or 308.
// net/http strips the Authorization header on a cross-host redirect, but it has
// no such rule for a form body, so a Location pointing anywhere else would hand
// the refresh token and client secret to whatever host it named. There is no
// legitimate redirect on any Google OAuth endpoint, so the safe policy is to
// have none.
//
// It also replaces http.DefaultClient as the fallback: a package that posts
// secrets must not inherit the process-wide client's timeout, transport, or
// redirect policy from whatever else set them.
func NewOAuthHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultOAuthTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// GoogleOAuthLoginFlow implements LoginFlow and TokenRefresher against Google's
// installed-app endpoints.
//
// Token exchange and refresh are hand-rolled rather than delegated to
// golang.org/x/oauth2 because oauth2.Token exposes AccessToken and
// RefreshToken as plain string fields with live JSON tags: any %+v, any
// structured log field, any snapshot marshal would leak a live credential and
// break the Secret invariant the whole package exists to preserve.
type GoogleOAuthLoginFlow struct {
	cfg GoogleOAuthConfig

	mu      sync.Mutex
	pending map[string]*oauthLoginAttempt

	// refreshMu guards refreshCalls and refreshMemo. refreshCalls makes
	// Refresh single-flight: N concurrent 401s share one exchange with Google
	// instead of racing to rotate the same refresh token N times.
	// refreshMemo remembers recently rotated results a little longer, so a
	// caller that shows up with the pre-rotation token after the in-flight
	// call is gone still receives the rotated set instead of sending Google a
	// spent token and being forced back to an interactive login. Both maps
	// are keyed by refreshKey - a digest, never the raw token.
	refreshMu    sync.Mutex
	refreshCalls map[string]*refreshCall
	refreshMemo  map[string]refreshMemoEntry
}

// Bounds for the rotated-refresh memo. The TTL only needs to cover the window
// between a rotation and the last straggler retrying with the old token -
// seconds in practice - and the size cap keeps a pathological caller from
// growing the map without bound; the oldest entry is evicted first.
const (
	refreshMemoTTL     = 2 * time.Minute
	refreshMemoEntries = 8
)

// refreshMemoEntry is one remembered rotation, keyed by the old token's digest.
type refreshMemoEntry struct {
	tokens   TokenSet
	storedAt time.Time
}

var (
	_ LoginFlow      = (*GoogleOAuthLoginFlow)(nil)
	_ TokenRefresher = (*GoogleOAuthLoginFlow)(nil)
)

// oauthLoginAttempt is the server-side half of one authorization attempt. It
// never leaves the package, and its secrets are only revealed at the two HTTP
// boundaries that must send them.
type oauthLoginAttempt struct {
	clientID     string
	clientSecret Secret
	redirectURI  string
	scopes       []Scope
	state        Secret
	verifier     Secret
	challenge    string
	loginHint    string
	forceConsent bool
	expiresAt    time.Time
	// listener is non-nil only when this flow bound the loopback port, in
	// which case the flow also owns closing it.
	listener *LoopbackServer
}

// refreshCall is one in-flight refresh shared by every caller that asked for
// the same refresh token.
type refreshCall struct {
	done   chan struct{}
	tokens TokenSet
	err    error
}

// NewGoogleOAuthLoginFlow returns a login flow with defaults applied for any
// unset endpoint, HTTP client, timeout, or clock.
func NewGoogleOAuthLoginFlow(cfg GoogleOAuthConfig) *GoogleOAuthLoginFlow {
	cfg.AuthEndpoint = endpointOrDefault(cfg.AuthEndpoint, GoogleAuthEndpoint)
	cfg.TokenEndpoint = endpointOrDefault(cfg.TokenEndpoint, GoogleTokenEndpoint)
	cfg.RevokeEndpoint = endpointOrDefault(cfg.RevokeEndpoint, GoogleRevokeEndpoint)
	cfg.TokenInfoEndpoint = endpointOrDefault(cfg.TokenInfoEndpoint, GoogleTokenInfoEndpoint)
	cfg.ChannelsEndpoint = endpointOrDefault(cfg.ChannelsEndpoint, GoogleChannelsEndpoint)
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewOAuthHTTPClient(cfg.Timeout)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultOAuthTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.CallbackTimeout <= 0 {
		cfg.CallbackTimeout = DefaultCallbackTimeout
	}
	if cfg.LoginTTL <= 0 {
		cfg.LoginTTL = defaultLoginTTL
	}
	if cfg.NewCodeVerifier == nil {
		cfg.NewCodeVerifier = NewCodeVerifier
	}
	if cfg.NewState == nil {
		cfg.NewState = NewState
	}
	cfg.Scopes = cloneScopes(cfg.Scopes)

	return &GoogleOAuthLoginFlow{
		cfg:          cfg,
		pending:      make(map[string]*oauthLoginAttempt),
		refreshCalls: make(map[string]*refreshCall),
		refreshMemo:  make(map[string]refreshMemoEntry, refreshMemoEntries),
	}
}

// BeginLogin generates the PKCE verifier and challenge, generates state, binds
// the loopback listener, and builds the authorization URL.
//
// The returned URL is a Secret: it carries state and the challenge, and must
// never be logged or error-wrapped.
func (f *GoogleOAuthLoginFlow) BeginLogin(ctx context.Context, request LoginRequest) (LoginChallenge, error) {
	if err := ctx.Err(); err != nil {
		return LoginChallenge{}, safeError("start Google OAuth login", err, request.Redactor())
	}

	attempt, err := f.newAttempt(request)
	if err != nil {
		return LoginChallenge{}, err
	}

	authorizationURL, err := f.authorizationURL(attempt)
	if err != nil {
		attempt.close()
		return LoginChallenge{}, safeError("build Google OAuth authorization URL", err, request.Redactor())
	}
	if err := f.storeAttempt(attempt); err != nil {
		attempt.close()
		return LoginChallenge{}, err
	}

	return LoginChallenge{
		AuthorizationURL: NewSecret(authorizationURL),
		State:            attempt.state,
		Scopes:           cloneScopes(attempt.scopes),
		RedirectURI:      attempt.redirectURI,
		ExpiresAt:        attempt.expiresAt,
	}, nil
}

// AwaitCallback blocks on the loopback listener BeginLogin bound for challenge
// and returns a callback ready to hand to CompleteLogin.
//
// It exists so LoginFlow stays a two-method interface: the listener is an
// implementation detail of this flow, not part of the boundary every caller has
// to model. A challenge whose redirect URI the caller supplied has no listener,
// and the caller is then responsible for delivering the callback itself.
func (f *GoogleOAuthLoginFlow) AwaitCallback(ctx context.Context, challenge LoginChallenge) (LoginCallback, error) {
	state := strings.TrimSpace(challenge.State.Reveal())
	f.mu.Lock()
	attempt, ok := f.pending[state]
	f.mu.Unlock()
	if !ok {
		return LoginCallback{}, safeErrorf("wait for OAuth callback",
			"this login attempt is unknown or has expired; run `yc login` again",
			Redactor{}, ErrLoginRequired)
	}
	if attempt.listener == nil {
		return LoginCallback{}, safeErrorf("wait for OAuth callback",
			"this login uses a caller-supplied redirect URI, so yc is not listening for the callback",
			Redactor{})
	}

	callback, err := attempt.listener.Wait(ctx)
	if err != nil {
		return LoginCallback{}, err
	}
	callback.ExpectedState = attempt.state
	callback.CodeVerifier = attempt.verifier
	callback.RedirectURI = attempt.redirectURI
	return callback, nil
}

// CompleteLogin validates the callback state, exchanges the authorization code
// with the stored verifier, validates the resulting token, and resolves the
// user's channel identity.
func (f *GoogleOAuthLoginFlow) CompleteLogin(ctx context.Context, callback LoginCallback) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, safeError("complete Google OAuth login", err, callback.Redactor())
	}

	state, err := validateCallbackState(callback)
	if err != nil {
		return LoginResult{}, err
	}
	// A denial is reported before the attempt lookup: it carries no
	// authorization code, so nothing is exchanged either way, and the user
	// deserves the real reason rather than a state error.
	if callback.Denied() {
		return LoginResult{}, deniedError(callback, callback.Redactor())
	}
	attempt, ok := f.consumeAttempt(state, callback)
	if !ok {
		// The pending-attempt lookup is the second half of the CSRF check:
		// completing a callback this flow never began would let an attacker
		// log the victim into the attacker's account (login CSRF). Only the
		// explicit AllowExternalCallbacks opt-in relaxes it.
		return LoginResult{}, safeErrorf("complete Google OAuth login",
			"this login attempt is unknown or has expired; run `yc login` again",
			callback.Redactor(), ErrStateMismatch)
	}
	defer attempt.close()

	redactor := NewRedactor(attempt.clientSecret, attempt.state, attempt.verifier, callback.Code, callback.State)

	if strings.TrimSpace(callback.Code.Reveal()) == "" {
		return LoginResult{}, safeErrorf("complete Google OAuth login",
			"the callback did not include an authorization code; run `yc login` again",
			redactor, ErrLoginDenied)
	}
	if !attempt.verifier.Present() {
		return LoginResult{}, safeErrorf("complete Google OAuth login",
			"the PKCE verifier for this login is unknown; run `yc login` again",
			redactor, ErrLoginRequired)
	}

	httpCtx, cancel := f.httpContext(ctx)
	defer cancel()

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", attempt.clientID)
	form.Set("code", callback.Code.Reveal())
	form.Set("code_verifier", attempt.verifier.Reveal())
	form.Set("redirect_uri", attempt.redirectURI)
	// An installed app "cannot keep secrets", so client_secret is optional.
	// Send it only when Google issued one for this client type.
	if attempt.clientSecret.Present() {
		form.Set("client_secret", attempt.clientSecret.Reveal())
	}

	tokens, err := f.postToken(httpCtx, "exchange Google OAuth code", form, redactor)
	if err != nil {
		return LoginResult{}, err
	}

	scopes := tokens.Scopes
	if len(scopes) == 0 {
		scopes = cloneScopes(attempt.scopes)
	}
	if missing := MissingScopes(scopes, attempt.scopes); len(missing) > 0 {
		return LoginResult{}, missingScopeError("the Google OAuth token", missing, attempt.scopes)
	}
	tokens.Scopes = cloneScopes(scopes)

	identity, err := f.resolveIdentity(httpCtx, tokens.AccessToken)
	if err != nil {
		return LoginResult{}, err
	}

	return LoginResult{
		Identity: identity,
		Tokens:   tokens,
		Scopes:   cloneScopes(scopes),
	}, nil
}

// Refresh exchanges a refresh token for a fresh access token.
//
// It is single-flight under a mutex so concurrent callers share one in-flight
// request, and an invalid_grant response is terminal: it means the refresh
// token was revoked or expired and only a new interactive login can recover.
func (f *GoogleOAuthLoginFlow) Refresh(ctx context.Context, refreshToken Secret) (TokenSet, error) {
	if err := ctx.Err(); err != nil {
		return TokenSet{}, safeError("refresh Google OAuth token", err, NewRedactor(refreshToken))
	}
	if !refreshToken.Present() {
		return TokenSet{}, safeErrorf("refresh Google OAuth token",
			"no refresh token is stored; run `yc login`",
			Redactor{}, ErrLoginRequired)
	}

	key := refreshKey(refreshToken)

	f.refreshMu.Lock()
	// A caller holding the pre-rotation token after the in-flight call has
	// already finished is served from the memo. Without it that caller would
	// send Google a token the rotation just spent, earn invalid_grant, and be
	// pushed into an interactive login even though a valid set exists.
	if tokens, ok := f.lookupRefreshMemo(key); ok {
		f.refreshMu.Unlock()
		return tokens, nil
	}
	if call, ok := f.refreshCalls[key]; ok {
		f.refreshMu.Unlock()
		select {
		case <-call.done:
			return call.tokens, call.err
		case <-ctx.Done():
			return TokenSet{}, safeError("refresh Google OAuth token", ctx.Err(), NewRedactor(refreshToken))
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	f.refreshCalls[key] = call
	f.refreshMu.Unlock()

	call.tokens, call.err = f.doRefresh(ctx, refreshToken)
	close(call.done)

	f.refreshMu.Lock()
	delete(f.refreshCalls, key)
	if call.err == nil {
		f.rememberRefresh(key, call.tokens)
	}
	f.refreshMu.Unlock()

	return call.tokens, call.err
}

// refreshKey digests a refresh token into a map key.
//
// The rest of this package keeps token material inside Secret so a stray %+v
// cannot print it; using the raw token as a map key would undo that for the
// one value most worth protecting, so callers index by digest instead.
func refreshKey(refreshToken Secret) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(refreshToken.Reveal())))
	return hex.EncodeToString(sum[:])
}

// lookupRefreshMemo returns a remembered rotation for key if one is still
// within its TTL. The caller must hold refreshMu.
func (f *GoogleOAuthLoginFlow) lookupRefreshMemo(key string) (TokenSet, bool) {
	entry, ok := f.refreshMemo[key]
	if !ok {
		return TokenSet{}, false
	}
	if f.cfg.Now().Sub(entry.storedAt) > refreshMemoTTL {
		delete(f.refreshMemo, key)
		return TokenSet{}, false
	}
	return entry.tokens, true
}

// rememberRefresh records a successful rotation against the old token's digest.
// The caller must hold refreshMu.
func (f *GoogleOAuthLoginFlow) rememberRefresh(key string, tokens TokenSet) {
	if f.refreshMemo == nil {
		f.refreshMemo = make(map[string]refreshMemoEntry, refreshMemoEntries)
	}
	now := f.cfg.Now()
	// Expired entries are swept on every write, so the size cap below is a
	// backstop for a burst rather than the usual way entries leave.
	for existing, entry := range f.refreshMemo {
		if now.Sub(entry.storedAt) > refreshMemoTTL {
			delete(f.refreshMemo, existing)
		}
	}
	for len(f.refreshMemo) >= refreshMemoEntries {
		oldest, found := "", time.Time{}
		for existing, entry := range f.refreshMemo {
			if !found.IsZero() && !entry.storedAt.Before(found) {
				continue
			}
			oldest, found = existing, entry.storedAt
		}
		if oldest == "" {
			break
		}
		delete(f.refreshMemo, oldest)
	}
	f.refreshMemo[key] = refreshMemoEntry{tokens: tokens, storedAt: now}
}

// doRefresh performs the single exchange behind Refresh.
func (f *GoogleOAuthLoginFlow) doRefresh(ctx context.Context, refreshToken Secret) (TokenSet, error) {
	redactor := NewRedactor(refreshToken, f.cfg.ClientSecret)

	httpCtx, cancel := f.httpContext(ctx)
	defer cancel()

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", f.cfg.ClientID)
	form.Set("refresh_token", refreshToken.Reveal())
	if f.cfg.ClientSecret.Present() {
		form.Set("client_secret", f.cfg.ClientSecret.Reveal())
	}

	tokens, err := f.postToken(httpCtx, "refresh Google OAuth token", form, redactor)
	if err != nil {
		return TokenSet{}, err
	}
	// Google omits refresh_token on a refresh response: an absent value
	// means "keep the one you have", not "you lost it".
	if !tokens.RefreshToken.Present() {
		tokens.RefreshToken = refreshToken
	}
	if len(tokens.Scopes) == 0 {
		tokens.Scopes = cloneScopes(f.cfg.Scopes)
	}
	return tokens, nil
}

// Revoke invalidates a token at Google.
func (f *GoogleOAuthLoginFlow) Revoke(ctx context.Context, token Secret) error {
	if err := ctx.Err(); err != nil {
		return safeError("revoke Google OAuth token", ctx.Err(), NewRedactor(token))
	}
	if !token.Present() {
		return nil
	}
	redactor := NewRedactor(token)

	httpCtx, cancel := f.httpContext(ctx)
	defer cancel()

	form := url.Values{}
	form.Set("token", token.Reveal())

	resp, err := f.postForm(httpCtx, f.cfg.RevokeEndpoint, form, redactor, "revoke Google OAuth token")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, readErr := responseDetail(resp.Body)
		if readErr != nil {
			return safeError("read Google OAuth revoke response", readErr, redactor)
		}
		return statusError("revoke Google OAuth token", resp.StatusCode, detail, redactor)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxOAuthResponseBytes))
	return nil
}

// TokenInfo validates an access token and reports its granted scopes and
// expiry, without ever returning the token itself.
func (f *GoogleOAuthLoginFlow) TokenInfo(ctx context.Context, accessToken Secret) (TokenSet, error) {
	if err := ctx.Err(); err != nil {
		return TokenSet{}, safeError("inspect Google OAuth token", ctx.Err(), NewRedactor(accessToken))
	}
	if !accessToken.Present() {
		return TokenSet{}, safeErrorf("inspect Google OAuth token",
			"no access token is configured",
			Redactor{}, ErrInvalidToken)
	}
	redactor := NewRedactor(accessToken)

	httpCtx, cancel := f.httpContext(ctx)
	defer cancel()

	// The token goes in the POST body rather than the query string so it
	// cannot reach a proxy log or a redirect's Referer header.
	form := url.Values{}
	form.Set("access_token", accessToken.Reveal())

	resp, err := f.postForm(httpCtx, f.cfg.TokenInfoEndpoint, form, redactor, "inspect Google OAuth token")
	if err != nil {
		return TokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, readErr := responseDetail(resp.Body)
		if readErr != nil {
			return TokenSet{}, safeError("read Google tokeninfo response", readErr, redactor)
		}
		return TokenSet{}, statusError("inspect Google OAuth token", resp.StatusCode, detail, redactor, ErrInvalidToken)
	}

	var decoded tokenInfoResponse
	if err := decodeJSON(resp.Body, &decoded); err != nil {
		return TokenSet{}, safeError("decode Google tokeninfo response", err, redactor)
	}

	// A token minted for a different OAuth client is not usable here, and
	// silently accepting it produces a confusing 403 later instead of a
	// clear message now.
	if f.cfg.ClientID != "" {
		audience := firstNonEmpty(decoded.AuthorizedParty, decoded.Audience)
		if audience != "" && audience != strings.TrimSpace(f.cfg.ClientID) {
			return TokenSet{}, safeErrorf("inspect Google OAuth token",
				"the stored token belongs to a different Google OAuth client; run `yc login` again",
				redactor, ErrInvalidToken)
		}
	}

	expiresIn := decoded.expiresInSeconds(f.cfg.Now())
	if expiresIn <= 0 {
		return TokenSet{}, safeErrorf("inspect Google OAuth token",
			"Google reports the access token as expired",
			redactor, ErrInvalidToken)
	}

	// The access token is deliberately absent from the result: TokenInfo
	// reports about a credential, it does not hand one back.
	return TokenSet{
		TokenType: "Bearer",
		ExpiresAt: f.cfg.Now().Add(time.Duration(expiresIn) * time.Second),
		Scopes:    Scopes(decoded.Scope),
	}, nil
}

// newAttempt validates the request, generates the PKCE and state material, and
// binds the loopback listener when the caller did not supply a redirect URI.
func (f *GoogleOAuthLoginFlow) newAttempt(request LoginRequest) (*oauthLoginAttempt, error) {
	clientID := firstNonEmpty(request.ClientID, f.cfg.ClientID)
	if clientID == "" {
		return nil, errors.New("start Google OAuth login: missing Google OAuth client ID; set google_client_id or run `yc setup`")
	}
	clientSecret := request.ClientSecret
	if !clientSecret.Present() {
		clientSecret = f.cfg.ClientSecret
	}

	scopes := request.Scopes
	if len(scopes) == 0 {
		scopes = f.cfg.Scopes
	}
	if len(scopes) == 0 {
		scopes = LoginScopes()
	}
	scopes = cloneScopes(scopes)

	state := request.State
	if !state.Present() {
		generated, err := f.cfg.NewState()
		if err != nil {
			return nil, safeError("generate OAuth state", err, request.Redactor())
		}
		state = generated
	}
	if !state.Present() {
		return nil, errors.New("start Google OAuth login: the OAuth state generator returned an empty value")
	}

	verifier := request.CodeVerifier
	if !verifier.Present() {
		generated, err := f.cfg.NewCodeVerifier()
		if err != nil {
			return nil, safeError("generate PKCE verifier", err, request.Redactor())
		}
		verifier = generated
	}
	if err := ValidateCodeVerifier(verifier); err != nil {
		return nil, err
	}
	challenge := strings.TrimSpace(request.CodeChallenge)
	if challenge == "" {
		challenge = CodeChallenge(verifier)
	}

	attempt := &oauthLoginAttempt{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  firstNonEmpty(request.RedirectURI, f.cfg.RedirectURI),
		scopes:       scopes,
		state:        state,
		verifier:     verifier,
		challenge:    challenge,
		loginHint:    strings.TrimSpace(request.LoginHint),
		forceConsent: request.ForceConsent,
		expiresAt:    f.cfg.Now().Add(f.cfg.LoginTTL),
	}

	if attempt.redirectURI == "" {
		listener, err := NewLoopbackServer(LoopbackConfig{
			Address:       f.cfg.LoopbackAddress,
			ExpectedState: state,
			Timeout:       f.cfg.CallbackTimeout,
		})
		if err != nil {
			return nil, err
		}
		attempt.listener = listener
		attempt.redirectURI = listener.RedirectURI()
	}
	return attempt, nil
}

// authorizationURL builds the browser URL for an attempt.
func (f *GoogleOAuthLoginFlow) authorizationURL(attempt *oauthLoginAttempt) (string, error) {
	authorizationURL, err := url.Parse(f.cfg.AuthEndpoint)
	if err != nil {
		return "", err
	}
	if authorizationURL.Scheme == "" || authorizationURL.Host == "" {
		return "", errors.New("the authorization endpoint must be an absolute URL")
	}

	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", attempt.clientID)
	query.Set("redirect_uri", attempt.redirectURI)
	query.Set("scope", strings.Join(ScopeValues(attempt.scopes), " "))
	query.Set("state", attempt.state.Reveal())
	query.Set("code_challenge", attempt.challenge)
	query.Set("code_challenge_method", "S256")
	if attempt.loginHint != "" {
		query.Set("login_hint", attempt.loginHint)
	}
	if attempt.forceConsent {
		// prompt=consent is how the installed-app flow forces a refresh
		// token to be re-issued. access_type=offline belongs to the
		// web-server flow and is not part of this parameter set.
		query.Set("prompt", "consent")
	}
	authorizationURL.RawQuery = query.Encode()
	return authorizationURL.String(), nil
}

// storeAttempt records a pending attempt keyed by its state.
func (f *GoogleOAuthLoginFlow) storeAttempt(attempt *oauthLoginAttempt) error {
	key := strings.TrimSpace(attempt.state.Reveal())
	now := f.cfg.Now()

	f.mu.Lock()
	defer f.mu.Unlock()

	f.expireLocked(now)
	if _, ok := f.pending[key]; ok {
		return errors.New("start Google OAuth login: this OAuth state is already pending; run `yc login` again")
	}
	f.pending[key] = attempt
	return nil
}

// consumeAttempt removes and returns the pending attempt for state, reporting
// whether the callback may proceed.
//
// A callback whose state is unknown or expired is refused by default: the
// pending map is what proves this flow began the login, and fabricating an
// attempt in its place used to silently skip that half of the CSRF check.
// With AllowExternalCallbacks set, and only then, such a callback is still
// completable when the caller supplied the verifier itself - the resumed or
// externally-driven login - and the flow config supplies the client identity.
func (f *GoogleOAuthLoginFlow) consumeAttempt(state string, callback LoginCallback) (*oauthLoginAttempt, bool) {
	f.mu.Lock()
	attempt, ok := f.pending[state]
	if ok {
		delete(f.pending, state)
	}
	f.mu.Unlock()

	if ok && !f.cfg.Now().After(attempt.expiresAt) {
		return attempt, true
	}
	if ok {
		attempt.close()
	}
	if !f.cfg.AllowExternalCallbacks {
		return nil, false
	}
	return &oauthLoginAttempt{
		clientID:     f.cfg.ClientID,
		clientSecret: f.cfg.ClientSecret,
		redirectURI:  firstNonEmpty(callback.RedirectURI, f.cfg.RedirectURI),
		scopes:       cloneScopes(f.cfg.Scopes),
		state:        callback.State,
		verifier:     callback.CodeVerifier,
	}, true
}

// expireLocked drops attempts past their TTL. The caller holds f.mu.
func (f *GoogleOAuthLoginFlow) expireLocked(now time.Time) {
	for key, attempt := range f.pending {
		if now.After(attempt.expiresAt) {
			attempt.close()
			delete(f.pending, key)
		}
	}
}

// Close releases every listener still bound by a pending attempt. A CLI that
// abandons a login calls this so the process can exit cleanly.
func (f *GoogleOAuthLoginFlow) Close() error {
	f.mu.Lock()
	pending := f.pending
	f.pending = make(map[string]*oauthLoginAttempt)
	f.mu.Unlock()

	for _, attempt := range pending {
		attempt.close()
	}
	return nil
}

// close releases the attempt's loopback listener, if it owns one.
func (a *oauthLoginAttempt) close() {
	if a == nil || a.listener == nil {
		return
	}
	_ = a.listener.Close()
	a.listener = nil
}

// postToken performs a token-endpoint exchange and decodes the response.
func (f *GoogleOAuthLoginFlow) postToken(ctx context.Context, action string, form url.Values, redactor Redactor) (TokenSet, error) {
	resp, err := f.postForm(ctx, f.cfg.TokenEndpoint, form, redactor, action)
	if err != nil {
		return TokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes))
		if readErr != nil {
			return TokenSet{}, safeError("read Google OAuth token response", readErr, redactor)
		}
		var decoded oauthErrorResponse
		_ = json.Unmarshal(body, &decoded)
		// invalid_grant is terminal: the refresh token or authorization
		// code is revoked, expired, or already used, and no amount of
		// retrying changes that.
		if strings.EqualFold(strings.TrimSpace(decoded.Error), "invalid_grant") {
			return TokenSet{}, safeErrorf(action,
				"Google rejected the grant as expired or revoked; run `yc login` again",
				redactor, ErrLoginRequired)
		}
		return TokenSet{}, statusError(action, resp.StatusCode, decoded.detail(body), redactor)
	}

	var decoded oauthTokenResponse
	if err := decodeJSON(resp.Body, &decoded); err != nil {
		return TokenSet{}, safeError("decode Google OAuth token response", err, redactor)
	}

	accessToken := strings.TrimSpace(decoded.AccessToken)
	if accessToken == "" {
		return TokenSet{}, safeErrorf(action, "the token response did not include an access token", redactor)
	}

	tokens := TokenSet{
		AccessToken:  NewSecret(accessToken),
		RefreshToken: NewSecret(strings.TrimSpace(decoded.RefreshToken)),
		TokenType:    firstNonEmpty(decoded.TokenType, "Bearer"),
		Scopes:       Scopes(decoded.Scope),
	}
	if decoded.ExpiresIn > 0 {
		tokens.ExpiresAt = f.cfg.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second)
	}
	return tokens, nil
}

// postForm issues a form-encoded POST with the shared headers and error
// handling every Google auth endpoint needs.
func (f *GoogleOAuthLoginFlow) postForm(ctx context.Context, endpoint string, form url.Values, redactor Redactor, action string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, safeError("create "+action+" request", err, redactor)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, safeError(action, err, redactor)
	}
	// A redirect here means either a misconfigured proxy or an endpoint trying
	// to move a credential-bearing body somewhere else. Neither is a case to
	// follow, and NewOAuthHTTPClient already declines to; this turns the
	// declined redirect into an error the user can read. The Location header is
	// deliberately not quoted.
	if isRedirectStatus(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxOAuthResponseBytes))
		_ = resp.Body.Close()
		return nil, safeErrorf(action,
			"the endpoint answered with HTTP "+strconv.Itoa(resp.StatusCode)+
				"; yc will not send credentials to a redirected address",
			redactor, ErrUnexpectedRedirect)
	}
	return resp, nil
}

// isRedirectStatus reports the 3xx statuses that carry a Location.
func isRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// httpContext bounds one HTTP call.
func (f *GoogleOAuthLoginFlow) httpContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if f.cfg.Timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, f.cfg.Timeout)
}

// resolveIdentity looks up the authorized channel through
// channels.list?mine=true.
//
// A Google account with no YouTube channel is a normal case - read-only chat
// still works - so an empty result yields an empty identity rather than an
// error. A rejected token is not: that has to surface now instead of as a
// confusing 403 on the first poll.
func (f *GoogleOAuthLoginFlow) resolveIdentity(ctx context.Context, accessToken Secret) (Identity, error) {
	if f.cfg.ResolveIdentity != nil {
		return f.cfg.ResolveIdentity(ctx, accessToken)
	}

	redactor := NewRedactor(accessToken)
	endpoint, err := url.Parse(f.cfg.ChannelsEndpoint)
	if err != nil {
		return Identity{}, safeError("resolve YouTube channel", err, redactor)
	}
	query := endpoint.Query()
	query.Set("part", "snippet")
	query.Set("mine", "true")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Identity{}, safeError("create YouTube channel request", err, redactor)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken.Reveal())

	resp, err := f.cfg.HTTPClient.Do(req)
	if err != nil {
		return Identity{}, safeError("resolve YouTube channel", err, redactor)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		detail, readErr := responseDetail(resp.Body)
		if readErr != nil {
			return Identity{}, safeError("read YouTube channel response", readErr, redactor)
		}
		return Identity{}, statusError("resolve YouTube channel", resp.StatusCode, detail, redactor, ErrInvalidToken)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Any other failure is informational: the login itself succeeded
		// and the identity is only a cached convenience.
		return Identity{}, nil
	}

	var decoded channelsListResponse
	if err := decodeJSON(resp.Body, &decoded); err != nil {
		return Identity{}, safeError("decode YouTube channel response", err, redactor)
	}
	if len(decoded.Items) == 0 {
		return Identity{}, nil
	}
	item := decoded.Items[0]
	return Identity{
		ChannelID:   strings.TrimSpace(item.ID),
		DisplayName: strings.TrimSpace(item.Snippet.Title),
		Handle:      strings.TrimSpace(item.Snippet.CustomURL),
	}, nil
}

// NewCodeVerifier returns a fresh PKCE verifier of the maximum permitted
// length, drawn from the unreserved character set.
func NewCodeVerifier() (Secret, error) {
	value, err := randomString(MaxCodeVerifierLength, codeVerifierAlphabet)
	if err != nil {
		return "", err
	}
	return NewSecret(value), nil
}

// ValidateCodeVerifier reports whether a verifier meets RFC 7636's length and
// alphabet requirements. The verifier itself never appears in the error.
func ValidateCodeVerifier(verifier Secret) error {
	value := verifier.Reveal()
	if len(value) < MinCodeVerifierLength || len(value) > MaxCodeVerifierLength {
		return errors.New("invalid PKCE verifier: length must be between " +
			strconv.Itoa(MinCodeVerifierLength) + " and " + strconv.Itoa(MaxCodeVerifierLength) + " characters")
	}
	for i := 0; i < len(value); i++ {
		if !strings.ContainsRune(codeVerifierAlphabet, rune(value[i])) {
			return errors.New("invalid PKCE verifier: characters must be drawn from [A-Za-z0-9-._~]")
		}
	}
	return nil
}

// CodeChallenge returns the S256 challenge for a verifier: the base64url
// encoding, without padding, of the verifier's SHA-256 digest.
//
// The challenge is not itself a secret - it is one-way - but it identifies the
// login attempt, so it is still kept out of logs.
func CodeChallenge(verifier Secret) string {
	digest := sha256.Sum256([]byte(verifier.Reveal()))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// VerifyCodeChallenge reports whether challenge is the S256 challenge for
// verifier, comparing in constant time.
func VerifyCodeChallenge(verifier Secret, challenge string) bool {
	expected := CodeChallenge(verifier)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(challenge))) == 1
}

// NewState returns a fresh, unguessable OAuth state value.
func NewState() (Secret, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return NewSecret(base64.RawURLEncoding.EncodeToString(data[:])), nil
}

// randomString draws length characters uniformly from alphabet.
//
// It rejects the tail of the byte range that would bias the modulo, so the
// verifier keeps its full entropy rather than losing a fraction of a bit for
// convenience.
func randomString(length int, alphabet string) (string, error) {
	if length <= 0 || alphabet == "" {
		return "", errors.New("random string requires a positive length and a non-empty alphabet")
	}
	if len(alphabet) > 256 {
		return "", errors.New("random string alphabet is longer than a byte can index")
	}
	size := len(alphabet)
	// Rejection-sampling threshold, kept as an int: as a byte it would wrap
	// to zero whenever size divides 256 evenly and reject every draw.
	limit := 256 - (256 % size)

	out := make([]byte, 0, length)
	buf := make([]byte, length)
	for len(out) < length {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%size])
			if len(out) == length {
				break
			}
		}
	}
	return string(out), nil
}

// validateCallbackState enforces the CSRF check before anything is sent to
// Google.
func validateCallbackState(callback LoginCallback) (string, error) {
	state := strings.TrimSpace(callback.State.Reveal())
	if state == "" {
		return "", safeErrorf("complete Google OAuth login",
			"the callback did not include OAuth state; run `yc login` again",
			callback.Redactor(), ErrStateMismatch)
	}
	expected := strings.TrimSpace(callback.ExpectedState.Reveal())
	if expected != "" && subtle.ConstantTimeCompare([]byte(state), []byte(expected)) != 1 {
		return "", safeErrorf("complete Google OAuth login",
			"the callback state did not match; run `yc login` again",
			callback.Redactor(), ErrStateMismatch)
	}
	return state, nil
}

// deniedError renders a provider denial. The error code and description come
// from Google, so both go through the redactor before they are shown.
func deniedError(callback LoginCallback, redactor Redactor) error {
	code := strings.TrimSpace(callback.Error)
	if code == "" {
		code = "access_denied"
	}
	detail := "Google denied authorization (" + code + ")"
	if description := strings.TrimSpace(callback.ErrorDescription); description != "" {
		detail += ": " + description
	}
	return safeErrorf("complete Google OAuth login", detail, redactor, ErrLoginDenied)
}

// oauthTokenResponse is the token endpoint's success payload.
type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	IDToken      string `json:"id_token"`
}

// oauthErrorResponse is the shared Google OAuth failure payload.
type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
}

// detail returns the most useful human-readable failure text, falling back to
// the raw body when the payload was not the documented shape.
func (r oauthErrorResponse) detail(body []byte) string {
	for _, value := range []string{r.ErrorDescription, r.Message, r.Error} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(string(body))
}

// tokenInfoResponse is the tokeninfo payload. Google returns exp and expires_in
// as JSON strings on this endpoint, so both are decoded as strings.
type tokenInfoResponse struct {
	AuthorizedParty string `json:"azp"`
	Audience        string `json:"aud"`
	Subject         string `json:"sub"`
	Scope           string `json:"scope"`
	Expires         string `json:"exp"`
	ExpiresIn       string `json:"expires_in"`
}

// expiresInSeconds returns the remaining lifetime in seconds, preferring the
// relative value Google documents for installed apps. The absolute exp claim
// is measured against the caller's clock rather than time.Now, because this
// package promises the whole flow can be driven by an injected Now.
func (r tokenInfoResponse) expiresInSeconds(now time.Time) int64 {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(r.ExpiresIn), 10, 64); err == nil {
		return seconds
	}
	if expires, err := strconv.ParseInt(strings.TrimSpace(r.Expires), 10, 64); err == nil {
		return expires - now.Unix()
	}
	return 0
}

// channelsListResponse is the minimal shape of channels.list?mine=true.
type channelsListResponse struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			Title     string `json:"title"`
			CustomURL string `json:"customUrl"`
		} `json:"snippet"`
	} `json:"items"`
}

// decodeJSON decodes a bounded response body.
func decodeJSON(body io.Reader, target any) error {
	return json.NewDecoder(io.LimitReader(body, maxOAuthResponseBytes)).Decode(target)
}

// responseDetail reads a bounded failure body and extracts the most useful
// message from it.
func responseDetail(body io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxOAuthResponseBytes))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", nil
	}
	var decoded oauthErrorResponse
	_ = json.Unmarshal(data, &decoded)
	return decoded.detail(data), nil
}

// endpointOrDefault trims an override and falls back to the documented URL.
func endpointOrDefault(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// firstNonEmpty returns the first trimmed non-empty value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
