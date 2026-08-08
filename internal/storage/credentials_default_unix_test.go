//go:build unix

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewDefaultCredentialStoreIsSupportedOnUnix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	store, err := NewDefaultCredentialStore()
	if err != nil {
		t.Fatalf("NewDefaultCredentialStore: %v", err)
	}
	if store == nil {
		t.Fatal("the Unix build must return a usable credential store")
	}

	fileStore, ok := store.(*CredentialFileStore)
	if !ok {
		t.Fatalf("expected the hardened file store, got %T", store)
	}
	if filepath.Base(fileStore.Path()) != "credentials.json" {
		t.Fatalf("unexpected credential path %q", fileStore.Path())
	}
	if err := credentialPlatformSupported(); err != nil {
		t.Fatalf("the Unix build must report platform support: %v", err)
	}
}

// TestOpenCredentialFileNoFollowRejectsSymlink covers the syscall-level half of
// the symlink defense: even if the Lstat check passed a moment ago, the open
// itself refuses to follow a link.
func TestOpenCredentialFileNoFollowRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{}"), CredentialFileMode); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	file, err := openCredentialFileNoFollow(link)
	if err == nil {
		_ = file.Close()
		t.Fatal("O_NOFOLLOW must refuse to open a symlink")
	}

	file, err = openCredentialFileNoFollow(target)
	if err != nil {
		t.Fatalf("opening a regular file must succeed: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := openCredentialFileNoFollow(filepath.Join(dir, "missing.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}
