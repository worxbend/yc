package youtube

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
)

// Classification is the one decision every other policy in yc reads off.
// Retryable drives the backoff ladder, Terminal decides whether the session
// ends with history intact, and the quota mode switch keys off ErrQuotaExceeded
// specifically. A reason that falls through to the wrong sentinel therefore
// does not produce a wrong error message - it produces a retry storm against an
// exhausted quota, or a chat that ends because it was briefly rate limited.
//
// TestClientClassifiesEveryDocumentedReason covers the reasons a human thought
// to list. These tests instead walk the tables themselves, so a reason added to
// the map without a matching sentinel decision fails here rather than at 3am on
// somebody's stream.

// expectedLegacyReason is the sentinel each legacy reason must classify to,
// written out independently of the map under test.
//
// This is deliberately a second copy rather than a loop over legacyReasons
// comparing it to itself: a table test that reads its expectations from the
// thing it is testing proves only that a map is a map. The completeness check
// below is what keeps the two in step.
var expectedLegacyReason = map[string]error{
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

var expectedErrorInfoReason = map[string]error{
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

var expectedCanonicalStatus = map[string]error{
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

// TestEveryClassificationTableEntryIsAccountedFor fails when a channel gains an
// entry that no test has an opinion about, in either direction.
func TestEveryClassificationTableEntryIsAccountedFor(t *testing.T) {
	for _, table := range []struct {
		name     string
		actual   map[string]error
		expected map[string]error
	}{
		{"legacy errors[].reason", legacyReasons, expectedLegacyReason},
		{"google.rpc.ErrorInfo reason", errorInfoReasons, expectedErrorInfoReason},
		{"canonical error.status", canonicalStatuses, expectedCanonicalStatus},
	} {
		t.Run(table.name, func(t *testing.T) {
			for key := range table.actual {
				if _, ok := table.expected[key]; !ok {
					t.Errorf("%q was added to the table with no test deciding what it means", key)
				}
			}
			for key := range table.expected {
				if _, ok := table.actual[key]; !ok {
					t.Errorf("%q is expected by the tests but no longer classified", key)
				}
			}
		})
	}
}

// TestEveryLegacyReasonClassifies drives each reason through the exported
// entry point on its own, with a deliberately unhelpful HTTP status, so the
// assertion is about the reason string and not about the status fallback.
func TestEveryLegacyReasonClassifies(t *testing.T) {
	for reason, want := range expectedLegacyReason {
		t.Run(reason, func(t *testing.T) {
			// 418 is in no fallback branch except "some other 4xx", so a
			// reason that failed to match would classify as ErrNotPermitted
			// and be caught here rather than passing by coincidence.
			if got := ClassifyAPIError(http.StatusTeapot, reason, "", ""); !errors.Is(got, want) {
				t.Fatalf("ClassifyAPIError(%q) = %v, want %v", reason, got, want)
			}
			// YouTube sends lowerCamelCase; the lookup lowercases. Both the
			// wire shape and a shouted variant must land in the same place.
			if got := ClassifyAPIError(http.StatusTeapot, strings.ToUpper(reason), "", ""); !errors.Is(got, want) {
				t.Fatalf("ClassifyAPIError(%q) = %v, want the same sentinel as the lowercase form", strings.ToUpper(reason), got)
			}
			if got := ClassifyAPIError(http.StatusTeapot, "  "+reason+"\t", "", ""); !errors.Is(got, want) {
				t.Fatalf("ClassifyAPIError(padded %q) = %v, want %v", reason, got, want)
			}
		})
	}
}

func TestEveryErrorInfoReasonClassifies(t *testing.T) {
	for reason, want := range expectedErrorInfoReason {
		t.Run(reason, func(t *testing.T) {
			if got := ClassifyAPIError(http.StatusTeapot, "", "", reason); !errors.Is(got, want) {
				t.Fatalf("ClassifyAPIError(errorInfo %q) = %v, want %v", reason, got, want)
			}
			if got := ClassifyAPIError(http.StatusTeapot, "", "", strings.ToLower(reason)); !errors.Is(got, want) {
				t.Fatalf("ClassifyAPIError(errorInfo %q lowercased) = %v, want %v", reason, got, want)
			}
		})
	}
}

func TestEveryCanonicalStatusClassifies(t *testing.T) {
	for status, want := range expectedCanonicalStatus {
		t.Run(status, func(t *testing.T) {
			if got := ClassifyAPIError(http.StatusTeapot, "", status, ""); !errors.Is(got, want) {
				t.Fatalf("ClassifyAPIError(status %q) = %v, want %v", status, got, want)
			}
		})
	}
}

// TestClassificationPrefersTheMostSpecificChannel pins the precedence, which is
// the whole reason all three channels are decoded.
//
// Google populates them simultaneously and they disagree: an invalid API key
// arrives as reason "forbidden", status "INVALID_ARGUMENT" and ErrorInfo
// "API_KEY_INVALID", and only one of those three is the truth. Reading them in
// the wrong order turns a credential problem into a permissions problem, which
// sends the user to the Cloud console to check scopes they already have.
func TestClassificationPrefersTheMostSpecificChannel(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		reason     string
		status     string
		errorInfo  string
		want       error
		because    string
	}{
		{
			name: "legacy reason beats ErrorInfo and status",
			// A chat that ended is reported with a forbidden-shaped
			// canonical status. Only the legacy reason says it ended
			// cleanly, and that distinction is the difference between
			// "chat is over" and "you are not allowed in here".
			statusCode: 403, reason: "liveChatEnded", status: "PERMISSION_DENIED", errorInfo: "SERVICE_DISABLED",
			want: ErrChatEnded, because: "the legacy reason is the most specific channel",
		},
		{
			name:       "ErrorInfo beats the canonical status",
			statusCode: 400, errorInfo: "API_KEY_INVALID", status: "INVALID_ARGUMENT",
			want: ErrAuthFailed, because: "an invalid key is a credential failure, not a bad argument",
		},
		{
			name:       "canonical status is consulted last",
			statusCode: 400, status: "FAILED_PRECONDITION",
			want: ErrMessageRejected, because: "nothing more specific was sent",
		},
		{
			name:       "an unknown reason falls through rather than swallowing the request",
			statusCode: 403, reason: "somethingYouTubeAddedThisMorning", status: "PERMISSION_DENIED",
			want: ErrNotPermitted, because: "an unmapped reason must degrade to the next channel",
		},
		{
			name:       "an unknown ErrorInfo falls through to the status",
			statusCode: 503, errorInfo: "BRAND_NEW_REASON", status: "UNAVAILABLE",
			want: ErrTransient, because: "an unmapped ErrorInfo must not strand the status",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyAPIError(test.statusCode, test.reason, test.status, test.errorInfo)
			if !errors.Is(got, test.want) {
				t.Fatalf("ClassifyAPIError = %v, want %v because %s", got, test.want, test.because)
			}
		})
	}
}

// TestClassificationFallsBackToTheHTTPStatus covers the branch taken when an
// intercepting proxy, a captive portal, or a Google outage answers with
// something that is not a Google error envelope at all.
func TestClassificationFallsBackToTheHTTPStatus(t *testing.T) {
	tests := []struct {
		statusCode int
		want       error
	}{
		{http.StatusUnauthorized, ErrAuthFailed},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusNotFound, ErrChatNotFound},
		{http.StatusBadRequest, ErrMessageRejected},
		{http.StatusForbidden, ErrNotPermitted},
		{http.StatusTeapot, ErrNotPermitted},
		{http.StatusInternalServerError, ErrTransient},
		{http.StatusBadGateway, ErrTransient},
		{http.StatusServiceUnavailable, ErrTransient},
		{http.StatusGatewayTimeout, ErrTransient},
		// Not a failure status at all. decodeError is only reached for
		// non-2xx, so this is the "something is very wrong" branch; it must
		// still produce a sentinel the ladder can act on rather than nil.
		{0, ErrTransient},
		{http.StatusOK, ErrTransient},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%d", test.statusCode), func(t *testing.T) {
			got := ClassifyAPIError(test.statusCode, "", "", "")
			if !errors.Is(got, test.want) {
				t.Fatalf("ClassifyAPIError(%d) = %v, want %v", test.statusCode, got, test.want)
			}
			if got == nil {
				t.Fatal("ClassifyAPIError returned nil; every failure must carry a sentinel")
			}
		})
	}
}

