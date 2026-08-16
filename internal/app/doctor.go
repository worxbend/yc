package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/render"
	"github.com/worxbend/yc/internal/storage"
	"github.com/worxbend/yc/internal/theme"
	"github.com/worxbend/yc/internal/youtube"
)

// DoctorStatus is a single diagnostic outcome.
type DoctorStatus string

// The three severities a doctor check can report.
const (
	DoctorStatusOK   DoctorStatus = "ok"
	DoctorStatusWarn DoctorStatus = "warn"
)

// DoctorReport is the full diagnostic result printed by `yc doctor`.
type DoctorReport struct {
	Checks []DoctorCheck
}

// DoctorCheck is one diagnostic line. Detail is already redacted.
type DoctorCheck struct {
	Name   string
	Status DoctorStatus
	Detail string
}

// ReachabilityProbe reports whether the YouTube API is reachable.
type ReachabilityProbe func(ctx context.Context) error

// DoctorOptions supplies the collaborators diagnostics need, so `yc doctor` can
// run fully offline in tests.
type DoctorOptions struct {
	Environ  []string
	CacheDir string
	// ConfigLoadError is surfaced as a check rather than aborting the run:
	// a broken config is exactly when someone runs doctor.
	ConfigLoadError   error
	ReachabilityProbe ReachabilityProbe
	IdentityLookup    IdentityLookup
	QuotaReporter     QuotaReporter
	// Targets are the configured chats to probe for reachability.
	Targets []youtube.ChatTarget
}

// Doctor runs the default diagnostic set.
func Doctor(ctx context.Context, cfg config.Config) DoctorReport {
	return DoctorWithOptions(ctx, cfg, DoctorOptions{ReachabilityProbe: ProbeYouTubeReachability})
}

// DoctorWithOptions runs diagnostics with explicit collaborators.
//
// Beyond twi's checks it must cover the YouTube-specific ground: API key versus
// OAuth presence, granted scopes, a resolvable own channel, live chat
// reachability for each configured target, today's estimated quota spend with
// its projected exhaustion, the effective poll interval, and the cost table
// actually in use - so a wrong estimate is visible rather than inferred.
func DoctorWithOptions(ctx context.Context, cfg config.Config, opts DoctorOptions) DoctorReport {
	report := DoctorReport{}
	add := func(check DoctorCheck) { report.Checks = append(report.Checks, check) }

	add(doctorConfigCheck(cfg, opts.ConfigLoadError))
	add(doctorCredentialModeCheck(cfg))
	add(doctorThemeCheck(cfg))
	add(doctorDisplayCheck(cfg))
	add(doctorCacheCheck(opts.CacheDir))
	add(doctorDebugLogCheck(cfg))
	report.Checks = append(report.Checks, doctorTargetChecks(cfg, opts.Targets)...)
	add(doctorCostTableCheck(cfg))
	add(doctorQuotaCheck(cfg, opts.QuotaReporter))
	add(doctorReachabilityCheck(ctx, opts.ReachabilityProbe))
	add(doctorIdentityCheck(ctx, cfg, opts.IdentityLookup))

	return report
}

// doctorConfigCheck reports where the config came from and whether it parsed.
func doctorConfigCheck(cfg config.Config, loadErr error) DoctorCheck {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "(no config file)"
	}
	if loadErr != nil {
		return DoctorCheck{
			Name:   "config",
			Status: DoctorStatusWarn,
			Detail: path + ": " + config.RedactDisplayValue(loadErr.Error()) + "; running on environment values and defaults",
		}
	}
	return DoctorCheck{Name: "config", Status: DoctorStatusOK, Detail: "loaded from " + path}
}

// doctorCredentialModeCheck names the mode yc will run in and what it can do.
//
// It reports presence, never a value: the whole point of the check is to be
// safe to paste into a bug report.
func doctorCredentialModeCheck(cfg config.Config) DoctorCheck {
	hasToken := strings.TrimSpace(cfg.Google.AccessToken) != ""
	hasRefresh := strings.TrimSpace(cfg.Google.RefreshToken) != ""
	hasKey := strings.TrimSpace(cfg.YouTube.APIKey) != ""

	switch {
	case hasToken && hasRefresh:
		return DoctorCheck{
			Name:   "credential mode",
			Status: DoctorStatusOK,
			Detail: "OAuth access token and refresh token present; reading, sending, and moderation are available if the granted scopes allow it",
		}
	case hasToken:
		return DoctorCheck{
			Name:   "credential mode",
			Status: DoctorStatusWarn,
			Detail: "OAuth access token present but no refresh token; the session will stop when the token expires (about an hour). Run `yc login`",
		}
	case hasKey:
		return DoctorCheck{
			Name:   "credential mode",
			Status: DoctorStatusOK,
			Detail: "API key only: public live chats are readable, but sending and moderation need `yc login`",
		}
	default:
		return DoctorCheck{
			Name:   "credential mode",
			Status: DoctorStatusWarn,
			Detail: "no credentials: run `yc login`, set YC_YOUTUBE_API_KEY for read-only access, or run `yc chat --mock`",
		}
	}
}

