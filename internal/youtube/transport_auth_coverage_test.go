package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
)

// transport_auth_test.go covers the refresh path's happy and terminal cases.
// These are the edges around it: what must *not* trigger an exchange, what a
// failure after a successful exchange is allowed to say, and the shapes a
// credential can take that no rendering of the resulting error may contain.

// Every fake credential yc could hold, in the exact shape Google issues it.
// Shape matters here rather than value: the pattern redactor recognizes some of
// these by their prefix alone, and a test using "fake-token-1" would pass
// without ever exercising that.
const (
	shapedAPIKey       = "AIzaSyTESTFAKEKEY0000000000000000000000"
	shapedAccessToken  = "ya29.a0TESTFAKEACCESSTOKENnotarealtokenatall"
	shapedRefreshToken = "1//0gTESTFAKEREFRESHTOKENnotarealtokenatall"
	shapedClientSecret = "GOCSPX-TESTFAKECLIENTSECRET0000000"
)

// everyCredentialShape is the set no error, log, or status line may quote.
func everyCredentialShape() []string {
	return []string{shapedAPIKey, shapedAccessToken, shapedRefreshToken, shapedClientSecret}
}

// TestAFailureAfterARefreshIsReportedAsItself is the case that would otherwise
// mislead the user the most.
//
// Once the token has been renewed, whatever the retry hits is a fresh problem.
// Reporting a 500 or a rate limit as "the sign-in expired and could not be
// renewed" would send someone to run `yc login` over a Google outage - and
// worse, it would classify a retryable failure as terminal, so the poll loop
// would stop instead of backing off.
func TestAFailureAfterARefreshIsReportedAsItself(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		reason     string
		want       error
		retryable  bool
	}{
		{"server error", http.StatusServiceUnavailable, "backendError", ErrTransient, true},
		{"rate limited", http.StatusTooManyRequests, "rateLimitExceeded", ErrRateLimited, true},
		{"chat ended while the token was being renewed", http.StatusForbidden, "liveChatEnded", ErrChatEnded, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
			var requests int
			var mu sync.Mutex
			client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				requests++
				n := requests
				mu.Unlock()
				if n == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, unauthorizedBody())
					return
				}
				w.WriteHeader(test.statusCode)
				fmt.Fprint(w, googleError(test.statusCode, test.reason, "", "", "the API said no"))
			})

			_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if errors.Is(err, ErrAuthFailed) {
				t.Fatalf("error = %v, want no credential blame for a failure the refresh already fixed", err)
			}
			if strings.Contains(err.Error(), "yc login") {
				t.Fatalf("error = %q, want no re-login instruction for a non-credential failure", err)
			}
			if Retryable(err) != test.retryable {
				t.Fatalf("Retryable = %v, want %v; the poll loop reads this to decide whether to stop", Retryable(err), test.retryable)
			}
			if source.refreshCount() != 1 {
				t.Fatalf("refreshes = %d, want exactly one", source.refreshCount())
			}
		})
	}
}

// TestOnlyA401TriggersAnExchange pins the narrow definition of "refreshable".
//
// ErrAuthFailed is not the same question as "would a new token help". A 403
// carrying ACCESS_TOKEN_EXPIRED classifies as ErrAuthFailed - Google's three
// channels disagree, and the ErrorInfo one wins - but a 403 is a scope or a
// role problem, and exchanging the refresh token would spend a network round
// trip and rotate a refresh token to be told exactly the same thing. Likewise a
// 401 against an API key: keys are not renewable at all.
func TestOnlyA401TriggersAnExchange(t *testing.T) {
	tests := []struct {
		name        string
		credentials func() *refreshingCredentials
		statusCode  int
		reason      string
		status      string
		errorInfo   string
		wantErr     error
		wantRefresh int
		wantRequest int
	}{
		{
			name:        "403 that classifies as an auth failure",
			credentials: func() *refreshingCredentials { return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)} },
			statusCode:  http.StatusForbidden,
			errorInfo:   "ACCESS_TOKEN_EXPIRED",
			wantErr:     ErrAuthFailed,
			wantRefresh: 0,
			wantRequest: 1,
		},
		{
			name:        "403 insufficient permissions",
			credentials: func() *refreshingCredentials { return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)} },
			statusCode:  http.StatusForbidden,
			reason:      "insufficientPermissions",
			wantErr:     ErrNotPermitted,
			wantRefresh: 0,
			wantRequest: 1,
		},
		{
			name: "401 against an API key, with a refresher present",
			// The hook exists because the source can renew a token, but this
			// call presented a key. Renewing the token changes nothing about
			// the key that was rejected.
			credentials: func() *refreshingCredentials { return &refreshingCredentials{key: auth.NewSecret(shapedAPIKey)} },
			statusCode:  http.StatusUnauthorized,
			errorInfo:   "API_KEY_INVALID",
			wantErr:     ErrAuthFailed,
			wantRefresh: 0,
			wantRequest: 1,
		},
		{
			name:        "429 is the ladder's problem, not the credential's",
			credentials: func() *refreshingCredentials { return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)} },
			statusCode:  http.StatusTooManyRequests,
			reason:      "rateLimitExceeded",
			wantErr:     ErrRateLimited,
			wantRefresh: 0,
			wantRequest: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := test.credentials()
			var requests int
			var mu sync.Mutex
			client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				requests++
				mu.Unlock()
				w.WriteHeader(test.statusCode)
				fmt.Fprint(w, googleError(test.statusCode, test.reason, test.status, test.errorInfo, "the API said no"))
			})

			_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if got := source.refreshCount(); got != test.wantRefresh {
				t.Fatalf("refreshes = %d, want %d", got, test.wantRefresh)
			}
			mu.Lock()
			got := requests
			mu.Unlock()
			if got != test.wantRequest {
				t.Fatalf("requests = %d, want %d; every dispatch is charged", got, test.wantRequest)
			}
		})
	}
}

