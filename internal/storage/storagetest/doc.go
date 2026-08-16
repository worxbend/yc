// Package storagetest holds the in-memory test doubles for internal/storage.
//
// A double is a stand-in for the real thing: MemoryCredentialStore satisfies
// storage.CredentialStore without touching disk, and it carries affordances no
// production caller wants - seeding a record, forcing load, save, or delete to
// fail, and reporting back what it was handed.
//
// Go's usual home for a helper like that is a _test.go file, and that is not an
// option here. A _test.go file is compiled only into its own package's test
// binary, so no other package can import it, and these doubles are used from
// another package's tests - internal/cli builds its command wiring against them.
// Giving them a package of their own is what makes them importable, and it also
// keeps the stub-configuration API out of internal/storage, whose production
// files carry the hardened credential handling and should not read as though
// tests can reach into them.
//
// This package imports internal/storage and never the reverse, so nothing
// production depends on it, and it cannot pull test-only behavior into a
// shipped binary.
package storagetest