// sentinelPolicy is what the poll loop does with a sentinel.
type sentinelPolicy int

const (
	// policyRetry goes on the backoff ladder.
	policyRetry sentinelPolicy = iota
	// policyTerminal ends the session cleanly, history intact.
	policyTerminal
	// policyReported is neither. handleListError's default arm ends the
	// session and shows the detail, which is the right answer for a
	// validation failure: a rejected request is not going to be accepted on
	// the third attempt, and it is not a state the chat can recover from
	// either. It is spelled out here rather than left implicit because
	// "neither retryable nor terminal" reads like an oversight and is not.
	policyReported
)

// expectedSentinelPolicy forces a decision for every sentinel. A new one added
// without an entry fails the completeness check below, which is the point: the
// poll loop's behavior for an unclassified error is to end the session, and
// that should be a choice somebody made rather than a fallthrough nobody saw.
var expectedSentinelPolicy = map[error]sentinelPolicy{
	ErrTransient:       policyRetry,
	ErrRateLimited:     policyRetry,
	ErrChatEnded:       policyTerminal,
	ErrChatDisabled:    policyTerminal,
	ErrChatNotFound:    policyTerminal,
	ErrQuotaExceeded:   policyTerminal,
	ErrAuthFailed:      policyTerminal,
	ErrNotPermitted:    policyTerminal,
	ErrNoCredentials:   policyTerminal,
	ErrMessageRejected: policyReported,
}

