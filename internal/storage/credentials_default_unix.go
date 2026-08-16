//go:build unix

package storage

import (
	"os"
	"syscall"
)

// NewDefaultCredentialStore returns the hardened credential file, which is the
// supported saved-credential backend on Unix builds.
func NewDefaultCredentialStore() (CredentialStore, error) {
	return NewDefaultCredentialFileStore()
}

// credentialPlatformSupported reports that this build can make the permission
// guarantees the credential file promises.
func credentialPlatformSupported() error {
	return nil
}

// openCredentialFileNoFollow opens the credential file without following a
// symlink at the final path component, which closes the window between the
// Lstat check and the open.
func openCredentialFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// openCacheFileNoFollow opens a cache entry with the same refusal to follow a
// symlink at the final path component that the credential reader uses.
func openCacheFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