// doctorThemeCheck reports whether the configured palette actually resolved.
func doctorThemeCheck(cfg config.Config) DoctorCheck {
	name := strings.TrimSpace(cfg.Features.ThemeName)
	if name == "" {
		name = theme.DefaultPaletteName
	}
	if _, ok := theme.ResolvePalette(name, cfg.Features.ThemeCustom); !ok {
		return DoctorCheck{
			Name:   "theme",
			Status: DoctorStatusWarn,
			Detail: fmt.Sprintf("unknown theme %q; using %s. Run `yc profile list` for the %d built-in palettes", name, theme.DefaultPaletteName, len(theme.PresetNames())),
		}
	}
	return DoctorCheck{Name: "theme", Status: DoctorStatusOK, Detail: name + " resolved"}
}

// doctorDisplayCheck reports the display modes, naming any value that had to be
// corrected rather than silently substituting it.
func doctorDisplayCheck(cfg config.Config) DoctorCheck {
	var corrected []string
	layout := render.NormalizeLayoutMode(cfg.Features.MessageLayout)
	if raw := strings.TrimSpace(cfg.Features.MessageLayout); raw != "" && !strings.EqualFold(raw, string(layout)) {
		corrected = append(corrected, fmt.Sprintf("message_layout %q -> %s", raw, layout))
	}
	badges := render.NormalizeBadgeMode(cfg.Features.BadgeMode)
	if raw := strings.TrimSpace(cfg.Features.BadgeMode); raw != "" && !strings.EqualFold(raw, string(badges)) {
		corrected = append(corrected, fmt.Sprintf("badge_mode %q -> %s", raw, badges))
	}
	avatar := strings.ToLower(strings.TrimSpace(cfg.Features.AvatarMode))
	if avatar != "" && avatar != "off" && avatar != "initials" {
		corrected = append(corrected, fmt.Sprintf("avatar_mode %q -> initials", cfg.Features.AvatarMode))
	}

	detail := fmt.Sprintf("layout=%s badges=%s avatars=%s animation=%s mouse=%t",
		layout, badges, cfg.Features.AvatarMode, cfg.Features.AnimationMode, cfg.Features.EnableMouse)
	if len(corrected) > 0 {
		return DoctorCheck{Name: "display", Status: DoctorStatusWarn, Detail: detail + "; corrected " + strings.Join(corrected, ", ")}
	}
	return DoctorCheck{Name: "display", Status: DoctorStatusOK, Detail: detail}
}

// doctorCacheCheck probes the cache directory, which is where the estimated
// quota ledger lives. A ledger that cannot be written forgets the day's spend
// on exit, which is a degraded meter and worth saying out loud.
func doctorCacheCheck(dir string) DoctorCheck {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return DoctorCheck{
			Name:   "cache",
			Status: DoctorStatusWarn,
			Detail: "no cache directory; the quota ledger will not survive a restart",
		}
	}
	if err := storage.ProbeWritableDir(dir); err != nil {
		return DoctorCheck{
			Name:   "cache",
			Status: DoctorStatusWarn,
			Detail: dir + " is not writable: " + config.RedactDisplayValue(err.Error()),
		}
	}
	return DoctorCheck{Name: "cache", Status: DoctorStatusOK, Detail: dir + " is writable"}
}

// doctorDebugLogCheck reports where redacted diagnostics will be written.
func doctorDebugLogCheck(cfg config.Config) DoctorCheck {
	if !cfg.Debug.Enabled {
		return DoctorCheck{Name: "debug log", Status: DoctorStatusOK, Detail: "disabled"}
	}
	path := strings.TrimSpace(cfg.Debug.LogPath)
	if path == "" {
		return DoctorCheck{Name: "debug log", Status: DoctorStatusOK, Detail: "enabled, writing to the default path under the cache directory"}
	}
	return DoctorCheck{Name: "debug log", Status: DoctorStatusOK, Detail: "enabled, writing to " + config.RedactDisplayValue(path)}
}

