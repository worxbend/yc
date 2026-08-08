package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Every call this package makes carries a credential in the request body, and
// net/http re-sends a body verbatim when it follows a 307 or 308. The
// Authorization header is stripped on a cross-host redirect; a form body is
// not. These tests pin that yc never follows one.

func TestTokenExchangeRefusesToFollowARedirect(t *testing.T) {
	var received atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if strings.Contains(string(body), "test-not-a-real-refresh-token") {
			received.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"attacker-issued","expires_in":3600}`))
	}))
	defer attacker.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 308 is the one that preserves the method and re-sends the body.
		http.Redirect(w, r, attacker.URL, http.StatusPermanentRedirect)
	}))
	defer redirector.Close()

	flow := NewGoogleOAuthLoginFlow(GoogleOAuthConfig{
		ClientID:      "test-client-id",
		TokenEndpoint: redirector.URL,
	})

	_, err := flow.Refresh(context.Background(), NewSecret("test-not-a-real-refresh-token"))
	if err == nil {
		t.Fatal("a redirected token endpoint must not be treated as a successful refresh")
	}
	if !errors.Is(err, ErrUnexpectedRedirect) {
		t.Errorf("error = %v, want ErrUnexpectedRedirect", err)
	}
	if got := received.Load(); got != 0 {
		t.Fatalf("the refresh token reached the redirect target %d time(s)", got)
	}
	if strings.Contains(err.Error(), "test-not-a-real-refresh-token") {
		t.Errorf("the refusal quoted the refresh token: %s", err)
	}
	if strings.Contains(err.Error(), attacker.URL) {
		t.Errorf("the refusal quoted the redirect target: %s", err)
	}
}

func TestTokenInfoRefusesToFollowARedirect(t *testing.T) {
	var received atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if strings.Contains(string(body), "test-not-a-real-access-token") {
			received.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	flow := NewGoogleOAuthLoginFlow(GoogleOAuthConfig{TokenInfoEndpoint: redirector.URL})
	if _, err := flow.TokenInfo(context.Background(), NewSecret("test-not-a-real-access-token")); !errors.Is(err, ErrUnexpectedRedirect) {
		t.Errorf("error = %v, want ErrUnexpectedRedirect", err)
	}
	if got := received.Load(); got != 0 {
		t.Fatalf("the access token reached the redirect target %d time(s)", got)
	}
}

func TestDefaultOAuthClientIsNotTheProcessWideClient(t *testing.T) {
	flow := NewGoogleOAuthLoginFlow(GoogleOAuthConfig{ClientID: "test-client-id"})
	if flow.cfg.HTTPClient == http.DefaultClient {
		t.Fatal("the OAuth flow must not inherit http.DefaultClient's transport, timeout, or redirect policy")
	}
	if flow.cfg.HTTPClient.CheckRedirect == nil {
		t.Fatal("the default OAuth client must decline redirects")
	}
	if flow.cfg.HTTPClient.Timeout <= 0 {
		t.Fatal("the default OAuth client must be time-bounded")
	}
}
