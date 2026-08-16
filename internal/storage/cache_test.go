package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "cache"))
	ctx := context.Background()

	if _, ok, err := cache.Get(ctx, "quota-ledger"); err != nil || ok {
		t.Fatalf("a missing record must be (nil, false, nil), got ok=%v err=%v", ok, err)
	}

	payload := []byte(`{"messages.list":45}`)
	if err := cache.Put(ctx, "quota-ledger", payload); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := cache.Get(ctx, "quota-ledger")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if string(got) != string(payload) {
		t.Fatalf("Get = %q, want %q", got, payload)
	}

	// Overwriting must replace rather than append.
	if err := cache.Put(ctx, "quota-ledger", []byte("second")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, _, err = cache.Get(ctx, "quota-ledger")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("Get after overwrite = %q", got)
	}

	if err := cache.Delete(ctx, "quota-ledger"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := cache.Get(ctx, "quota-ledger"); ok {
		t.Fatal("Delete did not remove the record")
	}
	// Deleting nothing is not an error.
	if err := cache.Delete(ctx, "quota-ledger"); err != nil {
		t.Fatalf("Delete of a missing record: %v", err)
	}
}

func TestCacheKeysNeverBecomeFilenames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	cache := NewCache(root)
	ctx := context.Background()

	if err := cache.Put(ctx, "ledger for UC_abc-123", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read cache root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one cache file, got %d", len(entries))
	}
	name := entries[0].Name()
	if strings.ContainsAny(name, " /\\") {
		t.Fatalf("cache filename is not sanitized: %q", name)
	}
	if !strings.HasSuffix(name, cacheFileSuffix) {
		t.Fatalf("cache filename must carry the owned suffix: %q", name)
	}
}

// TestCacheRejectsUnsafeKeys is the rule that keeps a token from becoming a
// filename in a directory that lives forever.
func TestCacheRejectsUnsafeKeys(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "cache"))
	ctx := context.Background()

	unsafe := []string{
		"",
		"   ",
		"access_token=ya29.value",
		"refresh_token",
		"api_key",
		"https://example.test/thing",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"client_secret",
		"code_verifier",
	}
	for _, key := range unsafe {
		if err := cache.Put(ctx, key, []byte("x")); !errors.Is(err, ErrCacheUnsafeKey) {
			t.Fatalf("Put(%q) should be refused, got %v", key, err)
		}
		if _, _, err := cache.Get(ctx, key); !errors.Is(err, ErrCacheUnsafeKey) {
			t.Fatalf("Get(%q) should be refused, got %v", key, err)
		}
	}
}

func TestCachePathRejectsTraversal(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "cache"))

	for _, part := range []string{"..", ".", "a/b", `a\b`, "  "} {
		if _, err := cache.Path(part); !errors.Is(err, ErrCacheUnsafeKey) {
			t.Fatalf("Path(%q) should be refused, got %v", part, err)
		}
	}

	path, err := cache.Path("ledger", "2026-08-08")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.HasPrefix(path, cache.Root()) {
		t.Fatalf("Path escaped the cache root: %s", path)
	}

	unconfigured := NewCache("")
	if _, err := unconfigured.Path("x"); !errors.Is(err, ErrCacheNotConfigured) {
		t.Fatalf("expected ErrCacheNotConfigured, got %v", err)
	}
}

