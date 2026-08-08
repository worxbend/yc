package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/worxbend/yc/internal/auth"
)

// Sentinel errors for the API conditions yc must react to differently. Callers
// classify with errors.Is; every wrapper must preserve unwrapping while
// redacting the message.
var (
	// ErrChatEnded is a 400/403 liveChatEnded, or a chatEndedEvent item. It
	// is a clean terminal state, not a failure: history stays on screen.
	ErrChatEnded = errors.New("live chat ended")
	// ErrChatDisabled is a 403 liveChatDisabled: the broadcast has chat
	// turned off.
	ErrChatDisabled = errors.New("live chat disabled")
	// ErrChatNotFound is a 404 liveChatNotFound.
	ErrChatNotFound = errors.New("live chat not found")
	// ErrQuotaExceeded is a 403 quotaExceeded or dailyLimitExceeded. Polling
	// stops entirely until the Pacific reset; it must never trigger a retry
	// storm.
	ErrQuotaExceeded = errors.New("quota exceeded")
	// ErrRateLimited is a 403 rateLimitExceeded on list, or a 429 on insert.
	// It drives the backoff ladder rather than a hard stop.
	ErrRateLimited = errors.New("rate limited")
	// ErrAuthFailed is a 401. It permits exactly one single-flight refresh,
	// then a re-auth prompt - never an infinite refresh loop.
	ErrAuthFailed = errors.New("invalid credentials")
	// ErrNotPermitted is a 403 forbidden or insufficientPermissions: the
	// credential is valid but lacks the scope or the role for this call.
	ErrNotPermitted = errors.New("not permitted")
	// ErrMessageRejected is a 400 messageTextInvalid or a related validation
	// failure on insert.
	ErrMessageRejected = errors.New("message rejected")
	// ErrTransient is a 5xx or a network failure. It drives reconnect with
	// backoff and a visible reconnecting state rather than a frozen UI.
	ErrTransient = errors.New("transient failure")
	// ErrNoCredentials means neither an API key nor an access token was
	// available for a call that requires one.
	ErrNoCredentials = errors.New("no credentials configured")
)

// APIError is a classified Google API error envelope.
//
// Google returns three parallel classification channels and they disagree:
// the legacy error.errors[].reason, the canonical error.status, and the modern
// error.details[] entry whose @type ends in google.rpc.ErrorInfo, whose reason
// is SCREAMING_SNAKE and does not match the legacy string. Classification must
// read all three, which is why all three are preserved here.
//
// Message is already redacted. APIError must never carry a request URL, an
// Authorization header, or any query string, because those carry the API key.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Reason is the legacy error.errors[0].reason, e.g. "liveChatEnded".
	Reason string
	// Status is the canonical error.status, e.g. "PERMISSION_DENIED".
	Status string
	// ErrorInfoReason is the google.rpc.ErrorInfo reason, e.g.
	// "API_KEY_INVALID".
	ErrorInfoReason string
	// Method is the yc endpoint name that was called, for the quota ledger
	// and diagnostics.
	Method string
	// Message is a short, redacted, user-facing detail.
	Message string
	// sentinel is the classified sentinel this error unwraps to.
	sentinel error
}

// Error returns the redacted message.
//
// The format is deliberately assembled from fields rather than from anything
// the transport saw, so there is no path by which a request URL, a query
// string, or an Authorization header can reach it.
func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	if e.Method != "" {
		b.WriteString(e.Method)
		b.WriteString(": ")
	}
	b.WriteString("HTTP ")
	b.WriteString(strconv.Itoa(e.StatusCode))
	if label := e.label(); label != "" {
		b.WriteString(" ")
		b.WriteString(label)
	}
	if e.sentinel != nil {
		b.WriteString(": ")
		b.WriteString(e.sentinel.Error())
	}
	if detail := strings.TrimSpace(e.Message); detail != "" {
		b.WriteString(": ")
		b.WriteString(detail)
	}
	return b.String()
}

