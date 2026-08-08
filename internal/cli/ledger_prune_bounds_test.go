package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// The startup sweep is housekeeping on an advisory meter riding on the launch
// path of an interactive program. Nothing about it may be able to keep the alt
// screen from coming up, and nothing about it may keep a goroutine alive after
// the user has moved on - a hung network filesystem is the realistic case for
// both, and it is exactly the case where "just wait for it" would freeze yc
// before it has drawn a single frame.
//
// ledger_prune_test.go covers what the sweep deletes. These cover the promise
// that it cannot cost anything: every failure mode still finishes, nothing
// propagates, and the goroutine has a deadline of its own.

// TestThePruneDeadlineIsShortEnoughToBeMeaningless pins the bound. It is not a
// tautology: a timeout of a minute would still "work", and would still leave a
// goroutine holding a file handle on a dead NFS mount for a minute of every
// launch.
func TestThePruneDeadlineIsShort(t *testing.T) {
	if ledgerPruneTimeout <= 0 {
		t.Fatal("the startup prune has no deadline; a hung filesystem would keep the goroutine alive for the whole session")
	}
	if ledgerPruneTimeout > 10*time.Second {
		t.Fatalf("ledgerPruneTimeout = %v, want a bound short enough that a stuck sweep is over before anyone notices", ledgerPruneTimeout)
	}
	// The retention window is a constant precisely so it cannot be widened
	// by configuration into a directory nobody ever sweeps.
	if youtube.LedgerRetentionDays < 1 {
		t.Fatalf("LedgerRetentionDays = %d, want at least today and yesterday kept", youtube.LedgerRetentionDays)
	}
}

// TestStartLedgerPruneAlwaysFinishesWhateverItFinds walks every filesystem
// state the ledger directory can be in on a real machine. In each one the sweep
// must complete, return nothing to the caller, and leave the launch path with
// no decision to make.
func TestStartLedgerPruneAlwaysFinishesWhateverItFinds(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{
			name:  "the directory has never existed",
			setup: func(*testing.T, string) {},
		},
		{
			name: "the directory is empty",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("create ledger directory: %v", err)
				}
			},
		},
		{
			name: "something else has taken the path",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
					t.Fatalf("create cache directory: %v", err)
				}
				if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write file at the ledger path: %v", err)
				}
			},
		},
		{
			name: "the directory cannot be read",
			setup: func(t *testing.T, dir string) {
				if runtime.GOOS == "windows" {
					t.Skip("directory permissions are a Unix concept here")
				}
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("create ledger directory: %v", err)
				}
				if err := os.Chmod(dir, 0o000); err != nil {
					t.Skipf("cannot make the directory unreadable: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				if _, err := os.ReadDir(dir); err == nil {
					t.Skip("this filesystem ignores directory permissions")
				}
			},
		},
		{
			name: "the records cannot be deleted",
			setup: func(t *testing.T, dir string) {
				if runtime.GOOS == "windows" {
					t.Skip("directory permissions are a Unix concept here")
				}
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("create ledger directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "aaaaaaaaaaaaaaaa-2019-01-01.json"), []byte(`{"version":1}`), 0o600); err != nil {
					t.Fatalf("write record: %v", err)
				}
				// Readable but not writable: the listing succeeds and every
				// unlink fails, which is the shape a read-only bind mount
				// produces.
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Skipf("cannot make the directory read-only: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			dir := ledgerStoreDir()
			if dir == "" {
				t.Fatal("no ledger directory under a cache directory that exists")
			}
			test.setup(t, dir)

			done := startLedgerPrune(context.Background(), debuglog.Logger{})
			select {
			case <-done:
			case <-time.After(ledgerPruneTimeout + 5*time.Second):
				t.Fatal("the sweep never finished, and startup would have carried it for the whole session")
			}
		})
	}
}

// A machine with no usable cache directory is a supported configuration: the
// meter simply forgets on exit. The sweep must recognize that immediately, do
// no work, and above all create nothing - housekeeping that makes the mess it
// is meant to clean is worse than no housekeeping.
func TestStartLedgerPruneCreatesNothingWhenThereIsNowhereToSweep(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	done := startLedgerPrune(context.Background(), debuglog.Logger{})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep never finished")
	}

	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatalf("read cache directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the sweep created %v; it must never create the directory it is cleaning", names)
	}
}

// The sweep is fast in the ordinary case, which is what makes the deadline a
// backstop rather than a routine cost. A directory holding a year of records
// for a handful of accounts is a realistic worst case for a long-time user.
func TestASweepOfAYearOfRecordsIsWellInsideTheDeadline(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := ledgerStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}

	loc, err := time.LoadLocation(youtube.QuotaResetLocation)
	if err != nil {
		loc = time.FixedZone("PST", -8*60*60)
	}
	now := time.Now().In(loc)
	for _, fingerprint := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd"} {
		for day := 0; day < 365; day++ {
			name := fingerprint + "-" + now.AddDate(0, 0, -day).Format("2006-01-02") + ".json"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"version":1,"by_endpoint":{}}`), 0o600); err != nil {
				t.Fatalf("write record: %v", err)
			}
		}
	}

	started := time.Now()
	done := startLedgerPrune(context.Background(), debuglog.Logger{})
	select {
	case <-done:
	case <-time.After(ledgerPruneTimeout + 5*time.Second):
		t.Fatal("a year of records did not sweep inside the deadline")
	}
	if elapsed := time.Since(started); elapsed >= ledgerPruneTimeout {
		t.Fatalf("the sweep took %v, at or past the %v deadline", elapsed, ledgerPruneTimeout)
	}

	// Only the retention window survives, and the whole of it does.
	remaining, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read ledger directory: %v", err)
	}
	// The window is inclusive of both ends: the oldest day kept is
	// today - LedgerRetentionDays, so a seven-day window retains eight dated
	// records per account. The off-by-one is worth pinning rather than
	// rounding off, because it is the difference between a session that
	// crosses the Pacific reset finding yesterday and not.
	if want := 4 * (youtube.LedgerRetentionDays + 1); len(remaining) != want {
		t.Fatalf("%d records remain, want %d (four accounts across the inclusive %d-day window)",
			len(remaining), want, youtube.LedgerRetentionDays)
	}
}
