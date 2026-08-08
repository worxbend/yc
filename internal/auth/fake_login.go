package auth

import (
	"context"
	"sync"
	"time"
)

// FakeTokenMarker is the obvious placeholder every test fixture uses in place
// of credential material. Redaction tests assert that formatting or marshaling
// any exported type never emits this substring.
const FakeTokenMarker = "test-not-a-real-token"

// FakeLoginFlow is a deterministic LoginFlow for CLI and app tests. It performs
// no network or filesystem work and returns only FakeTokenMarker-derived
// values, so a test that leaks a credential is trivially greppable.
type FakeLoginFlow struct {
	// mu guards the recorded slices so a fake shared across goroutines is
	// still safe to assert on.
	mu sync.Mutex

	// Challenge is returned by BeginLogin when set.
	Challenge LoginChallenge
	// Result is returned by CompleteLogin when set.
	Result LoginResult
	// BeginErr and CompleteErr force the corresponding failure path.
	BeginErr    error
	CompleteErr error
	// RefreshErr forces the refresh failure path.
	RefreshErr error

	// Requests and Callbacks record what the caller passed, for assertions.
	// They hold fake-marker secrets only and must not be printed outside
	// tests.
	Requests  []LoginRequest
	Callbacks []LoginCallback
}

var (
	_ LoginFlow      = (*FakeLoginFlow)(nil)
	_ TokenRefresher = (*FakeLoginFlow)(nil)
)

// fakeLoginExpiry is the fixed instant fake credentials expire at, chosen so a
// test never depends on the wall clock.
var fakeLoginExpiry = time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)

// NewFakeLoginFlow returns a fake flow pre-populated with fake-marker
// credentials and the full login scope set.
func NewFakeLoginFlow() *FakeLoginFlow {
	scopes := LoginScopes()
	tokens := TokenSet{
		AccessToken:  NewSecret(FakeTokenMarker + "-access"),
		RefreshToken: NewSecret(FakeTokenMarker + "-refresh"),
		TokenType:    "Bearer",
		ExpiresAt:    fakeLoginExpiry,
		Scopes:       cloneScopes(scopes),
	}
	return &FakeLoginFlow{
		Challenge: LoginChallenge{
			AuthorizationURL: NewSecret("https://accounts.google.com/o/oauth2/v2/auth?state=" + FakeTokenMarker + "-state"),
			State:            NewSecret(FakeTokenMarker + "-state"),
			Scopes:           cloneScopes(scopes),
			RedirectURI:      "http://127.0.0.1:0/",
			ExpiresAt:        fakeLoginExpiry,
		},
		Result: LoginResult{
			Identity: Identity{
				ChannelID:   "UC-test-not-a-real-channel",
				DisplayName: "yc test channel",
				Handle:      "@yctest",
			},
			Tokens: tokens,
			Scopes: cloneScopes(scopes),
		},
	}
}

// BeginLogin records the request and returns the configured challenge.
func (f *FakeLoginFlow) BeginLogin(ctx context.Context, request LoginRequest) (LoginChallenge, error) {
	if err := ctx.Err(); err != nil {
		return LoginChallenge{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.Requests = append(f.Requests, cloneLoginRequest(request))
	if f.BeginErr != nil {
		return LoginChallenge{}, f.BeginErr
	}
	return cloneLoginChallenge(f.Challenge), nil
}

// CompleteLogin records the callback and returns the configured result.
func (f *FakeLoginFlow) CompleteLogin(ctx context.Context, callback LoginCallback) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.Callbacks = append(f.Callbacks, callback)
	if f.CompleteErr != nil {
		return LoginResult{}, f.CompleteErr
	}
	return cloneLoginResult(f.Result), nil
}

// Refresh returns the configured result's tokens, so callers can exercise the
// refresh path without a network.
func (f *FakeLoginFlow) Refresh(ctx context.Context, refreshToken Secret) (TokenSet, error) {
	if err := ctx.Err(); err != nil {
		return TokenSet{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.RefreshErr != nil {
		return TokenSet{}, f.RefreshErr
	}
	tokens := cloneTokenSet(f.Result.Tokens)
	// Mirror Google's behavior: a refresh response omits the refresh token,
	// so the caller keeps the one it already holds.
	if !tokens.RefreshToken.Present() {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

// RecordedRequests returns a copy of the BeginLogin requests seen so far.
func (f *FakeLoginFlow) RecordedRequests() []LoginRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	requests := make([]LoginRequest, len(f.Requests))
	for i := range f.Requests {
		requests[i] = cloneLoginRequest(f.Requests[i])
	}
	return requests
}

// RecordedCallbacks returns a copy of the CompleteLogin callbacks seen so far.
func (f *FakeLoginFlow) RecordedCallbacks() []LoginCallback {
	f.mu.Lock()
	defer f.mu.Unlock()

	callbacks := make([]LoginCallback, len(f.Callbacks))
	copy(callbacks, f.Callbacks)
	return callbacks
}

func cloneLoginRequest(request LoginRequest) LoginRequest {
	request.Scopes = cloneScopes(request.Scopes)
	return request
}

func cloneLoginChallenge(challenge LoginChallenge) LoginChallenge {
	challenge.Scopes = cloneScopes(challenge.Scopes)
	return challenge
}

func cloneLoginResult(result LoginResult) LoginResult {
	result.Scopes = cloneScopes(result.Scopes)
	result.Tokens = cloneTokenSet(result.Tokens)
	return result
}

func cloneTokenSet(tokens TokenSet) TokenSet {
	tokens.Scopes = cloneScopes(tokens.Scopes)
	return tokens
}
