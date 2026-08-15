package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/debuglog"
	"github.com/worxbend/yc/internal/youtube"
)

// awaitPrune waits for a started sweep to finish.
func awaitPrune(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the ledger prune never finished")
	}
}

// pacificDay renders the Pacific day n days from now, which is the day a ledger
// record filename is keyed by.
func pacificDay(t *testing.T, n int) string {
	t.Helper()
	loc, err := time.LoadLocation(youtube.QuotaResetLocation)
	if err != nil {
		// tzdata is embedded, so this is a broken toolchain rather than a
		// broken machine; standard Pacific time still keys the filenames.
		loc = time.FixedZone("PST", -8*60*60)
	}
	return time.Now().In(loc).AddDate(0, 0, n).Format("2006-01-02")
}

// TestStartLedgerPruneSweepsStaleRecordsAtStartup is the whole reason Prune has
// a caller: one record per credential per Pacific day accumulates in the cache
// directory forever, and nothing else ever deletes them.
func TestStartLedgerPruneSweepsStaleRecordsAtStartup(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	dir := ledgerStoreDir()
	if dir == "" {
		t.Fatal("no ledger directory under a cache directory that exists")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}

	write := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"version":1,"by_endpoint":{}}`), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	today := write("aaaaaaaaaaaaaaaa-" + pacificDay(t, 0) + ".json")
	yesterday := write("aaaaaaaaaaaaaaaa-" + pacificDay(t, -1) + ".json")
	stale := write("aaaaaaaaaaaaaaaa-" + pacificDay(t, -(youtube.LedgerRetentionDays+1)) + ".json")
	ancient := write("bbbbbbbbbbbbbbbb-2021-03-04.json")

	awaitPrune(t, startLedgerPrune(context.Background(), debuglog.Logger{}))

	for _, path := range []string{today, yesterday} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("a current record was swept away: %s", filepath.Base(path))
		}
	}
	for _, path := range []string{stale, ancient} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("a stale record survived startup: %s", filepath.Base(path))
		}
	}
}

// TestStartLedgerPruneSurvivesAnUnwritableCacheDirectory pins the promise that
// housekeeping can never fail a launch. Startup ignores the returned channel;
// what matters is that nothing panics, nothing blocks, and nothing propagates.
func TestStartLedgerPruneSurvivesAnUnwritableCacheDirectory(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	dir := ledgerStoreDir()
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

	awaitPrune(t, startLedgerPrune(context.Background(), debuglog.Logger{}))
}

// TestStartLedgerPruneOutlivesTheCallersContext pins the detachment. The sweep
// is worth finishing even on the runs that fail immediately afterwards: those
// are precisely the runs that never get another chance to tidy up.
func TestStartLedgerPruneOutlivesTheCallersContext(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	dir := ledgerStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}
	stale := filepath.Join(dir, "aaaaaaaaaaaaaaaa-2021-03-04.json")
	if err := os.WriteFile(stale, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	awaitPrune(t, startLedgerPrune(ctx, debuglog.Logger{}))

	if _, err := os.Stat(stale); err == nil {
		t.Error("a canceled caller context abandoned the sweep")
	}
}
