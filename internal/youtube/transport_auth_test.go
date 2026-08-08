package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
)

// A Google access token lasts about an hour and a stream lasts longer, so the
// 401 that arrives mid-broadcast is the ordinary case rather than the
// exceptional one. These tests cover the whole of that path: the one retry, the
// single-flight that keeps a burst of simultaneous failures from starting a
// burst of exchanges, and the point at which yc stops trying and says so.

// Obvious fake markers, as everywhere in this package.
const (
	expiredTestToken   = "expired-not-a-real-token"
	refreshedTestToken = "refreshed-not-a-real-token"
)

// refreshingCredentials is a CredentialSource that can renew itself, which is
// the shape internal/cli's credential holder presents to the transport.
type refreshingCredentials struct {
	mu    sync.Mutex
	token auth.Secret
	key   auth.Secret
	calls int

	// refresh is the exchange. A nil refresh mints the standard refreshed
	// token; anything else stands in for a real one, including a failure.
	refresh func(ctx context.Context, call int) (auth.Secret, error)
}

func (c *refreshingCredentials) AccessToken() auth.Secret {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *refreshingCredentials) APIKey() auth.Secret {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.key
}

func (c *refreshingCredentials) RefreshCredentials(ctx context.Context) error {
	c.mu.Lock()
	c.calls++
	call := c.calls
	exchange := c.refresh
	c.mu.Unlock()

	token := auth.NewSecret(refreshedTestToken)
	if exchange != nil {
		fresh, err := exchange(ctx, call)
		if err != nil {
			return err
		}
		token = fresh
	}

	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

func (c *refreshingCredentials) refreshCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// unauthorizedBody is the envelope Google returns for an expired access token,
// with all three of its disagreeing classification channels populated.
func unauthorizedBody() string {
	return googleError(http.StatusUnauthorized, "authError", "UNAUTHENTICATED", "ACCESS_TOKEN_EXPIRED", "Invalid Credentials")
}

// bearerOf returns the token a request presented, or "" when it presented none.
func bearerOf(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// newAuthTestClient wires a client to an in-process server, leaving the caller
// to supply the credential source so the refresher can be absent, present, or
// broken.
func newAuthTestClient(t *testing.T, cfg ClientConfig, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	cfg.Endpoint = server.URL
	cfg.HTTPClient = server.Client()
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}
	return client
}

// broadcastBody is a minimal videos.list answer, so a test can assert on the
// auth path rather than on decoding.
const broadcastBody = `{"items":[{"id":"vid00000001","snippet":{"title":"t"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`

func TestClientRefreshesOnceAfterA401(t *testing.T) {
	tests := []struct {
		name string
		// credentials is the source under test.
		credentials func() CredentialSource
		// hook overrides the automatic wiring; nil leaves it alone.
		hook func(*testing.T, CredentialSource) func(context.Context) error
		// serve answers request n (1-based) with the token it presented.
		serve func(w http.ResponseWriter, token string, n int)

		wantErr       error
		wantRequests  int
		wantRefreshes int
		// wantDetail is a fragment the error must name, so a terminal auth
		// failure tells the user what to do rather than quoting an HTTP code.
		wantDetail string
	}{
		{
			// The ordinary mid-broadcast expiry: renew, re-send, carry on.
			name: "success after refresh",
			credentials: func() CredentialSource {
				return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
			},
			serve: func(w http.ResponseWriter, token string, _ int) {
				if token == expiredTestToken {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, unauthorizedBody())
					return
				}
				fmt.Fprint(w, broadcastBody)
			},
			wantRequests:  2,
			wantRefreshes: 1,
		},
		{
			// A revoked grant cannot be renewed. One exchange is attempted,
			// the request is not re-sent, and the user is told to sign in.
			name: "refresh fails",
			credentials: func() CredentialSource {
				return &refreshingCredentials{
					token: auth.NewSecret(expiredTestToken),
					refresh: func(context.Context, int) (auth.Secret, error) {
						return "", errors.New("invalid_grant")
					},
				}
			},
			serve: func(w http.ResponseWriter, _ string, _ int) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, unauthorizedBody())
			},
			wantErr:       ErrAuthFailed,
			wantRequests:  1,
			wantRefreshes: 1,
			wantDetail:    "yc login",
		},
		{
			// Nothing to call: a 401 is terminal immediately rather than
			// spending a second request to learn the same thing.
			name: "refresh hook absent",
			credentials: func() CredentialSource {
				return StaticCredentials{Token: auth.NewSecret(expiredTestToken)}
			},
			serve: func(w http.ResponseWriter, _ string, _ int) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, unauthorizedBody())
			},
			wantErr:      ErrAuthFailed,
			wantRequests: 1,
		},
		{
			// A freshly minted token rejected too. Another exchange would
			// produce another rejection, so this is where it stops.
			name: "still rejected after refresh",
			credentials: func() CredentialSource {
				return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
			},
			serve: func(w http.ResponseWriter, _ string, _ int) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, unauthorizedBody())
			},
			wantErr:       ErrAuthFailed,
			wantRequests:  2,
			wantRefreshes: 1,
			wantDetail:    "yc login",
		},
		{
			// An API key is not renewable, so a key-only read that was
			// rejected must not spend an exchange or a second request.
			name: "key-only rejection is not refreshable",
			credentials: func() CredentialSource {
				return &refreshingCredentials{key: auth.NewSecret(testAPIKey)}
			},
			serve: func(w http.ResponseWriter, _ string, _ int) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, googleError(http.StatusUnauthorized, "", "UNAUTHENTICATED", "API_KEY_INVALID", "API key not valid"))
			},
			wantErr:      ErrAuthFailed,
			wantRequests: 1,
		},
		{
			// A 403 is a scope or a role problem. A new token carrying the
			// same grant will not fix it, so nothing is retried.
			name: "not-permitted is not refreshable",
			credentials: func() CredentialSource {
				return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
			},
			serve: func(w http.ResponseWriter, _ string, _ int) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, googleError(http.StatusForbidden, "insufficientPermissions", "PERMISSION_DENIED", "", "Insufficient Permission"))
			},
			wantErr:      ErrNotPermitted,
			wantRequests: 1,
		},
		{
			// A rate limit belongs to the poller's ladder, which can see the
			// quota ledger. The transport must not touch it.
			name: "rate limit is not refreshable",
			credentials: func() CredentialSource {
				return &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
			},
			serve: func(w http.ResponseWriter, _ string, _ int) {
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, googleError(http.StatusTooManyRequests, "rateLimitExceeded", "RESOURCE_EXHAUSTED", "", "Too many requests"))
			},
			wantErr:      ErrRateLimited,
			wantRequests: 1,
		},
		{
			// An explicitly configured hook wins over the automatic wiring,
			// because a caller that supplied one meant it.
			name: "explicit hook overrides the credential source",
			credentials: func() CredentialSource {
				return &refreshingCredentials{
					token: auth.NewSecret(expiredTestToken),
					refresh: func(context.Context, int) (auth.Secret, error) {
						return "", errors.New("the source's own refresh ran")
					},
				}
			},
			hook: func(t *testing.T, source CredentialSource) func(context.Context) error {
				t.Helper()
				typed := source.(*refreshingCredentials)
				return func(context.Context) error {
					typed.mu.Lock()
					defer typed.mu.Unlock()
					typed.token = auth.NewSecret(refreshedTestToken)
					return nil
				}
			},
			serve: func(w http.ResponseWriter, token string, _ int) {
				if token == expiredTestToken {
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprint(w, unauthorizedBody())
					return
				}
				fmt.Fprint(w, broadcastBody)
			},
			wantRequests: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests int
			var presented []string

			credentials := tc.credentials()
			cfg := ClientConfig{Credentials: credentials}
			if tc.hook != nil {
				cfg.OnAuthFailure = tc.hook(t, credentials)
			}

			client := newAuthTestClient(t, cfg, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				n := requests
				token := bearerOf(r)
				presented = append(presented, token)
				mu.Unlock()
				tc.serve(w, token, n)
			})

			_, err := client.Broadcast(context.Background(), "vid00000001")
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("Broadcast error = %v, want the refreshed call to succeed", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("Broadcast error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantDetail != "" && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("error = %q, want it to name %q", err, tc.wantDetail)
			}

			mu.Lock()
			gotRequests, gotPresented := requests, append([]string(nil), presented...)
			mu.Unlock()
			if gotRequests != tc.wantRequests {
				t.Errorf("requests = %d, want %d", gotRequests, tc.wantRequests)
			}
			if source, ok := credentials.(*refreshingCredentials); ok && tc.hook == nil {
				if got := source.refreshCount(); got != tc.wantRefreshes {
					t.Errorf("refreshes = %d, want %d", got, tc.wantRefreshes)
				}
			}
			// The retry must present the renewed token; re-sending the
			// expired one would be a guaranteed second rejection.
			if len(gotPresented) == 2 && gotPresented[0] != "" && gotPresented[0] == gotPresented[1] {
				if tc.wantRefreshes > 0 {
					t.Errorf("the retry presented the same token as the failed attempt")
				}
			}
		})
	}
}

