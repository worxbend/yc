package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A Redactor holds, by construction, every secret in the process. That makes it
// the single most dangerous value in yc to hand to fmt: one `%v` on a redactor,
// or on any struct that has one as a field, would print the client secret, both
// tokens and the API key on one line - in a terminal that is very often on
// stream. Secret refuses to format itself for the same reason, and both
// refusals have to survive every verb rather than only the one somebody tried.

// Obvious fake markers in the shapes Google issues, so the pattern rules are
// exercised alongside the value list.
const (
	fmtTestKey     = "AIzaSyFMTFAKEKEY00000000000000000000000"
	fmtTestAccess  = "ya29.a0FMT-not-a-real-access-token"
	fmtTestRefresh = "1//0gFMT-not-a-real-refresh-token"
	fmtTestSecret  = "GOCSPX-FMTnotarealclientsecret000"
)

// everyVerb renders a value the ways a careless debug print reaches it.
func everyVerb(value any) map[string]string {
	return map[string]string{
		"%v":  fmt.Sprintf("%v", value),
		"%+v": fmt.Sprintf("%+v", value),
		"%#v": fmt.Sprintf("%#v", value),
		"%s":  fmt.Sprintf("%s", value),
		"%q":  fmt.Sprintf("%q", value),
	}
}

func assertNothingLeaked(t *testing.T, label string, renderings map[string]string, secrets ...string) {
	t.Helper()
	for verb, rendering := range renderings {
		for _, secret := range secrets {
			if strings.Contains(rendering, secret) {
				t.Errorf("%s printed with %s leaked a credential: %s", label, verb, rendering)
			}
		}
	}
}

// TestARedactorNeverPrintsWhatItHolds is the guard on the guard.
func TestARedactorNeverPrintsWhatItHolds(t *testing.T) {
	redactor := NewRedactor(
		NewSecret(fmtTestKey),
		NewSecret(fmtTestAccess),
		NewSecret(fmtTestRefresh),
		NewSecret(fmtTestSecret),
	)

	renderings := everyVerb(redactor)
	assertNothingLeaked(t, "a Redactor", renderings, fmtTestKey, fmtTestAccess, fmtTestRefresh, fmtTestSecret)

	// It still has to say something useful, or a developer debugging the
	// redaction has nothing to look at and will reach for the field instead.
	if got := redactor.String(); !strings.Contains(got, "4") {
		t.Errorf("Redactor.String() = %q, want it to report how many secrets it holds", got)
	}
	if redactor.GoString() != redactor.String() {
		t.Errorf("GoString = %q, want it to match String = %q", redactor.GoString(), redactor.String())
	}

	// A redactor inside a struct is the realistic accident: fmt reaches
	// unexported fields by reflection, and only the outermost value's own
	// String method can stop it.
	holder := struct {
		Name     string
		Redactor Redactor
	}{Name: "startup", Redactor: redactor}
	assertNothingLeaked(t, "a struct holding a Redactor", everyVerb(holder),
		fmtTestKey, fmtTestAccess, fmtTestRefresh, fmtTestSecret)
}

// An empty redactor prints as an empty redactor rather than as a panic or a
// bare struct, and still redacts by shape - which is the whole reason the
// pattern rules run unconditionally after the value list.
func TestAnEmptyRedactorStillRedactsByShape(t *testing.T) {
	redactor := NewRedactor()
	if got := redactor.String(); !strings.Contains(got, "0") {
		t.Errorf("Redactor.String() = %q, want it to report an empty redactor", got)
	}

	got := redactor.Redact("the key is " + fmtTestKey + " and the header was Bearer " + fmtTestAccess)
	if strings.Contains(got, fmtTestKey) {
		t.Errorf("an API key survived shape-based redaction: %q", got)
	}
	if strings.Contains(got, fmtTestAccess) {
		t.Errorf("a bearer token survived shape-based redaction: %q", got)
	}
	if !strings.Contains(got, RedactedSecret) {
		t.Errorf("Redact = %q, want the placeholder in place of what it removed", got)
	}
	if got := redactor.Redact(""); got != "" {
		t.Errorf("Redact(\"\") = %q, want empty", got)
	}
}

// A longer secret must not be left half-masked by a shorter one that is its
// prefix. Sorting longest-first is what prevents "ya29.abc" turning
// "ya29.abcdef" into "<redacted>def", which reads as redacted and is not.
func TestALongerSecretIsNotPartiallyMaskedByItsOwnPrefix(t *testing.T) {
	short := "ya29.prefix"
	long := short + "-and-the-rest-of-the-token"
	redactor := NewRedactor(NewSecret(short), NewSecret(long))

	got := redactor.Redact("token=" + long)
	if strings.Contains(got, "-and-the-rest-of-the-token") {
		t.Fatalf("Redact = %q, want the whole of the longer secret removed", got)
	}
	if strings.Contains(got, short) {
		t.Fatalf("Redact = %q, want no fragment of either secret", got)
	}
}

// Secret refuses to format itself under every verb, and reports "configured but
// hidden" distinctly from "not configured" - an operator has to be able to tell
// those apart without being shown the value.
func TestSecretFormattingDistinguishesUnsetFromHidden(t *testing.T) {
	set := NewSecret(fmtTestAccess)
	assertNothingLeaked(t, "a Secret", everyVerb(set), fmtTestAccess)

	if got := set.Redacted(); got != RedactedSecret {
		t.Errorf("Redacted() = %q, want %q", got, RedactedSecret)
	}
	var unset Secret
	if got := unset.Redacted(); got != "" {
		t.Errorf("an unset Secret rendered as %q, want empty so it reads as unconfigured", got)
	}
	if unset.Present() {
		t.Error("an unset Secret reports itself present")
	}
	if NewSecret("   ").Present() {
		t.Error("a whitespace-only Secret reports itself present")
	}

	// The JSON and text encoders are separate paths, and a struct with a
	// Secret field is exactly what gets marshaled into a debug record.
	encoded, err := set.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error = %v", err)
	}
	if strings.Contains(string(encoded), fmtTestAccess) {
		t.Errorf("MarshalJSON leaked a credential: %s", encoded)
	}
	text, err := set.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error = %v", err)
	}
	if strings.Contains(string(text), fmtTestAccess) {
		t.Errorf("MarshalText leaked a credential: %s", text)
	}
	if got := unset.GoString(); !strings.Contains(got, `""`) {
		t.Errorf("an unset Secret's GoString = %q, want it to read as empty", got)
	}
}

