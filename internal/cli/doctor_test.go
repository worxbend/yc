package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/app"
	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/storage"
)

// stubDoctorReport replaces the app-layer report with a deterministic one, so
// the CLI's own composition is what is under test rather than every check the
// app performs.
func stubDoctorReport(t *testing.T, checks ...app.DoctorCheck) *config.Config {
	t.Helper()
	original := buildDoctorReport
	t.Cleanup(func() { buildDoctorReport = original })

	var seen config.Config
	buildDoctorReport = func(_ context.Context, cfg config.Config, cfgErr error, opts app.DoctorOptions) app.DoctorReport {
		seen = cfg
		if cfgErr != nil {
			checks = append(checks, app.DoctorCheck{
				Name:   "config",
				Status: app.DoctorStatusWarn,
				Detail: "config load failed: " + config.RedactDisplayValue(cfgErr.Error()),
			})
		}
		return app.DoctorReport{Checks: checks}
	}
	return &seen
}

// Doctor is the command someone runs when something is already wrong, so it
// never aborts: it prints the locally derived checks first, because those are
// the ones that are true even when nothing is reachable.
func TestDoctorPrintsLocalChecksBeforeTheReport(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withMemoryCredentialStore(t, storage.CredentialRecord{})
	stubDoctorReport(t, app.DoctorCheck{Name: "reachability", Status: app.DoctorStatusOK, Detail: "probed"})
	path := writeTempConfig(t, "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("doctor = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()

	// The four locally derived checks, in order, then the report.
	local := []string{"credential store", "credentials", "quota budget", "debug log hardening"}
	last := -1
	for _, name := range local {
		index := strings.Index(out, name+":")
		if index < 0 {
			t.Errorf("doctor omitted the %q check:\n%s", name, out)
			continue
		}
		if index < last {
			t.Errorf("the %q check printed out of order:\n%s", name, out)
		}
		last = index
	}
	if reach := strings.Index(out, "reachability:"); reach >= 0 && reach < last {
		t.Errorf("the app report printed before the local checks:\n%s", out)
	}
	// Every line is a status-prefixed check.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "[") {
			t.Errorf("doctor printed a line that is not a check: %q", line)
		}
	}
}

// A load failure becomes a check line and the run continues on environment
// values and defaults, rather than aborting the one command that diagnoses it.
func TestDoctorSurvivesABrokenConfig(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withMemoryCredentialStore(t, storage.CredentialRecord{})
	stubDoctorReport(t)
	path := writeTempConfig(t, "this is not = valid toml [[[\n")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("doctor = %d, want it to keep going; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "config") {
		t.Errorf("the config failure did not become a check line:\n%s", stdout.String())
	}
}

// A chat yc cannot classify is reported rather than dropped: silently opening
// fewer chats than were asked for is the kind of failure a user does not notice
// until the stream is over.
func TestDoctorReportsUnreadableChats(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withMemoryCredentialStore(t, storage.CredentialRecord{})
	stubDoctorReport(t)
	path := writeTempConfig(t, "default_chats = [\"    \\u0000not a chat\"]\n")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--config", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("doctor = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "chats:") {
		t.Errorf("an unreadable chat produced no check line:\n%s", stdout.String())
	}
}

// The identity lookup is only wired when a user token exists: probing it with
// an API key would report a credential failure that is really a mode, and
// telling those apart is doctor's whole job.
func TestDoctorWiresTheIdentityLookupOnlyForOAuth(t *testing.T) {
	original := buildDoctorReport
	t.Cleanup(func() { buildDoctorReport = original })

	var identityWired bool
	buildDoctorReport = func(ctx context.Context, cfg config.Config, cfgErr error, opts app.DoctorOptions) app.DoctorReport {
		identityWired = opts.IdentityLookup != nil
		if opts.QuotaReporter == nil {
			t.Error("doctor did not pass a quota reporter")
		}
		return app.DoctorReport{}
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
		want  bool
	}{
		{"api key only", func(t *testing.T) { t.Setenv("YC_YOUTUBE_API_KEY", "AIza-"+fakeToken) }, false},
		{"no credentials", func(_ *testing.T) {}, false},
		{"oauth token", func(t *testing.T) { t.Setenv("YC_GOOGLE_ACCESS_TOKEN", fakeToken) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearCredentialEnv(t)
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			withMemoryCredentialStore(t, storage.CredentialRecord{})
			tc.setup(t)
			identityWired = false

			var stdout, stderr bytes.Buffer
			path := writeTempConfig(t, "")
			if code := Run([]string{"doctor", "--config", path}, &stdout, &stderr); code != ExitOK {
				t.Fatalf("doctor = %d, stderr=%s", code, stderr.String())
			}
			if identityWired != tc.want {
				t.Errorf("identity lookup wired = %v, want %v", identityWired, tc.want)
			}
		})
	}
}

func TestDoctorRejectsAnUnparseableFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--nope"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("doctor = %d, want %d", code, ExitUsage)
	}
}

// The credential-file check has to describe every state the store can be in,
// because it is the line a user reads when a login did not take effect.
func TestCredentialFileDoctorCheckCoversEveryStoreState(t *testing.T) {
	cases := []struct {
		name       string
		status     credentialLoadStatus
		wantStatus app.DoctorStatus
		wantDetail string
	}{
		{
			name:       "unsupported platform is a limitation, not a failure",
			status:     credentialLoadStatus{Err: storage.ErrCredentialsUnsupported},
			wantStatus: app.DoctorStatusWarn,
			wantDetail: "unsupported on this platform",
		},
		{
			name:       "loaded",
			status:     credentialLoadStatus{Present: true, Location: "/home/u/.config/yc/credentials.json"},
			wantStatus: app.DoctorStatusOK,
			wantDetail: "loaded",
		},
		{
			name:       "shadowed",
			status:     credentialLoadStatus{Present: true, TokenShadowed: true, Location: "/p"},
			wantStatus: app.DoctorStatusWarn,
			wantDetail: "shadowed",
		},
		{
			name:       "absent names the two ways forward",
			status:     credentialLoadStatus{Location: "/p"},
			wantStatus: app.DoctorStatusWarn,
			wantDetail: "yc login",
		},
		{
			name:       "load failed",
			status:     credentialLoadStatus{Err: errors.New("boom"), Location: "/p"},
			wantStatus: app.DoctorStatusWarn,
			wantDetail: "using env/config/defaults",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := credentialFileDoctorCheck(tc.status)
			if check.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", check.Status, tc.wantStatus)
			}
			if !strings.Contains(check.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", check.Detail, tc.wantDetail)
			}
			if check.Name == "" {
				t.Error("a check with no name is unreadable in the report")
			}
		})
	}

	// A store with no label still produces a usable line.
	bare := credentialFileDoctorCheck(credentialLoadStatus{})
	if bare.Name == "" || bare.Detail == "" {
		t.Errorf("check = %+v, want a fallback name and detail", bare)
	}
}

// yc degrades by capability rather than by hiding controls, so the check has to
// say what this run can actually do.
func TestCapabilityDoctorCheckStatesWhatTheRunCanDo(t *testing.T) {
	cfg := config.Default()

	none := capabilityDoctorCheck(describeCapability(cfg, credentialLoadStatus{}))
	if none.Status != app.DoctorStatusWarn || !strings.Contains(none.Detail, "yc login") {
		t.Errorf("no-credential check = %+v", none)
	}

	keyOnly := cfg
	keyOnly.YouTube.APIKey = "AIza-" + fakeToken
	key := capabilityDoctorCheck(describeCapability(keyOnly, credentialLoadStatus{}))
	if key.Status != app.DoctorStatusWarn || !strings.Contains(key.Detail, "read-only") {
		t.Errorf("api-key check = %+v", key)
	}

	oauth := cfg
	oauth.Google.AccessToken = fakeToken

	// Scopes from the credential file yc wrote are trustworthy, so a fully
	// granted token is the one state that reports OK.
	full := capabilityDoctorCheck(describeCapability(oauth, credentialLoadStatus{
		Present: true,
		Record:  storage.CredentialRecord{Scopes: auth.LoginScopes()},
	}))
	if full.Status != app.DoctorStatusOK {
		t.Errorf("fully scoped check = %+v, want OK", full)
	}
	for _, want := range []string{"read", "send", "moderate", "granted:"} {
		if !strings.Contains(full.Detail, want) {
			t.Errorf("detail is missing %q: %s", want, full.Detail)
		}
	}

	readOnly := capabilityDoctorCheck(describeCapability(oauth, credentialLoadStatus{
		Present: true,
		Record:  storage.CredentialRecord{Scopes: auth.ReadScopes()},
	}))
	if readOnly.Status != app.DoctorStatusWarn {
		t.Errorf("read-only scoped check = %+v, want a warning", readOnly)
	}

	// A token from the environment carries no scope record, so the check must
	// admit that rather than claiming a grant yc cannot vouch for.
	unknown := capabilityDoctorCheck(describeCapability(oauth, credentialLoadStatus{}))
	if unknown.Status != app.DoctorStatusWarn || !strings.Contains(unknown.Detail, "unknown") {
		t.Errorf("unknown-scope check = %+v", unknown)
	}
}