// TestEverySentinelHasAPolicy is the check that matters most: no sentinel may
// be both retryable and terminal, and every one a table can produce must have a
// decision recorded for it.
//
// A sentinel that is both is a session that ends and retries at the same time.
// One with no decision is a reason string that reaches the poll loop and takes
// whatever the default arm does, which is a policy chosen by accident.
func TestEverySentinelHasAPolicy(t *testing.T) {
	classified := map[error]bool{ErrNoCredentials: true}
	for _, table := range []map[string]error{legacyReasons, errorInfoReasons, canonicalStatuses} {
		for _, sentinel := range table {
			classified[sentinel] = true
		}
	}

	for sentinel := range classified {
		t.Run(sentinel.Error(), func(t *testing.T) {
			want, ok := expectedSentinelPolicy[sentinel]
			if !ok {
				t.Fatalf("%v is classified by a table but no test decides how the poll loop treats it", sentinel)
			}
			retryable, terminal := Retryable(sentinel), Terminal(sentinel)
			if retryable && terminal {
				t.Fatalf("%v is both retryable and terminal; the poll loop cannot obey both", sentinel)
			}
			switch want {
			case policyRetry:
				if !retryable {
					t.Fatalf("%v must go on the backoff ladder", sentinel)
				}
			case policyTerminal:
				if !terminal {
					t.Fatalf("%v must end the session cleanly", sentinel)
				}
			case policyReported:
				if retryable || terminal {
					t.Fatalf("%v is expected to be neither retryable nor terminal", sentinel)
				}
			}
		})
	}

	// And nothing in the expected set may have quietly stopped being
	// reachable, which would mean a condition yc claims to handle no longer
	// has a classification path.
	for sentinel := range expectedSentinelPolicy {
		if sentinel == ErrNoCredentials {
			continue
		}
		if !classified[sentinel] {
			t.Errorf("%v has a policy but no table produces it any more", sentinel)
		}
	}
}

// A canceled context is never retryable however it is wrapped: the user asked
// yc to stop, and a ladder that kept climbing would keep spending quota after
// the chat pane closed.
func TestCancellationIsNeverRetryable(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		errors.Join(ErrTransient, context.Canceled),
		errors.Join(ErrRateLimited, context.DeadlineExceeded),
		newSafeError("liveChatMessages.list: request failed", errors.Join(ErrTransient, context.Canceled)),
	} {
		if Retryable(err) {
			t.Errorf("Retryable(%v) = true, want false for a canceled caller", err)
		}
	}
	if Retryable(nil) || Terminal(nil) {
		t.Error("a nil error is neither retryable nor terminal")
	}
}

