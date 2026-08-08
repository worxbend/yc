package youtube

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLedgerRecord puts one record on disk for the given Pacific day.
func writeLedgerRecord(t *testing.T, dir, fingerprint, day string) string {
	t.Helper()
	if err := os.MkdirAll(dir, ledgerDirMode); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}
	path := filepath.Join(dir, fingerprint+"-"+day+".json")
	if err := os.WriteFile(path, []byte(`{"version":1,"day":"`+day+`","by_endpoint":{}}`), ledgerFileMode); err != nil {
		t.Fatalf("write record: %v", err)
	}
	return path
}

// pacificDayOffset renders the Pacific day n days from now.
func pacificDayOffset(n int) string {
	return time.Now().In(pacificLocation()).AddDate(0, 0, n).Format("2006-01-02")
}

// TestPruneAtTheRetentionWindowKeepsTodayAndDropsTheStale pins the window the
// startup sweep uses.
//
// Without a caller these records accumulate at one per credential per day, in
// the only directory yc writes to without being asked. With too aggressive a
// caller, a session running across the Pacific midnight loses the tally it is
// still adding to. The window has to keep both true.
func TestPruneAtTheRetentionWindowKeepsTodayAndDropsTheStale(t *testing.T) {
	root := t.TempDir()
	store := NewFileLedgerStore(root)

	kept := []string{
		writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", pacificDayOffset(0)),
		writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", pacificDayOffset(-1)),
		writeLedgerRecord(t, root, "bbbbbbbbbbbbbbbb", pacificDayOffset(-(LedgerRetentionDays - 1))),
		writeLedgerRecord(t, root, anonymousFingerprint, pacificDayOffset(0)),
	}
	stale := []string{
		writeLedgerRecord(t, root, "aaaaaaaaaaaaaaaa", pacificDayOffset(-(LedgerRetentionDays + 1))),
		writeLedgerRecord(t, root, "bbbbbbbbbbbbbbbb", "2020-01-01"),
		writeLedgerRecord(t, root, anonymousFingerprint, "2019-12-31"),
	}

	// A file that is not a record at all must be left alone: this directory
	// is shared with whatever else yc caches, and a sweep that deletes by
	// exclusion rather than by pattern is one bug away from deleting a
	// credential file.
	foreign := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(foreign, []byte("not a ledger record"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	if err := store.Prune(t.Context(), LedgerRetentionDays); err != nil {
		t.Fatalf("Prune error = %v", err)
	}

	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("record inside the retention window was deleted: %s", filepath.Base(path))
		}
	}
	for _, path := range stale {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("record outside the retention window survived: %s", filepath.Base(path))
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file that is not a ledger record was deleted: %v", err)
	}
}

// TestPruneOnAnAbsentDirectoryIsNotAnError covers the first run, where the
// sweep happens before anything has ever been written.
func TestPruneOnAnAbsentDirectoryIsNotAnError(t *testing.T) {
	store := NewFileLedgerStore(filepath.Join(t.TempDir(), "never-created"))
	if err := store.Prune(t.Context(), LedgerRetentionDays); err != nil {
		t.Fatalf("Prune error = %v, want nil for a directory that does not exist", err)
	}
}
