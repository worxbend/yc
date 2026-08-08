package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
)

// tokenValidator answers "is this access token still good, and what may it do".
// It is an interface so startup and diagnostics can both run against a fake.
type tokenValidator interface {
	TokenInfo(ctx context.Context, accessToken auth.Secret) (auth.TokenSet, error)
}

// newTokenValidator is a variable so tests never reach Google's tokeninfo
// endpoint.
var newTokenValidator = func(cfg config.Config) tokenValidator {
	return auth.NewGoogleOAuthLoginFlow(auth.GoogleOAuthConfig{
		ClientID:     strings.TrimSpace(cfg.Google.ClientID),
		ClientSecret: auth.NewSecret(cfg.Google.ClientSecret),
		Scopes:       auth.LoginScopes(),
		HTTPClient:   auth.NewOAuthHTTPClient(metadataHTTPTimeout),
		Timeout:      metadataHTTPTimeout,
	})
}

// tokenValidation is what yc learned about the configured token, including the
// case where it learned nothing.
//
// Reachable is separate from Valid on purpose: an offline laptop and a revoked
// token are different problems, and reporting the first as the second sends the
// user to re-authenticate for no reason.
type tokenValidation struct {
	Reachable bool
	Valid     bool
	ExpiresAt time.Time
	Scopes    []auth.Scope
	// Detail is already redacted and is safe to print.
	Detail string
}

// validateAccessToken checks the configured token against Google's tokeninfo
// endpoint.
//
// It never fails the caller: an unreachable endpoint yields Reachable false and
// the caller continues to the API, which is the real authority anyway. A
// definite rejection yields Valid false with a redacted detail, and that is
// worth stopping for, because starting the UI on a dead token produces a wall
// of errors instead of one sentence.
func validateAccessToken(ctx context.Context, cfg config.Config, validator tokenValidator) tokenValidation {
	token := strings.TrimSpace(cfg.Google.AccessToken)
	if token == "" {
		return tokenValidation{Detail: "no access token to validate"}
	}
	if validator == nil {
		return tokenValidation{Detail: "token validation is unavailable; continuing to the API"}
	}

	redactor := startupRedactor(cfg)
	info, err := validator.TokenInfo(ctx, auth.NewSecret(token))
	if err != nil {
		detail := safeStartupError(redactor, err)
		if isTokenRejection(err) {
			return tokenValidation{Reachable: true, Detail: detail}
		}
		return tokenValidation{Detail: "token validation unreachable (" + detail + ")"}
	}
	return tokenValidation{
		Reachable: true,
		Valid:     true,
		ExpiresAt: info.ExpiresAt,
		Scopes:    info.Scopes,
	}
}

// isTokenRejection distinguishes "Google said no" from "Google did not answer".
//
// tokeninfo answers a bad token with 400 or 401, so a transport-level failure
// - DNS, a proxy, a dropped connection - must not be reported as a revoked
// credential.
func isTokenRejection(err error) bool {
	if err == nil {
		return false
	}
	var httpErr interface{ StatusCode() int }
	if errors.As(err, &httpErr) {
		status := httpErr.StatusCode()
		return status == http.StatusBadRequest || status == http.StatusUnauthorized
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"invalid_token", "invalid token", "invalid_grant", "unauthorized", "401", "400"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// tokenScopeWarning reports scopes the token does not carry, so a disabled
// composer has a reason attached instead of appearing broken.
func tokenScopeWarning(validation tokenValidation) string {
	if !validation.Valid || len(validation.Scopes) == 0 {
		return ""
	}
	missing := auth.MissingScopes(validation.Scopes, auth.SendScopes())
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("warning: this token grants read access only (missing %s); "+
		"chat will be read-only until you re-run `yc login`", strings.Join(auth.ScopeValues(missing), ", "))
}

// tokenValidationError is the terminal message for a token Google rejected.
func tokenValidationError(validation tokenValidation) error {
	detail := strings.TrimSpace(validation.Detail)
	if detail == "" {
		detail = "the access token was rejected"
	}
	return fmt.Errorf("google rejected the configured access token: %s. "+
		"Run `yc login` to authenticate again, or `yc doctor` to see which credential source is in use; "+
		"`yc chat --mock` needs no credentials at all", config.RedactDisplayValue(detail))
}
