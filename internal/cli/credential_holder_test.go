package cli

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/storage"
)

// scriptedRefresher returns queued outcomes so a failure path is reachable
// without a network.
type scriptedRefresher struct {
	mu      sync.Mutex
	calls   int
	results []refreshOutcome
}

type refreshOutcome struct {
	tokens auth.TokenSet
	err    error
}

func (r *scriptedRefresher) Refresh(ctx context.Context, refreshToken auth.Secret) (auth.TokenSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.results) == 0 {
		return auth.TokenSet{AccessToken: auth.NewSecret("fresh-" + fakeToken)}, nil
	}
	next := r.results[0]
	r.results = r.results[1:]
	return next.tokens, next.err
}

func (r *scriptedRefresher) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// failingStore accepts loads but refuses to persist, so the "session still
// works, warn rather than fail" path is reachable.
type failingStore struct {
	storage.CredentialStore
	err error
}

func (s failingStore) SaveCredentials(context.Context, storage.CredentialRecord) error { return s.err }

func TestCredentialHolderAccessorsTolerateANilReceiver(t *testing.T) {
	var holder *credentialHolder
	if holder.AccessToken().Present() {
		t.Error("a nil holder produced an access token")
	}
	if holder.APIKey().Present() {
		t.Error("a nil holder produced an API key")
	}
	if !holder.expiresAt().IsZero() {
		t.Error("a nil holder produced an expiry")
	}
	if holder.canRefresh() {
		t.Error("a nil holder claimed it could refresh")
	}
	if err := holder.Refresh(context.Background()); err == nil {
		t.Error("a nil holder reported a successful refresh")
	}
	// startRefreshLoop must be a no-op rather than a panic.
	holder.startRefreshLoop(context.Background(), nil)
}

func TestCredentialHolderReportsWhatItCanDo(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC()
	record := storage.CredentialRecord{
		AccessToken:  auth.NewSecret("access-" + fakeToken),
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
		APIKey:       auth.NewSecret("AIza-" + fakeToken),
		ExpiresAt:    expiry,
	}

	holder := newCredentialHolder(record, nil, &scriptedRefresher{})
	if got := holder.AccessToken().Reveal(); got != "access-"+fakeToken {
		t.Errorf("access token = %q", got)
	}
	if got := holder.APIKey().Reveal(); got != "AIza-"+fakeToken {
		t.Errorf("api key = %q", got)
	}
	if !holder.expiresAt().Equal(expiry) {
		t.Errorf("expiry = %v, want %v", holder.expiresAt(), expiry)
	}
	if !holder.canRefresh() {
		t.Error("a holder with a refresh token and a refresher cannot refresh")
	}

	// Returning a typed nil as an interface would give the holder a refresher
	// that fails on every call, so an absent one is checked here.
	if newCredentialHolder(record, nil, nil).canRefresh() {
		t.Error("a holder with no refresher claimed it could refresh")
	}
	withoutToken := record
	withoutToken.RefreshToken = auth.Secret("")
	if newCredentialHolder(withoutToken, nil, &scriptedRefresher{}).canRefresh() {
		t.Error("a holder with no refresh token claimed it could refresh")
	}
}

func TestCredentialHolderRefreshWithoutARefreshTokenNamesTheWayForward(t *testing.T) {
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken: auth.NewSecret("access-" + fakeToken),
	}, nil, &scriptedRefresher{})

	err := holder.Refresh(context.Background())
	if err == nil {
		t.Fatal("a refresh with no refresh token succeeded")
	}
	if !strings.Contains(err.Error(), "yc login") {
		t.Errorf("error = %q, want it to name the way forward", err)
	}
}

func TestCredentialHolderRefreshWithoutARefresherIsReportedNotAttempted(t *testing.T) {
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, nil, nil)

	if err := holder.Refresh(context.Background()); err == nil {
		t.Fatal("a refresh with no refresher succeeded")
	}
}

