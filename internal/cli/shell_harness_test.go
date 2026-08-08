package cli

// The shell tooling has no compiler and no unit tests of its own, and
// scripts/install.sh is the one file in this repository that people run before
// they read it. Its test suite is written in shell, next to it:
//
//	scripts/install_test.sh      runs install.sh end to end against a local
//	                             HTTPS release server on 127.0.0.1
//	scripts/shell_lint_test.sh   bash -n, shellcheck, and the hygiene rules
//	                             over every script in scripts/
//
// This file is how those two reach CI: the gate already runs `go test ./...`,
// so wiring them here needs no new workflow step and no new runner image.
//
// For a faster edit loop, run them directly - they take a few seconds and need
// no Go toolchain:
//
//	scripts/install_test.sh              # everything
//	scripts/install_test.sh -f checksum  # one group, with -v for its output
//	scripts/install_test.sh --list       # the test names
//	scripts/shell_lint_test.sh           # static checks over scripts/*.sh
//
// Both skip themselves, rather than failing, when a prerequisite (bash,
// python3, openssl, curl, sha256sum) is missing, so a stripped-down container
// does not turn into a red build.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallScript(t *testing.T) {
	runShellHarness(t, "install_test.sh")
}

func TestShellScriptsAreStaticallyClean(t *testing.T) {
	runShellHarness(t, "shell_lint_test.sh")
}

// runShellHarness executes one scripts/*_test.sh and fails with its whole
// output. The environment is rebuilt from scratch: the harnesses assert that
// install.sh writes nothing outside the directories it was given, and a
// YC_INSTALL_DIR inherited from the developer's shell would quietly move the
// target of that assertion.
func runShellHarness(t *testing.T, name string) {
	t.Helper()

	if testing.Short() {
		t.Skip("shell harness: skipped in -short mode")
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("shell harness: bash is not installed: %v", err)
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatalf("resolve scripts/%s: %v", name, err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("scripts/%s is missing; the shell tooling must keep its tests: %v", name, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, bash, script)
	cmd.Dir = filepath.Dir(script)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
		"NO_COLOR=1",
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	output := out.String()

	if line, ok := skipLine(output); ok {
		t.Skipf("scripts/%s: %s", name, line)
	}
	if runErr != nil {
		t.Fatalf("scripts/%s failed: %v\n%s", name, runErr, output)
	}
	t.Logf("scripts/%s:\n%s", name, output)
}

// skipLine reports the harness's own "SKIP <script>: <reason>" line, which it
// prints when a prerequisite is missing rather than failing the build.
func skipLine(output string) (string, bool) {
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SKIP ") {
			return line, true
		}
	}
	return "", false
}
