package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/debuglog"
)

// Debug log permissions. The log holds redacted records, but redaction is a
// best effort applied to values yc knows about, so the file is still treated as
// private: a diagnostic that leaks is worse than no diagnostic.
const (
	debugLogDirMode  fs.FileMode = 0o700
	debugLogFileMode fs.FileMode = 0o600
)

// debugLogFlag is a tri-state boolean flag.
//
// It distinguishes "flag absent" from "--debug-log=false", which matters
// because only the explicit false may override debug_logging = true in the
// config file.
type debugLogFlag struct {
	set   bool
	value bool
}

// String renders the flag's current value.
func (f *debugLogFlag) String() string {
	if f == nil || !f.set {
		return ""
	}
	return strconv.FormatBool(f.value)
}

// Set parses a boolean value.
func (f *debugLogFlag) Set(value string) error {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
}

// IsBoolFlag lets the flag package accept a bare --debug-log.
func (f *debugLogFlag) IsBoolFlag() bool { return true }

// debugFlagOptions carries the two debug flags every subcommand accepts.
type debugFlagOptions struct {
	enabled debugLogFlag
	path    string
}

// addDebugFlags registers the shared debug flags on a flag set.
func addDebugFlags(fs *flag.FlagSet, opts *debugFlagOptions) {
	fs.Var(&opts.enabled, "debug-log", "enable redacted JSON-line debug logging")
	fs.StringVar(&opts.path, "debug-log-path", "", "debug log file path; defaults to the yc cache directory")
}

// applyDebugFlagOverrides folds the parsed debug flags into config overrides.
func applyDebugFlagOverrides(overrides *config.Overrides, opts debugFlagOptions) {
	if opts.enabled.set {
		overrides.DebugLogSet = true
		overrides.DebugLogEnabled = opts.enabled.value
	}
	if strings.TrimSpace(opts.path) != "" {
		overrides.DebugLogPath = opts.path
	}
}

// closerFunc adapts a plain function to io.Closer, so a disabled logger can
// still hand back a closer the caller defers unconditionally.
type closerFunc func() error

// Close runs the wrapped function.
func (f closerFunc) Close() error {
	if f == nil {
		return nil
	}
	return f()
}

// noopCloser is the closer returned when nothing was opened.
func noopCloser() io.Closer {
	return closerFunc(func() error { return nil })
}

// openDebugLogger opens the configured debug-log destination through hardened
// helpers that reject an unsafe file before writing, and returns a logger plus
// its closer. A disabled config yields the zero Logger, which discards.
//
// The logger is seeded with every configured secret so that a value which does
// reach a log attribute is replaced rather than written. stderr receives the
// one-line notice naming the destination, because a user who turned debug
// logging on needs to know where to look and must not have to guess.
func openDebugLogger(cfg config.Config, stderr io.Writer) (debuglog.Logger, io.Closer, error) {
	if !cfg.Debug.Enabled {
		return debuglog.Logger{}, noopCloser(), nil
	}

	path := strings.TrimSpace(cfg.Debug.LogPath)
	if path == "" {
		cacheDir, err := config.DefaultCacheDir()
		if err != nil {
			return debuglog.Logger{}, noopCloser(), err
		}
		path = filepath.Join(cacheDir, "debug.log")
	}
	if err := os.MkdirAll(filepath.Dir(path), debugLogDirMode); err != nil {
		return debuglog.Logger{}, noopCloser(), err
	}
	file, err := openDebugLogFile(path)
	if err != nil {
		return debuglog.Logger{}, noopCloser(), err
	}

	logger := debuglog.New(file, debuglog.Options{
		Enabled:  true,
		Redactor: configRedactor(cfg),
	})
	logger.Log(context.Background(), "cli.debug_log.opened")
	if stderr != nil {
		fmt.Fprintf(stderr, "debug log: %s\n", config.RedactDisplayValue(path))
	}
	return logger, closerFunc(file.Close), nil
}

// configRedactor returns a redactor loaded with every secret the config knows.
func configRedactor(cfg config.Config) auth.Redactor {
	return auth.NewRedactor(
		auth.NewSecret(cfg.Google.ClientSecret),
		auth.NewSecret(cfg.Google.AccessToken),
		auth.NewSecret(cfg.Google.RefreshToken),
		auth.NewSecret(cfg.YouTube.APIKey),
	)
}

// validateOpenedDebugLogFile checks the file yc actually holds a descriptor
// for, rather than the path it asked for. Checking the path can only tell you
// what was true a moment ago.
func validateOpenedDebugLogFile(path string, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return debugLogOperationError("stat", path, err)
	}
	return validateDebugLogFileInfo(path, info)
}

// validateDebugLogPath checks a path without opening it, for the platforms that
// cannot open with no-follow semantics.
func validateDebugLogPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateDebugLogFileInfo(path, info)
}

// validateDebugLogFileInfo rejects anything that is not a private regular file.
func validateDebugLogFileInfo(path string, info fs.FileInfo) error {
	display := config.RedactDisplayValue(path)
	if info.IsDir() {
		return fmt.Errorf("debug log path is a directory: %s", display)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("debug log path must not be a symlink: %s", display)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("debug log path is not a regular file: %s", display)
	}
	if perms := info.Mode().Perm(); perms&0o077 != 0 {
		return fmt.Errorf("debug log file permissions %04o allow group/other access; require private user-only permissions: %s", perms, display)
	}
	return nil
}

// debugLogOpenFileError turns an open failure into a message that names the
// cause without quoting a filesystem error that may embed the path twice.
func debugLogOpenFileError(path string, err error) error {
	if err == nil {
		return nil
	}
	if debugLogOpenErrorIsSymlink(err) {
		return fmt.Errorf("debug log path must not be a symlink: %s", config.RedactDisplayValue(path))
	}
	if statErr := validateDebugLogPath(path); statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	return debugLogOperationError("open", path, err)
}

// debugLogOperationError formats a redacted failure for one file operation.
func debugLogOperationError(action, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s debug log file %s: %s", action, config.RedactDisplayValue(path), safeDebugLogErrorDetail(err))
}

// safeDebugLogErrorDetail unwraps a PathError so the detail is the underlying
// cause rather than a repetition of the path.
func safeDebugLogErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return config.RedactDisplayValue(pathErr.Err.Error())
	}
	return config.RedactDisplayValue(err.Error())
}

// closeDebugLogFileWithError closes a file that failed validation, so a
// rejected log never leaves a descriptor behind.
func closeDebugLogFileWithError(file *os.File, err error) (*os.File, error) {
	if file != nil {
		_ = file.Close()
	}
	return nil, err
}
