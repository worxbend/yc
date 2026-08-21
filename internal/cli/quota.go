package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/youtube"
)

// runQuota prints today's estimated ledger by endpoint and the projected
// exhaustion at the current cadence, with the estimate marked as such.
//
// It spends nothing: the ledger is read from the persisted store, so the
// command that tells you how much budget is left cannot itself consume any.
func runQuota(args []string, stdout, stderr io.Writer) int {
	cfg, code := loadConfigForFlags("quota", quotaUsage, "quota", args, stdout, stderr)
	if code != ExitOK {
		return code
	}
	if _, err := applyStoredCredentials(context.Background(), &cfg); err != nil {
		fmt.Fprintf(stderr, "load credentials: %s\n", config.RedactDisplayValue(err.Error()))
		return ExitFailure
	}

	ledger := newQuotaLedger(cfg)
	snapshot := ledger.Snapshot()
	printQuotaSnapshot(stdout, cfg, snapshot)
	return ExitOK
}

// printQuotaSnapshot renders a ledger snapshot as plain text.
//
// Every total carries the "est." marker. Even where a per-method cost is
// published, the total is yc's own tally of calls yc believes it made, so
// presenting it as fact would invite a user to trust yc's arithmetic over the
// Cloud Console's, which is the one authority that actually knows. The costs
// that are themselves guesses - every live chat method - are named by
// `yc doctor`, which is where a per-method breakdown belongs.
func printQuotaSnapshot(stdout io.Writer, cfg config.Config, snapshot quota.Snapshot) {
	limit := snapshot.LimitUnits
	if limit <= 0 {
		limit = cfg.Quota.DailyQuotaUnits
	}

	fmt.Fprintf(stdout, "used = %d/%d units est.\n", snapshot.UsedUnits, limit)
	fmt.Fprintf(stdout, "remaining = %d units est. (%.0f%%)\n", snapshot.RemainingUnits, snapshot.RemainingPercent())
	fmt.Fprintf(stdout, "search = %d/%d calls\n", snapshot.SearchUsed, snapshot.SearchLimit)
	fmt.Fprintf(stdout, "mode = %s\n", quotaModeLabel(snapshot.Mode))
	fmt.Fprintf(stdout, "resets = %s (%s)\n", formatQuotaTime(snapshot.ResetAt), quota.ResetLocation)
	fmt.Fprintf(stdout, "effective_interval = %s\n", formatQuotaDuration(snapshot.EffectiveInterval))
	fmt.Fprintf(stdout, "server_floor = %s\n", formatQuotaDuration(snapshot.ServerFloor))
	fmt.Fprintf(stdout, "budget_floor = %s\n", formatQuotaDuration(snapshot.BudgetFloor))

	costPerPoll := costTable(cfg.Quota.Costs).Cost(quota.EndpointMessagesList)
	if remaining, ok := snapshot.ProjectedExhaustion(costPerPoll); ok {
		fmt.Fprintf(stdout, "projected_exhaustion = %s at the current cadence, est.\n", formatQuotaDuration(remaining))
	} else {
		fmt.Fprintln(stdout, "projected_exhaustion = unknown; no cadence has been established yet")
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "by endpoint (est. units):")
	if len(snapshot.ByEndpoint) == 0 {
		fmt.Fprintln(stdout, "  (nothing charged today)")
		return
	}
	// quota.SortedEndpoints rather than a local sort: the quota package owns
	// the reserved bookkeeping key that must never be shown, and listing a
	// tally is its job. `yc doctor` already goes through it.
	for _, endpoint := range quota.SortedEndpoints(snapshot.ByEndpoint) {
		fmt.Fprintf(stdout, "  %-26s %d\n", endpoint, snapshot.ByEndpoint[endpoint])
	}
}

// quotaModeLabel renders a cadence mode with the reason it is in force, because
// "stretched" on its own reads like a fault rather than a decision.
func quotaModeLabel(mode quota.Mode) string {
	switch mode {
	case quota.ModeLive:
		return "live (polling at the server-advised cadence)"
	case quota.ModeStretched:
		return "stretched (polling slower than allowed so the daily budget lasts)"
	case quota.ModeBackoff:
		return "backoff (the API asked yc to slow down)"
	case quota.ModePaused:
		return "paused (the daily allowance or the reserve threshold was reached)"
	case "":
		return "idle (no chat has polled yet today)"
	default:
		return string(mode)
	}
}

// formatQuotaDuration renders a cadence, or a dash when it is not yet known.
func formatQuotaDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return d.Round(100 * time.Millisecond).String()
}

// formatQuotaTime renders a reset boundary in local time.
func formatQuotaTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format(time.RFC1123)
}

// staticCredentials snapshots the effective credentials for a one-shot client
// that will not outlive the command, such as the diagnostic identity lookup.
//
// The live chat path uses the credential holder instead: it reads at request
// time so a refresh reaches every client, which a snapshot cannot do.
func staticCredentials(cfg config.Config) youtube.CredentialSource {
	return youtube.StaticCredentials{
		Token: authSecret(cfg.Google.AccessToken),
		Key:   authSecret(cfg.YouTube.APIKey),
	}
}

// quotaEnvironmentSummary is used by setup and doctor output to state the
// arithmetic in one line.
func quotaEnvironmentSummary(cfg config.Config) string {
	cost := costTable(cfg.Quota.Costs).Cost(quota.EndpointMessagesList)
	units := cfg.Quota.DailyQuotaUnits
	if units <= 0 {
		units = config.DefaultDailyQuotaUnits
	}
	polls := units / cost
	if polls <= 0 {
		return ""
	}
	interval := time.Duration(float64(24*time.Hour) / float64(polls))
	return fmt.Sprintf("%d units/day at an estimated %d per poll is about %d polls, or one poll every %s to last a full day",
		units, cost, polls, formatQuotaDuration(interval.Round(time.Second)))
}

// authSecret wraps a config string as a Secret, so a credential never travels
// as a bare string once it leaves the config struct.
func authSecret(value string) auth.Secret {
	return auth.NewSecret(strings.TrimSpace(value))
}
