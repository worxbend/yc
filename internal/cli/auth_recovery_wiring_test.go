package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/storage"
	"github.com/worxbend/yc/internal/storage/storagetest"
	"github.com/worxbend/yc/internal/youtube"
)

// The mid-session 401 recovery is assembled by interface assertion: the
// transport asks whether its CredentialSource also implements
// CredentialRefresher and, if so, wires the hook itself. Nothing at the call
// site mentions it. That is what makes it convenient - no adapter has to
// remember - and it is also what makes it fragile: a holder that stops
// satisfying the interface loses the recovery silently, and the symptom does
// not appear until an hour into someone's stream, as several unrelated
// features breaking at once.
//
// credential_holder.go carries compile-time assertions for the interface
// itself. These are the behavioral half: the wiring actually fires for the
// value production passes, and the exchange it triggers is the holder's own.

// staticRefresher hands back one fixed token per call and counts the calls.
type staticRefresher struct {
	mu    sync.Mutex
	calls int
	token string
	err   error
}

func (r *staticRefresher) Refresh(context.Context, auth.Secret) (auth.TokenSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return auth.TokenSet{}, r.err
	}
	return auth.TokenSet{
		AccessToken: auth.NewSecret(r.token),
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour),
	}, nil
}

func (r *staticRefresher) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Obvious fake markers. Shape rather than value, so a leak into an error is
// caught by the redactor's pattern rules as well as by its value list.
const (
	staleWireToken = "ya29.STALE-not-a-real-access-token"
	freshWireToken = "ya29.FRESH-not-a-real-access-token"
	wireRefresh    = "1//0gNOT-a-real-refresh-token"
)

// newWiredClient builds the REST client the way runChat does - holder in,
// nothing else - but pointed at an in-process server.
func newWiredClient(t *testing.T, holder *credentialHolder, handler http.HandlerFunc) *youtube.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := youtube.NewClient(youtube.ClientConfig{
		Credentials: holder,
		Endpoint:    server.URL,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient error = %v", err)
	}
	return client
}

// TestTheHolderIsWiredToTheTransports401Recovery is the end-to-end assertion
// that the hour mark does not end a session.
func TestTheHolderIsWiredToTheTransports401Recovery(t *testing.T) {
	refresher := &staticRefresher{token: freshWireToken}
	holder := newCredentialHolder(storage.CredentialRecord{
		ClientID:     "fake.apps.googleusercontent.com",
		AccessToken:  auth.NewSecret(staleWireToken),
		RefreshToken: auth.NewSecret(wireRefresh),
	}, storagetest.NewMemoryCredentialStore(), refresher)

	var presented []string
	var mu sync.Mutex
	client := newWiredClient(t, holder, func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		presented = append(presented, token)
		mu.Unlock()
		if token == staleWireToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"vid00000001","snippet":{"title":"t"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
	})

	if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
		t.Fatalf("Broadcast error = %v, want the expired sign-in to be renewed and the call retried", err)
	}
	if got := refresher.callCount(); got != 1 {
		t.Fatalf("exchanges = %d, want exactly one", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(presented) != 2 {
		t.Fatalf("requests = %d, want the rejected one and its retry", len(presented))
	}
	if presented[0] != staleWireToken {
		t.Fatalf("first request presented %q, want the stale token", presented[0])
	}
	if presented[1] != freshWireToken {
		t.Fatalf("retry presented %q, want the renewed token", presented[1])
	}
	// The holder is the shared credential source, so the renewed token is
	// what every other client reads from now on rather than a value the
	// transport kept to itself.
	if got := holder.AccessToken().Reveal(); got != freshWireToken {
		t.Fatal("the renewed token did not reach the shared holder")
	}
}

// A grant Google has revoked cannot be renewed. One exchange is attempted, the
// request is not re-sent, and what the user is told is what to do about it -
// not an HTTP status, and never the token that failed.
func TestARevokedGrantEndsTheSessionWithAnInstruction(t *testing.T) {
	refresher := &staticRefresher{err: errors.New("invalid_grant: token has been expired or revoked; refresh_token=" + wireRefresh)}
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken:  auth.NewSecret(staleWireToken),
		RefreshToken: auth.NewSecret(wireRefresh),
	}, storagetest.NewMemoryCredentialStore(), refresher)

	var requests int
	var mu sync.Mutex
	client := newWiredClient(t, holder, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`)
	})

	_, err := client.Broadcast(context.Background(), "vid00000001")
	if !errors.Is(err, youtube.ErrAuthFailed) {
		t.Fatalf("error = %v, want youtube.ErrAuthFailed", err)
	}
	if !youtube.Terminal(err) {
		t.Fatal("a revoked grant must end the session rather than climb the backoff ladder")
	}
	if !strings.Contains(err.Error(), "yc login") {
		t.Fatalf("error = %q, want it to name the way forward", err)
	}
	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 1 {
		t.Fatalf("requests = %d, want no retry once the exchange failed", got)
	}

	// The failure came from the OAuth exchange, which is the one error in
	// this path most likely to quote a credential back.
	for _, secret := range []string{staleWireToken, freshWireToken, wireRefresh} {
		for label, rendering := range map[string]string{
			"Error()": err.Error(),
			"%v":      fmt.Sprintf("%v", err),
			"%+v":     fmt.Sprintf("%+v", err),
			"%#v":     fmt.Sprintf("%#v", err),
		} {
			if strings.Contains(rendering, secret) {
				t.Errorf("%s of the terminal error quoted a credential: %s", label, rendering)
			}
		}
	}
}

// A key-only session has nothing to refresh, and a 401 against an API key is
// not renewable in any case. It must be terminal on the first answer rather
// than spending a second request to be told the same thing.
func TestAKeyOnlySessionDoesNotAttemptARefresh(t *testing.T) {
	refresher := &staticRefresher{token: freshWireToken}
	holder := newCredentialHolder(storage.CredentialRecord{
		APIKey: auth.NewSecret("AIzaSyFAKE00000000000000000000000000010"),
	}, storagetest.NewMemoryCredentialStore(), refresher)

	var requests int
	var mu sync.Mutex
	client := newWiredClient(t, holder, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID"}]}}`)
	})

	if _, err := client.Broadcast(context.Background(), "vid00000001"); !errors.Is(err, youtube.ErrAuthFailed) {
		t.Fatalf("error = %v, want youtube.ErrAuthFailed", err)
	}
	if got := refresher.callCount(); got != 0 {
		t.Fatalf("exchanges = %d, want none for a key-only session", got)
	}
	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 1 {
		t.Fatalf("requests = %d, want one; a rejected key is not renewable", got)
	}
}

