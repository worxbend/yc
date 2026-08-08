package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newTestLoopback(t *testing.T, cfg LoopbackConfig) *LoopbackServer {
	t.Helper()
	server, err := NewLoopbackServer(cfg)
	if err != nil {
		t.Fatalf("NewLoopbackServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestLoopbackServerBindsLoopbackOnly(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{ExpectedState: NewSecret(testState)})

	redirect := server.RedirectURI()
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") && !strings.HasPrefix(redirect, "http://[::1]:") {
		t.Fatalf("redirect URI must be loopback, got %q", redirect)
	}
	if !strings.HasSuffix(redirect, "/") {
		t.Fatalf("redirect URI must carry a path, got %q", redirect)
	}
	if strings.HasSuffix(redirect, ":0/") {
		t.Fatalf("redirect URI must carry the bound port, got %q", redirect)
	}
}

func TestLoopbackServerDeliversCallback(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{ExpectedState: NewSecret(testState)})

	go func() {
		resp, err := http.Get(server.RedirectURI() + "?code=" + testCode + "&state=" + testState)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	callback, err := server.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if callback.Code.Reveal() != testCode {
		t.Fatalf("code = %q", callback.Code.Reveal())
	}
	if callback.State.Reveal() != testState {
		t.Fatalf("state = %q", callback.State.Reveal())
	}
	if callback.Denied() {
		t.Fatal("a successful callback must not read as denied")
	}
}

// TestLoopbackServerRejectsStateMismatch is the CSRF guard: a forged callback
// must neither end the wait nor reach the token exchange.
func TestLoopbackServerRejectsStateMismatch(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{
		ExpectedState: NewSecret(testState),
		Timeout:       150 * time.Millisecond,
	})

	resp, err := http.Get(server.RedirectURI() + "?code=" + testCode + "&state=forged")
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a forged callback should be refused, got HTTP %d", resp.StatusCode)
	}

	if _, err := server.Wait(context.Background()); !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("a forged callback must not satisfy Wait, got %v", err)
	}
}

func TestLoopbackServerIgnoresNonCallbackRequests(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{
		ExpectedState: NewSecret(testState),
		Timeout:       150 * time.Millisecond,
	})

	// Browsers request /favicon.ico; that must not consume the one-shot
	// delivery and end the login early.
	resp, err := http.Get(server.RedirectURI() + "favicon.ico")
	if err != nil {
		t.Fatalf("favicon request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a non-callback request, got %d", resp.StatusCode)
	}

	if _, err := server.Wait(context.Background()); !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("expected a timeout, got %v", err)
	}
}

// TestLoopbackServerPageDoesNotEchoQuery keeps the authorization code out of
// the browser tab, the history entry, and any screen share.
func TestLoopbackServerPageDoesNotEchoQuery(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{ExpectedState: NewSecret(testState)})

	resp, err := http.Get(server.RedirectURI() + "?code=" + testCode + "&state=" + testState)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 8192)
	n, _ := resp.Body.Read(buf)
	page := string(buf[:n])
	for _, secret := range []string{testCode, testState, FakeTokenMarker} {
		if strings.Contains(page, secret) {
			t.Fatalf("the success page echoed %q", secret)
		}
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the callback page must not be cached")
	}
}

func TestLoopbackServerDeliversDenial(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{ExpectedState: NewSecret(testState)})

	go func() {
		resp, err := http.Get(server.RedirectURI() + "?error=access_denied&state=" + testState)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	callback, err := server.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !callback.Denied() {
		t.Fatal("a denial callback must read as denied")
	}
	if callback.Error != "access_denied" {
		t.Fatalf("error = %q", callback.Error)
	}
}

func TestLoopbackServerHonorsContextCancellation(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{Timeout: time.Minute})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestLoopbackServerCloseIsIdempotent(t *testing.T) {
	server := newTestLoopback(t, LoopbackConfig{})
	if err := server.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestBeginLoginBindsLoopbackWhenNoRedirectConfigured covers the default
// desktop path end to end: no configured redirect URI means yc binds one.
func TestBeginLoginBindsLoopbackWhenNoRedirectConfigured(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, func(cfg *GoogleOAuthConfig) { cfg.RedirectURI = "" })

	challenge, err := flow.BeginLogin(context.Background(), LoginRequest{})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if !strings.HasPrefix(challenge.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("expected a bound loopback redirect, got %q", challenge.RedirectURI)
	}

	go func() {
		resp, err := http.Get(challenge.RedirectURI + "?code=" + testCode + "&state=" + testState)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	callback, err := flow.AwaitCallback(context.Background(), challenge)
	if err != nil {
		t.Fatalf("AwaitCallback: %v", err)
	}
	if callback.CodeVerifier.Reveal() != testVerifier {
		t.Fatal("AwaitCallback must attach the attempt's PKCE verifier")
	}
	if callback.RedirectURI != challenge.RedirectURI {
		t.Fatalf("AwaitCallback must attach the bound redirect URI, got %q", callback.RedirectURI)
	}

	result, err := flow.CompleteLogin(context.Background(), callback)
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if got := stub.lastTokenForm(t).Get("redirect_uri"); got != challenge.RedirectURI {
		t.Fatalf("the exchange must reuse the bound redirect URI, got %q", got)
	}
	if result.Tokens.AccessToken.Reveal() != testAccessToken {
		t.Fatal("the exchange did not return the expected token")
	}
}

func TestAwaitCallbackRejectsUnknownChallenge(t *testing.T) {
	stub := newOAuthTestServer(t)
	flow := stub.flow(t, nil)

	_, err := flow.AwaitCallback(context.Background(), LoginChallenge{State: NewSecret("never-issued")})
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expected ErrLoginRequired, got %v", err)
	}
}
