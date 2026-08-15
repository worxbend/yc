package auth

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// RedactedSecret is the placeholder used when a sensitive auth value is
// formatted or redacted for user-facing output.
const RedactedSecret = "<redacted>"

var (
	// bearerTokenPattern catches an Authorization header value that reached
	// a string it should never have reached.
	bearerTokenPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s"'&?]+`)
	// credentialKeyPattern catches "key=value" and "key: value" shapes for
	// every credential-bearing parameter in the Google installed-app flow
	// whose name is unambiguous wherever it appears. The api_key and id_token
	// entries are yc additions over twi's set.
	credentialKeyPattern = regexp.MustCompile(`(?i)((?:access[_-]token|refresh[_-]token|id[_-]token|client[_-]secret|api[_-]?key|authorization_code|code_verifier|code_challenge)(?:["']?\s*[:=]\s*["']?))[^"'\s&?]+`)
	// "state" and "code" are the two OAuth parameter names that are also
	// ordinary English, so they are matched by separator rather than bare.
	//
	// Matched bare they destroyed real diagnostics: "connection state:
	// connected", "error code: 403" and "country code: US" all became
	// "<redacted>", on every debug-log attribute and every user-facing
	// error. Splitting them by separator keeps every credential shape while
	// leaving prose intact, because the two separators are not ambiguous in
	// the same way.
	//
	// ambiguousAssignPattern handles "=", which is only ever an assignment:
	// a query string, a form body, a redirect URI. English does not write
	// "state=", so any prefix is accepted here except one that would make
	// this a longer identifier ("code_verifier=", "passcode=").
	ambiguousAssignPattern = regexp.MustCompile(`(?i)((?:^|[^a-z0-9_-])["']?(?:state|code)["']?\s*=\s*["']?)[^"'\s&?]+`)
	// ambiguousColonPattern handles ":", which is ambiguous - it is both a
	// JSON key separator and the way English writes "error code: 403". So
	// the name must sit in a parameter position: at the start, or after
	// "?", "&", "{" or ",", optionally quoted. A bare "code: <value>" in
	// prose is deliberately left alone; Secret is the primary defense and
	// an authorization code does not reach a string that way.
	ambiguousColonPattern = regexp.MustCompile(`(?i)((?:^|[?&{,])\s*["']?(?:state|code)["']?\s*:\s*["']?)[^"'\s&?,}]+`)
	// googleAPIKeyPattern catches a bare Google API key by its own shape, so
	// a key pasted into a path or a URL is redacted even without a key= label.
	googleAPIKeyPattern = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
)

// Secret wraps a sensitive auth value. Its default string formatting is
// redacted; callers must use Reveal when they intentionally need the raw value
// for an OAuth HTTP request, a tokeninfo request, a signed API call, or a test
// assertion.
type Secret string

// NewSecret returns a Secret containing value.
func NewSecret(value string) Secret {
	return Secret(value)
}

// Present reports whether the secret contains a non-empty value after trimming
// surrounding whitespace.
func (s Secret) Present() bool {
	return strings.TrimSpace(s.Reveal()) != ""
}

// Reveal returns the raw secret value. Do not include this value in logs,
// formatted errors, diagnostics, snapshots, or persisted records before the
// credential storage boundary explicitly owns that behavior.
func (s Secret) Reveal() string {
	return string(s)
}

// Redacted returns the printable representation for this secret. An unset
// secret prints as empty so an operator can tell "not configured" from
// "configured but hidden".
func (s Secret) Redacted() string {
	if !s.Present() {
		return ""
	}
	return RedactedSecret
}

// String returns a redacted representation of the secret.
func (s Secret) String() string {
	return s.Redacted()
}

// GoString returns a redacted representation used by %#v formatting.
func (s Secret) GoString() string {
	if !s.Present() {
		return `auth.Secret("")`
	}
	return "auth.Secret(" + RedactedSecret + ")"
}

// MarshalJSON encodes the redacted representation so accidental structured
// output does not persist raw secrets.
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(s.Redacted())), nil
}

// MarshalText encodes the redacted representation for text encoders.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.Redacted()), nil
}

// Redactor redacts auth secrets and common OAuth credential patterns from text
// that may become user-facing output.
type Redactor struct {
	secrets []string
}

// NewRedactor creates a redactor for explicit secret values.
func NewRedactor(secrets ...Secret) Redactor {
	return Redactor{secrets: secretStrings(secrets)}
}

// String describes the redactor without listing what it holds.
//
// A Redactor carries every secret in the process by construction, which makes
// it the single most dangerous value in yc to hand to fmt. Without this, one
// `%v` on a redactor - or on any struct that has one as an exported field -
// prints the client secret, both tokens, and the API key in one line. Secret
// itself refuses to format for the same reason.
func (r Redactor) String() string {
	return fmt.Sprintf("auth.Redactor(%d secrets)", len(r.secrets))
}

// GoString keeps %#v from printing the secret slice verbatim.
func (r Redactor) GoString() string { return r.String() }

// Redact removes the explicit secrets configured on the redactor and every
// known credential pattern from value.
func (r Redactor) Redact(value string) string {
	if value == "" {
		return ""
	}
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, RedactedSecret)
	}
	value = bearerTokenPattern.ReplaceAllString(value, "${1}"+RedactedSecret)
	value = credentialKeyPattern.ReplaceAllString(value, "${1}"+RedactedSecret)
	value = ambiguousAssignPattern.ReplaceAllString(value, "${1}"+RedactedSecret)
	value = ambiguousColonPattern.ReplaceAllString(value, "${1}"+RedactedSecret)
	value = googleAPIKeyPattern.ReplaceAllString(value, RedactedSecret)
	return value
}

// secretStrings returns the non-empty secret values, longest first so a
// longer secret cannot be partially masked by a shorter one that is its prefix.
func secretStrings(secrets []Secret) []string {
	seen := map[string]bool{}
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		value := strings.TrimSpace(secret.Reveal())
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	return values
}
