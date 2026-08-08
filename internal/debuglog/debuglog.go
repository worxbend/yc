package debuglog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/worxbend/yc/internal/auth"
)

// Redacted is the placeholder every removed credential leaves behind. It is the
// same marker auth uses, so a redacted debug line and a redacted user-facing
// error read identically.
const Redacted = auth.RedactedSecret

// rawURLPattern finds an absolute URL anywhere inside a free-text field. A URL
// that reaches a log through an error message still has to lose its userinfo.
var rawURLPattern = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)

// Options configures a Logger.
type Options struct {
	// Enabled gates every write. Debug logging is opt-in.
	Enabled bool
	// Redactor removes known secrets and credential patterns from every
	// attribute before it is written.
	Redactor auth.Redactor
}

// Logger writes redacted JSON lines.
//
// The zero Logger is valid and discards everything, so callers never need a nil
// check. Callers pass curated fields; raw structs, transport events, HTTP
// headers, OAuth callback queries, and unfiltered errors from auth, storage, or
// network code must never be handed to it.
type Logger struct {
	w        io.Writer
	enabled  bool
	redactor auth.Redactor
	// secrets accumulates the values handed to WithSecrets so a derived
	// logger can rebuild its redactor. auth.Redactor is immutable by
	// design, so extending one means constructing a new one.
	secrets []auth.Secret
	extra   auth.Redactor
	handler slog.Handler
}

// New returns a logger writing JSON lines to w.
//
// A disabled logger and a nil writer both return the zero Logger, which keeps
// "debug logging is off" a single state rather than two that behave alike.
func New(w io.Writer, opts Options) Logger {
	if !opts.Enabled || w == nil {
		return Logger{}
	}
	return Logger{
		w:        w,
		enabled:  true,
		redactor: opts.Redactor,
		handler:  slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}),
	}
}

// Enabled reports whether the logger writes anything.
func (l Logger) Enabled() bool {
	return l.enabled && l.handler != nil && l.w != nil
}

// WithSecrets returns a copy of the logger that also redacts the given secrets.
func (l Logger) WithSecrets(secrets ...auth.Secret) Logger {
	if len(secrets) == 0 {
		return l
	}
	l.secrets = append(append([]auth.Secret(nil), l.secrets...), secrets...)
	l.extra = auth.NewRedactor(l.secrets...)
	return l
}

// Redact applies the logger's redaction to a value, for callers assembling a
// user-facing string from the same material.
func (l Logger) Redact(value string) string {
	if value == "" {
		return ""
	}
	value = l.redactor.Redact(value)
	value = l.extra.Redact(value)
	return redactURLsInText(value)
}

// Log writes one event with curated attributes.
func (l Logger) Log(ctx context.Context, event string, attrs ...slog.Attr) {
	if !l.Enabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	safeAttrs := make([]slog.Attr, 0, len(attrs)+1)
	safeAttrs = append(safeAttrs, slog.String("event", l.Redact(event)))
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		safeAttrs = append(safeAttrs, l.sanitizeAttr(attr))
	}
	slog.New(l.handler).LogAttrs(ctx, slog.LevelDebug, "debug", safeAttrs...)
}

// Err returns a redaction-safe attribute for an error. It records the error's
// message through the redactor rather than the error value itself, because an
// error's dynamic type can carry fields no redactor sees.
func Err(key string, err error) slog.Attr {
	if err == nil {
		return slog.String(key, "")
	}
	return slog.String(key, err.Error())
}

// URLFields returns redacted attributes describing a URL: scheme, host, and
// path only. Query strings are never emitted, because the API key travels in
// one.
func URLFields(prefix, raw string) []slog.Attr {
	raw = strings.TrimSpace(raw)
	fields := []slog.Attr{slog.Bool(prefix+"_present", raw != "")}
	if raw == "" {
		return fields
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		fields = append(fields, slog.Bool(prefix+"_parse_error", true))
		return fields
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if scheme != "" {
		fields = append(fields, slog.String(prefix+"_scheme", scheme))
	}
	if host != "" {
		fields = append(fields, slog.String(prefix+"_host", host))
	}
	// The path is the only part of a YouTube Data API URL that identifies
	// the call; it is emitted without the query, which is where the key,
	// the page token, and the live chat ID live.
	if path := strings.TrimSpace(parsed.EscapedPath()); path != "" {
		fields = append(fields, slog.String(prefix+"_path", path))
	}
	fields = append(fields,
		slog.Bool(prefix+"_has_userinfo", parsed.User != nil),
		slog.Bool(prefix+"_has_credential_marker", urlHasCredentialMarker(parsed)),
	)
	return fields
}

// sanitizeAttr redacts one attribute. Groups recurse; anything typed as Any is
// replaced by its type name, because a struct's fields are exactly the material
// this package exists to keep out of the log.
func (l Logger) sanitizeAttr(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(l.Redact(attr.Value.String()))
	case slog.KindGroup:
		group := attr.Value.Group()
		safe := make([]slog.Attr, 0, len(group))
		for _, child := range group {
			if child.Key == "" {
				continue
			}
			safe = append(safe, l.sanitizeAttr(child))
		}
		attr.Value = slog.GroupValue(safe...)
	case slog.KindAny:
		attr.Value = slog.StringValue(l.safeAnyValue(attr.Value.Any()))
	}
	return attr
}

func (l Logger) safeAnyValue(value any) string {
	if value == nil {
		return ""
	}
	if err, ok := value.(error); ok {
		return l.Redact(err.Error())
	}
	typ := reflect.TypeOf(value)
	if typ == nil {
		return ""
	}
	return fmt.Sprintf("<%s>", typ.String())
}

func redactURLsInText(value string) string {
	return rawURLPattern.ReplaceAllStringFunc(value, redactURLToken)
}

// redactURLToken strips userinfo from an absolute URL found in free text. The
// query is left to the credential patterns, which name the parameters yc cares
// about; blanking every query outright would erase the diagnostics that make a
// transport log worth reading.
func redactURLToken(value string) string {
	trimmed := strings.TrimRight(value, ".,);]")
	suffix := strings.TrimPrefix(value, trimmed)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.User == nil {
		return value
	}
	clone := *parsed
	clone.User = url.User(Redacted)
	return clone.String() + suffix
}

func urlHasCredentialMarker(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.User != nil {
		return true
	}
	values := []string{parsed.RawQuery, parsed.EscapedPath(), parsed.Fragment}
	for _, value := range values {
		if containsCredentialMarker(value) {
			return true
		}
		if unescaped, err := url.QueryUnescape(value); err == nil && containsCredentialMarker(unescaped) {
			return true
		}
		if unescaped, err := url.PathUnescape(value); err == nil && containsCredentialMarker(unescaped) {
			return true
		}
	}
	return false
}

// credentialMarkers are the substrings that mean a URL carried something yc
// must never record. They cover the Google installed-app flow, the Data API
// key, and the bearer header shape.
var credentialMarkers = []string{
	"access_token",
	"refresh_token",
	"id_token",
	"client_secret",
	"api_key",
	"apikey",
	"key=",
	"authorization_code",
	"code_verifier",
	"code_challenge",
	"authorization",
	"bearer ",
	"state=",
	"state:",
	"code=",
	"code:",
}

func containsCredentialMarker(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range credentialMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
