package auth

import (
	"sort"
	"strings"
	"time"
)

// Scope is a Google OAuth scope requested or granted during login.
type Scope string

const (
	// ScopeYouTubeReadonly allows reading live chat, broadcast metadata, the
	// user's own channel, and their subscriptions.
	ScopeYouTubeReadonly Scope = "https://www.googleapis.com/auth/youtube.readonly"
	// ScopeYouTubeForceSSL allows sending chat messages, deleting messages,
	// and managing live chat bans. It is required for every write path.
	ScopeYouTubeForceSSL Scope = "https://www.googleapis.com/auth/youtube.force-ssl"
)

// ScopeYouTubeFull is the broad scope the API also accepts for every method yc
// calls. yc never requests it, but Google may report it as granted for a
// credential minted elsewhere, so capability checks must honor it.
const ScopeYouTubeFull Scope = "https://www.googleapis.com/auth/youtube"

// ReadScopes returns the scopes required to read a live chat over OAuth.
func ReadScopes() []Scope {
	return []Scope{ScopeYouTubeReadonly}
}

// SendScopes returns the scopes required to send chat messages.
//
// The API also accepts the broad .../auth/youtube scope, but yc never requests
// it: force-ssl is the narrowest scope that covers every write yc performs.
func SendScopes() []Scope {
	return []Scope{ScopeYouTubeForceSSL}
}

// ModerateScopes returns the scopes required to delete messages and manage
// bans.
func ModerateScopes() []Scope {
	return []Scope{ScopeYouTubeForceSSL}
}

// StreamManageScopes returns the scopes required to read and update broadcast
// metadata behind the Stream Info tab.
func StreamManageScopes() []Scope {
	return []Scope{ScopeYouTubeForceSSL}
}

// LoginScopes returns the full set requested by `yc login`, which is every
// scope yc can make use of.
func LoginScopes() []Scope {
	return []Scope{ScopeYouTubeReadonly, ScopeYouTubeForceSSL}
}

