package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/worxbend/yc/internal/config"
)

// debugConfig is a config with debug logging pointed at a scratch path.
func debugConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Debug.Enabled = true
	cfg.Debug.LogPath = path
	cfg.Google.ClientSecret = "secret-" + fakeToken
	cfg.Google.AccessToken = "access-" + fakeToken
	cfg.Google.RefreshToken = "refresh-" + fakeToken
	cfg.YouTube.APIKey = "AIza-" + fakeToken
	return cfg
}

// A disabled config must yield the zero Logger, which discards, plus a closer
// the caller can defer unconditionally.
func TestOpenDebugLoggerDisabledOpensNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	cfg := debugConfig(t, path)
	cfg.Debug.Enabled = false

	var stderr bytes.Buffer
	logger, closer, err := openDebugLogger(cfg, &stderr)
	if err != nil {
		t.Fatalf("openDebugLogger: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	logger.Log(context.Background(), "should.discard")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a disabled debug log created a file")
	}
	if stderr.Len() != 0 {
		t.Errorf("a disabled debug log announced itself: %s", stderr.String())
	}
}

// The log holds redacted records, but redaction is a best effort applied to
// values yc knows about, so the file is still treated as private.
func TestOpenDebugLoggerCreatesAPrivateFileAndNamesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "debug.log")
	cfg := debugConfig(t, path)

	var stderr bytes.Buffer
	logger, closer, err := openDebugLogger(cfg, &stderr)
	if err != nil {
		t.Fatalf("openDebugLogger: %v", err)
	}
	defer closer.Close()

	if !strings.Contains(stderr.String(), "debug log:") {
		t.Errorf("stderr did not name the destination: %q", stderr.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("debug log mode = %04o, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("debug log directory mode = %04o, want no group/other access", perm)
	}

	// Every configured secret is seeded into the redactor, so a value that
	// does reach an attribute is replaced rather than written.
	logger.Log(context.Background(), "cli.test",
		logString("client_secret", cfg.Google.ClientSecret),
		logString("access_token", cfg.Google.AccessToken),
		logString("refresh_token", cfg.Google.RefreshToken),
		logString("api_key", cfg.YouTube.APIKey),
	)
	closer.Close()

	contents := readConfigFile(t, path)
	for _, secret := range []string{
		cfg.Google.ClientSecret, cfg.Google.AccessToken, cfg.Google.RefreshToken, cfg.YouTube.APIKey,
	} {
		if strings.Contains(contents, secret) {
			t.Fatalf("the debug log leaked %q:\n%s", secret, contents)
		}
	}
	// The records must still be machine-readable JSON lines.
	for _, line := range strings.Split(strings.TrimSpace(contents), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("debug log line is not JSON: %q", line)
		}
	}
}

// Reopening appends rather than truncating: a second run must not erase the
// records from the run the user is trying to report.
func TestOpenDebugLoggerAppendsToAnExistingPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")
	cfg := debugConfig(t, path)

	for i := 0; i < 2; i++ {
		logger, closer, err := openDebugLogger(cfg, nil)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		logger.Log(context.Background(), "cli.run")
		closer.Close()
	}

	lines := strings.Count(strings.TrimSpace(readConfigFile(t, path)), "\n") + 1
	if lines < 4 {
		t.Errorf("%d records after two runs, want the first run's records kept", lines)
	}
}

// A symlink planted at the path is refused by the kernel rather than detected
// by a stat that a racing rename could have invalidated.
func TestOpenDebugLoggerRefusesASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on this platform")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "debug.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, closer, err := openDebugLogger(debugConfig(t, link), nil)
	if err == nil {
		closer.Close()
		t.Fatal("a symlinked debug log path was accepted")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %q, want it to name the symlink", err)
	}
}

// A world-readable log is refused: a diagnostic that leaks is worse than no
// diagnostic.
func TestOpenDebugLoggerRefusesAWorldReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits differ on this platform")
	}
	path := filepath.Join(t.TempDir(), "debug.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, closer, err := openDebugLogger(debugConfig(t, path), nil)
	if err == nil {
		closer.Close()
		t.Fatal("a group/other-readable debug log was accepted")
	}
	if !strings.Contains(err.Error(), "permissions") {
		t.Errorf("error = %q, want it to name the permission problem", err)
	}
}

func TestOpenDebugLoggerRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	_, closer, err := openDebugLogger(debugConfig(t, dir), nil)
	if err == nil {
		closer.Close()
		t.Fatal("a directory was accepted as a debug log")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error = %q, want it to name the directory", err)
	}
}

