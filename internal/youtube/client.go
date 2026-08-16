package youtube

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rivo/uniseg"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/quota"
)

// DefaultEndpoint is the YouTube Data API v3 base URL.
const DefaultEndpoint = "https://www.googleapis.com/youtube/v3"

// defaultTimeout bounds one API request. It is generous enough for a slow
// mobile link and short enough that a hung request cannot outlive the poll
// interval it was scheduled inside.
const defaultTimeout = 20 * time.Second

// CredentialSource supplies credentials at request time rather than at
// construction time, so a mid-session token refresh reaches every in-flight
// client without rebuilding it.
//
// Both methods may return an empty Secret: an empty access token with a present
// API key is the supported read-only mode, and both empty is ErrNoCredentials.
type CredentialSource interface {
	AccessToken() auth.Secret
	APIKey() auth.Secret
}

// CredentialRefresher is the optional half of CredentialSource: a source that
// can renew its own access token.
//
// A CredentialSource that implements it is wired to OnAuthFailure
// automatically, which is what lets a session survive the hour mark without
// internal/cli having to hand the transport a second callback that would
// inevitably drift out of step with the source it refreshes. The signature is
// deliberately (context.Context) error and not one that returns a token: the
// transport re-reads credentials per request, so a refresh that returned a
// value would give it a second, staler way to learn the same thing.
//
// Returning nil must mean the next AccessToken read is usable. An
// implementation that renewed the token but failed to persist it should report
// that to the user by another route and return nil here: the request that
// triggered the refresh can be retried, and ending a live session over a
// read-only disk helps nobody.
type CredentialRefresher interface {
	RefreshCredentials(ctx context.Context) error
}

// StaticCredentials is a CredentialSource with fixed values, for tests and for
// the key-only read path.
type StaticCredentials struct {
	Token auth.Secret
	Key   auth.Secret
}

// AccessToken returns the fixed access token.
func (c StaticCredentials) AccessToken() auth.Secret { return c.Token }

// APIKey returns the fixed API key.
func (c StaticCredentials) APIKey() auth.Secret { return c.Key }

