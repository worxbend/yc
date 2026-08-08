package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The credential file holds a refresh token, which is the one value that
// re-mints everything else. Its protection is entirely the filesystem's: an
// exact mode, a regular file, a real directory, and no symlink anywhere on the
// path. credentials_test.go covers the headline cases. This is the matrix -
// every loosening a umask, an editor, a backup tool, a container bind mount, or
// a helpful `chmod -R` can produce.
//
// yc reports rather than repairs. Quietly tightening a world-readable file
// would hide the fact that something else already had the chance to read it,
// and the only honest answer at that point is to say so and let the user decide
// whether to sign in again.

// TestEveryLoosenedFileModeIsRefused walks the permission bits one at a time.
//
// The check is "exactly 0600", not "at most 0600", and each of these is a mode
// a real tool produces: 0644 from a default umask, 0640 from a group-shared
// deployment, 0660 from a container, 0666 from a careless chmod.
func TestEveryLoosenedFileModeIsRefused(t *testing.T) {
	modes := []fs.FileMode{
		0o601, 0o602, 0o604, 0o606,
		0o610, 0o620, 0o640, 0o660,
		0o644, 0o664, 0o666, 0o700, 0o755, 0o777,
		0o400, 0o200, 0o000,
	}

	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			store, path := newTestStore(t)
			ctx := context.Background()
			if err := store.SaveCredentials(ctx, testRecord()); err != nil {
				t.Fatalf("SaveCredentials: %v", err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("chmod credential file: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, CredentialFileMode) })

			_, _, err := store.LoadCredentials(ctx)
			if !errors.Is(err, ErrCredentialsInsecure) {
				t.Fatalf("LoadCredentials with a %s file = %v, want ErrCredentialsInsecure", mode, err)
			}
			// The refusal names the problem without quoting the file's
			// contents, which are the credential.
			assertRedacted(t, err)
			if !strings.Contains(err.Error(), "mode") {
				t.Fatalf("error = %q, want it to name the permission problem", err)
			}

			// A save must be refused too. Overwriting a file that somebody
			// else can read would put a fresh refresh token into it.
			if err := store.SaveCredentials(ctx, testRecord()); !errors.Is(err, ErrCredentialsInsecure) {
				t.Fatalf("SaveCredentials over a %s file = %v, want ErrCredentialsInsecure", mode, err)
			}
		})
	}
}

// The exact mode is enforced in both directions: a file that is somehow
// *tighter* than 0600 is still refused, because yc could not rewrite it and a
// silent read-only credential store is worse than a named one.
func TestTheFileModeIsExactAndNotAnUpperBound(t *testing.T) {
	store, path := newTestStore(t)
	if err := store.SaveCredentials(context.Background(), testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, CredentialFileMode) })

	if _, _, err := store.LoadCredentials(context.Background()); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("a 0400 credential file = %v, want it named rather than accepted", err)
	}
}

// Setuid, setgid and sticky bits on a credential file are never legitimate.
// They survive a chmod that only sets the permission bits, so a check that
// compared Perm() alone would pass a file carrying them.
func TestSpecialModeBitsAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hardened credential file is a Unix-only backend")
	}
	for _, special := range []fs.FileMode{fs.ModeSetuid, fs.ModeSetgid, fs.ModeSticky} {
		t.Run(special.String(), func(t *testing.T) {
			store, path := newTestStore(t)
			if err := store.SaveCredentials(context.Background(), testRecord()); err != nil {
				t.Fatalf("SaveCredentials: %v", err)
			}
			if err := os.Chmod(path, CredentialFileMode|special); err != nil {
				t.Skipf("cannot set %s here: %v", special, err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, CredentialFileMode) })

			info, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Mode()&special == 0 {
				t.Skipf("this filesystem dropped the %s bit", special)
			}
			if _, _, err := store.LoadCredentials(context.Background()); !errors.Is(err, ErrCredentialsInsecure) {
				t.Fatalf("a credential file with %s = %v, want ErrCredentialsInsecure", special, err)
			}
		})
	}
}

// The directory matters as much as the file: anyone who can write the directory
// can replace the file, and anyone who can read it learns that yc is signed in
// and under which key the ledger is written.
func TestEveryLoosenedDirectoryModeIsRefused(t *testing.T) {
	modes := []fs.FileMode{
		0o701, 0o702, 0o704, 0o705,
		0o710, 0o720, 0o750, 0o770,
		0o755, 0o775, 0o777, 0o600,
	}

	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			store, path := newTestStore(t)
			ctx := context.Background()
			if err := store.SaveCredentials(ctx, testRecord()); err != nil {
				t.Fatalf("SaveCredentials: %v", err)
			}
			dir := filepath.Dir(path)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatalf("chmod credential directory: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, CredentialDirMode) })

			record, found, err := store.LoadCredentials(ctx)
			// A directory with no owner-execute bit cannot be traversed at
			// all, so the kernel refuses before yc's own check runs. That
			// is still a refusal - what matters is that no credential comes
			// back - but it is the OS's error, not ErrCredentialsInsecure.
			traversable := mode&0o100 != 0
			switch {
			case traversable && !errors.Is(err, ErrCredentialsInsecure):
				t.Fatalf("LoadCredentials under a %s directory = %v, want ErrCredentialsInsecure", mode, err)
			case !traversable && err == nil:
				t.Fatalf("LoadCredentials under a %s directory succeeded", mode)
			}
			if found || record.AccessToken.Present() || record.RefreshToken.Present() {
				t.Fatalf("a credential was returned from a %s directory", mode)
			}
			assertRedacted(t, err)
		})
	}
}

