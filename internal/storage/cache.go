package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache defaults. The records yc caches are the quota ledger and small
// diagnostic snapshots, so the byte budget is a guard rail rather than a
// working limit.
const (
	// DefaultCacheMaxAge is how long a cached record stays useful before
	// Prune is entitled to drop it.
	DefaultCacheMaxAge = 30 * 24 * time.Hour
	// DefaultCacheMaxBytes is the on-disk budget PruneToSize enforces.
	DefaultCacheMaxBytes int64 = 8 << 20
	// cacheFileSuffix marks the files this cache owns. Prune only ever
	// removes files carrying it, so pointing a cache at a populated
	// directory cannot delete someone else's data.
	cacheFileSuffix = ".cache"
	// cacheDirMode and cacheFileMode keep cached records private. They are
	// not credential material, but a quota ledger still says what the user
	// was watching and when.
	cacheDirMode  fs.FileMode = 0o700
	cacheFileMode fs.FileMode = 0o600
	// maxCacheEntryBytes bounds a single cached record read.
	maxCacheEntryBytes = 4 << 20
)

var (
	// ErrCacheUnsafeKey reports a cache key that cannot be turned into a
	// safe filename, or that looks like it carries credential material.
	ErrCacheUnsafeKey = errors.New("unsafe cache key")
	// ErrCacheNotConfigured reports a cache with no root directory.
	ErrCacheNotConfigured = errors.New("cache root is not configured")
)

// Store is the cache behavior yc depends on. Keeping it an interface is what
// lets the quota ledger and the doctor run against MemoryCache in tests without
// touching a filesystem.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, data []byte) error
	Prune(ctx context.Context, maxAge time.Duration) error
}

// Cache is a small on-disk key/value area under the platform cache directory.
//
// yc never downloads asset bytes, so this holds only tiny records: the
// persisted quota ledger and diagnostic snapshots. Keys are sanitized into safe
// path components; a key that cannot be made safe is rejected rather than
// escaped, because a credential-shaped key must never become a filename.
type Cache struct {
	root string
}

var _ Store = Cache{}

// NewCache returns a cache rooted at root.
func NewCache(root string) Cache {
	return Cache{root: root}
}

// DefaultCacheDir returns the platform cache directory for yc.
func DefaultCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache directory: %w", err)
	}
	return filepath.Join(dir, "yc"), nil
}

// NewDefaultCache returns a cache under the platform cache directory.
func NewDefaultCache() (Cache, error) {
	root, err := DefaultCacheDir()
	if err != nil {
		return Cache{}, err
	}
	return NewCache(root), nil
}

// Root returns the cache's root directory.
func (c Cache) Root() string { return c.root }

// Path returns the on-disk path for the given key parts.
//
// Each part is sanitized independently and rejected when it is empty after
// sanitizing, contains a path separator, or matches a credential shape, so the
// result can never escape the root or name a token.
func (c Cache) Path(parts ...string) (string, error) {
	if strings.TrimSpace(c.root) == "" {
		return "", ErrCacheNotConfigured
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("%w: no key parts", ErrCacheUnsafeKey)
	}

	safe := make([]string, 0, len(parts)+1)
	safe = append(safe, c.root)
	for _, part := range parts {
		component, err := safeCacheComponent(part)
		if err != nil {
			return "", err
		}
		safe = append(safe, component)
	}
	return filepath.Join(safe...), nil
}

// Get reads a cached record. A missing record is (nil, false, nil).
func (c Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path, err := c.entryPath(key)
	if err != nil {
		return nil, false, err
	}

	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read cache entry: %w", err)
	}
	// A cache entry that is not a regular file was not written by yc, and
	// following it would read whatever it points at.
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("read cache entry: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxCacheEntryBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read cache entry: %w", err)
	}
	if len(data) > maxCacheEntryBytes {
		return nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Put writes a cached record atomically.
func (c Cache) Put(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := c.entryPath(key)
	if err != nil {
		return err
	}
	if len(data) > maxCacheEntryBytes {
		return fmt.Errorf("write cache entry: record is larger than %d bytes", maxCacheEntryBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), cacheDirMode); err != nil {
		return fmt.Errorf("prepare cache directory: %w", err)
	}
	if err := writeCacheFileAtomically(ctx, path, data); err != nil {
		return fmt.Errorf("write cache entry: %w", err)
	}
	return nil
}

// Delete removes a cached record. A missing record is not an error.
func (c Cache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := c.entryPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete cache entry: %w", err)
	}
	return nil
}

// Prune removes records older than maxAge. A non-positive maxAge uses
// DefaultCacheMaxAge. A missing cache directory is not an error.
func (c Cache) Prune(ctx context.Context, maxAge time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if maxAge <= 0 {
		maxAge = DefaultCacheMaxAge
	}
	entries, err := c.entries(ctx)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.modTime.After(cutoff) {
			continue
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("prune cache: %w", err)
		}
	}
	return nil
}

// PruneToSize removes the oldest records until the cache fits maxBytes. A
// non-positive maxBytes uses DefaultCacheMaxBytes.
func (c Cache) PruneToSize(ctx context.Context, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if maxBytes <= 0 {
		maxBytes = DefaultCacheMaxBytes
	}
	entries, err := c.entries(ctx)
	if err != nil {
		return err
	}

	var total int64
	for _, entry := range entries {
		total += entry.size
	}
	if total <= maxBytes {
		return nil
	}

	// Oldest first: the most recently written record is the one most likely
	// to still be in use.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime.Before(entries[j].modTime)
	})
	for _, entry := range entries {
		if total <= maxBytes {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(entry.path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("prune cache: %w", err)
		}
		total -= entry.size
	}
	return nil
}

