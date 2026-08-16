package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoCredentialStoreErrorEverCarriesASecret is the boundary guarantee the
// diagnostics upstream depend on.
//
// internal/cli pastes a store error straight into a `yc doctor` line, and it
// cannot redact a bare opaque token it never saw. So the rule is enforced here,
// at the only layer that has both the file's contents and the record's values:
// no error this package produces, on any failure path, may quote a credential.
func TestNoCredentialStoreErrorEverCarriesASecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hardened credential file is a Unix-only backend")
	}
	ctx := context.Background()
	record := testRecord()

	// Every failure the store can reach, each with a file whose bytes hold
	// real credential values.
	cases := map[string]func(t *testing.T) error{
		"truncated json": func(t *testing.T) error {
			store, path := seededCredentialFile(t, `{"version":1,"google":{"access_token":"`+testAccessToken+`"`)
			_, _, err := store.LoadCredentials(ctx)
			_ = path
			return err
		},
		"wrong type for a secret field": func(t *testing.T) error {
			store, _ := seededCredentialFile(t, `{"version":1,"google":{"access_token":`+fmt.Sprintf("%q", testAccessToken)+`,"refresh_token":12345}}`)
			_, _, err := store.LoadCredentials(ctx)
			return err
		},
		"unsupported version": func(t *testing.T) error {
			store, _ := seededCredentialFile(t, `{"version":9999,"google":{"access_token":"`+testAccessToken+`"}}`)
			_, _, err := store.LoadCredentials(ctx)
			return err
		},
		"loose file permissions": func(t *testing.T) error {
			store, path := seededCredentialFile(t, `{"version":1,"google":{"access_token":"`+testAccessToken+`"}}`)
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod: %v", err)
			}
			_, _, err := store.LoadCredentials(ctx)
			return err
		},
		"loose directory permissions": func(t *testing.T) error {
			store, path := seededCredentialFile(t, `{"version":1,"google":{"access_token":"`+testAccessToken+`"}}`)
			if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("chmod dir: %v", err)
			}
			_, _, err := store.LoadCredentials(ctx)
			return err
		},
		"symlinked file": func(t *testing.T) error {
			store, path := seededCredentialFile(t, `{"version":1}`)
			target := path + ".real"
			if err := os.Rename(path, target); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			_, _, err := store.LoadCredentials(ctx)
			return err
		},
		"save into a read-only directory": func(t *testing.T) error {
			store, path := seededCredentialFile(t, `{"version":1}`)
			dir := filepath.Dir(path)
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatalf("chmod dir: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, CredentialDirMode) })
			return store.SaveCredentials(ctx, record)
		},
		"path is a directory": func(t *testing.T) error {
			path := filepath.Join(t.TempDir(), "yc", "credentials.json")
			if err := os.MkdirAll(path, CredentialDirMode); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			plan, err := NewCredentialFilePlan(path)
			if err != nil {
				return err
			}
			store, err := NewCredentialFileStore(plan)
			if err != nil {
				return err
			}
			_, _, err = store.LoadCredentials(ctx)
			return err
		},
	}

	secrets := []string{testAccessToken, testRefreshToken, testAPIKey}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			err := run(t)
			if err == nil {
				t.Skip("this platform did not reach the failure path")
			}
			// Both the message and the %+v form: an error is as likely to be
			// formatted as it is to be printed.
			for _, rendered := range []string{
				err.Error(),
				fmt.Sprintf("%v", err),
				fmt.Sprintf("%+v", err),
				fmt.Sprintf("%#v", err),
			} {
				for _, secret := range secrets {
					if strings.Contains(rendered, secret) {
						t.Fatalf("a %s error quoted a credential:\n%s", name, rendered)
					}
				}
			}
		})
	}
}

// seededCredentialFile writes raw bytes to a correctly permissioned credential
// file and returns a store pointed at it.
func seededCredentialFile(t *testing.T, contents string) (*CredentialFileStore, string) {
	t.Helper()
	store, path := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("create credential directory: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("chmod credential directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), CredentialFileMode); err != nil {
		t.Fatalf("write credential file: %v", err)
	}
	if err := os.Chmod(path, CredentialFileMode); err != nil {
		t.Fatalf("chmod credential file: %v", err)
	}
	return store, path
}

// The plan a store enforces is immutable and readable, so diagnostics can
// report the modes yc requires without reaching into the store.
func TestCredentialFileStorePlanIsReadableAndNilSafe(t *testing.T) {
	store, path := newTestStore(t)

	plan := store.Plan()
	if plan.Path != path {
		t.Errorf("plan path = %q, want %q", plan.Path, path)
	}
	if plan.Mode != CredentialFileMode || plan.DirMode != CredentialDirMode {
		t.Errorf("plan modes = %v/%v, want %v/%v", plan.Mode, plan.DirMode, CredentialFileMode, CredentialDirMode)
	}

	var nilStore *CredentialFileStore
	if got := nilStore.Plan(); got.Path != "" {
		t.Errorf("nil store plan = %+v, want the zero plan", got)
	}
	if got := nilStore.Path(); got != "" {
		t.Errorf("nil store path = %q", got)
	}
}
