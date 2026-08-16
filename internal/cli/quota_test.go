package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/quota"
	"github.com/worxbend/yc/internal/storage"
)

// Every unit figure yc prints is an estimate: Google publishes no per-method
// cost for any live chat method. Presenting one as fact would invite a user to
// trust yc's arithmetic over the Cloud Console's, which is the one authority
// that actually knows.
func TestQuotaSnapshotMarksEveryUnitFigureAsAnEstimate(t *testing.T) {
	cfg := config.Default()
	snapshot := quota.Snapshot{
		UsedUnits:         3240,
		LimitUnits:        10000,
		RemainingUnits:    6760,
		SearchUsed:        2,
		SearchLimit:       100,
		Mode:              quota.ModeStretched,
		ResetAt:           time.Date(2026, 8, 9, 7, 0, 0, 0, time.UTC),
		EffectiveInterval: 5 * time.Second,
		ServerFloor:       2 * time.Second,
		BudgetFloor:       5 * time.Second,
		Estimated:         true,
		ByEndpoint: map[string]int{
			"liveChatMessages.list": 3200,
			"videos.list":           40,
		},
	}

	var out bytes.Buffer
	printQuotaSnapshot(&out, cfg, snapshot)
	got := out.String()

	for _, want := range []string{
		"used = 3240/10000 units est.",
		"remaining = 6760 units est.",
		"search = 2/100 calls",
		"stretched (polling slower than allowed so the daily budget lasts)",
		"effective_interval = 5s",
		"server_floor = 2s",
		"budget_floor = 5s",
		"by endpoint (est. units):",
		"liveChatMessages.list",
		"videos.list",
		string(quota.ResetLocation),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("quota output is missing %q:\n%s", want, got)
		}
	}
	// Endpoints are sorted so the output is diffable between runs.
	if strings.Index(got, "liveChatMessages.list") > strings.Index(got, "videos.list") {
		t.Errorf("endpoints are not sorted:\n%s", got)
	}
}

func TestQuotaSnapshotWithNothingChargedSaysSo(t *testing.T) {
	var out bytes.Buffer
	printQuotaSnapshot(&out, config.Default(), quota.Snapshot{LimitUnits: 10000})
	got := out.String()
	if !strings.Contains(got, "(nothing charged today)") {
		t.Errorf("an empty ledger did not say so:\n%s", got)
	}
	if !strings.Contains(got, "no cadence has been established yet") {
		t.Errorf("an unprojectable exhaustion did not explain itself:\n%s", got)
	}
	if !strings.Contains(got, "idle (no chat has polled yet today)") {
		t.Errorf("an empty mode did not render as idle:\n%s", got)
	}
}

// A zero limit falls back to the configured allowance rather than printing
// "used = N/0", which reads as a broken meter.
func TestQuotaSnapshotFallsBackToTheConfiguredLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Quota.DailyQuotaUnits = 50000

	var out bytes.Buffer
	printQuotaSnapshot(&out, cfg, quota.Snapshot{UsedUnits: 10})
	if !strings.Contains(out.String(), "used = 10/50000 units est.") {
		t.Errorf("limit fallback did not apply:\n%s", out.String())
	}
}

// "stretched" on its own reads like a fault rather than a decision, so every
// mode carries the reason it is in force.
func TestQuotaModeLabelAlwaysCarriesItsReason(t *testing.T) {
	cases := map[quota.Mode]string{
		quota.ModeLive:      "server-advised",
		quota.ModeStretched: "daily budget lasts",
		quota.ModeBackoff:   "asked yc to slow down",
		quota.ModePaused:    "reserve threshold",
		"":                  "idle",
	}
	for mode, want := range cases {
		got := quotaModeLabel(mode)
		if !strings.Contains(got, want) {
			t.Errorf("quotaModeLabel(%q) = %q, want it to mention %q", mode, got, want)
		}
	}
	// An unrecognized mode from a future transport degrades to itself rather
	// than to an empty cell.
	if got := quotaModeLabel(quota.Mode("hibernating")); got != "hibernating" {
		t.Errorf("unknown mode = %q, want it echoed", got)
	}
}

func TestFormatQuotaDurationAndTime(t *testing.T) {
	if got := formatQuotaDuration(0); got != "-" {
		t.Errorf("zero duration = %q, want a dash rather than %q", got, "0s")
	}
	if got := formatQuotaDuration(-time.Second); got != "-" {
		t.Errorf("negative duration = %q, want a dash", got)
	}
	if got := formatQuotaDuration(5234 * time.Millisecond); got != "5.2s" {
		t.Errorf("duration = %q, want it rounded to a tenth", got)
	}
	if got := formatQuotaTime(time.Time{}); got != "-" {
		t.Errorf("zero time = %q, want a dash", got)
	}
	moment := time.Date(2026, 8, 9, 7, 0, 0, 0, time.UTC)
	if got := formatQuotaTime(moment); !strings.Contains(got, "2026") {
		t.Errorf("time = %q, want a readable local timestamp", got)
	}
}