// Errors about the log must not repeat the path twice or quote a raw
// filesystem error that embeds it.
func TestDebugLogErrorsAreRedactedAndUnwrapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.log")

	wrapped := debugLogOperationError("open", path, &os.PathError{
		Op: "open", Path: path, Err: os.ErrPermission,
	})
	if strings.Count(wrapped.Error(), path) > 1 {
		t.Errorf("error repeats the path: %q", wrapped)
	}
	if !strings.Contains(wrapped.Error(), os.ErrPermission.Error()) {
		t.Errorf("error = %q, want the underlying cause", wrapped)
	}
	if debugLogOperationError("open", path, nil) != nil {
		t.Error("a nil cause produced an error")
	}
	if debugLogOpenFileError(path, nil) != nil {
		t.Error("a nil open failure produced an error")
	}
	if got := safeDebugLogErrorDetail(nil); got != "" {
		t.Errorf("safeDebugLogErrorDetail(nil) = %q", got)
	}
}

// The guarantee this build makes is reported rather than assumed.
func TestDebugLogPlatformDoctorCheckStatesTheGuarantee(t *testing.T) {
	check := debugLogPlatformDoctorCheck()
	if check.Detail != debugLogOpenPlatformNote || check.Detail == "" {
		t.Errorf("check = %+v, want the platform note", check)
	}
	if runtime.GOOS != "windows" && !strings.Contains(check.Detail, "O_NOFOLLOW") {
		t.Errorf("a Unix build must name its no-follow guarantee: %q", check.Detail)
	}
}

// Only an explicit --debug-log=false may override debug_logging = true in the
// config file, which is why the flag is tri-state.
func TestDebugFlagOverridesAreTriState(t *testing.T) {
	var absent config.Overrides
	applyDebugFlagOverrides(&absent, debugFlagOptions{})
	if absent.DebugLogSet {
		t.Errorf("overrides = %+v; an absent flag must not override the config", absent)
	}

	var enabled config.Overrides
	on := debugLogFlag{}
	if err := on.Set("true"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	applyDebugFlagOverrides(&enabled, debugFlagOptions{enabled: on, path: "  /tmp/x.log  "})
	if !enabled.DebugLogSet || !enabled.DebugLogEnabled {
		t.Errorf("overrides = %+v, want an explicit enable", enabled)
	}
	if enabled.DebugLogPath != "  /tmp/x.log  " {
		t.Errorf("path = %q, want the value passed through for the loader to normalize", enabled.DebugLogPath)
	}

	var blankPath config.Overrides
	applyDebugFlagOverrides(&blankPath, debugFlagOptions{path: "   "})
	if blankPath.DebugLogPath != "" {
		t.Errorf("path = %q; a whitespace path must not override", blankPath.DebugLogPath)
	}

	var bad debugLogFlag
	if err := bad.Set("perhaps"); err == nil {
		t.Error("an unparseable --debug-log value must be a usage error")
	}
	var nilFlag *debugLogFlag
	if got := nilFlag.String(); got != "" {
		t.Errorf("nil String() = %q", got)
	}
	if !(&debugLogFlag{}).IsBoolFlag() {
		t.Error("the bare --debug-log spelling must be accepted")
	}
}

func TestConfigRedactorCarriesEverySecretTheConfigKnows(t *testing.T) {
	cfg := debugConfig(t, "")
	redactor := configRedactor(cfg)
	message := strings.Join([]string{
		cfg.Google.ClientSecret, cfg.Google.AccessToken, cfg.Google.RefreshToken, cfg.YouTube.APIKey,
	}, " and ")
	got := redactor.Redact(message)
	for _, secret := range []string{
		cfg.Google.ClientSecret, cfg.Google.AccessToken, cfg.Google.RefreshToken, cfg.YouTube.APIKey,
	} {
		if strings.Contains(got, secret) {
			t.Errorf("redactor missed %q: %q", secret, got)
		}
	}
}

func TestNoopCloserIsSafe(t *testing.T) {
	if err := noopCloser().Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	var nilFunc closerFunc
	if err := nilFunc.Close(); err != nil {
		t.Errorf("nil closerFunc Close: %v", err)
	}
}

// logString is a tiny slog.String alias so the test reads as attributes rather
// than as logging plumbing.
func logString(key, value string) slog.Attr { return slog.String(key, value) }