// A token renewed but not written back keeps the session alive. The request
// that triggered the refresh is retried, and the write failure reaches the user
// by the reporter rather than by ending a live chat over a read-only disk.
func TestAnUnwritableStoreStillLetsTheRequestThrough(t *testing.T) {
	refresher := &staticRefresher{token: freshWireToken}
	saveErr := errors.New("read-only file system")
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken:  auth.NewSecret(staleWireToken),
		RefreshToken: auth.NewSecret(wireRefresh),
	}, failingStore{CredentialStore: storagetest.NewMemoryCredentialStore(), err: saveErr}, refresher)

	reported := make(chan error, 4)
	holder.onError = func(err error) { reported <- err }

	client := newWiredClient(t, holder, func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == staleWireToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"vid00000001","snippet":{"title":"t"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
	})

	if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
		t.Fatalf("Broadcast error = %v, want the session to survive a failed write", err)
	}

	select {
	case err := <-reported:
		if !errors.Is(err, errCredentialsNotPersisted) {
			t.Fatalf("reported error = %v, want errCredentialsNotPersisted", err)
		}
		if !errors.Is(err, saveErr) {
			t.Fatalf("reported error = %v, want the store's own failure preserved", err)
		}
		for _, secret := range []string{freshWireToken, wireRefresh} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the write-failure warning quoted a credential: %v", err)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the failed write was never reported; the next start would read a rotated refresh token with no warning")
	}
}

// Concurrent 401s share one exchange. Google rotates a refresh token as it
// consumes it, so a second simultaneous exchange fails and reads as a revoked
// grant - the user would be told to sign in again because two polls happened to
// expire at the same instant.
func TestConcurrent401sShareOneExchangeThroughTheHolder(t *testing.T) {
	const callers = 8

	refresher := &staticRefresher{token: freshWireToken}
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken:  auth.NewSecret(staleWireToken),
		RefreshToken: auth.NewSecret(wireRefresh),
	}, storagetest.NewMemoryCredentialStore(), refresher)

	// Every caller is held at the barrier until all of them have had their
	// token rejected, so each one provably reaches the refresh path. Without
	// this the first caller could finish the whole exchange before the
	// second one dispatched, and the test would prove nothing.
	var barrier sync.WaitGroup
	barrier.Add(callers)
	released := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var rejected int

	client := newWiredClient(t, holder, func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != staleWireToken {
			fmt.Fprint(w, `{"items":[{"id":"vid00000001","snippet":{"title":"t"},"liveStreamingDetails":{"activeLiveChatId":"chat-1"}}]}`)
			return
		}
		mu.Lock()
		rejected++
		mu.Unlock()
		barrier.Done()
		<-released
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":401,"message":"Invalid Credentials","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`)
	})

	go func() {
		barrier.Wait()
		once.Do(func() { close(released) })
	}()

	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.Broadcast(context.Background(), "vid00000001"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Broadcast error = %v, want every caller to recover", err)
	}

	if got := refresher.callCount(); got != 1 {
		t.Fatalf("exchanges = %d, want exactly one shared by all %d callers", got, callers)
	}
	mu.Lock()
	defer mu.Unlock()
	if rejected != callers {
		t.Fatalf("%d callers were rejected, want all %d to have reached the refresh path", rejected, callers)
	}
}
