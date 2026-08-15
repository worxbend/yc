package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/app"
	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// HTTP timeouts. The chat poll is the long one because a list call can return
// two thousand messages; metadata lookups are short because they block a UI
// refresh and a stale viewer count is better than a stalled frame.
const (
	chatHTTPTimeout     = 20 * time.Second
	metadataHTTPTimeout = 10 * time.Second
)

// manualPollInterval backs poll_interval_mode = "off".
//
// The poll loop still exists so ctrl+r refreshes on demand and the session
// keeps its page token, but it never comes round on its own inside a sitting.
// Stopping the loop outright would lose the token and re-deliver the entire
// backlog on the next manual refresh.
const manualPollInterval = 24 * time.Hour

// runLiveChat is the app entrypoint, indirected so tests can drive startup
// without a terminal.
var runLiveChat = app.RunClientWithOptions

// The poller must keep satisfying the adapter's optional restart capability.
// Losing it is silent - the adapter falls back to rebuilding the transport, and
// the only symptom is ctrl+r reprinting a few minutes of chat and spending a
// resolve unit to do it - so it is asserted here, at the one place both types
// are visible.
var (
	_ app.LiveChatTransport   = (*youtube.Poller)(nil)
	_ app.LiveChatReconnector = (*youtube.Poller)(nil)
)

// newLiveChatClient builds the live chat client for the resolved targets. It is
// a variable so tests can substitute a fake transport.
var newLiveChatClient = func(client *youtube.Client, cfg config.Config, targets []youtube.ChatTarget, logger debuglog.Logger) (app.ChatClient, error) {
	minInterval, maxInterval := pollIntervalBounds(cfg.Quota)
	factory := func(target youtube.ChatTarget) (app.LiveChatTransport, error) {
		return youtube.NewPoller(youtube.PollerConfig{
			Client:              client,
			Target:              target,
			MinInterval:         minInterval,
			MaxInterval:         maxInterval,
			FollowServerCadence: cfg.Quota.FollowServerCadence,
			SessionHorizon:      time.Duration(cfg.Quota.SessionHours) * time.Hour,
			ReservePercent:      cfg.Quota.ReservePercent,
			Logger:              logger,
		})
	}
	return app.NewLiveChatClient(app.LiveChatConfig{
		Factory: factory,
		Targets: targets,
		Logger:  logger,
		// Moderation is a call on the credential, not on a poll loop, so the
		// shared REST client is handed over directly. It is the same client
		// the pollers use, which keeps every moderation call on the one quota
		// ledger the status bar reports.
		Moderator: client,
	})
}

// newYouTubeClient builds the REST client every live feature shares.
//
// One client means one quota ledger: the meter in the status bar counts the
// chat poll, the metadata refresh, and the send path together, which is the
// only way the number matches what the Cloud Console will show.
func newYouTubeClient(_ config.Config, credentials youtube.CredentialSource, ledger *youtube.QuotaLedger, logger debuglog.Logger) (*youtube.Client, error) {
	return youtube.NewClient(youtube.ClientConfig{
		Credentials: credentials,
		HTTPClient:  youtube.NewAPIHTTPClient(chatHTTPTimeout),
		Timeout:     chatHTTPTimeout,
		Ledger:      ledger,
		Logger:      logger,
	})
}

// newQuotaLedger builds the estimated ledger, persisted so a restart does not
// zero the meter and hand the user a false sense of budget.
//
// A ledger that cannot find a cache directory still works; it just forgets on
// exit, which is a degraded meter rather than a broken startup.
func newQuotaLedger(cfg config.Config) *youtube.QuotaLedger {
	ledgerCfg := youtube.LedgerConfig{
		DailyUnits:  cfg.Quota.DailyQuotaUnits,
		SearchCalls: cfg.Quota.SearchQuotaCalls,
		Costs:       costTable(cfg.Quota.Costs),
		Fingerprint: youtube.CredentialFingerprint(cfg.Google.ClientID, cfg.YouTube.ChannelID),
	}
	if dir := ledgerStoreDir(); dir != "" {
		ledgerCfg.Store = youtube.NewFileLedgerStore(dir)
	}
	return youtube.NewQuotaLedger(ledgerCfg)
}

// ledgerStoreDir is where the persisted quota ledger lives, or "" on a machine
// with no usable cache directory.
func ledgerStoreDir() string {
	dir, err := config.DefaultCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "quota")
}

// ledgerPruneTimeout bounds the startup prune. It is generous for a directory
// listing and a handful of unlinks, and short enough that a hung filesystem
// cannot keep a goroutine alive for the length of a stream.
const ledgerPruneTimeout = 5 * time.Second

