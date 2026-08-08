package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/storage"
	"github.com/worxbend/yc/internal/youtube"
)

// The holder is the API transport's credential source and its 401 recovery
// path. Both are asserted here so a signature change breaks the build rather
// than silently dropping the mid-session refresh - the transport wires the hook
// by interface assertion, which has no compile-time complaint of its own.
var (
	_ youtube.CredentialSource    = (*credentialHolder)(nil)
	_ youtube.CredentialRefresher = (*credentialHolder)(nil)
)

// errCredentialsNotPersisted marks a refresh that produced a working token yc
// could not write back.
//
// It is a distinct condition rather than a plain failure because the two want
// opposite handling: the session in front of the user still works and must not
// be torn down, but the next process start will read a refresh token Google has
// already rotated away, so the user has to be told.
var errCredentialsNotPersisted = errors.New("credentials were refreshed but could not be saved")

// refreshLeadTime is how far before expiry the background refresh fires. It is
// larger than auth.DefaultExpirySkew so the token is replaced during a quiet
// moment rather than in the middle of a poll.
const refreshLeadTime = 5 * time.Minute

// credentialHolder is the one live credential set shared by every API client.
//
// It exists so a mid-session refresh reaches all of them: clients read the
// token at request time through the CredentialSource interface rather than
// capturing it at construction, and the holder is what makes that read see the
// new value. Refresh is single-flight - concurrent 401s share one exchange -
// and the rotated refresh token is persisted before the new access token is
// handed out.
type credentialHolder struct {
	mu        sync.Mutex
	token     auth.Secret
	refresh   auth.Secret
	apiKey    auth.Secret
	store     storage.CredentialStore
	refresher auth.TokenRefresher

	// record is the persisted shape of the current credentials, kept so a
	// refresh writes back a complete record rather than a token-only one
	// that would lose the cached identity and scopes.
	record storage.CredentialRecord
	// pending is the in-flight refresh, shared by every caller that arrives
	// while it runs. A plain mutex would serialize them into N sequential
	// exchanges, and Google invalidates a refresh token it has just rotated,
	// so the second exchange would fail and look like a revoked grant.
	pending *refreshCall
	// onError reports a refresh problem the caller should see but that must
	// not stop the session, which is how a failed write reaches the user
	// without failing the request that triggered the refresh.
	onError func(error)
}

// refreshCall is one in-flight token exchange awaited by every caller that
// arrived while it was running.
type refreshCall struct {
	done chan struct{}
	err  error
}

// newCredentialHolder returns a holder seeded from a credential record.
func newCredentialHolder(record storage.CredentialRecord, store storage.CredentialStore, refresher auth.TokenRefresher) *credentialHolder {
	return &credentialHolder{
		token:     record.AccessToken,
		refresh:   record.RefreshToken,
		apiKey:    record.APIKey,
		store:     store,
		refresher: refresher,
		record:    record.Clone(),
	}
}

// credentialRecordFromConfig overlays the effective config credentials onto the
// stored record.
//
// The config values already sit above the credential file in the precedence
// order, so this is what makes an exported token win over a saved one while
// still keeping the record's cached identity, scopes, and expiry for the fields
// the export did not supply.
func credentialRecordFromConfig(cfg config.Config, base storage.CredentialRecord) storage.CredentialRecord {
	record := base.Clone()
	if clientID := strings.TrimSpace(cfg.Google.ClientID); clientID != "" {
		record.ClientID = clientID
	}
	if token := strings.TrimSpace(cfg.Google.AccessToken); token != "" && token != record.AccessToken.Reveal() {
		record.AccessToken = auth.NewSecret(token)
		// An externally supplied token has an unknown expiry and unknown
		// scopes. Claiming the stored ones would schedule a refresh for the
		// wrong moment and report a grant yc cannot vouch for.
		record.ExpiresAt = time.Time{}
		record.Scopes = nil
	}
	if refresh := strings.TrimSpace(cfg.Google.RefreshToken); refresh != "" {
		record.RefreshToken = auth.NewSecret(refresh)
	}
	if key := strings.TrimSpace(cfg.YouTube.APIKey); key != "" {
		record.APIKey = auth.NewSecret(key)
	}
	if channelID := strings.TrimSpace(cfg.YouTube.ChannelID); channelID != "" && record.ChannelID == "" {
		record.ChannelID = channelID
	}
	return record
}

