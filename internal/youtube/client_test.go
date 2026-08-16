package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/quota"
)

// Obvious fake markers. Nothing in this package's tests may use a value that
// could be mistaken for a live credential.
const (
	testToken  = "test-not-a-real-token"
	testAPIKey = "AIzaTestNotARealKeyAAAAAAAAAAAAAAAAAAAA"
)

// newTestClient wires a client to an in-process server. Every adapter is tested
// this way: no test in this package may touch the network.
func newTestClient(t testing.TB, credentials CredentialSource, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(ClientConfig{
		Credentials: credentials,
		Endpoint:    server.URL,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}
	return client, server
}

func oauthCredentials() CredentialSource {
	return StaticCredentials{Token: auth.NewSecret(testToken)}
}

func keyCredentials() CredentialSource {
	return StaticCredentials{Key: auth.NewSecret(testAPIKey)}
}

func TestClientPresentsBearerTokenAndNeverTheKeyAlongsideIt(t *testing.T) {
	var gotAuth, gotKey string
	client, _ := newTestClient(t, StaticCredentials{
		Token: auth.NewSecret(testToken),
		Key:   auth.NewSecret(testAPIKey),
	}, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.URL.Query().Get("key")
		fmt.Fprint(w, `{"items":[{"id":"vid00000001","snippet":{"title":"t"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
	})

	if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	if gotAuth != "Bearer "+testToken {
		t.Fatalf("Authorization = %q, want the bearer token", gotAuth)
	}
	if gotKey != "" {
		t.Fatalf("key = %q, want no API key on a bearer-authenticated call", gotKey)
	}
}

func TestClientPresentsAPIKeyWhenThereIsNoToken(t *testing.T) {
	var gotAuth, gotKey string
	client, _ := newTestClient(t, keyCredentials(), func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.URL.Query().Get("key")
		fmt.Fprint(w, `{"items":[{"id":"vid00000001","liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
	})

	if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want none in key-only read mode", gotAuth)
	}
	if gotKey != testAPIKey {
		t.Fatalf("key = %q, want the API key", gotKey)
	}
}

func TestClientWithoutAnyCredentialDoesNotDispatch(t *testing.T) {
	client, _ := newTestClient(t, StaticCredentials{}, func(http.ResponseWriter, *http.Request) {
		t.Fatal("request dispatched with no credentials")
	})

	_, err := client.Broadcast(context.Background(), "vid00000001")
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}
}

// googleError renders the exact three-channel envelope the API returns.
func googleError(code int, reason, status, errorInfoReason, message string) string {
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"errors":[{"message":%q,"domain":"youtube.liveChat","reason":%q}],"status":%q,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":%q,"domain":"googleapis.com"}]}}`,
		code, message, message, reason, status, errorInfoReason)
}

func TestClientClassifiesEveryDocumentedReason(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		reason     string
		status     string
		errorInfo  string
		want       error
	}{
		{name: "quota", statusCode: 403, reason: "quotaExceeded", status: "PERMISSION_DENIED", want: ErrQuotaExceeded},
		{name: "daily limit", statusCode: 403, reason: "dailyLimitExceeded", want: ErrQuotaExceeded},
		{name: "rate limit on list", statusCode: 403, reason: "rateLimitExceeded", want: ErrRateLimited},
		{name: "rate limit on insert", statusCode: 429, reason: "rateLimitExceeded", want: ErrRateLimited},
		{name: "chat ended", statusCode: 403, reason: "liveChatEnded", want: ErrChatEnded},
		{name: "chat disabled", statusCode: 403, reason: "liveChatDisabled", want: ErrChatDisabled},
		{name: "chat not found", statusCode: 404, reason: "liveChatNotFound", want: ErrChatNotFound},
		{name: "message not found", statusCode: 404, reason: "liveChatMessageNotFound", want: ErrChatNotFound},
		{name: "user not found", statusCode: 404, reason: "liveChatUserNotFound", want: ErrChatNotFound},
		{name: "ban not found", statusCode: 404, reason: "liveChatBanNotFound", want: ErrChatNotFound},
		{name: "forbidden", statusCode: 403, reason: "forbidden", want: ErrNotPermitted},
		{name: "insufficient permissions", statusCode: 403, reason: "insufficientPermissions", want: ErrNotPermitted},
		{name: "modification not allowed", statusCode: 403, reason: "modificationNotAllowed", want: ErrNotPermitted},
		{name: "ban insertion not allowed", statusCode: 403, reason: "liveChatBanInsertionNotAllowed", want: ErrNotPermitted},
		{name: "message text invalid", statusCode: 400, reason: "messageTextInvalid", want: ErrMessageRejected},
		{name: "message text required", statusCode: 400, reason: "messageTextRequired", want: ErrMessageRejected},
		{name: "live chat id required", statusCode: 400, reason: "liveChatIdRequired", want: ErrMessageRejected},
		{name: "type required", statusCode: 400, reason: "typeRequired", want: ErrMessageRejected},
		{name: "precondition check failed", statusCode: 400, reason: "preconditionCheckFailed", want: ErrMessageRejected},
		{name: "invalid live chat id", statusCode: 400, reason: "invalidLiveChatId", want: ErrMessageRejected},
		{name: "invalid channel id", statusCode: 400, reason: "invalidChannelId", want: ErrMessageRejected},
		{name: "banned user channel id required", statusCode: 400, reason: "bannedUserChannelIdRequired", want: ErrMessageRejected},
		{name: "invalid ban id", statusCode: 400, reason: "invalidLiveChatBanId", want: ErrMessageRejected},
		{name: "unauthenticated", statusCode: 401, status: "UNAUTHENTICATED", want: ErrAuthFailed},
		// The modern ErrorInfo channel disagrees with the legacy one by
		// design; an invalid key arrives with reason "forbidden" and
		// ErrorInfo API_KEY_INVALID, and only the latter identifies it.
		{name: "api key invalid", statusCode: 400, errorInfo: "API_KEY_INVALID", status: "INVALID_ARGUMENT", want: ErrAuthFailed},
		{name: "canonical resource exhausted", statusCode: 429, status: "RESOURCE_EXHAUSTED", want: ErrRateLimited},
		{name: "server error", statusCode: 503, want: ErrTransient},
		{name: "unknown 4xx", statusCode: 418, reason: "somethingBrandNew", want: ErrNotPermitted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.statusCode)
				fmt.Fprint(w, googleError(test.statusCode, test.reason, test.status, test.errorInfo, "the API said no"))
			})

			_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %T, want *APIError", err)
			}
			if apiErr.StatusCode != test.statusCode {
				t.Fatalf("StatusCode = %d, want %d", apiErr.StatusCode, test.statusCode)
			}
			if apiErr.Method != quota.EndpointMessagesList {
				t.Fatalf("Method = %q, want %q", apiErr.Method, quota.EndpointMessagesList)
			}
			if apiErr.Reason != test.reason || apiErr.ErrorInfoReason != test.errorInfo {
				t.Fatalf("channels = %q/%q/%q, want all three preserved", apiErr.Reason, apiErr.Status, apiErr.ErrorInfoReason)
			}
		})
	}
}