// A 401 whose body is not a Google error envelope at all - a proxy's HTML, a
// captive portal, an empty response - still classifies from the status and must
// still be refreshable. This is the branch decodeError takes when json.Unmarshal
// fails or the envelope is empty, and it is a realistic one: the 401 that
// arrives through a corporate proxy rarely looks like Google's.
func TestARefreshHappensEvenWhenTheBodyIsNotAnEnvelope(t *testing.T) {
	for _, body := range []string{"", "<html>401 Unauthorized</html>", "{}", "not json at all"} {
		t.Run(fmt.Sprintf("%.16q", body), func(t *testing.T) {
			source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
			client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
				if bearerOf(r) == expiredTestToken {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, body)
					return
				}
				fmt.Fprint(w, `{"items":[]}`)
			})

			if _, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"}); err != nil {
				t.Fatalf("ListMessages error = %v, want the retry to succeed", err)
			}
			if got := source.refreshCount(); got != 1 {
				t.Fatalf("refreshes = %d, want 1", got)
			}
		})
	}
}

// TestNoCredentialShapeSurvivesTheAuthPath walks every error the refresh path
// can produce and asserts that none of the four credential shapes yc holds
// appears in any rendering of it.
//
// The value-based redactor only removes what it was handed. This asserts the
// outcome rather than the mechanism: whether a shape is caught by the redactor
// holding it, by the URL scrub, or by the pattern matcher, it must not come out
// the other end.
func TestNoCredentialShapeSurvivesTheAuthPath(t *testing.T) {
	tests := []struct {
		name string
		// refresh stands in for the OAuth exchange, whose failures are the
		// ones most likely to quote something they should not.
		refresh func(context.Context, int) (auth.Secret, error)
		serve   func(w http.ResponseWriter, token string) // n/a when nil
	}{
		{
			name: "the exchange echoes the refresh token back",
			refresh: func(context.Context, int) (auth.Secret, error) {
				return "", fmt.Errorf("oauth: token exchange failed: refresh_token=%s client_secret=%s", shapedRefreshToken, shapedClientSecret)
			},
		},
		{
			name: "the exchange names the token endpoint",
			refresh: func(context.Context, int) (auth.Secret, error) {
				return "", fmt.Errorf(`Post "https://oauth2.googleapis.com/token?key=%s": dial tcp: connection refused`, shapedAPIKey)
			},
		},
		{
			name: "the exchange quotes the stale access token",
			refresh: func(context.Context, int) (auth.Secret, error) {
				return "", fmt.Errorf("invalid_grant for %s", shapedAccessToken)
			},
		},
		{
			name: "the API echoes both credentials back in its 401",
			refresh: func(context.Context, int) (auth.Secret, error) {
				return auth.NewSecret(shapedAccessToken), nil
			},
			serve: func(w http.ResponseWriter, _ string) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":{"code":401,"message":"rejected Bearer %s and key=%s","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`,
					shapedAccessToken, shapedAPIKey)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &refreshingCredentials{
				token:   auth.NewSecret(shapedAccessToken),
				key:     auth.NewSecret(shapedAPIKey),
				refresh: test.refresh,
			}
			serve := test.serve
			if serve == nil {
				serve = func(w http.ResponseWriter, _ string) {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, unauthorizedBody())
				}
			}
			client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
				serve(w, bearerOf(r))
			})

			_, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			if err == nil {
				t.Fatal("error = nil, want a terminal credential failure")
			}
			assertNoCredential(t, err, everyCredentialShape()...)
			// The instruction has to survive the scrubbing, or the user is
			// left with a redacted sentence and no next step.
			if !strings.Contains(err.Error(), "yc login") {
				t.Fatalf("error = %q, want it to name the way forward", err)
			}
		})
	}
}

// A refresh storm must not leave goroutines behind.
//
// The single-flight parks every concurrent 401 on one channel, and the leader
// runs the exchange outside the lock. A waiter that failed to be released - or
// a leader whose close was skipped on an error path - would be a goroutine per
// poll for the length of the stream, and yc runs one poller per open chat tab.
func TestARefreshStormLeavesNoGoroutinesBehind(t *testing.T) {
	settle := func() int {
		// Two settling passes: the first lets the HTTP server's per-connection
		// goroutines finish, the second confirms the count has stopped moving.
		last := -1
		for i := 0; i < 50; i++ {
			runtime.GC()
			time.Sleep(10 * time.Millisecond)
			now := runtime.NumGoroutine()
			if now == last {
				return now
			}
			last = now
		}
		return last
	}

	source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
		if bearerOf(r) == expiredTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, unauthorizedBody())
			return
		}
		fmt.Fprint(w, `{"items":[]}`)
	})

	// One warm-up storm, so the HTTP client's idle connections and the
	// server's accept goroutines exist before the baseline is taken.
	runStorm := func(n int) {
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"})
			}()
		}
		wg.Wait()
	}
	runStorm(8)
	baseline := settle()

	for round := 0; round < 5; round++ {
		// Put the source back to an expired token so every round really
		// goes through the refresh path rather than sailing past it.
		source.mu.Lock()
		source.token = auth.NewSecret(expiredTestToken)
		source.mu.Unlock()
		runStorm(16)
	}

	after := settle()
	// A small allowance for the runtime's own bookkeeping; a leak of one
	// goroutine per request would be 80 here, not 4.
	if after > baseline+4 {
		t.Fatalf("goroutines = %d after five refresh storms, baseline %d", after, baseline)
	}
}

// The epoch guard exists so a 401 collected against a token that has since been
// replaced does not queue another exchange behind the one that already fixed
// it. This drives it through the exported path rather than by calling
// refreshCredentials directly: a request that starts before a refresh and
// arrives after it must retry, not exchange.
func TestAStaleEpochRetriesWithoutExchanging(t *testing.T) {
	source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
		if bearerOf(r) == expiredTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, unauthorizedBody())
			return
		}
		fmt.Fprint(w, `{"items":[]}`)
	})

	// One ordinary refresh, which advances the epoch to 1.
	if _, err := client.ListMessages(context.Background(), ListRequest{LiveChatID: "chat-1"}); err != nil {
		t.Fatalf("ListMessages error = %v", err)
	}
	if got := client.authEpoch(); got != 1 {
		t.Fatalf("epoch = %d, want 1 after one exchange", got)
	}

	// A request that sampled epoch 0 now reports its 401. There is nothing
	// to exchange: the token it was rejected for is already gone.
	before := source.refreshCount()
	if err := client.refreshCredentials(context.Background(), 0); err != nil {
		t.Fatalf("refreshCredentials with a stale epoch = %v, want it to be told to just retry", err)
	}
	if got := source.refreshCount(); got != before {
		t.Fatalf("refreshes = %d, want no exchange for an epoch that is already past", got)
	}
	if got := client.authEpoch(); got != 1 {
		t.Fatalf("epoch = %d, want the stale caller to leave it alone", got)
	}
}

// A caller that is already canceled must not dispatch at all: the whole point
// of the ctx check at the top of doJSON is that a quit keystroke stops spending
// quota immediately rather than after one more round trip.
func TestACancelledCallerNeitherDispatchesNorRefreshes(t *testing.T) {
	source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(http.ResponseWriter, *http.Request) {
		t.Error("a request was dispatched for a canceled caller")
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.ListMessages(ctx, ListRequest{LiveChatID: "chat-1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got := source.refreshCount(); got != 0 {
		t.Fatalf("refreshes = %d, want none for a canceled caller", got)
	}
}

// When the caller's context is canceled while the exchange is running, the
// error reported is the cancellation and not a credential verdict. Telling a
// user who just quit that their sign-in expired would send them to `yc login`
// over their own keystroke.
func TestCancellationDuringTheExchangeIsReportedAsCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	source := &refreshingCredentials{
		token: auth.NewSecret(expiredTestToken),
		refresh: func(refreshCtx context.Context, _ int) (auth.Secret, error) {
			close(entered)
			select {
			case <-refreshCtx.Done():
				return "", refreshCtx.Err()
			case <-release:
				return auth.NewSecret(refreshedTestToken), nil
			}
		},
	}
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, unauthorizedBody())
	})

	result := make(chan error, 1)
	go func() {
		_, err := client.ListMessages(ctx, ListRequest{LiveChatID: "chat-1"})
		result <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the exchange never started")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if strings.Contains(err.Error(), "yc login") {
			t.Fatalf("error = %q, want no credential blame for the user's own quit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request never returned after its context was canceled")
	}
}