// String describes the holder without printing anything it holds.
//
// auth.Secret refuses to format itself, but that guard does not survive being
// reached through an unexported field: fmt cannot call methods on values it
// obtains by reflecting into one, so it falls back to printing the underlying
// string. Every credential this type owns sits behind an unexported field, so
// without this a single `%v` on the holder prints both tokens and the API key.
func (h *credentialHolder) String() string {
	if h == nil {
		return "cli.credentialHolder(nil)"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("cli.credentialHolder(token=%v refresh=%v api_key=%v)",
		h.token.Present(), h.refresh.Present(), h.apiKey.Present())
}

// GoString keeps %#v from reflecting into the fields.
func (h *credentialHolder) GoString() string { return h.String() }

// AccessToken returns the current access token.
//
// It never blocks on the network: the transport reads it once per request, and
// a refresh that happened between two requests is visible simply because this
// reads the field rather than a captured copy.
func (h *credentialHolder) AccessToken() auth.Secret {
	if h == nil {
		return auth.Secret("")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.token
}

// APIKey returns the configured API key.
func (h *credentialHolder) APIKey() auth.Secret {
	if h == nil {
		return auth.Secret("")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.apiKey
}

// expiresAt returns the current access token's expiry, zero when unknown.
func (h *credentialHolder) expiresAt() time.Time {
	if h == nil {
		return time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.record.ExpiresAt
}

// canRefresh reports whether a refresh is possible at all.
func (h *credentialHolder) canRefresh() bool {
	if h == nil || h.refresher == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.refresh.Present()
}

// Refresh exchanges the refresh token once, shared across concurrent callers,
// and persists the result before returning.
func (h *credentialHolder) Refresh(ctx context.Context) error {
	if h == nil {
		return errors.New("no credentials to refresh")
	}
	if h.refresher == nil {
		return errors.New("token refresh is unavailable; run `yc login` again")
	}

	h.mu.Lock()
	if h.pending != nil {
		call := h.pending
		h.mu.Unlock()
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !h.refresh.Present() {
		h.mu.Unlock()
		return errors.New("no refresh token is available; run `yc login` again")
	}
	call := &refreshCall{done: make(chan struct{})}
	h.pending = call
	refreshToken := h.refresh
	h.mu.Unlock()

	call.err = h.exchange(ctx, refreshToken)

	h.mu.Lock()
	h.pending = nil
	h.mu.Unlock()
	close(call.done)
	return call.err
}

// RefreshCredentials is the hook the API transport calls when Google answers a
// request with 401, and is what makes a session survive its access token
// expiring mid-broadcast.
//
// It differs from Refresh in exactly one judgement. A refresh that produced a
// working token but could not be written to the credential store is not a
// failure here: the request that hit the 401 can be retried with the token now
// in memory, and reporting failure would end a live session over a read-only
// disk. The write problem is still surfaced, through the same reporter the
// background loop uses, because the next start will read a stale refresh token.
//
// Both entry points share the single-flight in Refresh, so a burst of
// simultaneous 401s and the scheduled pre-expiry refresh cannot race into two
// exchanges of a refresh token Google rotates as it consumes it.
func (h *credentialHolder) RefreshCredentials(ctx context.Context) error {
	if h == nil {
		return errors.New("no credentials to refresh")
	}
	err := h.Refresh(ctx)
	if err != nil && errors.Is(err, errCredentialsNotPersisted) {
		h.report(err)
		return nil
	}
	return err
}

// report hands a non-fatal refresh problem to the configured reporter.
func (h *credentialHolder) report(err error) {
	if h == nil || err == nil {
		return
	}
	h.mu.Lock()
	onError := h.onError
	h.mu.Unlock()
	if onError != nil {
		onError(err)
	}
}

// exchange performs one refresh and persists the rotated credentials before the
// new access token becomes visible.
//
// Persisting first is deliberate: if the write fails after the in-memory token
// had already been swapped, the next process start would read a refresh token
// Google has already rotated away, and the user would be locked out with no
// indication why. A failed write is reported as a warning by the caller, not as
// a failed refresh, because the session in front of the user still works.
func (h *credentialHolder) exchange(ctx context.Context, refreshToken auth.Secret) error {
	tokens, err := h.refresher.Refresh(ctx, refreshToken)
	if err != nil {
		return err
	}

	h.mu.Lock()
	record := h.record.Clone()
	record.AccessToken = tokens.AccessToken
	// Google omits the refresh token on a refresh response, which means
	// "keep the one you have" rather than "you lost it".
	if tokens.RefreshToken.Present() {
		record.RefreshToken = tokens.RefreshToken
	}
	if strings.TrimSpace(tokens.TokenType) != "" {
		record.TokenType = strings.TrimSpace(tokens.TokenType)
	}
	if !tokens.ExpiresAt.IsZero() {
		record.ExpiresAt = tokens.ExpiresAt
	}
	if len(tokens.Scopes) > 0 {
		record.Scopes = append([]auth.Scope(nil), tokens.Scopes...)
	}
	record.UpdatedAt = time.Now().UTC()
	store := h.store
	h.mu.Unlock()

	var saveErr error
	if store != nil {
		saveErr = store.SaveCredentials(ctx, record)
	}

	h.mu.Lock()
	h.record = record
	h.token = record.AccessToken
	h.refresh = record.RefreshToken
	h.mu.Unlock()

	if saveErr != nil {
		// Marked, not replaced: callers that only want to know whether the
		// token is usable check the marker, and the store's own reason stays
		// reachable for the message the user reads.
		return fmt.Errorf("%w: %w", errCredentialsNotPersisted, saveErr)
	}
	return nil
}

// startRefreshLoop keeps a long session alive by refreshing before the access
// token expires rather than after a request has already failed.
//
// A Google access token lasts about an hour, and a stream lasts longer than
// that. Without this the session dies mid-broadcast with a 401 that looks like
// several unrelated features breaking at once. It returns immediately when the
// expiry is unknown or no refresh token is held, because guessing a refresh
// schedule is worse than leaving the failure visible.
func (h *credentialHolder) startRefreshLoop(ctx context.Context, onError func(error)) {
	if h == nil {
		return
	}
	// The reporter is recorded even when the loop declines to start: the
	// transport's 401 path uses it too, and that path is live whenever a
	// refresh token exists at all.
	h.mu.Lock()
	h.onError = onError
	h.mu.Unlock()
	if !h.canRefresh() {
		return
	}
	go func() {
		for {
			expiry := h.expiresAt()
			if expiry.IsZero() {
				return
			}
			wait := time.Until(expiry) - refreshLeadTime
			if wait < time.Second {
				wait = time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if err := h.Refresh(ctx); err != nil && onError != nil {
				onError(err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
}
