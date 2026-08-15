package auth

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Sentinel errors for the auth boundary. Callers match with errors.Is instead
// of reading messages, because every message that reaches a user has already
// been through a Redactor and may have lost detail.
var (
	// ErrLoginRequired reports that stored credentials can no longer be
	// refreshed. Google answers a revoked or expired refresh token with
	// invalid_grant, and only a new interactive login recovers from it.
	ErrLoginRequired = errors.New("interactive login is required")
	// ErrInvalidToken reports that a token was rejected as invalid or
	// expired by Google's tokeninfo endpoint.
	ErrInvalidToken = errors.New("access token is invalid or expired")
	// ErrMissingScope reports that a credential is missing a scope the
	// requested operation requires.
	ErrMissingScope = errors.New("missing required OAuth scope")
	// ErrLoginDenied reports that the user declined the authorization
	// request, or Google refused it.
	ErrLoginDenied = errors.New("authorization was denied")
	// ErrLoginTimeout reports that no OAuth callback arrived before the
	// loopback listener's deadline.
	ErrLoginTimeout = errors.New("timed out waiting for the OAuth callback")
	// ErrStateMismatch reports a callback whose state did not match the one
	// yc generated, which is the CSRF check failing.
	ErrStateMismatch = errors.New("OAuth state did not match")
)

// redactedError is an error whose message has already been redacted and whose
// Error/GoString can therefore be printed anywhere.
//
// It keeps a curated match list rather than the original cause so that
// errors.Is still works for the states callers act on, without any chance of a
// wrapped transport error re-introducing a URL, a header, or a query string
// into a formatted message.
type redactedError struct {
	message string
	matches []error
}

func (e redactedError) Error() string { return e.message }

// GoString keeps %#v from printing the struct's fields verbatim.
func (e redactedError) GoString() string { return e.message }

func (e redactedError) Is(target error) bool {
	for _, match := range e.matches {
		if errors.Is(match, target) {
			return true
		}
	}
	return false
}

// safeError wraps err into a redacted error carrying action for context.
//
// Only context cancellation and the auth sentinels survive as match targets;
// the original error's text is redacted and its identity is deliberately
// dropped so nothing downstream can unwrap back to a transport error holding a
// request URL.
func safeError(action string, err error, redactor Redactor) error {
	if err == nil {
		return nil
	}
	var matched []error
	switch {
	case errors.Is(err, context.Canceled):
		matched = append(matched, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		matched = append(matched, context.DeadlineExceeded)
	}
	return redactedError{
		message: redactAll(redactor, action+": "+safeErrorDetail(err)),
		matches: matched,
	}
}

// safeErrorDetail renders an error's text without the request URL.
//
// net/http wraps every transport failure in a *url.Error whose Error() prints
// the full URL it was dialing. Dropping the identity of the cause, as safeError
// does, is not enough on its own: the URL is in the message text. A URL in an
// error is a credential in an error whenever anything sensitive rides in the
// query string, and it is the endpoint layout regardless, which is why
// statusError already refuses to quote one.
func safeErrorDetail(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		// Keep the operation ("Post", "Get") for context and drop the URL.
		return urlErr.Op + ": " + safeErrorDetail(urlErr.Err)
	}
	return err.Error()
}

// safeErrorf builds a redacted error from a message yc composed itself.
func safeErrorf(action, detail string, redactor Redactor, matches ...error) error {
	message := action
	if strings.TrimSpace(detail) != "" {
		message += ": " + strings.TrimSpace(detail)
	}
	return redactedError{message: redactAll(redactor, message), matches: matches}
}

// statusError renders an HTTP failure without the request URL, the request
// headers, or the response body's credential-shaped fields.
func statusError(action string, statusCode int, detail string, redactor Redactor, matches ...error) error {
	message := action + ": Google returned HTTP " + strconv.Itoa(statusCode)
	if strings.TrimSpace(detail) != "" {
		message += ": " + strings.TrimSpace(detail)
	}
	return redactedError{message: redactAll(redactor, message), matches: matches}
}

// missingScopeError names the scopes the user has to approve, which are public
// URLs and safe to print.
func missingScopeError(source string, missing, required []Scope) error {
	message := source + " is missing required OAuth scope(s): " +
		strings.Join(SortedScopeValues(missing), ", ") +
		"; run `yc login` again and approve: " +
		strings.Join(SortedScopeValues(required), ", ")
	return redactedError{message: message, matches: []error{ErrMissingScope}}
}

// redactAll applies the caller's redactor and then the pattern-only redactor,
// so a value the caller did not know about is still caught by shape.
func redactAll(redactor Redactor, message string) string {
	return NewRedactor().Redact(redactor.Redact(message))
}
