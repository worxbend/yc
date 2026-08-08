package config

import (
	"net/url"
	"regexp"
	"strings"
)

// googleAPIKeyPattern matches the shape of a Google API key. yc's own key never
// reaches a display path, but a user-supplied redirect URL or debug log path
// can be made to carry one, and the shape is distinctive enough to catch it
// without a false positive on ordinary text.
var googleAPIKeyPattern = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)

// credentialMarkers are the substrings that make a value unsafe to print.
//
// They are matched case-insensitively against the whole value, and a hit
// replaces the entire value rather than the matched span: a partially redacted
// credential is still a credential, and the surrounding text is rarely worth
// the risk of getting the boundary wrong.
var credentialMarkers = []string{
	"access_token=",
	"access-token=",
	"access_token:",
	"access-token:",
	"refresh_token=",
	"refresh-token=",
	"refresh_token:",
	"refresh-token:",
	"id_token=",
	"id-token=",
	"id_token:",
	"id-token:",
	"client_secret=",
	"client-secret=",
	"client_secret:",
	"client-secret:",
	"api_key=",
	"api-key=",
	"apikey=",
	"api_key:",
	"api-key:",
	"apikey:",
	"?key=",
	"&key=",
	"code_verifier=",
	"code-verifier=",
	"code_verifier:",
	"code-verifier:",
	"code_challenge=",
	"code-challenge=",
	"code_challenge:",
	"code-challenge:",
	"authorization_code=",
	"authorization-code=",
	"state=",
	"state:",
	"code=",
	"code:",
	"authorization=",
	"authorization: bearer",
	"bearer ",
	"bearer%20",
}

// RedactDisplayValue scans a non-secret, user-controlled value (a path, a
// redirect URL) for credential-shaped markers and URL userinfo, replacing the
// whole value when one is found. It is the last line of defence for values that
// are not themselves secrets but can be made to carry one.
func RedactDisplayValue(value string) string {
	if value == "" {
		return ""
	}
	if containsCredentialMarker(value) || containsURLUserInfo(value) {
		return Redacted
	}
	return value
}

func containsCredentialMarker(value string) bool {
	if googleAPIKeyPattern.MatchString(value) {
		return true
	}
	lower := strings.ToLower(value)
	for _, marker := range credentialMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// containsURLUserInfo reports a "scheme://user:pass@host" form, which is the
// one place a credential hides in an otherwise ordinary-looking URL.
func containsURLUserInfo(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return parsed.User != nil
}