func TestCachePruneRemovesOnlyStaleOwnedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	cache := NewCache(root)
	ctx := context.Background()

	if err := cache.Put(ctx, "stale", []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := cache.Put(ctx, "fresh", []byte("new")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stalePath, err := cache.entryPath("stale")
	if err != nil {
		t.Fatalf("entryPath: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// A file yc does not own must survive, so pointing the cache at a
	// populated directory cannot delete someone else's data.
	foreign := filepath.Join(root, "not-ours.txt")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if err := cache.Prune(ctx, 24*time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, ok, _ := cache.Get(ctx, "stale"); ok {
		t.Fatal("Prune left a stale record behind")
	}
	if _, ok, _ := cache.Get(ctx, "fresh"); !ok {
		t.Fatal("Prune removed a fresh record")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("Prune removed a file it does not own: %v", err)
	}

	// Pruning a cache that was never written is not an error.
	if err := NewCache(filepath.Join(t.TempDir(), "missing")).Prune(ctx, time.Hour); err != nil {
		t.Fatalf("Prune of a missing cache: %v", err)
	}
}

func TestCachePruneToSizeDropsOldestFirst(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "cache"))
	ctx := context.Background()

	payload := make([]byte, 512)
	for i, key := range []string{"one", "two", "three"} {
		if err := cache.Put(ctx, key, payload); err != nil {
			t.Fatalf("Put(%s): %v", key, err)
		}
		path, err := cache.entryPath(key)
		if err != nil {
			t.Fatalf("entryPath: %v", err)
		}
		when := time.Now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	if err := cache.PruneToSize(ctx, 1024); err != nil {
		t.Fatalf("PruneToSize: %v", err)
	}
	if _, ok, _ := cache.Get(ctx, "one"); ok {
		t.Fatal("PruneToSize should have dropped the oldest record")
	}
	if _, ok, _ := cache.Get(ctx, "three"); !ok {
		t.Fatal("PruneToSize dropped the newest record")
	}
}

func TestCacheHonorsContextCancellation(t *testing.T) {
	cache := NewCache(filepath.Join(t.TempDir(), "cache"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := cache.Get(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get: expected context.Canceled, got %v", err)
	}
	if err := cache.Put(ctx, "key", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put: expected context.Canceled, got %v", err)
	}
	if err := cache.Prune(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Prune: expected context.Canceled, got %v", err)
	}
}

func TestCacheIgnoresNonRegularEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	cache := NewCache(root)
	ctx := context.Background()

	path, err := cache.entryPath("linked")
	if err != nil {
		t.Fatalf("entryPath: %v", err)
	}
	if err := os.MkdirAll(root, cacheDirMode); err != nil {
		t.Fatalf("create cache root: %v", err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	if _, ok, err := cache.Get(ctx, "linked"); err != nil || ok {
		t.Fatalf("a symlinked cache entry must not be read through, got ok=%v err=%v", ok, err)
	}
}

func TestMemoryCache(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	cache := NewMemoryCache()
	cache.Now = func() time.Time { return now }
	ctx := context.Background()

	if err := cache.Put(ctx, "ledger", []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := cache.Get(ctx, "ledger")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if string(got) != "value" {
		t.Fatalf("Get = %q", got)
	}
	// The fake must hand out copies, like the disk cache does.
	got[0] = 'X'
	again, _, _ := cache.Get(ctx, "ledger")
	if string(again) != "value" {
		t.Fatal("MemoryCache handed out its own backing array")
	}

	// The fake must reject the same keys the real cache rejects, or a test
	// can pass against a key production would refuse.
	if err := cache.Put(ctx, "access_token=x", []byte("x")); !errors.Is(err, ErrCacheUnsafeKey) {
		t.Fatalf("MemoryCache accepted an unsafe key: %v", err)
	}

	now = now.Add(48 * time.Hour)
	if err := cache.Prune(ctx, 24*time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if cache.Len() != 0 {
		t.Fatalf("Prune left %d records", cache.Len())
	}
}

func TestDefaultCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir: %v", err)
	}
	if filepath.Base(dir) != "yc" {
		t.Fatalf("cache dir should be namespaced to yc, got %q", dir)
	}
}

// TestPruneRemovesAStrandedTempEntry covers the orphan an interrupted atomic
// write leaves behind: ".<name>.cache.tmp-<digits>" is cache-owned, so both
// the age prune and the size accounting have to see it. Before that name was
// recognized, a crashed Put left a file no prune would ever remove and no
// budget would ever count.
func TestPruneRemovesAStrandedTempEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	cache := NewCache(root)
	ctx := context.Background()
	if err := cache.Put(ctx, "quota-ledger", []byte(`{"messages.list":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	stray := filepath.Join(root, ".quota-ledger.cache.tmp-123")
	if err := os.WriteFile(stray, []byte("stranded"), 0o600); err != nil {
		t.Fatalf("plant temp entry: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stray, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	entries, err := cache.entries(ctx)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want the record plus the stranded temp file", len(entries))
	}

	if err := cache.Prune(ctx, time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Lstat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stranded temp entry survived Prune: err=%v", err)
	}
	if _, ok, err := cache.Get(ctx, "quota-ledger"); err != nil || !ok {
		t.Fatalf("the fresh record must survive: ok=%v err=%v", ok, err)
	}
}

func TestPutCreatesAPrivateCacheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply on windows")
	}
	root := filepath.Join(t.TempDir(), "cache", "nested")
	cache := NewCache(root)
	if err := cache.Put(context.Background(), "quota-ledger", []byte("{}")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// MkdirAll subtracts the umask, so without the chmod that follows it this
	// is 0755 on a default-umask machine.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("cache directory mode = %04o, want 0700", perm)
	}
}