// ClientConfig configures the REST client.
//
// Endpoint and HTTPClient are injectable so every adapter can be tested against
// a deterministic in-process server with no network access, which is how the
// whole package is tested.
type ClientConfig struct {
	Credentials CredentialSource
	Endpoint    string
	HTTPClient  *http.Client
	Timeout     time.Duration
	Ledger      *quota.Ledger
	Logger      debuglog.Logger
	// OnAuthFailure renews the access token after the API rejects one with a
	// 401, and is the only retry this transport performs.
	//
	// It is invoked at most once per request, under a client-wide
	// single-flight so a burst of simultaneous 401s produces one token
	// exchange rather than one per in-flight call - Google rotates a refresh
	// token as it consumes it, so the second concurrent exchange would fail
	// and read as a revoked grant. A nil hook means a 401 is terminal, which
	// is the correct behavior for a key-only read and for any caller that
	// has no refresh token.
	//
	// It must not return a token: the transport re-reads credentials per
	// request and takes the new one from the CredentialSource. When
	// Credentials implements CredentialRefresher this is populated for you.
	OnAuthFailure func(ctx context.Context) error
	// HL localizes the API's amountDisplayString. Empty uses the account
	// default.
	HL string
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// String returns a redacted summary.
//
// The config holds a CredentialSource and a debuglog.Logger, both of which
// carry secrets behind unexported fields that fmt reaches by reflection and
// cannot ask to redact themselves. Formatting the whole struct is therefore
// answered here rather than field by field.
func (c ClientConfig) String() string {
	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return "youtube.ClientConfig{endpoint: " + endpoint + "}"
}

// GoString mirrors String for %#v.
func (c ClientConfig) GoString() string { return c.String() }

// Client is the hand-rolled YouTube Data API v3 REST client.
//
// It is hand-rolled rather than generated from google.golang.org/api because
// that module drags in roughly forty transitive dependencies, hides the HTTP
// layer that quota accounting, adaptive backoff, and redaction all need to own,
// and would tempt internal/app toward concrete API types the lane rules forbid.
// Five endpoints do not justify it.
//
// Every method charges the ledger even when the request fails, because Google
// charges for invalid requests too. No method may return an error carrying a
// request URL, a query string, or an Authorization header.
type Client struct {
	cfg ClientConfig

	// authMu guards the credential-refresh single-flight. It is the client's
	// only mutable state; everything else is read-only after NewClient, so a
	// *Client is safe to share across every poller and the send path.
	authMu sync.Mutex
	// authGeneration counts completed refreshes. A request reads it before
	// it dispatches, so a 401 collected against a token that has since been
	// replaced can be told apart from one that still needs an exchange -
	// which is what keeps a burst of failures from queueing a burst of
	// refreshes behind each other.
	authGeneration uint64
	// authPending is the in-flight exchange every concurrent 401 waits on.
	authPending *authCall
}

// authCall is one in-flight credential refresh.
type authCall struct {
	done chan struct{}
	err  error
}

// NewAPIHTTPClient returns the HTTP client the YouTube transport should use.
//
// It refuses redirects outright. The JSON API has no legitimate redirect, and
// yc puts its API key in the query string: net/http strips the Authorization
// header when a redirect crosses hosts, but nothing strips a query parameter
// that the Location happens to preserve. Declining is cheaper than reasoning
// about which of those protections still applies after the next change.
func NewAPIHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewClient returns a client with defaults applied for any unset endpoint, HTTP
// client, timeout, cost table, or clock.
func NewClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if _, err := url.Parse(cfg.Endpoint); err != nil {
		// The endpoint is operator-supplied configuration, not a
		// credential, but a key pasted into it would still be a credential,
		// so the failure is reported without echoing the value.
		return nil, newSafeError("youtube: endpoint is not a valid URL", errors.New("invalid endpoint"))
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewAPIHTTPClient(cfg.Timeout)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Credentials == nil {
		cfg.Credentials = StaticCredentials{}
	}
	// A credential source that can renew itself is wired up unless the
	// caller supplied its own hook, so the mid-session refresh is on by
	// construction rather than by every call site remembering it.
	if cfg.OnAuthFailure == nil {
		if refresher, ok := cfg.Credentials.(CredentialRefresher); ok {
			cfg.OnAuthFailure = refresher.RefreshCredentials
		}
	}
	return &Client{cfg: cfg}, nil
}

// String and GoString keep the client out of formatted output.
//
// This is not decoration. fmt reaches unexported fields by reflection and
// cannot call String on them, so a %v or %+v of a *Client would print the
// credential source verbatim and defeat auth.Secret's redaction entirely. The
// only reliable defense is for the outermost value to format itself.
func (c *Client) String() string { return "youtube.Client{}" }

// GoString mirrors String for %#v.
func (c *Client) GoString() string { return c.String() }

// credentials reads the current access token and API key. They are read per
// request rather than captured at construction so a refresh performed by
// internal/auth mid-session reaches every client that was built at startup.
func (c *Client) credentials() (auth.Secret, auth.Secret) {
	if c == nil || c.cfg.Credentials == nil {
		return "", ""
	}
	return c.cfg.Credentials.AccessToken(), c.cfg.Credentials.APIKey()
}

// authEpoch reads the current refresh generation.
//
// It is read before a request is dispatched, not after it fails, because the
// question a 401 has to answer is "was this token already replaced while I was
// on the wire?" - and only a value sampled beforehand can answer it.
func (c *Client) authEpoch() uint64 {
	if c == nil {
		return 0
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.authGeneration
}

// refreshCredentials renews the access token at most once for the given epoch.
//
// Three callers arrive here and each gets a different answer. One whose epoch
// is stale is told the token has already been replaced and simply retries. One
// that finds an exchange running waits for it and shares its outcome. The first
// one to arrive performs the exchange. Nothing here retries: a failed exchange
// is reported to exactly the requests that were waiting on it, and the next 401
// starts a new epoch's worth of work rather than looping on this one.
//
// The context is the failed request's, so a user who quits mid-refresh stops
// waiting immediately even though the exchange the leader started runs to
// completion for whoever else is still listening.
func (c *Client) refreshCredentials(ctx context.Context, epoch uint64) error {
	if c == nil || c.cfg.OnAuthFailure == nil {
		return errors.New("no credential refresh is configured")
	}

	c.authMu.Lock()
	if c.authGeneration != epoch {
		// A refresh completed while this request was in flight, so its 401
		// was answered by a token that is already gone. Retrying with the
		// current one costs nothing and is the whole point of sharing one
		// credential source.
		c.authMu.Unlock()
		return nil
	}
	if call := c.authPending; call != nil {
		c.authMu.Unlock()
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &authCall{done: make(chan struct{})}
	c.authPending = call
	hook := c.cfg.OnAuthFailure
	c.authMu.Unlock()

	// The hook runs outside the lock: it performs a network exchange, and
	// holding a mutex across one would stall every other request's read of
	// the epoch behind it.
	err := hook(ctx)

	c.authMu.Lock()
	c.authPending = nil
	if err == nil {
		c.authGeneration++
	}
	call.err = err
	c.authMu.Unlock()
	// Waiters read call.err only after this close, which orders the write
	// above ahead of every read of it.
	close(call.done)
	return err
}

// log writes a redacted debug record. Nothing this package logs may name a
// credential, so callers pass curated fields only.
func (c *Client) log(ctx context.Context, event string, attrs ...slog.Attr) {
	if c == nil || !c.cfg.Logger.Enabled() {
		return
	}
	c.cfg.Logger.Log(ctx, event, attrs...)
}

// hasToken reports whether an OAuth access token is available. Every write path
// checks it first: an API key can never satisfy insert, delete, or bans, and
// discovering that from a 401 costs an estimated 50 units.
func (c *Client) hasToken() bool {
	token, _ := c.credentials()
	return token.Present()
}

// redact removes the live credentials and every known credential pattern from a
// string bound for an error or a debug record.
func (c *Client) redact(value string) string {
	token, key := c.credentials()
	return auth.NewRedactor(token, key).Redact(value)
}

// safeCause reduces a transport error to a cause that carries no request URL
// and no credential, so retaining it for errors.Is cannot leak one. See
// transportCause: the API key rides in the query string, which puts it inside
// every *url.Error net/http produces.
func (c *Client) safeCause(err error) error {
	return transportCause(err, c.redact)
}

// endpoint returns the configured base URL.
func (c *Client) endpoint() string {
	if strings.TrimSpace(c.cfg.Endpoint) == "" {
		return DefaultEndpoint
	}
	return c.cfg.Endpoint
}

// httpClient returns the configured HTTP client.
func (c *Client) httpClient() *http.Client {
	if c.cfg.HTTPClient == nil {
		return http.DefaultClient
	}
	return c.cfg.HTTPClient
}

// now returns the injected clock.
func (c *Client) now() time.Time {
	if c.cfg.Now == nil {
		return time.Now()
	}
	return c.cfg.Now()
}

// charge records one dispatched request against the estimated ledger and
// returns the units charged. A nil ledger is a supported configuration - the
// fake client and the unit tests run without one - and charges nothing.
func (c *Client) charge(endpoint string) int {
	if c == nil || c.cfg.Ledger == nil {
		return 0
	}
	return c.cfg.Ledger.Charge(endpoint)
}

// cost returns the estimated unit cost of an endpoint without charging it.
//
// It consults the ledger's table rather than the built-in one so a config
// override is reflected everywhere a cost is quoted, not only where it is
// charged.
func (c *Client) cost(endpoint string) int {
	if c != nil && c.cfg.Ledger != nil {
		return c.cfg.Ledger.Cost(endpoint)
	}
	return quota.DefaultCostTable().Cost(endpoint)
}

// CostOf quotes the estimated unit cost of an endpoint without charging it.
// It is the exported face of cost, present so PollerClient - the poller's view
// of this client - can include the price lookup its budget arithmetic needs.
func (c *Client) CostOf(endpoint string) int {
	return c.cost(endpoint)
}

// Quota returns the current estimated ledger snapshot.
//
// Every figure in it is an estimate: Google publishes no quota cost for any
// live chat method, so the cost table is community-observed and
// config-overridable. Callers must render the estimate marker alongside any
// number taken from here.
func (c *Client) Quota() quota.Snapshot {
	if c == nil || c.cfg.Ledger == nil {
		return quota.Snapshot{Estimated: true, At: c.now()}
	}
	return c.cfg.Ledger.Snapshot()
}

// truncateRunes shortens a diagnostic string on a grapheme-cluster boundary.
// Error detail reaches the status line, so it is cut with the same width-aware
// rules as chat text rather than by byte.
func truncateRunes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	graphemes := uniseg.NewGraphemes(value)
	count := 0
	end := 0
	for graphemes.Next() {
		count++
		if count > limit {
			return value[:end] + "..."
		}
		_, end = graphemes.Positions()
	}
	return value
}
