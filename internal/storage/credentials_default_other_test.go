//go:build !unix

package storage

import (
	"errors"
	"strings"
	"testing"
)

func TestNewDefaultCredentialStoreIsUnsupportedOffUnix(t *testing.T) {
	store, err := NewDefaultCredentialStore()
	if store != nil {
		t.Fatal("a platform without the hardened backend must not return a store")
	}
	if !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
	// The sentinel has to tell the user what to do instead of failing blind.
	if !strings.Contains(err.Error(), "YC_GOOGLE_ACCESS_TOKEN") {
		t.Fatalf("the unsupported sentinel should name the workaround: %v", err)
	}
}

func TestCredentialFileStoreIsUnavailableOffUnix(t *testing.T) {
	plan := CredentialFilePlan{
		Path:    `C:\yc\credentials.json`,
		Dir:     `C:\yc`,
		DirMode: CredentialDirMode,
		Mode:    CredentialFileMode,
	}
	if _, err := NewCredentialFileStore(plan); !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
	if err := credentialPlatformSupported(); !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
	if _, err := openCredentialFileNoFollow("anything"); !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
}