// MissingScopes returns the required scopes absent from granted. It is how yc
// degrades by capability: a missing scope disables a control with an explicit
// reason rather than hiding it.
//
// force-ssl and the broad youtube scope both subsume readonly, so a credential
// granted only force-ssl is not reported as missing read access.
func MissingScopes(granted, required []Scope) []Scope {
	have := make(map[Scope]bool, len(granted))
	for _, scope := range granted {
		scope = normalizeScope(scope)
		if scope == "" {
			continue
		}
		have[scope] = true
	}

	missing := make([]Scope, 0, len(required))
	for _, scope := range required {
		scope = normalizeScope(scope)
		if scope == "" || have[scope] {
			continue
		}
		if scope == ScopeYouTubeReadonly && (have[ScopeYouTubeForceSSL] || have[ScopeYouTubeFull]) {
			continue
		}
		if scope == ScopeYouTubeForceSSL && have[ScopeYouTubeFull] {
			continue
		}
		missing = append(missing, scope)
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

// HasScope reports whether granted covers required, applying the same
// subsumption rules as MissingScopes.
func HasScope(granted []Scope, required Scope) bool {
	return len(MissingScopes(granted, []Scope{required})) == 0
}

// Scopes converts raw scope strings into typed scopes, dropping empties.
//
// Google returns granted scopes as a single space-delimited string on some
// endpoints and as a JSON array on others, so space-separated values are split.
func Scopes(values ...string) []Scope {
	scopes := make([]Scope, 0, len(values))
	seen := make(map[Scope]bool, len(values))
	for _, value := range values {
		for _, field := range strings.Fields(value) {
			scope := normalizeScope(Scope(field))
			if scope == "" || seen[scope] {
				continue
			}
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		return nil
	}
	return scopes
}

// ScopeValues converts typed scopes back into the raw strings used on the wire
// and in the credential file.
func ScopeValues(scopes []Scope) []string {
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		value := strings.TrimSpace(string(scope))
		if value == "" {
			continue
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

// SortedScopeValues returns the scope strings in a stable order, for
// diagnostics that must not vary between runs.
func SortedScopeValues(scopes []Scope) []string {
	values := ScopeValues(scopes)
	sort.Strings(values)
	return values
}

// Capability is one thing yc can do with a credential. Capabilities are what
// the UI degrades on: a control whose capability is unavailable is disabled
// with a reason string, never hidden.
type Capability string

const (
	// CapabilityRead covers reading a public live chat.
	CapabilityRead Capability = "read"
	// CapabilitySend covers posting a chat message.
	CapabilitySend Capability = "send"
	// CapabilityModerate covers deleting messages and managing bans.
	CapabilityModerate Capability = "moderate"
	// CapabilityManageStream covers reading and updating broadcast metadata.
	CapabilityManageStream Capability = "manage-stream"
)

// ScopesFor returns the OAuth scopes a capability requires.
func ScopesFor(capability Capability) []Scope {
	switch capability {
	case CapabilityRead:
		return ReadScopes()
	case CapabilitySend:
		return SendScopes()
	case CapabilityModerate:
		return ModerateScopes()
	case CapabilityManageStream:
		return StreamManageScopes()
	default:
		return nil
	}
}

// CredentialKind identifies which credential mode yc is running in. Key-only
// mode is first class: an API key alone reads a public live chat with no OAuth
// at all, which is the zero-friction first-run path.
type CredentialKind string

const (
	// CredentialKindNone means nothing is configured; only --mock runs.
	CredentialKindNone CredentialKind = "none"
	// CredentialKindAPIKey means an API key with no OAuth token: public
	// read only, no send, no moderation.
	CredentialKindAPIKey CredentialKind = "api-key"
	// CredentialKindOAuth means an OAuth access token whose granted scopes
	// decide what is possible.
	CredentialKindOAuth CredentialKind = "oauth"
)

// Credentials is the capability view of whatever yc has to work with. It holds
// Secret values so formatting it anywhere prints placeholders.
type Credentials struct {
	ClientID     string
	AccessToken  Secret
	RefreshToken Secret
	APIKey       Secret
	Scopes       []Scope
	ExpiresAt    time.Time
}

// NewAPIKeyCredentials returns key-only read credentials.
func NewAPIKeyCredentials(apiKey Secret) Credentials {
	return Credentials{APIKey: apiKey}
}

// NewOAuthCredentials returns credentials backed by a completed token set.
func NewOAuthCredentials(tokens TokenSet) Credentials {
	return Credentials{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Scopes:       cloneScopes(tokens.Scopes),
		ExpiresAt:    tokens.ExpiresAt,
	}
}

// Kind reports which credential mode these values represent.
func (c Credentials) Kind() CredentialKind {
	switch {
	case c.AccessToken.Present():
		return CredentialKindOAuth
	case c.APIKey.Present():
		return CredentialKindAPIKey
	default:
		return CredentialKindNone
	}
}

// Can reports whether the credential covers capability.
func (c Credentials) Can(capability Capability) bool {
	switch c.Kind() {
	case CredentialKindAPIKey:
		// An API key is an accepted caller identity for reading a public
		// live chat, and nothing else.
		return capability == CapabilityRead
	case CredentialKindOAuth:
		return len(MissingScopes(c.Scopes, ScopesFor(capability))) == 0
	default:
		return false
	}
}

// CanRead reports whether the credential can read a live chat.
func (c Credentials) CanRead() bool { return c.Can(CapabilityRead) }

// CanSend reports whether the credential can post a chat message.
func (c Credentials) CanSend() bool { return c.Can(CapabilitySend) }

// CanModerate reports whether the credential can delete messages and ban users.
func (c Credentials) CanModerate() bool { return c.Can(CapabilityModerate) }

// CanManageStream reports whether the credential can update broadcast metadata.
func (c Credentials) CanManageStream() bool { return c.Can(CapabilityManageStream) }

// Reason returns the user-facing explanation for a capability yc cannot offer,
// or an empty string when it can. The text never contains credential material.
func (c Credentials) Reason(capability Capability) string {
	if c.Can(capability) {
		return ""
	}
	switch c.Kind() {
	case CredentialKindNone:
		return "no credentials configured; run `yc login` or set an API key"
	case CredentialKindAPIKey:
		return "API-key mode is read-only; run `yc login` to " + capabilityVerb(capability)
	default:
		missing := MissingScopes(c.Scopes, ScopesFor(capability))
		if len(missing) == 0 {
			return "credential cannot " + capabilityVerb(capability)
		}
		return "missing OAuth scope " + strings.Join(SortedScopeValues(missing), ", ") + "; run `yc login` again and approve it"
	}
}

// Expired reports whether the access token is at or inside skew of its stated
// expiry. A zero expiry is treated as unknown, not expired.
func (c Credentials) Expired(now time.Time, skew time.Duration) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	if skew < 0 {
		skew = 0
	}
	return !now.Add(skew).Before(c.ExpiresAt)
}

// Refreshable reports whether a refresh token is available to recover from an
// expired access token without another interactive login.
func (c Credentials) Refreshable() bool {
	return c.RefreshToken.Present()
}

// Redactor returns a redactor configured with every secret in the credential.
func (c Credentials) Redactor() Redactor {
	return NewRedactor(c.AccessToken, c.RefreshToken, c.APIKey)
}

// capabilityVerb renders a capability as an action for a reason string.
func capabilityVerb(capability Capability) string {
	switch capability {
	case CapabilityRead:
		return "read chat"
	case CapabilitySend:
		return "send messages"
	case CapabilityModerate:
		return "moderate chat"
	case CapabilityManageStream:
		return "edit stream info"
	default:
		return "use that feature"
	}
}

// normalizeScope trims a scope and drops it when empty.
func normalizeScope(scope Scope) Scope {
	return Scope(strings.TrimSpace(string(scope)))
}

// cloneScopes copies a scope slice so a caller cannot mutate stored state.
func cloneScopes(scopes []Scope) []Scope {
	if len(scopes) == 0 {
		return nil
	}
	return append([]Scope(nil), scopes...)
}