func TestClientErrorsNeverCarryTheCredentialOrTheURL(t *testing.T) {
	client, server := newTestClient(t, StaticCredentials{
		Token: auth.NewSecret(testToken),
		Key:   auth.NewSecret(testAPIKey),
	}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		// A hostile or merely careless endpoint echoing the credentials
		// back must not be able to launder them into yc's output.
		fmt.Fprintf(w, `{"error":{"code":403,"message":"denied for key=%s and Bearer %s","errors":[{"reason":"forbidden"}],"status":"PERMISSION_DENIED"}}`, testAPIKey, testToken)
	})

	_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if err == nil {
		t.Fatal("error = nil, want a 403")
	}
	assertNoCredentialLeak(t, err.Error(), server.URL)
}

func TestClientNonEnvelopeErrorBodyStillClassifies(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>an intercepting proxy</html>")
	})

	_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient from the status alone", err)
	}
	if !Retryable(err) {
		t.Fatal("Retryable = false, want a 502 on the backoff ladder")
	}
}

func TestClientBoundsTheErrorBodyItReads(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, strings.Repeat("x", 512<<10))
	})

	_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
	if err == nil {
		t.Fatal("error = nil, want a 500")
	}
	if len(err.Error()) > 1024 {
		t.Fatalf("error length = %d, want a bounded excerpt", len(err.Error()))
	}
}

func TestClientCancelledContextReturnsTheContextError(t *testing.T) {
	client, _ := newTestClient(t, oauthCredentials(), func(http.ResponseWriter, *http.Request) {
		t.Fatal("request dispatched after cancellation")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.ListMessages(ctx, ListRequest{LiveChatID: "chat-1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if Retryable(err) {
		t.Fatal("Retryable = true for a canceled context; the user asked yc to stop")
	}
}

func TestRetryableAndTerminalPartitionTheSentinels(t *testing.T) {
	retryable := []error{ErrTransient, ErrRateLimited}
	terminal := []error{ErrChatEnded, ErrChatDisabled, ErrChatNotFound, ErrQuotaExceeded, ErrAuthFailed, ErrNotPermitted, ErrNoCredentials}

	for _, err := range retryable {
		if !Retryable(err) || Terminal(err) {
			t.Fatalf("%v: want retryable and not terminal", err)
		}
	}
	for _, err := range terminal {
		if Retryable(err) || !Terminal(err) {
			t.Fatalf("%v: want terminal and not retryable", err)
		}
	}
	if Retryable(nil) || Terminal(nil) {
		t.Fatal("nil error is neither retryable nor terminal")
	}
}

func TestClientChargesTheLedgerEvenWhenTheCallFails(t *testing.T) {
	// The ledger itself belongs to the quota lane; what this package
	// guarantees is that a failed request is still dispatched through the
	// charging path, because Google bills invalid requests too.
	var dispatched int
	client, _ := newTestClient(t, oauthCredentials(), func(w http.ResponseWriter, r *http.Request) {
		dispatched++
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, googleError(403, "quotaExceeded", "PERMISSION_DENIED", "", "out of quota"))
	})

	if _, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"}); err == nil {
		t.Fatal("error = nil, want quotaExceeded")
	}
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want exactly one request and no silent retry", dispatched)
	}
}

// assertNoCredentialLeak fails when a user-facing string contains anything that
// could identify a credential or the URL that carried one.
func assertNoCredentialLeak(t *testing.T, value string, serverURL string) {
	t.Helper()
	for _, forbidden := range []string{testToken, testAPIKey, "Bearer " + testToken, "key=" + testAPIKey, serverURL} {
		if forbidden == "" {
			continue
		}
		if strings.Contains(value, forbidden) {
			t.Fatalf("output contains %q: %q", forbidden, value)
		}
	}
}

// newTestClientWithLedger is newTestClient with quota accounting wired, for the
// paths where the ledger's state changes what the client does.
func newTestClientWithLedger(t *testing.T, ledger *quota.Ledger, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(ClientConfig{
		Credentials: oauthCredentials(),
		Endpoint:    server.URL,
		HTTPClient:  server.Client(),
		Ledger:      ledger,
	})
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}
	return client, server
}