// label picks the most specific of the three classification channels for
// display. They disagree by design, so showing all three would be noise.
func (e *APIError) label() string {
	for _, candidate := range []string{e.Reason, e.ErrorInfoReason, e.Status} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// Unwrap returns the classified sentinel so errors.Is works against the
// package's sentinel set.
func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.sentinel
}

// legacyReasons maps error.errors[].reason onto a sentinel. These are the exact
// strings from the per-method error tables in the YouTube reference; anything
// absent falls through to the canonical status and then to the HTTP code, so a
// reason YouTube adds tomorrow degrades into a sane retry policy instead of an
// unhandled state.
var legacyReasons = map[string]error{
	"quotaexceeded":                  ErrQuotaExceeded,
	"dailylimitexceeded":             ErrQuotaExceeded,
	"ratelimitexceeded":              ErrRateLimited,
	"userratelimitexceeded":          ErrRateLimited,
	"livechatended":                  ErrChatEnded,
	"livechatdisabled":               ErrChatDisabled,
	"livechatnotfound":               ErrChatNotFound,
	"livechatmessagenotfound":        ErrChatNotFound,
	"livechatusernotfound":           ErrChatNotFound,
	"livechatbannotfound":            ErrChatNotFound,
	"videonotfound":                  ErrChatNotFound,
	"channelnotfound":                ErrChatNotFound,
	"forbidden":                      ErrNotPermitted,
	"insufficientpermissions":        ErrNotPermitted,
	"modificationnotallowed":         ErrNotPermitted,
	"livechatbaninsertionnotallowed": ErrNotPermitted,
	"autherror":                      ErrAuthFailed,
	"unauthorized":                   ErrAuthFailed,
	"messagetextinvalid":             ErrMessageRejected,
	"messagetextrequired":            ErrMessageRejected,
	"livechatidrequired":             ErrMessageRejected,
	"typerequired":                   ErrMessageRejected,
	"preconditioncheckfailed":        ErrMessageRejected,
	"invalidlivechatid":              ErrMessageRejected,
	"invalidchannelid":               ErrMessageRejected,
	"invalidlivechatbanid":           ErrMessageRejected,
	"banneduserchannelidrequired":    ErrMessageRejected,
	"backenderror":                   ErrTransient,
	"internalerror":                  ErrTransient,
}

// errorInfoReasons maps the modern google.rpc.ErrorInfo reason - which is
// SCREAMING_SNAKE and deliberately does not match the legacy string - onto a
// sentinel.
var errorInfoReasons = map[string]error{
	"API_KEY_INVALID":         ErrAuthFailed,
	"API_KEY_EXPIRED":         ErrAuthFailed,
	"API_KEY_SERVICE_BLOCKED": ErrNotPermitted,
	"ACCESS_TOKEN_EXPIRED":    ErrAuthFailed,
	"CREDENTIALS_MISSING":     ErrAuthFailed,
	"SERVICE_DISABLED":        ErrNotPermitted,
	"CONSUMER_SUSPENDED":      ErrNotPermitted,
	"RATE_LIMIT_EXCEEDED":     ErrRateLimited,
	"QUOTA_EXCEEDED":          ErrQuotaExceeded,
	"DAILY_LIMIT_EXCEEDED":    ErrQuotaExceeded,
}

// canonicalStatuses maps error.status - the google.rpc.Code name - onto a
// sentinel. It is the coarsest of the three channels and is consulted last.
var canonicalStatuses = map[string]error{
	"PERMISSION_DENIED":   ErrNotPermitted,
	"UNAUTHENTICATED":     ErrAuthFailed,
	"NOT_FOUND":           ErrChatNotFound,
	"RESOURCE_EXHAUSTED":  ErrRateLimited,
	"INVALID_ARGUMENT":    ErrMessageRejected,
	"FAILED_PRECONDITION": ErrMessageRejected,
	"UNAVAILABLE":         ErrTransient,
	"INTERNAL":            ErrTransient,
	"DEADLINE_EXCEEDED":   ErrTransient,
	"ABORTED":             ErrTransient,
}

