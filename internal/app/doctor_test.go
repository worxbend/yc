package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/youtube"
)

// offlineDoctorOptions runs diagnostics with no network at all.
func offlineDoctorOptions(t *testing.T) DoctorOptions {
	t.Helper()
	return DoctorOptions{
		CacheDir:          t.TempDir(),
		ReachabilityProbe: func(context.Context) error { return nil },
	}
}

// checkNamed returns the named check, failing if diagnostics omitted it.
func checkNamed(t *testing.T, report DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("report has no %q check; got %v", name, checkNames(report))
	return DoctorCheck{}
}

func checkNames(report DoctorReport) []string {
	names := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		names = append(names, check.Name)
	}
	return names
}

func TestDoctorRunsOfflineAndCoversTheYouTubeGround(t *testing.T) {
	report := DoctorWithOptions(t.Context(), config.Default(), offlineDoctorOptions(t))

	for _, name := range []string{"config", "credential mode", "theme", "display", "cache", "chats", "cost table", "quota", "reachability", "identity"} {
		checkNamed(t, report, name)
	}
}

func TestDoctorReportsABrokenConfigInsteadOfAborting(t *testing.T) {
	opts := offlineDoctorOptions(t)
	opts.ConfigLoadError = errors.New("config.toml:4: not a key = value line")

	report := DoctorWithOptions(t.Context(), config.Default(), opts)
	check := checkNamed(t, report, "config")
	if check.Status != DoctorStatusWarn {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	// Doctor is what someone runs when the config is already broken, so a
	// load failure has to be a line in the report rather than the end of it.
	if len(report.Checks) < 5 {
		t.Fatalf("diagnostics stopped after the config failure: %v", checkNames(report))
	}
}

func TestDoctorNamesTheCredentialModeWithoutEchoingACredential(t *testing.T) {
	const fakeToken = "test-not-a-real-token"
	for name, tc := range map[string]struct {
		mutate func(*config.Config)
		want   DoctorStatus
		says   string
	}{
		"nothing configured": {
			mutate: func(cfg *config.Config) {},
			want:   DoctorStatusWarn,
			says:   "yc login",
		},
		"api key only": {
			mutate: func(cfg *config.Config) { cfg.YouTube.APIKey = fakeToken },
			want:   DoctorStatusOK,
			says:   "API key only",
		},
		"token without refresh": {
			mutate: func(cfg *config.Config) { cfg.Google.AccessToken = fakeToken },
			want:   DoctorStatusWarn,
			says:   "no refresh token",
		},
		"full oauth": {
			mutate: func(cfg *config.Config) {
				cfg.Google.AccessToken = fakeToken
				cfg.Google.RefreshToken = fakeToken
			},
			want: DoctorStatusOK,
			says: "refresh token present",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			tc.mutate(&cfg)

			report := DoctorWithOptions(t.Context(), cfg, offlineDoctorOptions(t))
			check := checkNamed(t, report, "credential mode")
			if check.Status != tc.want {
				t.Fatalf("status = %q, want %q (%q)", check.Status, tc.want, check.Detail)
			}
			if !strings.Contains(check.Detail, tc.says) {
				t.Fatalf("detail = %q, want it to mention %q", check.Detail, tc.says)
			}
			// The whole point of the report is that it is safe to paste into
			// a bug thread.
			for _, line := range report.Checks {
				if strings.Contains(line.Detail, fakeToken) {
					t.Fatalf("check %q leaked a credential: %q", line.Name, line.Detail)
				}
			}
		})
	}
}