// A burst of in-flight requests all failing at once must produce one token
// exchange, not one per request: Google rotates a refresh token as it consumes
// it, so the second concurrent exchange would fail and read as a revoked grant.
func TestClientRefreshIsSingleFlightAcrossConcurrent401s(t *testing.T) {
	const callers = 8

	// Every rejection reports itself here. The exchange does not complete
	// until all of them have arrived, which guarantees every caller reaches
	// the refresh path rather than sailing past on an already-renewed token.
	rejected := make(chan struct{}, callers)

	source := &refreshingCredentials{
		token: auth.NewSecret(expiredTestToken),
		refresh: func(ctx context.Context, call int) (auth.Secret, error) {
			if call != 1 {
				return "", fmt.Errorf("exchange %d ran; the refresh was not single-flight", call)
			}
			for i := 0; i < callers; i++ {
				select {
				case <-rejected:
				case <-time.After(10 * time.Second):
					return "", errors.New("not every caller reached the refresh")
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return auth.NewSecret(refreshedTestToken), nil
		},
	}

	var mu sync.Mutex
	var requests int
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		if bearerOf(r) == expiredTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, unauthorizedBody())
			rejected <- struct{}{}
			return
		}
		fmt.Fprint(w, broadcastBody)
	})

	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = client.Broadcast(context.Background(), "vid00000001")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: Broadcast error = %v", i, err)
		}
	}
	if got := source.refreshCount(); got != 1 {
		t.Errorf("exchanges = %d, want exactly 1 for %d simultaneous rejections", got, callers)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2*callers {
		t.Errorf("requests = %d, want %d: one rejection and one retry per caller", requests, 2*callers)
	}
}