// ClassifyAPIError maps an HTTP status and the three envelope channels onto one
// of the package sentinels. An unrecognized combination classifies as
// ErrTransient for 5xx and ErrNotPermitted for other 4xx, so a new reason
// string degrades into a sane retry policy rather than an unhandled state.
func ClassifyAPIError(statusCode int, reason, status, errorInfoReason string) error {
	if sentinel, ok := legacyReasons[strings.ToLower(strings.TrimSpace(reason))]; ok {
		return sentinel
	}
	if sentinel, ok := errorInfoReasons[strings.ToUpper(strings.TrimSpace(errorInfoReason))]; ok {
		return sentinel
	}
	if sentinel, ok := canonicalStatuses[strings.ToUpper(strings.TrimSpace(status))]; ok {
		return sentinel
	}
	switch {
	case statusCode == http.StatusUnauthorized:
		return ErrAuthFailed
	case statusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case statusCode == http.StatusNotFound:
		return ErrChatNotFound
	case statusCode == http.StatusBadRequest:
		return ErrMessageRejected
	case statusCode >= 500:
		return ErrTransient
	case statusCode >= 400:
		return ErrNotPermitted
	default:
		return ErrTransient
	}
}

// Retryable reports whether an error should be retried on the backoff ladder
// rather than ending the session.
//
// A cancelled context is never retryable: the caller asked yc to stop, and
// retrying would keep spending quota after the user closed the chat.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, ErrTransient) || errors.Is(err, ErrRateLimited)
}

// Terminal reports whether an error ends the chat session cleanly, leaving the
// history readable and offering a re-resolve.
func Terminal(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range []error{
		ErrChatEnded,
		ErrChatDisabled,
		ErrChatNotFound,
		ErrQuotaExceeded,
		ErrAuthFailed,
		ErrNotPermitted,
		ErrNoCredentials,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// safeError reports a redacted message while keeping the original error
// reachable through errors.Is and errors.As.
//
// Replacing an error with errors.New(redact(err.Error())) would also keep
// tokens out of the message, but it throws the cause away, leaving callers
// with nothing but a string to classify by. Redacting the message and
// preserving the chain gives both.
type safeError struct {
	detail string
	cause  error
}

func (e *safeError) Error() string { return e.detail }

func (e *safeError) Unwrap() error { return e.cause }

// newSafeError wraps cause with a message safe to display. Callers are
// responsible for having redacted detail already, and for having passed a cause
// that is itself safe - see transportCause for the transport path.
func newSafeError(detail string, cause error) error {
	if cause == nil {
		return nil
	}
	return &safeError{detail: detail, cause: cause}
}

// transportCause reduces a transport failure to a cause that cannot carry a
// request URL.
//
// net/http wraps every transport failure in a *url.Error whose Error() prints
// the full URL it was dialing, and the YouTube Data API takes its API key as a
// query parameter. That URL is therefore a credential. safeError deliberately
// keeps its cause reachable so callers can classify with errors.Is, which means
// a raw *url.Error retained there puts the key one errors.Unwrap, one
// errors.Join Error(), or one %+v of the chain away from an output path - even
// though safeError.Error() itself never prints it.
//
// So the transport error's text is stripped of its URL, redacted, and rebuilt;
// its identity is dropped. Only the context sentinels survive, because callers
// distinguish "the user cancelled" from "the network failed".
func transportCause(err error, redact func(string) string) error {
	if err == nil {
		return nil
	}
	detail := stripRequestURL(err)
	if redact != nil {
		detail = redact(detail)
	}
	// The pattern redactor runs unconditionally afterwards, so a value the
	// caller's redactor does not hold is still caught by shape.
	cause := errors.New(auth.NewRedactor().Redact(detail))
	switch {
	case errors.Is(err, context.Canceled):
		return errors.Join(cause, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.Join(cause, context.DeadlineExceeded)
	}
	return cause
}

// stripRequestURL renders an error's text without the request URL, keeping the
// operation ("Get", "Post") because it is the only part of the URL worth having.
func stripRequestURL(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Op + ": " + stripRequestURL(urlErr.Err)
	}
	return err.Error()
}
