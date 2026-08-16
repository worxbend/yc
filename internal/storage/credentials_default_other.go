//go:build !unix

package storage

import "os"

// NewDefaultCredentialStore reports that this platform has no supported
// saved-credential backend.
//
// yc will not write token material to a file whose owner-only semantics it
// cannot enforce, so the non-Unix answer is a redacted sentinel and an
// environment-variable workaround rather than a weaker file.
func NewDefaultCredentialStore() (CredentialStore, error) {
	return nil, unsupportedCredentialsError()
}

// credentialPlatformSupported reports that the hardened credential file is
// unavailable on this platform.
func credentialPlatformSupported() error {
	return unsupportedCredentialsError()
}

// openCredentialFileNoFollow never opens anything off Unix: without an
// O_NOFOLLOW equivalent and reparse-point handling, yc cannot promise the file
// it reads is the file it checked.
func openCredentialFileNoFollow(path string) (*os.File, error) {
	return nil, unsupportedCredentialsError()
}

// openCacheFileNoFollow falls back to an ordinary open off Unix.
//
// The credential reader refuses to open at all here because token material is
// worth failing over. The cache holds only re-fetchable public data - stream
// titles, quota bookkeeping - so refusing to read it would disable yc on this
// platform to defend against a swapped symlink that can leak nothing secret.
// Get still rejects anything that is not a regular file before calling this.
func openCacheFileNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