// TestAPIErrorLabelPrefersTheMostSpecificChannelToo mirrors the classification
// order in what the user is shown, so the label and the behavior cannot
// disagree.
func TestAPIErrorLabelPrefersTheMostSpecificChannel(t *testing.T) {
	err := &APIError{
		StatusCode:      403,
		Reason:          "liveChatEnded",
		Status:          "PERMISSION_DENIED",
		ErrorInfoReason: "SERVICE_DISABLED",
		Method:          EndpointMessagesList,
		sentinel:        ErrChatEnded,
	}
	text := err.Error()
	if !strings.Contains(text, "liveChatEnded") {
		t.Fatalf("Error() = %q, want the legacy reason as the label", text)
	}
	if strings.Contains(text, "PERMISSION_DENIED") || strings.Contains(text, "SERVICE_DISABLED") {
		t.Fatalf("Error() = %q, want only the most specific channel shown", text)
	}

	// With no legacy reason the ErrorInfo channel is next, then the status.
	err.Reason = ""
	if text := err.Error(); !strings.Contains(text, "SERVICE_DISABLED") {
		t.Fatalf("Error() = %q, want the ErrorInfo reason once the legacy one is absent", text)
	}
	err.ErrorInfoReason = ""
	if text := err.Error(); !strings.Contains(text, "PERMISSION_DENIED") {
		t.Fatalf("Error() = %q, want the canonical status as the last resort", text)
	}
	err.Status = ""
	if text := err.Error(); !strings.Contains(text, "HTTP 403") {
		t.Fatalf("Error() = %q, want the HTTP status when no channel was sent", text)
	}
}

// A nil *APIError must answer rather than panic: it is reached through
// errors.As, which hands the caller a typed nil whenever the chain has none.
func TestNilAPIErrorIsInert(t *testing.T) {
	var err *APIError
	if got := err.Error(); got != "" {
		t.Fatalf("(*APIError)(nil).Error() = %q, want empty", got)
	}
	if got := err.Unwrap(); got != nil {
		t.Fatalf("(*APIError)(nil).Unwrap() = %v, want nil", got)
	}
}

// TestClassificationSurvivesTheWholeTransport walks a representative reason from
// each channel through a real response body, because the tables above are only
// correct if classify() actually reaches them with the strings YouTube sends.
func TestClassificationSurvivesTheWholeTransport(t *testing.T) {
	tests := []struct {
		name      string
		code      int
		reason    string
		status    string
		errorInfo string
		want      error
	}{
		{"legacy channel", 403, "userRateLimitExceeded", "", "", ErrRateLimited},
		{"errorinfo channel", 403, "", "PERMISSION_DENIED", "CONSUMER_SUSPENDED", ErrNotPermitted},
		{"canonical channel", 504, "", "DEADLINE_EXCEEDED", "", ErrTransient},
		{"video not found", 404, "videoNotFound", "NOT_FOUND", "", ErrChatNotFound},
		{"channel not found", 404, "channelNotFound", "NOT_FOUND", "", ErrChatNotFound},
		{"backend error", 500, "backendError", "INTERNAL", "", ErrTransient},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.code)
				fmt.Fprint(w, googleError(test.code, test.reason, test.status, test.errorInfo, "the API said no"))
			})
			_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

// A 3xx is never a legitimate answer from the JSON API. The production HTTP
// client declines to follow one - net/http strips Authorization across hosts
// but nothing strips the API key out of a query string a Location happens to
// preserve - so the 3xx arrives at the transport, which must name it rather
// than let it decay into a transient error the ladder would retry forever. The
// Location header is attacker controlled and must not be quoted.
//
// This is run against NewAPIHTTPClient rather than httptest's own client
// precisely because the redirect policy is the thing under test; a client that
// followed the redirect would never reach the branch.
func TestRedirectIsRefusedAndNotQuoted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://evil.example/steal?key="+redactionTestKey)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(ClientConfig{
		Credentials: StaticCredentials{Key: auth.NewSecret(redactionTestKey)},
		Endpoint:    server.URL,
		HTTPClient:  NewAPIHTTPClient(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}

	_, listErr := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if !errors.Is(listErr, ErrNotPermitted) {
		t.Fatalf("error = %v, want ErrNotPermitted for a redirect", listErr)
	}
	if Retryable(listErr) {
		t.Fatal("a redirect must not be retried; the ladder would spend the day on it")
	}
	if text := errorChainText(listErr); strings.Contains(text, "evil.example") || strings.Contains(text, "AIza") {
		t.Fatalf("the redirect target reached the error chain: %s", text)
	}
}

// The redirect refusal is a property of the client the production path builds,
// so it is asserted directly as well: a future change that drops CheckRedirect
// would otherwise only show up as the test above passing for the wrong reason.
func TestAPIHTTPClientRefusesToFollowRedirects(t *testing.T) {
	client := NewAPIHTTPClient(0)
	if client.CheckRedirect == nil {
		t.Fatal("the API HTTP client follows redirects; the API key rides in the query string")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
	if client.Timeout <= 0 {
		t.Fatal("the API HTTP client has no timeout; a hung request would outlive the poll interval")
	}
}
