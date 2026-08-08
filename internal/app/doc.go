// Package app owns the terminal UI model and the app-facing chat boundary.
//
// It contains the Bubble Tea update/view state, per-chat UI state, command
// palette, local filters, inspect/reply behavior, the mock chat source, and the
// live-client adapter. The package consumes normalized YouTube models from
// internal/youtube and app-facing interfaces declared in contract.go; it must
// not depend on concrete YouTube Data API JSON types, and it must not perform
// blocking network or filesystem work from View.
package app