// A request whose 401 was collected against a token another request has already
// replaced must retry with the current one rather than starting an exchange of
// its own. Without this a burst that arrives slightly staggered walks the
// refresh token through one rotation per request.
func TestClientDoesNotExchangeAgainForAnAlreadyReplacedToken(t *testing.T) {
	source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
		if bearerOf(r) == expiredTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, unauthorizedBody())
			return
		}
		fmt.Fprint(w, broadcastBody)
	})

	// One real rejection renews the token and moves the epoch to 1.
	if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	if got := client.authEpoch(); got != 1 {
		t.Fatalf("epoch = %d, want one completed refresh", got)
	}

	// A request that was already on the wire during that refresh arrives
	// carrying the epoch it sampled before dispatching. Its 401 is stale
	// news, and it must be told so rather than starting a second exchange.
	if err := client.refreshCredentials(context.Background(), 0); err != nil {
		t.Fatalf("refreshCredentials for a stale epoch = %v, want it to be a no-op", err)
	}
	if got := source.refreshCount(); got != 1 {
		t.Errorf("exchanges = %d, want the stale epoch to have skipped its own", got)
	}
	if got := client.authEpoch(); got != 1 {
		t.Errorf("epoch = %d, want a skipped refresh not to advance it", got)
	}
}

// Every dispatched request is charged, including the one the API rejected:
// Google bills an invalid request too, and a meter that hid the retry would
// drift from what the Cloud Console shows.
func TestClientChargesBothTheRejectedRequestAndTheRetry(t *testing.T) {
	source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
	ledger := NewQuotaLedger(LedgerConfig{DailyUnits: 10000})
	client := newAuthTestClient(t, ClientConfig{Credentials: source, Ledger: ledger}, func(w http.ResponseWriter, r *http.Request) {
		if bearerOf(r) == expiredTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, unauthorizedBody())
			return
		}
		fmt.Fprint(w, broadcastBody)
	})

	if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
		t.Fatalf("Broadcast error = %v", err)
	}
	want := 2 * client.cost(EndpointVideosList)
	if got := ledger.Snapshot().UsedUnits; got != want {
		t.Errorf("charged %d units, want %d for two dispatched requests", got, want)
	}
}