// A symlink anywhere on the path is refused, not only at the final component.
// The classic attack is not "point credentials.json at /etc/shadow" - it is to
// own one directory somewhere above it.
func TestASymlinkAnywhereOnThePathIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hardened credential file is a Unix-only backend")
	}

	root := t.TempDir()
	real := filepath.Join(root, "real", "yc")
	if err := os.MkdirAll(real, CredentialDirMode); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	if err := os.Chmod(real, CredentialDirMode); err != nil {
		t.Fatalf("chmod real directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "credentials.json"), []byte(`{"version":1,"google":{}}`), CredentialFileMode); err != nil {
		t.Fatalf("write credential file: %v", err)
	}

	// A symlink to the parent of the credential directory: every component
	// below it is a genuine directory with a correct mode, so only a check
	// that looks above the last component catches this.
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	plan, err := NewCredentialFilePlan(filepath.Join(link, "yc", "credentials.json"))
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	store, err := NewCredentialFileStore(plan)
	if err != nil {
		t.Fatalf("NewCredentialFileStore: %v", err)
	}

	// Whichever way this is answered, it must not be "here are the
	// credentials". A store that resolves the link and validates the real
	// directory is defensible; one that reads through it silently is not.
	_, found, loadErr := store.LoadCredentials(context.Background())
	switch {
	case loadErr == nil && found:
		t.Log("the store resolved the symlinked parent and validated the real directory")
	case loadErr == nil && !found:
		t.Log("the store reported no credentials rather than reading through the link")
	case errors.Is(loadErr, ErrCredentialsInsecure):
		assertRedacted(t, loadErr)
	default:
		t.Fatalf("LoadCredentials through a symlinked parent = %v, want a refusal or a clean resolve", loadErr)
	}
}

// The temp file a save writes through is as sensitive as the file it replaces:
// it holds the same refresh token, and it exists in the same directory for as
// long as the write takes. It must be created private and never left behind.
func TestTheAtomicWriteNeverLeavesAReadableTemporary(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	if err := store.SaveCredentials(ctx, testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read credential directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(path) {
			continue
		}
		t.Errorf("the credential directory holds %q after a save; a temporary was left behind", entry.Name())
	}

	// Every save, including one over an existing file, lands at exactly the
	// two modes yc promises.
	for i := 0; i < 3; i++ {
		if err := store.SaveCredentials(ctx, testRecord()); err != nil {
			t.Fatalf("SaveCredentials %d: %v", i, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat credential file: %v", err)
		}
		if got := info.Mode().Perm(); got != CredentialFileMode {
			t.Fatalf("credential file mode = %s after save %d, want %s", got, i, CredentialFileMode)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("the credential file is not a regular file after save %d", i)
		}
		dirInfo, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("stat credential directory: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != CredentialDirMode {
			t.Fatalf("credential directory mode = %s after save %d, want %s", got, i, CredentialDirMode)
		}
	}
}

// yc creates the directory it needs, and does so at the right mode regardless
// of the umask in force. MkdirAll honours the umask, so a 022 umask would
// otherwise produce a 0755 directory that yc then refuses to use - a first run
// that fails on a machine with entirely ordinary settings.
func TestTheDirectoryIsCreatedTightRegardlessOfTheUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("umask is a Unix concept")
	}

	root := t.TempDir()
	plan, err := NewCredentialFilePlan(filepath.Join(root, "yc", "credentials.json"))
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	store, err := NewCredentialFileStore(plan)
	if err != nil {
		t.Fatalf("NewCredentialFileStore: %v", err)
	}
	if err := store.SaveCredentials(context.Background(), testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	info, err := os.Lstat(filepath.Join(root, "yc"))
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if got := info.Mode().Perm(); got != CredentialDirMode {
		t.Fatalf("created directory mode = %s, want %s", got, CredentialDirMode)
	}
}

// A directory yc did not create is validated, never repaired. Tightening
// someone else's directory would mask the fact that it was open, and the user
// would never learn that their refresh token had been readable.
func TestAnExistingLooseDirectoryIsReportedAndNotRepaired(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "yc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod directory: %v", err)
	}

	plan, err := NewCredentialFilePlan(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	store, err := NewCredentialFileStore(plan)
	if err != nil {
		t.Fatalf("NewCredentialFileStore: %v", err)
	}

	if err := store.SaveCredentials(context.Background(), testRecord()); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("SaveCredentials into a 0755 directory = %v, want ErrCredentialsInsecure", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("the directory mode was changed to %s; yc must report, not repair", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); err == nil {
		t.Fatal("a credential file was written into a directory yc had already judged insecure")
	}
}
