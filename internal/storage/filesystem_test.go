package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("theme_name = \"claude\"\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := CheckReadableFile(path); err != nil {
		t.Fatalf("CheckReadableFile on a regular file: %v", err)
	}
	if err := CheckReadableFile(dir); !errors.Is(err, ErrPathIsDirectory) {
		t.Fatalf("expected ErrPathIsDirectory, got %v", err)
	}
	if err := CheckReadableFile(filepath.Join(dir, "missing.toml")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

// TestCheckReadableFileRejectsSymlink keeps the probe from following a link
// into a file the user did not mean to expose.
func TestCheckReadableFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.toml")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	if err := CheckReadableFile(link); !errors.Is(err, ErrPathNotRegular) {
		t.Fatalf("expected ErrPathNotRegular for a symlink, got %v", err)
	}
}

func TestProbeWritableDirLeavesNothingBehind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "nested")

	if err := ProbeWritableDir(dir); err != nil {
		t.Fatalf("ProbeWritableDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read probed directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the probe left files behind: %v", names)
	}

	// Running twice must still succeed: the probe cleans up after itself.
	if err := ProbeWritableDir(dir); err != nil {
		t.Fatalf("second ProbeWritableDir: %v", err)
	}
}

func TestProbeWritableDirReportsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("create read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := ProbeWritableDir(dir); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected fs.ErrPermission, got %v", err)
	}
}

func TestProbeWritableDirRequiresAPath(t *testing.T) {
	if err := ProbeWritableDir(""); err == nil {
		t.Fatal("an empty directory path must be rejected")
	}
}

// TestProbeWritableDirClearsAStaleProbeFile covers a probe that was killed
// before it could clean up: the next probe has to remove the leftover instead
// of stepping around it forever.
func TestProbeWritableDirClearsAStaleProbeFile(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".yc-doctor-write-test")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatalf("plant stale probe: %v", err)
	}
	if err := ProbeWritableDir(dir); err != nil {
		t.Fatalf("ProbeWritableDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("probe left files behind: %v", names)
	}
}
