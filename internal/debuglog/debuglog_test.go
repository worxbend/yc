package debuglog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/auth"
)

// fakeToken is the marker every credential fixture in yc uses, so a leak shows
// up as an obviously fake string rather than as something plausible.
const fakeToken = "test-not-a-real-token"

func TestZeroLoggerIsUsable(t *testing.T) {
	var logger Logger
	if logger.Enabled() {
		t.Fatal("zero Logger reports enabled, want disabled")
	}
	logger.Log(context.Background(), "event", slog.String("key", "value"))
	if got := logger.Redact(fakeToken + "-value"); got == "" {
		t.Fatal("zero Logger.Redact() returned empty, want the value back")
	}
}

func TestDisabledLoggerWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, Options{})

	logger.Log(context.Background(), "test", slog.String("token", fakeToken))

	if buf.Len() != 0 {
		t.Fatalf("disabled logger wrote %q, want empty", buf.String())
	}
}

func TestLoggerRedactsSecretsAndAvoidsRawAnyDumps(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, Options{
		Enabled:  true,
		Redactor: auth.NewRedactor(auth.NewSecret(fakeToken + "-refresh")),
	}).WithSecrets(auth.NewSecret(fakeToken + "-client-secret"))

	logger.Log(context.Background(), "redaction.test",
		slog.String("refresh", fakeToken+"-refresh"),
		slog.String("client", fakeToken+"-client-secret"),
		slog.String("auth_header", "Authorization: Bearer "+fakeToken+"-bearer"),
		slog.String("callback", "http://127.0.0.1:8080/callback?code="+fakeToken+"-code&state="+fakeToken+"-state"),
		slog.String("api_url", "https://www.googleapis.com/youtube/v3/liveChat/messages?key=AIzaBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
		slog.String("userinfo_url", "open https://user:password@example.com/path"),
		slog.Any("raw_struct", struct {
			Token string
		}{Token: fakeToken + "-any"}),
		slog.Group("nested", slog.String("verifier", "code_verifier="+fakeToken+"-verifier")),
	)

	output := buf.String()
	for _, secret := range []string{
		fakeToken + "-refresh",
		fakeToken + "-client-secret",
		fakeToken + "-bearer",
		fakeToken + "-code",
		fakeToken + "-state",
		fakeToken + "-verifier",
		fakeToken + "-any",
		"AIzaBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"user:password",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("debug log leaked %q:\n%s", secret, output)
		}
	}
	for _, want := range []string{`"event":"redaction.test"`, Redacted, `"raw_struct":"<struct { Token string }>"`} {
		if !strings.Contains(output, want) {
			t.Fatalf("debug log missing %q:\n%s", want, output)
		}
	}
}

// Err records the message, not the error value: an error's dynamic type can
// carry fields no redactor ever sees.
func TestErrAttributeIsRedactedThroughTheLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, Options{Enabled: true})

	logger.Log(context.Background(), "err.test", Err("err", &leakyError{}))

	output := buf.String()
	if strings.Contains(output, fakeToken) {
		t.Fatalf("Err() leaked the token:\n%s", output)
	}
	if !strings.Contains(output, "quota exceeded") {
		t.Fatalf("Err() dropped the useful part of the message:\n%s", output)
	}
	if got := Err("err", nil); got.Value.String() != "" {
		t.Fatalf("Err(nil) = %q, want empty", got.Value.String())
	}
}

func TestURLFieldsDoNotIncludeRawQuery(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, Options{Enabled: true})

	raw := "https://www.googleapis.com/youtube/v3/liveChat/messages?key=AIzaBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB&liveChatId=abc"
	logger.Log(context.Background(), "url.test", URLFields("api_url", raw)...)

	output := buf.String()
	for _, leak := range []string{"AIzaBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", "liveChatId", "messages?"} {
		if strings.Contains(output, leak) {
			t.Fatalf("URL fields leaked %q:\n%s", leak, output)
		}
	}
	for _, want := range []string{
		`"api_url_scheme":"https"`,
		`"api_url_host":"www.googleapis.com"`,
		`"api_url_path":"/youtube/v3/liveChat/messages"`,
		`"api_url_has_credential_marker":true`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("URL fields missing %q:\n%s", want, output)
		}
	}
}

func TestURLFieldsReportAbsentAndUnparseableValues(t *testing.T) {
	fields := URLFields("api_url", "   ")
	if len(fields) != 1 || fields[0].Key != "api_url_present" || fields[0].Value.Bool() {
		t.Fatalf("URLFields(empty) = %+v, want a single false presence field", fields)
	}

	fields = URLFields("api_url", "https://exa mple.com/\x7f")
	found := false
	for _, field := range fields {
		if field.Key == "api_url_parse_error" && field.Value.Bool() {
			found = true
		}
	}
	if !found {
		t.Fatalf("URLFields(malformed) = %+v, want a parse-error field", fields)
	}
}

// leakyError models the realistic failure mode: transport code wraps a URL that
// still carries a credential into an error string.
type leakyError struct{}

func (*leakyError) Error() string {
	return "quota exceeded: https://oauth2.googleapis.com/token?access_token=" + fakeToken
}
