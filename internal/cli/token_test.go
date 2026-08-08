package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
)

// newTokenInfoFlow points a real OAuth flow at an in-process tokeninfo
// endpoint, so validation is exercised through the transport yc actually ships
// rather than through a hand-written double that could drift from it.
func newTokenInfoFlow(t *testing.T, clientID string, handler http.HandlerFunc) *auth.GoogleOAuthLoginFlow {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return auth.NewGoogleOAuthLoginFlow(auth.GoogleOAuthConfig{
		ClientID:          clientID,
		Scopes:            auth.LoginScopes(),
		HTTPClient:        server.Client(),
		Timeout:           5 * time.Second,
		TokenInfoEndpoint: server.URL,
	})
}

// tokenInfoConfig is a config carrying a token worth validating.
func tokenInfoConfig(clientID string) config.Config {
	cfg := config.Default()
	cfg.Google.ClientID = clientID
	cfg.Google.AccessToken = fakeToken
	return cfg
}

func TestValidateAccessTokenAcceptsALiveTokenAndReportsItsScopes(t *testing.T) {
	const clientID = "client-abc.apps.googleusercontent.com"
	flow := newTokenInfoFlow(t, clientID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("tokeninfo method = %s, want POST; the token must never ride in a query string", r.Method)
		}
		if raw := r.URL.RawQuery; strings.Contains(raw, "access_token") {
			t.Errorf("access token reached the query string: %q", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"aud":%q,"scope":%q,"expires_in":"3400"}`,
			clientID, strings.Join(auth.ScopeValues(auth.LoginScopes()), " "))
	})

	validation := validateAccessToken(context.Background(), tokenInfoConfig(clientID), flow)
	if !validation.Reachable || !validation.Valid {
		t.Fatalf("validation = %+v, want reachable and valid", validation)
	}
	if len(validation.Scopes) != len(auth.LoginScopes()) {
		t.Errorf("scopes = %v, want the full login set", auth.ScopeValues(validation.Scopes))
	}
	if validation.ExpiresAt.IsZero() {
		t.Error("a live token must report an expiry so the refresh loop can schedule itself")
	}
	if warning := tokenScopeWarning(validation); warning != "" {
		t.Errorf("a fully scoped token produced a warning: %q", warning)
	}
}

// A 400 or 401 is Google saying no. That is worth stopping the session for,
// because starting the UI on a dead token produces a wall of errors instead of
// one sentence.
func TestValidateAccessTokenTreatsA400AsARejection(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			flow := newTokenInfoFlow(t, "", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":"invalid_token","error_description":"Invalid Value %s"}`, fakeToken)
			})

			validation := validateAccessToken(context.Background(), tokenInfoConfig(""), flow)
			if !validation.Reachable {
				t.Fatalf("validation = %+v; Google answered, so it was reachable", validation)
			}
			if validation.Valid {
				t.Fatal("a rejected token must not be reported as valid")
			}
			if strings.Contains(validation.Detail, fakeToken) {
				t.Fatalf("the rejection detail echoed the token back: %q", validation.Detail)
			}
			if err := tokenValidationError(validation); !strings.Contains(err.Error(), "yc login") {
				t.Errorf("terminal message = %q, want it to name the way forward", err)
			}
		})
	}
}

// An offline laptop and a revoked token are different problems, and reporting
// the first as the second sends the user to re-authenticate for no reason.
func TestValidateAccessTokenTreatsATransportFailureAsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close() // nothing is listening now

	flow := auth.NewGoogleOAuthLoginFlow(auth.GoogleOAuthConfig{
		HTTPClient:        &http.Client{Timeout: time.Second},
		Timeout:           time.Second,
		TokenInfoEndpoint: endpoint,
	})

	validation := validateAccessToken(context.Background(), tokenInfoConfig(""), flow)
	if validation.Reachable || validation.Valid {
		t.Fatalf("validation = %+v, want unreachable and not valid", validation)
	}
	if !strings.Contains(validation.Detail, "unreachable") {
		t.Errorf("detail = %q, want it to say the endpoint could not be reached", validation.Detail)
	}
	if strings.Contains(validation.Detail, endpoint) {
		t.Errorf("detail quoted the request URL: %q", validation.Detail)
	}
}