func TestDoctorWarnsAboutAnUnknownTheme(t *testing.T) {
	cfg := config.Default()
	cfg.Features.ThemeName = "not-a-real-theme"

	check := checkNamed(t, DoctorWithOptions(t.Context(), cfg, offlineDoctorOptions(t)), "theme")
	if check.Status != DoctorStatusWarn {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	if !strings.Contains(check.Detail, "not-a-real-theme") {
		t.Fatalf("detail = %q, want it to name the unknown theme", check.Detail)
	}
}

func TestDoctorPrintsTheCostTableItIsActuallyUsing(t *testing.T) {
	cfg := config.Default()
	cfg.Quota.Costs.List = 7

	check := checkNamed(t, DoctorWithOptions(t.Context(), cfg, offlineDoctorOptions(t)), "cost table")
	if !strings.Contains(check.Detail, youtube.EndpointMessagesList+"=7") {
		t.Fatalf("detail = %q, want the overridden list cost", check.Detail)
	}
	// A wrong constant is only visible if the table is printed, and a printed
	// number is only actionable if the reader knows whether it came from Google
	// or from yc. Marking the whole table "estimated" was the earlier shorthand
	// and it was false in both directions: it cast doubt on six documented
	// figures, and it hid that liveChatMessages.list - the one number the whole
	// poll budget rests on - is the guess.
	for _, endpoint := range []string{
		youtube.EndpointMessagesList,
		youtube.EndpointMessagesInsert,
		youtube.EndpointMessagesDelete,
		youtube.EndpointBansInsert,
		youtube.EndpointBansDelete,
	} {
		if youtube.IsPublishedCost(endpoint) {
			t.Fatalf("%s is classified as published; Google documents no live chat cost", endpoint)
		}
		if !strings.Contains(check.Detail, endpoint+"=") {
			t.Fatalf("detail = %q, want it to list %s", check.Detail, endpoint)
		}
	}
	for _, endpoint := range []string{
		youtube.EndpointVideosList,
		youtube.EndpointChannelsList,
		youtube.EndpointSubscriptions,
		youtube.EndpointCategoriesList,
		youtube.EndpointVideosUpdate,
		youtube.EndpointSearchList,
	} {
		if !youtube.IsPublishedCost(endpoint) {
			t.Fatalf("%s is classified as an estimate; Google publishes its cost", endpoint)
		}
	}
	if !strings.Contains(check.Detail, "(est)") || !strings.Contains(check.Detail, "(pub)") {
		t.Fatalf("detail = %q, want both classes marked per row", check.Detail)
	}
	if !strings.Contains(check.Detail, "every live chat method") {
		t.Fatalf("detail = %q, want it to name which class is estimated", check.Detail)
	}
}

func TestDoctorProjectsWhenTheBudgetRunsOut(t *testing.T) {
	opts := offlineDoctorOptions(t)
	opts.QuotaReporter = fixedQuotaReporter{snapshot: youtube.QuotaSnapshot{
		UsedUnits:         9000,
		LimitUnits:        10000,
		RemainingUnits:    1000,
		SearchUsed:        3,
		SearchLimit:       100,
		ResetAt:           time.Now().Add(6 * time.Hour),
		EffectiveInterval: 5 * time.Second,
		Estimated:         true,
	}}

	check := checkNamed(t, DoctorWithOptions(t.Context(), config.Default(), opts), "quota")
	if check.Status != DoctorStatusWarn {
		t.Fatalf("status = %q, want warn at 10%% remaining", check.Status)
	}
	if !strings.Contains(check.Detail, "est.") {
		t.Fatalf("detail = %q, want the estimate marker", check.Detail)
	}
	if !strings.Contains(check.Detail, "projected exhaustion") {
		t.Fatalf("detail = %q, want the projection", check.Detail)
	}
}

func TestDoctorReportsAnUnreachableAPIAsAWarningNotAFailure(t *testing.T) {
	opts := offlineDoctorOptions(t)
	opts.ReachabilityProbe = func(context.Context) error { return errors.New("dns lookup failed") }

	check := checkNamed(t, DoctorWithOptions(t.Context(), config.Default(), opts), "reachability")
	if check.Status != DoctorStatusWarn {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	if !strings.Contains(check.Detail, "unreachable") {
		t.Fatalf("detail = %q", check.Detail)
	}
}

func TestDoctorSeparatesAnIdentityFailureFromAMissingToken(t *testing.T) {
	withoutToken := DoctorWithOptions(t.Context(), config.Default(), offlineDoctorOptions(t))
	if got := checkNamed(t, withoutToken, "identity").Detail; !strings.Contains(got, "skipped") {
		t.Fatalf("detail = %q, want the lookup skipped when there is no token", got)
	}

	opts := offlineDoctorOptions(t)
	opts.IdentityLookup = failingIdentityLookup{}
	check := checkNamed(t, DoctorWithOptions(t.Context(), config.Default(), opts), "identity")
	if check.Status != DoctorStatusWarn {
		t.Fatalf("status = %q, want warn", check.Status)
	}
}

func TestDoctorDescribesEachConfiguredChatWithoutSpendingQuota(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultChats = []string{"dQw4w9WgXcQ", "@somehandle"}
	opts := offlineDoctorOptions(t)
	for _, raw := range cfg.DefaultChats {
		target, err := youtube.ParseChatTarget(raw)
		if err != nil {
			t.Fatalf("ParseChatTarget(%q) error = %v", raw, err)
		}
		opts.Targets = append(opts.Targets, target)
	}

	check := checkNamed(t, DoctorWithOptions(t.Context(), cfg, opts), "chats")
	for _, want := range []string{"dQw4w9WgXcQ", "videos.list", "@somehandle", "channels.list"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("detail = %q, want it to mention %q", check.Detail, want)
		}
	}
}

func TestDoctorWarnsWhenTheLedgerCannotBePersisted(t *testing.T) {
	opts := offlineDoctorOptions(t)
	opts.CacheDir = ""

	check := checkNamed(t, DoctorWithOptions(t.Context(), config.Default(), opts), "cache")
	if check.Status != DoctorStatusWarn {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	// A ledger that forgets on exit hands the user a false sense of budget
	// on the next run, which they discover mid-stream.
	if !strings.Contains(check.Detail, "restart") {
		t.Fatalf("detail = %q, want it to name the consequence", check.Detail)
	}
}

// fixedQuotaReporter answers with a canned snapshot.
type fixedQuotaReporter struct{ snapshot youtube.QuotaSnapshot }

func (r fixedQuotaReporter) Quota() youtube.QuotaSnapshot { return r.snapshot }

// failingIdentityLookup stands in for a credential that no longer works.
type failingIdentityLookup struct{}

func (failingIdentityLookup) Identity(context.Context) (youtube.Identity, error) {
	return youtube.Identity{}, errors.New("the credential was rejected")
}