// doctorTargetChecks reports what each configured chat resolved to, without
// spending a unit: classification is pure.
func doctorTargetChecks(cfg config.Config, targets []youtube.ChatTarget) []DoctorCheck {
	if len(targets) == 0 {
		if len(cfg.DefaultChats) == 0 {
			return []DoctorCheck{{
				Name:   "chats",
				Status: DoctorStatusOK,
				Detail: "no default chats configured; yc starts on the empty state and opens one with space c",
			}}
		}
		targets = configuredTargets(cfg.DefaultChats)
	}

	descriptions := make([]string, 0, len(targets))
	for _, target := range targets {
		descriptions = append(descriptions, describeTarget(target))
	}
	return []DoctorCheck{{
		Name:   "chats",
		Status: DoctorStatusOK,
		Detail: fmt.Sprintf("%d configured: %s", len(targets), strings.Join(descriptions, ", ")),
	}}
}

// describeTarget names how a target will be reached and what it will cost.
func describeTarget(target youtube.ChatTarget) string {
	label := strings.TrimSpace(target.Raw)
	if label == "" {
		label = target.Label()
	}
	switch target.Kind {
	case youtube.TargetLiveChatID:
		return label + " (live chat id, no resolution needed)"
	case youtube.TargetVideoID:
		return label + " (video, resolves through videos.list)"
	case youtube.TargetChannelID, youtube.TargetHandle:
		return label + " (channel, resolves through channels.list)"
	default:
		return label + " (unrecognized; yc cannot resolve this)"
	}
}

// doctorCostTableCheck prints the cost table actually in force.
//
// Google publishes no quota cost for any live chat method, so the numbers yc
// meters with are community-observed estimates. Printing the table is the only
// way a wrong constant becomes visible rather than merely wrong.
func doctorCostTableCheck(cfg config.Config) DoctorCheck {
	table := quota.DefaultCostTable()
	overrides := map[string]int{
		quota.EndpointMessagesList:   cfg.Quota.Costs.List,
		quota.EndpointMessagesInsert: cfg.Quota.Costs.Insert,
		quota.EndpointMessagesDelete: cfg.Quota.Costs.Delete,
		quota.EndpointBansInsert:     cfg.Quota.Costs.BansInsert,
		quota.EndpointBansDelete:     cfg.Quota.Costs.BansDelete,
		quota.EndpointVideosList:     cfg.Quota.Costs.VideosList,
		quota.EndpointChannelsList:   cfg.Quota.Costs.ChannelsList,
		quota.EndpointSearchList:     cfg.Quota.Costs.SearchList,
	}
	overridden := 0
	for endpoint, cost := range overrides {
		if cost > 0 {
			table[endpoint] = cost
			overridden++
		}
	}

	// Each cost is marked with the class it belongs to, because "all of these
	// are estimates" was false and unhelpful in both directions: six of them
	// are documented, and hiding that made the one figure the whole poll
	// budget rests on - liveChatMessages.list - look no shakier than the rest.
	parts := make([]string, 0, len(table))
	estimated := 0
	for _, endpoint := range quota.SortedEndpoints(table) {
		mark := "est"
		if quota.IsPublishedCost(endpoint) {
			mark = "pub"
		} else {
			estimated++
		}
		parts = append(parts, fmt.Sprintf("%s=%d(%s)", endpoint, table[endpoint], mark))
	}
	detail := "units: " + strings.Join(parts, " ")
	if overridden > 0 {
		detail += fmt.Sprintf(" (%d overridden from config)", overridden)
	}
	detail += fmt.Sprintf("; pub = Google's published cost, est = yc's estimate (%d of %d, every live chat method)",
		estimated, len(table))
	return DoctorCheck{Name: "cost table", Status: DoctorStatusOK, Detail: detail}
}