// A 500 is Google failing, not Google refusing. Retrying later is right;
// sending the user back to the browser is not.
func TestValidateAccessTokenTreatsA500AsUnreachable(t *testing.T) {
	flow := newTokenInfoFlow(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"backend error"}`)
	})

	validation := validateAccessToken(context.Background(), tokenInfoConfig(""), flow)
	if validation.Reachable {
		t.Fatalf("validation = %+v; a 5xx is not a credential decision", validation)
	}
}

// A token minted for another OAuth client would otherwise produce a confusing
// 403 three screens later.
func TestValidateAccessTokenRejectsATokenFromAnotherClient(t *testing.T) {
	flow := newTokenInfoFlow(t, "ours.apps.googleusercontent.com", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"aud":"theirs.apps.googleusercontent.com","scope":"","expires_in":"3400"}`)
	})

	validation := validateAccessToken(context.Background(), tokenInfoConfig("ours.apps.googleusercontent.com"), flow)
	if validation.Valid {
		t.Fatal("a token issued to a different client must not validate")
	}
	if !strings.Contains(validation.Detail, "different Google OAuth client") {
		t.Errorf("detail = %q, want it to name the mismatch", validation.Detail)
	}
}

func TestValidateAccessTokenWithNothingToValidate(t *testing.T) {
	cfg := config.Default()
	validation := validateAccessToken(context.Background(), cfg, nil)
	if validation.Reachable || validation.Valid {
		t.Fatalf("validation = %+v, want the no-token outcome", validation)
	}
	if !strings.Contains(validation.Detail, "no access token") {
		t.Errorf("detail = %q, want it to say there was nothing to check", validation.Detail)
	}

	withToken := tokenInfoConfig("")
	if got := validateAccessToken(context.Background(), withToken, nil); got.Reachable {
		t.Fatalf("a nil validator reported reachability: %+v", got)
	}
}

func TestIsTokenRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"http 400 in the message", errors.New("inspect token: Google returned HTTP 400"), true},
		{"http 401 in the message", errors.New("inspect token: Google returned HTTP 401"), true},
		{"invalid_token", errors.New("invalid_token"), true},
		{"unauthorized", errors.New("Unauthorized"), true},
		{"invalid_grant", errors.New("invalid_grant"), true},
		{"dns failure", errors.New("dial tcp: no such host"), false},
		{"timeout", errors.New("context deadline exceeded"), false},
		{"http 500", errors.New("Google returned HTTP 500"), false},
		{"status interface", statusCodeError{status: http.StatusUnauthorized}, true},
		{"status interface 503", statusCodeError{status: http.StatusServiceUnavailable}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTokenRejection(tc.err); got != tc.want {
				t.Errorf("isTokenRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// statusCodeError exercises the structured branch of isTokenRejection, which a
// transport carrying a real status code would take.
type statusCodeError struct{ status int }

func (e statusCodeError) Error() string   { return "transport failure" }
func (e statusCodeError) StatusCode() int { return e.status }

// A read-only token must disable the composer with a reason attached: a control
// that explains itself teaches the user what to fix and a missing one does not.
func TestTokenScopeWarningNamesTheMissingScope(t *testing.T) {
	warning := tokenScopeWarning(tokenValidation{Valid: true, Scopes: auth.ReadScopes()})
	if warning == "" {
		t.Fatal("a read-only token produced no warning")
	}
	for _, want := range []string{"read access only", "youtube.force-ssl", "yc login"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning is missing %q:\n%s", want, warning)
		}
	}

	if got := tokenScopeWarning(tokenValidation{Valid: false, Scopes: auth.ReadScopes()}); got != "" {
		t.Errorf("an invalid token produced a scope warning: %q", got)
	}
	if got := tokenScopeWarning(tokenValidation{Valid: true}); got != "" {
		t.Errorf("unknown scopes produced a warning yc cannot back up: %q", got)
	}
}

func TestTokenValidationErrorAlwaysNamesAWayForward(t *testing.T) {
	err := tokenValidationError(tokenValidation{})
	for _, want := range []string{"yc login", "yc doctor", "--mock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q:\n%s", want, err)
		}
	}

	// tokenValidation.Detail is redacted by construction, so the guarantee
	// worth asserting is the one along the real path: a server that echoes the
	// token back must not get it printed to the terminal.
	flow := newTokenInfoFlow(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error_description":"Invalid Value: %s"}`, fakeToken)
	})
	validation := validateAccessToken(context.Background(), tokenInfoConfig(""), flow)
	if got := tokenValidationError(validation).Error(); strings.Contains(got, fakeToken) {
		t.Fatalf("the terminal message leaked a token echoed back by the endpoint: %s", got)
	}
}