// Google omits the refresh token on a refresh response, which means "keep the
// one you have" rather than "you lost it". A rotated one replaces it.
func TestCredentialHolderKeepsOrRotatesTheRefreshTokenAsGoogleSays(t *testing.T) {
	t.Run("omitted keeps the current one", func(t *testing.T) {
		store := storage.NewMemoryCredentialStore()
		holder := newCredentialHolder(storage.CredentialRecord{
			RefreshToken: auth.NewSecret("original-" + fakeToken),
			Scopes:       auth.LoginScopes(),
			ChannelID:    "UC123",
		}, store, &scriptedRefresher{results: []refreshOutcome{{
			tokens: auth.TokenSet{AccessToken: auth.NewSecret("fresh-" + fakeToken), TokenType: "Bearer"},
		}}})

		if err := holder.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		saved, ok, err := store.LoadCredentials(context.Background())
		if err != nil || !ok {
			t.Fatalf("LoadCredentials: ok=%v err=%v", ok, err)
		}
		if got := saved.RefreshToken.Reveal(); got != "original-"+fakeToken {
			t.Errorf("refresh token = %q, want the one yc already held", got)
		}
		// A refresh must write back a complete record, not a token-only one
		// that loses the cached identity and scopes.
		if saved.ChannelID != "UC123" || len(saved.Scopes) != len(auth.LoginScopes()) {
			t.Errorf("record = %+v, want the cached identity and scopes kept", saved)
		}
	})

	t.Run("returned replaces it", func(t *testing.T) {
		store := storage.NewMemoryCredentialStore()
		holder := newCredentialHolder(storage.CredentialRecord{
			RefreshToken: auth.NewSecret("original-" + fakeToken),
		}, store, &scriptedRefresher{results: []refreshOutcome{{
			tokens: auth.TokenSet{
				AccessToken:  auth.NewSecret("fresh-" + fakeToken),
				RefreshToken: auth.NewSecret("rotated-" + fakeToken),
				ExpiresAt:    time.Now().Add(time.Hour),
				Scopes:       auth.ReadScopes(),
			},
		}}})

		if err := holder.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		saved, _, _ := store.LoadCredentials(context.Background())
		if got := saved.RefreshToken.Reveal(); got != "rotated-"+fakeToken {
			t.Errorf("refresh token = %q, want the rotated one", got)
		}
		if len(saved.Scopes) != len(auth.ReadScopes()) {
			t.Errorf("scopes = %v, want the ones the refresh reported", auth.ScopeValues(saved.Scopes))
		}
		if saved.UpdatedAt.IsZero() {
			t.Error("the persisted record carries no update time")
		}
	})
}

// A failed write is reported as a warning by the caller, not as a failed
// refresh: the session in front of the user still works.
func TestCredentialHolderReportsAFailedWriteButKeepsTheNewToken(t *testing.T) {
	saveErr := errors.New("disk is read-only")
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, failingStore{CredentialStore: storage.NewMemoryCredentialStore(), err: saveErr}, &scriptedRefresher{})

	err := holder.Refresh(context.Background())
	if !errors.Is(err, saveErr) {
		t.Fatalf("Refresh error = %v, want the save failure surfaced", err)
	}
	if got := holder.AccessToken().Reveal(); got != "fresh-"+fakeToken {
		t.Errorf("access token = %q; the refreshed token must still be usable", got)
	}
}

// A refresh that Google rejects must not swap the in-memory token: the next
// process start would otherwise read a refresh token Google already rotated
// away, and the user would be locked out with no indication why.
func TestCredentialHolderKeepsTheOldTokenWhenTheExchangeFails(t *testing.T) {
	store := storage.NewMemoryCredentialStore()
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken:  auth.NewSecret("stale-" + fakeToken),
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, store, &scriptedRefresher{results: []refreshOutcome{{err: auth.ErrLoginRequired}}})

	err := holder.Refresh(context.Background())
	if !errors.Is(err, auth.ErrLoginRequired) {
		t.Fatalf("Refresh error = %v, want the rejection", err)
	}
	if got := holder.AccessToken().Reveal(); got != "stale-"+fakeToken {
		t.Errorf("access token = %q; a failed exchange must not replace it", got)
	}
	if _, ok, _ := store.LoadCredentials(context.Background()); ok {
		t.Error("a failed exchange persisted a record")
	}
}

// The transport's 401 hook and the scheduled loop differ in exactly one
// judgement, and it is this one: a token that was renewed but not written is
// still a token the pending request can be retried with, so tearing the session
// down over a read-only disk would be the wrong answer.
func TestRefreshCredentialsTreatsAnUnwritableStoreAsAWarning(t *testing.T) {
	saveErr := errors.New("disk is read-only")
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, failingStore{CredentialStore: storage.NewMemoryCredentialStore(), err: saveErr}, &scriptedRefresher{})

	var reported []error
	holder.startRefreshLoop(context.Background(), func(err error) { reported = append(reported, err) })

	if err := holder.RefreshCredentials(context.Background()); err != nil {
		t.Fatalf("RefreshCredentials error = %v; a failed write must not fail the retry", err)
	}
	if got := holder.AccessToken().Reveal(); got != "fresh-"+fakeToken {
		t.Errorf("access token = %q, want the renewed one the retry will present", got)
	}
	if len(reported) != 1 || !errors.Is(reported[0], saveErr) {
		t.Fatalf("reported = %v, want the write failure surfaced exactly once", reported)
	}
	// The user has to learn that the next start will read a stale refresh
	// token, so the warning has to say more than "error".
	if !strings.Contains(reported[0].Error(), "could not be saved") {
		t.Errorf("warning = %q, want it to name what did not happen", reported[0])
	}

	// The same condition through Refresh stays an error: the background loop
	// has no request waiting on it and the caller decides what to do.
	if err := holder.Refresh(context.Background()); !errors.Is(err, errCredentialsNotPersisted) {
		t.Errorf("Refresh error = %v, want the unwritten marker", err)
	}
}

