// Package debuglog writes opt-in JSON-line diagnostics with explicit redaction.
//
// Callers provide curated fields instead of raw structs. This keeps transport,
// auth, config, storage, quota, and render diagnostics useful without exposing
// OAuth tokens, refresh tokens, client secrets, API keys, callback values, or
// authorization URLs.
package debuglog
