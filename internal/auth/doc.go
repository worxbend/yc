// Package auth contains Google OAuth2 installed-app login, token, scope, and
// secret primitives.
//
// Secret values are wrapped so ordinary formatting and JSON encoding redact
// them. Callers must use explicit reveal paths only at the boundary that sends
// credentials to Google or writes them through the storage layer. Access
// tokens, refresh tokens, client secrets, API keys, authorization codes, OAuth
// state, PKCE verifiers, authorization URLs, and bearer headers must never
// appear in a log line, a printed string, or a wrapped error.
package auth