// An exchange Google rejected is a real failure on both paths: there is no
// usable token, and the request that hit the 401 has nothing to retry with.
func TestRefreshCredentialsReportsARejectedExchange(t *testing.T) {
	holder := newCredentialHolder(storage.CredentialRecord{
		AccessToken:  auth.NewSecret("stale-" + fakeToken),
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, storage.NewMemoryCredentialStore(), &scriptedRefresher{results: []refreshOutcome{{err: auth.ErrLoginRequired}}})

	if err := holder.RefreshCredentials(context.Background()); !errors.Is(err, auth.ErrLoginRequired) {
		t.Fatalf("RefreshCredentials error = %v, want the rejection", err)
	}
	if got := holder.AccessToken().Reveal(); got != "stale-"+fakeToken {
		t.Errorf("access token = %q; a failed exchange must not replace it", got)
	}

	var nilHolder *credentialHolder
	if err := nilHolder.RefreshCredentials(context.Background()); err == nil {
		t.Error("a nil holder reported a successful refresh")
	}
}

// Both entry points share one in-flight exchange. Google rotates a refresh
// token as it consumes it, so a burst of 401s arriving alongside the scheduled
// refresh must not turn into two exchanges racing each other.
func TestRefreshCredentialsSharesTheSingleFlightWithRefresh(t *testing.T) {
	release := make(chan struct{})
	refresher := &countingRefresher{release: release, token: auth.NewSecret("fresh-" + fakeToken)}
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, storage.NewMemoryCredentialStore(), refresher)

	const callers = 8
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			if i%2 == 0 {
				errs <- holder.RefreshCredentials(context.Background())
				return
			}
			errs <- holder.Refresh(context.Background())
		}(i)
	}
	// Let every caller arrive before the single exchange completes.
	time.Sleep(20 * time.Millisecond)
	close(release)
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := refresher.calls.Load(); got != 1 {
		t.Errorf("exchanges = %d, want exactly 1 for %d simultaneous callers", got, callers)
	}
}

// A second Refresh after the first completed must actually run: single-flight
// shares one in-flight call, it does not memoize the result forever.
func TestCredentialHolderRefreshIsRepeatable(t *testing.T) {
	refresher := &scriptedRefresher{}
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, storage.NewMemoryCredentialStore(), refresher)

	for i := 0; i < 3; i++ {
		if err := holder.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh %d: %v", i, err)
		}
	}
	if got := refresher.callCount(); got != 3 {
		t.Errorf("exchanges = %d, want 3; single-flight must not memoize", got)
	}
}

// A caller whose context expires while sharing someone else's in-flight refresh
// gets its own cancellation rather than blocking on the other caller.
func TestCredentialHolderRefreshHonorsTheCallerContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	blocking := &countingRefresher{release: release, token: auth.NewSecret("fresh-" + fakeToken)}
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, storage.NewMemoryCredentialStore(), blocking)

	started := make(chan struct{})
	go func() {
		close(started)
		_ = holder.Refresh(context.Background())
	}()
	<-started

	// Wait until the first caller owns the in-flight slot, then arrive with an
	// already-cancelled context.
	deadline := time.After(2 * time.Second)
	for {
		holder.mu.Lock()
		pending := holder.pending != nil
		holder.mu.Unlock()
		if pending {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the first refresh never started")
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := holder.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh error = %v, want the caller's own cancellation", err)
	}
}

// A Google access token lasts about an hour and a stream lasts longer. Without
// a scheduled refresh the session dies mid-broadcast with a 401 that looks like
// several unrelated features breaking at once.
func TestStartRefreshLoopRefreshesAheadOfExpiry(t *testing.T) {
	refresher := &scriptedRefresher{}
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
		// Already inside the lead time, so the loop fires on its first pass
		// against its own one-second floor rather than after a real hour.
		ExpiresAt: time.Now().Add(time.Second),
	}, storage.NewMemoryCredentialStore(), refresher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holder.startRefreshLoop(ctx, nil)

	deadline := time.After(5 * time.Second)
	for refresher.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("the refresh loop never fired before expiry")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Guessing a refresh schedule is worse than leaving the failure visible, so an