// startLedgerPrune deletes ledger records older than the retention window and
// returns a channel closed when it is finished.
//
// It runs off the hot path, in a goroutine with its own deadline, and every
// failure is swallowed into the debug log. That is deliberate: this is
// housekeeping on an advisory meter, and there is no outcome of it - a
// read-only cache directory, a slow disk, no cache directory at all - that
// should delay the alt screen coming up or stop chat from opening. The returned
// channel exists for tests; startup ignores it.
func startLedgerPrune(ctx context.Context, logger debuglog.Logger) <-chan struct{} {
	done := make(chan struct{})
	dir := ledgerStoreDir()
	if dir == "" {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		// Detached from the caller's context on purpose: the prune is not
		// worth canceling, and inheriting a context the caller cancels on
		// its own error path would leave the records behind on exactly the
		// runs that never get another chance to tidy up.
		pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerPruneTimeout)
		defer cancel()
		if err := youtube.NewFileLedgerStore(dir).Prune(pruneCtx, youtube.LedgerRetentionDays); err != nil {
			logger.Log(pruneCtx, "cli.quota.ledger_prune_failed", debuglog.Err("error", err))
		}
	}()
	return done
}

// costTable maps the config-overridable cost block onto the transport's table.
//
// Google publishes a cost for every method yc calls outside live chat, and none
// for any live chat method, so the five liveChat values here are estimates and
// yc labels them as such at every surface that shows a number. The table is
// data precisely so a corrected figure is a config line rather than a release.
func costTable(costs config.QuotaCostConfig) youtube.CostTable {
	table := youtube.DefaultCostTable()
	overrides := map[string]int{
		youtube.EndpointMessagesList:   costs.List,
		youtube.EndpointMessagesInsert: costs.Insert,
		youtube.EndpointMessagesDelete: costs.Delete,
		youtube.EndpointBansInsert:     costs.BansInsert,
		youtube.EndpointBansDelete:     costs.BansDelete,
		youtube.EndpointVideosList:     costs.VideosList,
		youtube.EndpointChannelsList:   costs.ChannelsList,
		youtube.EndpointSearchList:     costs.SearchList,
	}
	for endpoint, cost := range overrides {
		if cost > 0 {
			table[endpoint] = cost
		}
	}
	return table
}

// pollIntervalBounds maps poll_interval_mode onto the poller's clamp.
//
// The server's pollingIntervalMillis is an absolute floor beneath both of
// these and is enforced by the poller itself; these bounds only ever slow yc
// down, never speed it up.
func pollIntervalBounds(quota config.QuotaConfig) (minInterval, maxInterval time.Duration) {
	minInterval = time.Duration(quota.PollIntervalFloorMS) * time.Millisecond
	if minInterval <= 0 {
		minInterval = time.Second
	}
	maxInterval = time.Duration(quota.PollIntervalCeilingMS) * time.Millisecond

	switch strings.ToLower(strings.TrimSpace(quota.PollIntervalMode)) {
	case "economy":
		if minInterval < 5*time.Second {
			minInterval = 5 * time.Second
		}
	case "off":
		minInterval = manualPollInterval
		maxInterval = manualPollInterval
	}
	if maxInterval > 0 && maxInterval < minInterval {
		maxInterval = minInterval
	}
	return minInterval, maxInterval
}

// newLiveClientOptions wires the app's optional collaborators to the shared
// REST client.
//
// The lookups that need a user token are gated on having one: with an API key
// alone they would fail on every call and turn one missing credential into
// several unexplained errors on unrelated surfaces. The ones a key can answer
// stay wired, because read-only mode should still label chats by title and show
// a viewer count.
func newLiveClientOptions(_ config.Config, client *youtube.Client, capability credentialCapability, logger debuglog.Logger, notifier app.SystemNotifier) app.ClientOptions {
	opts := app.ClientOptions{
		SystemNotifier:    notifier,
		DebugLogger:       logger,
		BroadcastResolver: client,
		CategoryLookup:    client,
	}
	if capability.Mode == credentialModeOAuth {
		opts.IdentityLookup = client
		opts.SubscriptionLookup = client
		opts.StreamInfoManager = client
	}
	return opts
}

// chatTargets classifies every chat the user named, without any network work.
//
// An explicit live chat ID comes first and skips resolution entirely, which is
// the only zero-quota way to open a chat. A target that cannot be classified is
// reported rather than dropped: silently opening fewer chats than were asked
// for is the kind of failure a user does not notice until the stream is over.
func chatTargets(cfg config.Config, liveChatID string) ([]youtube.ChatTarget, error) {
	var targets []youtube.ChatTarget
	if id := strings.TrimSpace(liveChatID); id != "" {
		targets = append(targets, youtube.ChatTarget{
			Raw:        id,
			Kind:       youtube.TargetLiveChatID,
			LiveChatID: id,
		})
	}

	var problems []string
	for _, raw := range cfg.DefaultChats {
		target, err := youtube.ParseChatTarget(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%q: %s", raw, config.RedactDisplayValue(err.Error())))
			continue
		}
		targets = append(targets, target)
	}
	if len(problems) > 0 {
		return targets, fmt.Errorf("could not read %s; name a chat by video ID, watch/live/shorts URL, youtu.be link, @handle, channel ID, or --live-chat-id", strings.Join(problems, "; "))
	}
	return targets, nil
}