// The retried request has to be rebuilt, not replayed: a body read once is
// consumed, and a POST that arrives empty is a rejected message rather than a
// recovered one.
func TestClientResendsTheRequestBodyAfterARefresh(t *testing.T) {
	source := &refreshingCredentials{token: auth.NewSecret(expiredTestToken)}
	var mu sync.Mutex
	var bodies []string
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		if bearerOf(r) == expiredTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, unauthorizedBody())
			return
		}
		fmt.Fprint(w, `{"id":"sent-1","snippet":{"publishedAt":"2026-08-08T20:20:00Z"}}`)
	})

	if _, err := client.SendMessage(context.Background(), SendRequest{LiveChatID: "chat-1", Text: "hello chat"}); err != nil {
		t.Fatalf("SendMessage error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want the insert sent twice", len(bodies))
	}
	if bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("retry body = %q, want the same insert as %q", bodies[1], bodies[0])
	}
	var decoded liveChatInsertRequest
	if err := json.Unmarshal([]byte(bodies[1]), &decoded); err != nil {
		t.Fatalf("decode retried body: %v", err)
	}
	if decoded.Snippet.TextMessageDetails.MessageText != "hello chat" {
		t.Errorf("retried text = %q", decoded.Snippet.TextMessageDetails.MessageText)
	}
}

// The refresh path handles both tokens and the failure of an OAuth exchange
// that can echo a credential straight back. Nothing it produces may quote the
// expired token, the renewed one, the API key, or the token endpoint.
func TestRefreshFailureNeverQuotesACredential(t *testing.T) {
	const (
		stale     = "ya29.expired-not-a-real-token-abcdefghij"
		renewed   = "ya29.renewed-not-a-real-token-klmnopqrst"
		refreshTk = "1//refresh-not-a-real-token-uvwxyz"
		key       = redactionTestKey
	)

	source := &refreshingCredentials{
		token: auth.NewSecret(stale),
		key:   auth.NewSecret(key),
		refresh: func(context.Context, int) (auth.Secret, error) {
			// What a real exchange failure looks like: the endpoint it
			// dialled, the grant it presented, and the token it was given.
			return "", fmt.Errorf("Post \"https://oauth2.googleapis.com/token?key=%s\": invalid_grant for refresh_token=%s (previous access_token=%s, new access_token=%s)",
				key, refreshTk, stale, renewed)
		},
	}

	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, unauthorizedBody())
	})

	_, err := client.Broadcast(context.Background(), "vid00000001")
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("Broadcast error = %v, want ErrAuthFailed", err)
	}
	if !Terminal(err) {
		t.Error("an unrenewable credential must be terminal, not retried forever")
	}
	assertNoCredential(t, err, stale, renewed, refreshTk, key)
	if strings.Contains(errorChainText(err), "oauth2.googleapis.com") {
		t.Errorf("error = %q, want no request URL from the token exchange", err)
	}
}

// A caller that quits mid-refresh stops waiting on it rather than blocking on
// somebody else's exchange, and reports its own cancellation.
func TestRefreshHonorsTheCallerContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	entered := make(chan struct{})

	source := &refreshingCredentials{
		token: auth.NewSecret(expiredTestToken),
		refresh: func(ctx context.Context, call int) (auth.Secret, error) {
			if call == 1 {
				close(entered)
				<-release
			}
			return auth.NewSecret(refreshedTestToken), nil
		},
	}
	client := newAuthTestClient(t, ClientConfig{Credentials: source}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, unauthorizedBody())
	})

	go func() { _, _ = client.Broadcast(context.Background(), "vid00000001") }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first refresh never started")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.refreshCredentials(ctx, client.authEpoch()); !errors.Is(err, context.Canceled) {
		t.Fatalf("refreshCredentials error = %v, want the caller's own cancellation", err)
	}
}

// A client with no hook at all must answer rather than panic, and must say the
// refresh is unavailable rather than claim one happened.
func TestRefreshWithoutAHookIsReportedNotAttempted(t *testing.T) {
	client, err := NewClient(ClientConfig{Credentials: StaticCredentials{Token: auth.NewSecret(testToken)}})
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}
	if client.cfg.OnAuthFailure != nil {
		t.Fatal("a credential source that cannot refresh was wired to the 401 hook")
	}
	if err := client.refreshCredentials(context.Background(), 0); err == nil {
		t.Error("a client with no hook reported a successful refresh")
	}
	if got := client.authEpoch(); got != 0 {
		t.Errorf("epoch = %d, want an unrefreshed client to stay at 0", got)
	}
}