// doctorQuotaCheck reports today's estimated spend and when it runs out.
func doctorQuotaCheck(cfg config.Config, reporter QuotaReporter) DoctorCheck {
	limit := cfg.Quota.DailyQuotaUnits
	if limit <= 0 {
		limit = config.DefaultDailyQuotaUnits
	}
	if reporter == nil {
		return DoctorCheck{
			Name:   "quota",
			Status: DoctorStatusOK,
			Detail: fmt.Sprintf("no ledger available; the daily allowance is configured as %d units est.", limit),
		}
	}

	snapshot := reporter.Quota()
	if snapshot.LimitUnits <= 0 {
		snapshot.LimitUnits = limit
	}
	detail := fmt.Sprintf("%d/%d units used today est. (%.0f%% left), resets %s",
		snapshot.UsedUnits, snapshot.LimitUnits, snapshot.RemainingPercent(),
		formatDoctorTime(snapshot.ResetAt))

	if snapshot.SearchLimit > 0 {
		detail += fmt.Sprintf("; search %d/%d calls", snapshot.SearchUsed, snapshot.SearchLimit)
	}
	if snapshot.EffectiveInterval > 0 {
		detail += "; poll interval " + snapshot.EffectiveInterval.Round(100*time.Millisecond).String()
	}

	cost := cfg.Quota.Costs.List
	if cost <= 0 {
		cost = quota.DefaultCostTable().Cost(quota.EndpointMessagesList)
	}
	if remaining, ok := snapshot.ProjectedExhaustion(cost); ok {
		detail += "; projected exhaustion in " + remaining.Round(time.Minute).String() + " at the current cadence"
	}

	status := DoctorStatusOK
	if snapshot.LimitUnits > 0 && snapshot.RemainingPercent() < 25 {
		status = DoctorStatusWarn
	}
	return DoctorCheck{Name: "quota", Status: status, Detail: detail}
}

// doctorReachabilityCheck reports whether the API answered at all.
func doctorReachabilityCheck(ctx context.Context, probe ReachabilityProbe) DoctorCheck {
	if probe == nil {
		return DoctorCheck{Name: "reachability", Status: DoctorStatusOK, Detail: "skipped (no probe configured)"}
	}
	if err := probe(ctx); err != nil {
		return DoctorCheck{
			Name:   "reachability",
			Status: DoctorStatusWarn,
			Detail: "youtube api unreachable: " + config.RedactDisplayValue(err.Error()),
		}
	}
	return DoctorCheck{Name: "reachability", Status: DoctorStatusOK, Detail: "youtube api answered"}
}

// doctorIdentityCheck resolves the signed-in channel, which is what the local
// echo of a sent message renders from.
func doctorIdentityCheck(ctx context.Context, cfg config.Config, lookup IdentityLookup) DoctorCheck {
	if lookup == nil {
		if configured := strings.TrimSpace(cfg.YouTube.ChannelID); configured != "" {
			return DoctorCheck{
				Name:   "identity",
				Status: DoctorStatusWarn,
				Detail: "not resolved; falling back to the configured youtube_channel_id, which yc cannot verify is current",
			}
		}
		return DoctorCheck{Name: "identity", Status: DoctorStatusOK, Detail: "skipped (no user token)"}
	}

	identity, err := lookup.Identity(ctx)
	if err != nil {
		return DoctorCheck{
			Name:   "identity",
			Status: DoctorStatusWarn,
			Detail: "could not resolve your channel: " + config.RedactDisplayValue(err.Error()),
		}
	}
	if strings.TrimSpace(identity.ChannelID) == "" {
		return DoctorCheck{
			Name:   "identity",
			Status: DoctorStatusWarn,
			Detail: "the signed-in account has no YouTube channel, so sending is not possible",
		}
	}

	detail := identity.DisplayName
	if handle := strings.TrimSpace(identity.Handle); handle != "" {
		detail += " (" + handle + ")"
	}
	if identity.SubscriberCountKnown {
		detail += fmt.Sprintf(", %d subscribers", identity.SubscriberCount)
	}
	if len(identity.Scopes) > 0 {
		detail += "; scopes " + strings.Join(identity.Scopes, " ")
	}
	return DoctorCheck{Name: "identity", Status: DoctorStatusOK, Detail: detail}
}

// formatDoctorTime renders a reset boundary in local time.
func formatDoctorTime(at time.Time) string {
	if at.IsZero() {
		return "(unknown)"
	}
	return at.Local().Format("2006-01-02 15:04 MST")
}

// ProbeYouTubeReachability reports whether the API endpoint is reachable.
//
// It delegates to internal/youtube: that lane owns every socket yc opens, and
// keeping the HTTP call there is what lets internal/app stay free of network
// I/O. This wrapper exists so the doctor report has one probe symbol to bind.
func ProbeYouTubeReachability(ctx context.Context) error {
	return youtube.ProbeReachability(ctx)
}