// ledgerQuotaReporter adapts the transport's ledger to the app's optional
// QuotaReporter capability, so `yc doctor` can report today's estimated spend
// without constructing a poller or spending a unit.
type ledgerQuotaReporter struct {
	ledger *youtube.QuotaLedger
}

// Quota returns the current estimated snapshot.
func (r ledgerQuotaReporter) Quota() youtube.QuotaSnapshot {
	if r.ledger == nil {
		return youtube.QuotaSnapshot{}
	}
	return r.ledger.Snapshot()
}

// validateLiveChatCredentials reports the credentials live chat cannot start
// without, before anything opens a socket.
//
// Running with no credentials must fail here rather than at the first request:
// an error that arrives after the alt screen is up is invisible, and the
// message has to name the three ways forward, one of which needs nothing at all.
func validateLiveChatCredentials(capability credentialCapability) error {
	if capability.Mode != credentialModeNone {
		return nil
	}
	return errors.New("no YouTube credentials configured. " +
		"Run `yc login` for full access, set YC_YOUTUBE_API_KEY to read public chats without logging in, " +
		"or run `yc chat --mock` for the credential-free demo")
}

// refreshCapabilityWarning names the gap that ends a long session.
//
// A Google access token lasts about an hour, and a stream lasts longer. yc
// refreshes on its own, but only when it holds a refresh token; a token pasted
// into the environment has none, so chat simply stops partway through the
// stream having warned about nothing. Saying so at startup costs one line and
// turns a mystery disconnect into a known limitation.
func refreshCapabilityWarning(cfg config.Config, capability credentialCapability) string {
	if capability.Mode != credentialModeOAuth {
		return ""
	}
	if strings.TrimSpace(cfg.Google.RefreshToken) != "" {
		return ""
	}
	return "warning: no refresh token is configured, so this access token cannot be renewed. " +
		"Live chat will stop when it expires (about an hour) and will not resume on its own. " +
		"Run `yc login` to save a refresh token."
}

// clientSecretWarning explains the unattended-refresh caveat for an installed
// app whose OAuth client requires a secret.
func clientSecretWarning(cfg config.Config) string {
	if strings.TrimSpace(cfg.Google.RefreshToken) == "" || strings.TrimSpace(cfg.Google.ClientSecret) != "" {
		return ""
	}
	if strings.TrimSpace(cfg.Google.ClientID) == "" {
		return ""
	}
	return "note: no client secret is configured. Desktop OAuth clients created as \"Desktop app\" refresh with PKCE alone, " +
		"but if your client was issued a secret, set YC_GOOGLE_CLIENT_SECRET so the saved refresh token can be redeemed."
}

// quotaWarning states the arithmetic that defines this client, once, at startup.
func quotaWarning(cfg config.Config) string {
	cost := cfg.Quota.Costs.List
	if cost <= 0 {
		cost = youtube.DefaultCostTable().Cost(youtube.EndpointMessagesList)
	}
	units := cfg.Quota.DailyQuotaUnits
	if units <= 0 {
		units = config.DefaultDailyQuotaUnits
	}
	if !cfg.Quota.FollowServerCadence {
		return ""
	}
	polls := units / cost
	if polls <= 0 {
		return ""
	}
	return fmt.Sprintf("warning: follow_server_cadence is on, so yc will poll at YouTube's advised interval. "+
		"At an estimated %d units per poll that is about %d polls before the daily allowance is gone; "+
		"unset it to let yc stretch its cadence and last the day.", cost, polls)
}

// startupRedactor returns a redactor loaded with every credential in play, for
// the error paths that report a failure the transport produced.
func startupRedactor(cfg config.Config) auth.Redactor {
	return auth.NewRedactor(
		auth.NewSecret(cfg.Google.ClientSecret),
		auth.NewSecret(cfg.Google.AccessToken),
		auth.NewSecret(cfg.Google.RefreshToken),
		auth.NewSecret(cfg.YouTube.APIKey),
	)
}

// safeStartupError renders an error for the terminal with both the config-level
// display redaction and every known secret removed.
func safeStartupError(redactor auth.Redactor, err error) string {
	if err == nil {
		return ""
	}
	return config.RedactDisplayValue(redactor.Redact(err.Error()))
}