// unknown expiry stops the loop instead of inventing one.
func TestStartRefreshLoopDeclinesWhenItCannotSchedule(t *testing.T) {
	refresher := &scriptedRefresher{}

	noExpiry := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
	}, storage.NewMemoryCredentialStore(), refresher)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	noExpiry.startRefreshLoop(ctx, nil)

	noRefreshToken := newCredentialHolder(storage.CredentialRecord{
		ExpiresAt: time.Now().Add(time.Second),
	}, storage.NewMemoryCredentialStore(), refresher)
	noRefreshToken.startRefreshLoop(ctx, nil)

	time.Sleep(50 * time.Millisecond)
	if got := refresher.callCount(); got != 0 {
		t.Errorf("%d exchanges dispatched with nothing to schedule against", got)
	}
}

// A cancelled context must stop the loop rather than leaving a goroutine
// refreshing a session nobody is watching.
func TestStartRefreshLoopStopsOnContextCancellation(t *testing.T) {
	refresher := &scriptedRefresher{}
	holder := newCredentialHolder(storage.CredentialRecord{
		RefreshToken: auth.NewSecret("refresh-" + fakeToken),
		ExpiresAt:    time.Now().Add(time.Hour),
	}, storage.NewMemoryCredentialStore(), refresher)

	ctx, cancel := context.WithCancel(context.Background())
	holder.startRefreshLoop(ctx, nil)
	cancel()

	time.Sleep(50 * time.Millisecond)
	if got := refresher.callCount(); got != 0 {
		t.Errorf("%d exchanges dispatched after cancellation", got)
	}
}

// The config values sit above the credential file in the precedence order, and
// what they do not supply is inherited from the stored record.
func TestCredentialRecordFromConfigOverlaysWithoutLosingTheRest(t *testing.T) {
	stored := storage.CredentialRecord{
		ClientID:     "stored-client",
		AccessToken:  auth.NewSecret("stored-access-" + fakeToken),
		RefreshToken: auth.NewSecret("stored-refresh-" + fakeToken),
		APIKey:       auth.NewSecret("stored-key-" + fakeToken),
		ChannelID:    "UC-stored",
		Scopes:       auth.LoginScopes(),
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	// An empty config changes nothing.
	unchanged := credentialRecordFromConfig(baseConfigWithoutCredentials(), stored)
	if unchanged.AccessToken.Reveal() != stored.AccessToken.Reveal() ||
		unchanged.ClientID != "stored-client" || unchanged.ChannelID != "UC-stored" {
		t.Errorf("record = %+v; an empty config must not overwrite the store", unchanged)
	}
	if len(unchanged.Scopes) != len(auth.LoginScopes()) || unchanged.ExpiresAt.IsZero() {
		t.Error("an unchanged token must keep its expiry and scopes")
	}

	// Supplying the same token is not an override and must not discard the
	// expiry and scopes that describe it.
	same := baseConfigWithoutCredentials()
	same.Google.AccessToken = stored.AccessToken.Reveal()
	kept := credentialRecordFromConfig(same, stored)
	if kept.ExpiresAt.IsZero() || len(kept.Scopes) == 0 {
		t.Errorf("record = %+v; re-supplying the stored token must not blank its metadata", kept)
	}

	// Individual overrides land without disturbing the others.
	overridden := baseConfigWithoutCredentials()
	overridden.Google.ClientID = "env-client"
	overridden.Google.RefreshToken = "env-refresh-" + fakeToken
	overridden.YouTube.APIKey = "env-key-" + fakeToken
	overridden.YouTube.ChannelID = "UC-env"
	got := credentialRecordFromConfig(overridden, stored)
	if got.ClientID != "env-client" {
		t.Errorf("client ID = %q", got.ClientID)
	}
	if got.RefreshToken.Reveal() != "env-refresh-"+fakeToken {
		t.Errorf("refresh token = %q", got.RefreshToken.Reveal())
	}
	if got.APIKey.Reveal() != "env-key-"+fakeToken {
		t.Errorf("api key = %q", got.APIKey.Reveal())
	}
	// The cached identity wins over a configured one, because it came from the
	// token itself.
	if got.ChannelID != "UC-stored" {
		t.Errorf("channel ID = %q, want the resolved identity to win", got.ChannelID)
	}
}

// baseConfigWithoutCredentials is the default config with every credential
// field explicitly empty, so an override under test is the only one in play.
func baseConfigWithoutCredentials() config.Config {
	cfg := config.Default()
	cfg.Google.ClientID = ""
	cfg.Google.ClientSecret = ""
	cfg.Google.AccessToken = ""
	cfg.Google.RefreshToken = ""
	cfg.YouTube.APIKey = ""
	cfg.YouTube.ChannelID = ""
	return cfg
}