// cacheEntry is one pruneable file.
type cacheEntry struct {
	path    string
	size    int64
	modTime time.Time
}

// entries lists the cache-owned files under the root.
func (c Cache) entries(ctx context.Context) ([]cacheEntry, error) {
	if strings.TrimSpace(c.root) == "" {
		return nil, ErrCacheNotConfigured
	}

	var entries []cacheEntry
	err := filepath.WalkDir(c.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !cacheOwnedFile(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		entries = append(entries, cacheEntry{path: path, size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("scan cache: %w", err)
	}
	return entries, nil
}

// entryPath maps a key onto its file.
//
// The filename carries a sanitized prefix so a human can recognize an entry,
// plus a hash of the raw key so two keys that sanitize alike cannot collide.
func (c Cache) entryPath(key string) (string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty key", ErrCacheUnsafeKey)
	}
	if containsCredentialMarker(strings.ToLower(trimmed)) {
		return "", fmt.Errorf("%w: key looks like credential material", ErrCacheUnsafeKey)
	}

	digest := sha256.Sum256([]byte(trimmed))
	name := sanitizeCacheComponent(trimmed)
	if len(name) > 40 {
		name = name[:40]
	}
	// The suffix is appended after Path sanitizes the component, because the
	// sanitizer would otherwise rewrite the dot in ".cache".
	path, err := c.Path(name + "-" + hex.EncodeToString(digest[:])[:16])
	if err != nil {
		return "", err
	}
	return path + cacheFileSuffix, nil
}

// cacheOwnedFile reports whether Prune may remove a file by name.
func cacheOwnedFile(name string) bool {
	return strings.HasSuffix(name, cacheFileSuffix) || strings.HasSuffix(name, ".tmp")
}

// safeCacheComponent sanitizes and validates one path component.
func safeCacheComponent(part string) (string, error) {
	trimmed := strings.TrimSpace(part)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty key part", ErrCacheUnsafeKey)
	}
	if strings.ContainsAny(trimmed, `/\`) || trimmed == "." || trimmed == ".." {
		return "", fmt.Errorf("%w: key part must not contain a path separator", ErrCacheUnsafeKey)
	}
	if containsCredentialMarker(strings.ToLower(trimmed)) {
		return "", fmt.Errorf("%w: key part looks like credential material", ErrCacheUnsafeKey)
	}
	sanitized := sanitizeCacheComponent(trimmed)
	if sanitized == "" {
		return "", fmt.Errorf("%w: key part has no usable characters", ErrCacheUnsafeKey)
	}
	return sanitized, nil
}

// sanitizeCacheComponent reduces a value to lowercase ASCII word characters, so
// nothing in a filename depends on the filesystem's encoding or case folding.
func sanitizeCacheComponent(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return strings.Trim(builder.String(), "_")
}

// containsCredentialMarker reports whether a lowercased value carries a
// credential-shaped marker. A cache key is a filename, and a filename is the
// last place a token should end up.
func containsCredentialMarker(lower string) bool {
	markers := []string{
		"://",
		"access_token",
		"refresh_token",
		"id_token",
		"client_secret",
		"api_key",
		"apikey",
		"authorization",
		"bearer ",
		"code_verifier",
		"code_challenge",
		"password",
		"secret",
		"cookie",
		"aiza",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// writeCacheFileAtomically writes through a private temp file and renames.
func writeCacheFileAtomically(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := file.Chmod(cacheFileMode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

// MemoryCache is an in-memory Store for tests. It performs no file I/O and
// applies the same key validation as the disk cache, so a test cannot pass with
// a key the real cache would reject.
type MemoryCache struct {
	mu      sync.RWMutex
	records map[string]memoryCacheRecord
	// Now is injectable so Prune is testable without a wall clock.
	Now func() time.Time
}

type memoryCacheRecord struct {
	data      []byte
	writtenAt time.Time
}

var _ Store = (*MemoryCache)(nil)

// NewMemoryCache returns an empty in-memory cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{records: make(map[string]memoryCacheRecord)}
}

func (c *MemoryCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get returns a cached record.
func (c *MemoryCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validateMemoryCacheKey(key); err != nil {
		return nil, false, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	record, ok := c.records[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), record.data...), true, nil
}

// Put stores a copy of data.
func (c *MemoryCache) Put(ctx context.Context, key string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMemoryCacheKey(key); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.records == nil {
		c.records = make(map[string]memoryCacheRecord)
	}
	c.records[key] = memoryCacheRecord{
		data:      append([]byte(nil), data...),
		writtenAt: c.now(),
	}
	return nil
}

// Prune drops records older than maxAge.
func (c *MemoryCache) Prune(ctx context.Context, maxAge time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if maxAge <= 0 {
		maxAge = DefaultCacheMaxAge
	}
	cutoff := c.now().Add(-maxAge)

	c.mu.Lock()
	defer c.mu.Unlock()

	for key, record := range c.records {
		if !record.writtenAt.After(cutoff) {
			delete(c.records, key)
		}
	}
	return nil
}

// Len reports how many records the cache holds.
func (c *MemoryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.records)
}

// validateMemoryCacheKey applies the disk cache's key rules so the fake cannot
// accept a key the real one would refuse.
func validateMemoryCacheKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("%w: empty key", ErrCacheUnsafeKey)
	}
	if containsCredentialMarker(strings.ToLower(trimmed)) {
		return fmt.Errorf("%w: key looks like credential material", ErrCacheUnsafeKey)
	}
	return nil
}
