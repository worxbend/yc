// Package storage owns persistent cache and credential storage boundaries.
//
// Credential persistence is supported only on Go unix builds through a
// restrictive credential file with exact private permissions, symlink
// rejection, no-follow opens, and atomic replacement; non-Unix saved
// credentials remain unsupported and must keep returning a redacted sentinel
// error. The disk cache holds small diagnostic and quota-ledger records only -
// yc never downloads asset bytes.
package storage
