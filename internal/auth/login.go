package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// LoginFlow is the boundary for interactive OAuth login. Implementations may
// talk to Google and to a local loopback callback server, but callers should
// only ever need these typed requests and responses.
type LoginFlow interface {
	BeginLogin(ctx context.Context, request LoginRequest) (LoginChallenge, error)
	CompleteLogin(ctx context.Context, callback LoginCallback) (LoginResult, error)
}

// TokenRefresher exchanges a refresh token for a fresh access token.
//
// Implementations must be single-flight: N concurrent 401s must cause one
// refresh, not N. They must apply an expiry skew so a token is replaced before
// it expires rather than after a failure, and they must treat an invalid_grant
// response as terminal - the refresh token was revoked or expired, and the user
// has to log in again.
type TokenRefresher interface {
	Refresh(ctx context.Context, refreshToken Secret) (TokenSet, error)
}

// LoginRequest describes the start of an OAuth login attempt. Sensitive fields
// use Secret so accidental formatting does not print raw values.
type LoginRequest struct {
	ClientID string
	// ClientSecret is optional. An installed app cannot keep a secret, so
	// the desktop flow relies on PKCE; a configured secret only enables
	// unattended refresh.
	ClientSecret Secret
	// RedirectURI is the loopback callback. An empty value means "listen on
	// 127.0.0.1 on an ephemeral port", which is what Google recommends for a
	// desktop app.
	RedirectURI string
	Scopes      []Scope
	State       Secret
	// CodeVerifier is the PKCE verifier: 43-128 characters from
	// [A-Za-z0-9-._~]. CodeChallenge is its base64url-unpadded SHA-256.
	CodeVerifier  Secret
	CodeChallenge string
	LoginHint     string
	// ForceConsent sends prompt=consent, which is how the installed-app flow
	// forces a refresh token to be re-issued. Note that access_type=offline
	// belongs to the web-server flow and is not part of the documented
	// desktop parameter set.
	ForceConsent bool
}

// RequiredScopes returns the request scopes, or LoginScopes when none were
// provided.
func (r LoginRequest) RequiredScopes() []Scope {
	if len(r.Scopes) == 0 {
		return LoginScopes()
	}
	return append([]Scope(nil), r.Scopes...)
}

// Redactor returns a redactor configured with every secret in the request.
func (r LoginRequest) Redactor() Redactor {
	return NewRedactor(r.ClientSecret, r.State, r.CodeVerifier)
}

// LoginChallenge is the user-visible authorization challenge produced by a
// login flow.
//
// AuthorizationURL is a Secret because it carries state and the PKCE challenge,
// so printing it must be a deliberate act at the one boundary that shows it to
// the user.
type LoginChallenge struct {
	AuthorizationURL Secret
	State            Secret
	Scopes           []Scope
	// RedirectURI is the loopback address actually bound, including the
	// ephemeral port.
	RedirectURI string
	ExpiresAt   time.Time
}

// Redactor returns a redactor configured with challenge secrets.
func (c LoginChallenge) Redactor() Redactor {
	return NewRedactor(c.AuthorizationURL, c.State)
}

// LoginCallback contains the OAuth callback values received after a user
// authorizes or denies the login request.
type LoginCallback struct {
	Code             Secret
	State            Secret
	ExpectedState    Secret
	CodeVerifier     Secret
	RedirectURI      string
	Error            string
	ErrorDescription string
}

// LoginCallbackFromRequest extracts OAuth callback values from a loopback HTTP
// request. Code and state are Secret values so accidental formatting of the
// callback - or of the whole request - stays redacted.
func LoginCallbackFromRequest(r *http.Request, expectedState Secret) LoginCallback {
	callback := LoginCallback{ExpectedState: expectedState}
	if r == nil || r.URL == nil {
		return callback
	}
	query := r.URL.Query()
	callback.Code = NewSecret(query.Get("code"))
	callback.State = NewSecret(query.Get("state"))
	callback.Error = strings.TrimSpace(query.Get("error"))
	callback.ErrorDescription = strings.TrimSpace(query.Get("error_description"))
	return callback
}

// Denied reports whether the callback carries a provider denial instead of an
// authorization code.
func (c LoginCallback) Denied() bool {
	return strings.TrimSpace(c.Error) != ""
}

// Redactor returns a redactor configured with callback secrets.
func (c LoginCallback) Redactor() Redactor {
	return NewRedactor(c.Code, c.State, c.ExpectedState, c.CodeVerifier)
}

// Identity is the Google/YouTube identity associated with a completed login,
// resolved through channels.list?mine=true.
type Identity struct {
	ChannelID   string
	DisplayName string
	Handle      string
}

// TokenSet is the OAuth credential material returned by a completed login or a
// refresh. AccessToken and RefreshToken are Secret values and never print raw.
type TokenSet struct {
	AccessToken  Secret
	RefreshToken Secret
	TokenType    string
	ExpiresAt    time.Time
	Scopes       []Scope
}

// RefreshAvailable reports whether the response included a refresh token.
// Google omits it on a refresh response, so an absent value on refresh means
// "keep the one you have", not "you lost it".
func (t TokenSet) RefreshAvailable() bool {
	return t.RefreshToken.Present()
}

// Redactor returns a redactor configured with token secrets.
func (t TokenSet) Redactor() Redactor {
	return NewRedactor(t.AccessToken, t.RefreshToken)
}

// LoginResult is the typed result of a completed OAuth login. It carries token
// values for the caller but does not decide where or whether they are stored.
type LoginResult struct {
	Identity Identity
	Tokens   TokenSet
	Scopes   []Scope
}

// MissingRequiredScopes returns the read scopes absent from the login result.
func (r LoginResult) MissingRequiredScopes() []Scope {
	scopes := r.Scopes
	if len(scopes) == 0 {
		scopes = r.Tokens.Scopes
	}
	return MissingScopes(scopes, ReadScopes())
}

// Redactor returns a redactor configured with result secrets.
func (r LoginResult) Redactor() Redactor {
	return r.Tokens.Redactor()
}
