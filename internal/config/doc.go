// Package config loads yc's flat configuration from defaults, files,
// environment variables, and selected CLI overrides.
//
// It also owns display redaction for config-shaped values. Credential
// persistence is intentionally outside this package so config loading stays
// separate from secret storage, and no network client is ever constructed here.
package config
