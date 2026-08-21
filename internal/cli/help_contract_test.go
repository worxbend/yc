package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestHelpIsNeverAUsageError pins the CLI's help contract for every subcommand.
//
// Asking for help is a request that succeeded, not a mistake: it exits zero and
// the page goes to stdout, so `yc doctor --help | less` shows something and a
// wrapper script reading the exit code does not conclude the command was
// misused.
//
// This is a regression test with a real failure behind it. The help shortcut
// used to be skipped for subcommands that had no prose page, which let --help
// fall through to flag.FlagSet.Parse. The flag package reports --help as an
// error, so `yc doctor`, `yc quota`, `yc config show`, `yc profile list`,
// `yc profile show`, `yc profile set` and `yc export superchats` all answered a
// request for help with a bare flag dump on stderr and exit code 2.
//
// The list is spelled out rather than derived so that adding a subcommand
// without adding it here is a visible omission rather than silent coverage.
func TestHelpIsNeverAUsageError(t *testing.T) {
	subcommands := [][]string{
		{"chat"},
		{"config", "path"},
		{"config", "show"},
		{"doctor"},
		{"export", "superchats"},
		{"login"},
		{"logout"},
		{"profile", "list"},
		{"profile", "set", "nord"},
		{"profile", "show", "nord"},
		{"quota"},
		{"setup"},
	}

	for _, subcommand := range subcommands {
		name := strings.Join(subcommand, " ")
		t.Run(name, func(t *testing.T) {
			for _, flag := range []string{"--help", "-h"} {
				var stdout, stderr bytes.Buffer
				args := append(append([]string{}, subcommand...), flag)

				if code := Run(args, &stdout, &stderr); code != ExitOK {
					t.Errorf("`yc %s %s` exited %d, want %d: asking for help is not a usage error",
						name, flag, code, ExitOK)
				}
				if stdout.Len() == 0 {
					t.Errorf("`yc %s %s` printed no help on stdout", name, flag)
				}
				if stderr.Len() != 0 {
					t.Errorf("`yc %s %s` wrote to stderr, which breaks `| less`:\n%s",
						name, flag, stderr.String())
				}
			}
		})
	}
}

// TestStrayPositionalArgumentIsRejected pins the other half of the contract the
// help fix touched: a subcommand that takes no positional arguments says so
// rather than silently ignoring the word.
//
// Before, only the subcommands that happened to route through parseCommandFlags
// checked. `yc config show extra` and `yc profile set nord extra` ignored the
// stray word while `yc chat extra` rejected it, so the same mistake produced
// three different outcomes depending on which command you made it in.
func TestStrayPositionalArgumentIsRejected(t *testing.T) {
	cases := [][]string{
		{"chat", "extra"},
		{"config", "show", "extra"},
		{"doctor", "extra"},
		{"export", "superchats", "extra"},
		{"profile", "set", "nord", "extra"},
		{"quota", "extra"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != ExitUsage {
				t.Errorf("`yc %s` exited %d, want %d", strings.Join(args, " "), code, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Error("a usage failure must explain itself on stderr")
			}
			if strings.Contains(stderr.String(), "argument argument") {
				t.Errorf("the complaint stutters:\n%s", stderr.String())
			}
		})
	}
}