// Google omits refresh_token on a refresh response. Reading that as "the
// refresh token is gone" would make yc discard the one credential that re-mints
// everything else, and the user would be signed out an hour into a stream for
// no reason at all.
func TestAnAbsentRefreshTokenMeansKeepTheOneYouHave(t *testing.T) {
	withRefresh := TokenSet{
		AccessToken:  NewSecret(fmtTestAccess),
		RefreshToken: NewSecret(fmtTestRefresh),
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if !withRefresh.RefreshAvailable() {
		t.Error("a response carrying a refresh token reported none")
	}

	refreshResponse := TokenSet{
		AccessToken: NewSecret(fmtTestAccess),
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if refreshResponse.RefreshAvailable() {
		t.Error("a refresh response with no refresh token reported one; the stored token would be overwritten with nothing")
	}
	if NewSecret("   ") != "   " {
		t.Fatal("NewSecret altered its input")
	}
	if (TokenSet{RefreshToken: NewSecret("  ")}).RefreshAvailable() {
		t.Error("a whitespace-only refresh token reported as available")
	}

	// The redactor a TokenSet hands out holds both tokens and prints
	// neither.
	assertNothingLeaked(t, "a TokenSet redactor", everyVerb(withRefresh.Redactor()), fmtTestAccess, fmtTestRefresh)
	if got := withRefresh.Redactor().Redact("access=" + fmtTestAccess + " refresh=" + fmtTestRefresh); strings.Contains(got, "FMT-not-a-real") {
		t.Errorf("the TokenSet redactor left a token in place: %q", got)
	}
}

// A login is only usable if it came back with the read scopes. The check has to
// read the result's own scope list first and fall back to the token set's,
// because the two are populated by different halves of the flow.
func TestMissingRequiredScopesReadsWhicheverListWasPopulated(t *testing.T) {
	read := ReadScopes()
	if len(read) == 0 {
		t.Skip("this build requires no read scopes")
	}

	full := LoginResult{Scopes: read}
	if missing := full.MissingRequiredScopes(); len(missing) != 0 {
		t.Errorf("a complete grant reported %v missing", missing)
	}

	// The result's own list is empty, so the token set's is consulted.
	fromTokens := LoginResult{Tokens: TokenSet{Scopes: read}}
	if missing := fromTokens.MissingRequiredScopes(); len(missing) != 0 {
		t.Errorf("a grant recorded only on the token set reported %v missing", missing)
	}

	empty := LoginResult{}
	if missing := empty.MissingRequiredScopes(); len(missing) != len(read) {
		t.Errorf("an empty grant reported %d missing, want all %d read scopes", len(missing), len(read))
	}

	partial := LoginResult{Scopes: read[:len(read)-1]}
	if missing := partial.MissingRequiredScopes(); len(missing) != 1 {
		t.Errorf("a grant short one scope reported %v, want exactly the missing one", missing)
	}

	// And the result never prints its tokens on the way to reporting that.
	result := LoginResult{
		Scopes: read,
		Tokens: TokenSet{AccessToken: NewSecret(fmtTestAccess), RefreshToken: NewSecret(fmtTestRefresh)},
	}
	assertNothingLeaked(t, "a LoginResult redactor", everyVerb(result.Redactor()), fmtTestAccess, fmtTestRefresh)
}

// tokeninfo reports the remaining lifetime two different ways and yc prefers
// the relative one, because the absolute one is only as good as the local
// clock - and a machine whose clock is wrong is a machine that would refresh
// constantly or never.
func TestTokenInfoPrefersTheRelativeLifetime(t *testing.T) {
	// The absolute-exp cases are measured against this fixed instant rather
	// than the wall clock, which is what taking now as a parameter buys: the
	// expectations are exact seconds instead of tolerance windows that would
	// flake on a slow machine.
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name     string
		response tokenInfoResponse
		want     int64
	}{
		{
			name:     "expires_in wins",
			response: tokenInfoResponse{ExpiresIn: "3599", Expires: "1"},
			want:     3599,
		},
		{
			name:     "expires_in with surrounding whitespace",
			response: tokenInfoResponse{ExpiresIn: "  120 "},
			want:     120,
		},
		{
			name:     "absolute exp is used when expires_in is absent",
			response: tokenInfoResponse{Expires: fmt.Sprint(now.Add(10 * time.Minute).Unix())},
			want:     600,
		},
		{
			name:     "neither field is zero rather than a guess",
			response: tokenInfoResponse{},
			want:     0,
		},
		{
			name:     "an unparseable pair is zero rather than a guess",
			response: tokenInfoResponse{ExpiresIn: "soon", Expires: "later"},
			want:     0,
		},
		{
			name:     "an already expired absolute value is negative, not clamped",
			response: tokenInfoResponse{Expires: fmt.Sprint(now.Add(-time.Minute).Unix())},
			want:     -60,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.response.expiresInSeconds(now); got != test.want {
				t.Fatalf("expiresInSeconds = %d, want %d", got, test.want)
			}
		})
	}
}