// The arithmetic that governs poll cadence is stated in one line, so the number
// in the status bar has an explanation behind it.
func TestQuotaEnvironmentSummaryStatesTheArithmetic(t *testing.T) {
	cfg := config.Default()
	got := quotaEnvironmentSummary(cfg)
	for _, want := range []string{"10000 units/day", "5 per poll", "2000 polls", "one poll every"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}

	// A cost that exceeds the whole allowance yields no polls, and claiming a
	// cadence for it would be arithmetic nobody can act on.
	starved := config.Default()
	starved.Quota.DailyQuotaUnits = 1
	starved.Quota.Costs.List = 500
	if got := quotaEnvironmentSummary(starved); got != "" {
		t.Errorf("summary = %q, want nothing when the budget buys no polls", got)
	}
}

func TestQuotaBudgetDoctorCheckReportsTheArithmeticOrItsAbsence(t *testing.T) {
	check := quotaBudgetDoctorCheck(config.Default())
	if !strings.Contains(check.Detail, "estimates") {
		t.Errorf("detail = %q, want the estimate marker", check.Detail)
	}

	starved := config.Default()
	starved.Quota.DailyQuotaUnits = 1
	starved.Quota.Costs.List = 500
	if got := quotaBudgetDoctorCheck(starved); got.Detail == "" || !strings.Contains(got.Detail, "not configured") {
		t.Errorf("check = %+v, want it to report an unusable budget", got)
	}
}

// The command that tells you how much budget is left must not itself consume
// any: it reads the persisted ledger and dispatches nothing.
func TestQuotaCommandSpendsNothing(t *testing.T) {
	clearCredentialEnv(t)
	withMemoryCredentialStore(t, storage.CredentialRecord{})
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := writeTempConfig(t, "")

	restore := swapLiveChatClient(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"quota", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("quota = %d, stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"used =", "remaining =", "resets =", "by endpoint"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("quota output is missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestQuotaCommandRejectsAnUnparseableFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"quota", "--nope"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("quota = %d, want %d", code, ExitUsage)
	}
}

// A one-shot client that will not outlive the command snapshots credentials;
// the live path uses the holder instead so a refresh reaches every client.
func TestStaticCredentialsSnapshotsTheEffectiveValues(t *testing.T) {
	cfg := config.Default()
	cfg.Google.AccessToken = "  " + fakeToken + "  "
	cfg.YouTube.APIKey = "AIza-" + fakeToken

	source := staticCredentials(cfg)
	if got := source.AccessToken().Reveal(); got != fakeToken {
		t.Errorf("access token = %q, want the trimmed value", got)
	}
	if got := source.APIKey().Reveal(); got != "AIza-"+fakeToken {
		t.Errorf("api key = %q", got)
	}

	// A credential never travels as a bare string once it leaves the config.
	if got := authSecret("  " + fakeToken + " ").String(); strings.Contains(got, fakeToken) {
		t.Fatalf("Secret.String() revealed its value: %q", got)
	}
	if authSecret("   ").Present() {
		t.Error("whitespace must not count as a configured credential")
	}
}

// The ledger adapter lets doctor report today's spend without constructing a
// poller or spending a unit, and a nil ledger degrades to an empty snapshot
// rather than to a panic.
func TestLedgerQuotaReporter(t *testing.T) {
	if got := (ledgerQuotaReporter{}).Quota(); got.LimitUnits != 0 || got.UsedUnits != 0 {
		t.Errorf("nil ledger = %+v, want the zero snapshot", got)
	}

	ledger := quota.NewLedger(quota.Config{DailyUnits: 500})
	ledger.Charge(quota.EndpointMessagesList)
	snapshot := ledgerQuotaReporter{ledger: ledger}.Quota()
	if snapshot.UsedUnits == 0 {
		t.Error("the reporter did not see a charged call")
	}
	if snapshot.LimitUnits != 500 {
		t.Errorf("limit = %d, want 500", snapshot.LimitUnits)
	}
}

// A ledger that cannot find a cache directory still works; it just forgets on
// exit, which is a degraded meter rather than a broken startup.
func TestNewQuotaLedgerPersistsUnderTheCacheDirectory(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	cfg := config.Default()
	cfg.Google.ClientID = "client-abc"
	cfg.YouTube.ChannelID = "UC123"
	ledger := newQuotaLedger(cfg)
	ledger.Charge(quota.EndpointMessagesList)

	// A second ledger built the same way must find the first one's tally:
	// restarting yc must not zero the meter and hand back a false budget.
	reloaded := newQuotaLedger(cfg)
	if got := reloaded.Snapshot().UsedUnits; got == 0 {
		t.Errorf("used = %d after a restart, want the persisted tally", got)
	}

	// A different account gets its own ledger.
	other := config.Default()
	other.Google.ClientID = "client-xyz"
	if got := newQuotaLedger(other).Snapshot().UsedUnits; got != 0 {
		t.Errorf("a second account inherited %d units from the first", got)
	}

	if _, err := filepath.Glob(filepath.Join(cache, "*", "quota", "*")); err != nil {
		t.Fatalf("glob: %v", err)
	}
}
