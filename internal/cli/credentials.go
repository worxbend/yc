package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/worxbend/yc/internal/app"
	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/config"
	"github.com/worxbend/yc/internal/storage"
)

// newCredentialStore is a variable so tests can substitute an in-memory store
// and never touch a real credential file.
var newCredentialStore = func() (storage.CredentialStore, error) {
	return storage.NewDefaultCredentialFileStore()
}

// credentialLoadStatus records what the credential store had to say, so the
// caller can report it once and reuse it for diagnostics and for persisting a
// refresh later.
type credentialLoadStatus struct {
	Path     string
	Label    string
	Location string
	Present  bool
	// TokenShadowed marks the confusing case: saved credentials exist but an
	// environment or config token outranks them. Without saying so, a user
	// who just ran `yc login` sees the old token's failure and concludes the
	// login did not work.
	TokenShadowed bool
	Err           error
	Store         storage.CredentialStore
	Record        storage.CredentialRecord
}

// credentialMode names how yc is authenticating, which decides what the UI can
// offer.
type credentialMode string

const (
	// credentialModeNone has nothing to authenticate with.
	credentialModeNone credentialMode = "none"
	// credentialModeAPIKey reads public live chats with an API key and can
	// neither send nor moderate.
	credentialModeAPIKey credentialMode = "api-key"
	// credentialModeOAuth holds a user token; what it can do depends on the
	// scopes that were granted.
	credentialModeOAuth credentialMode = "oauth"
)

// credentialCapability is the honest answer to "what can this run actually do".
//
// yc degrades by capability rather than by hiding controls: the composer stays
// visible with a reason attached, because a disabled control that explains
// itself teaches the user what to fix and a missing one does not.
type credentialCapability struct {
	Mode credentialMode
	// ScopesKnown is false when the token arrived through the environment or
	// the config file, where yc has no record of what was granted. In that
	// case CanSend and CanModerate are optimistic and the API is the
	// authority.
	ScopesKnown bool
	Granted     []auth.Scope
	CanRead     bool
	CanSend     bool
	CanModerate bool
	// Reason explains a missing capability in one line, suitable for the
	// status bar or a disabled composer.
	Reason string
}

// describeCapability classifies the effective credentials without any network
// access, so `yc chat` can refuse early and `yc doctor` can report offline.
func describeCapability(cfg config.Config, status credentialLoadStatus) credentialCapability {
	token := strings.TrimSpace(cfg.Google.AccessToken)
	refresh := strings.TrimSpace(cfg.Google.RefreshToken)
	apiKey := strings.TrimSpace(cfg.YouTube.APIKey)

	switch {
	case token == "" && refresh == "" && apiKey == "":
		return credentialCapability{
			Mode:   credentialModeNone,
			Reason: "no credentials configured; run `yc login`, set YC_YOUTUBE_API_KEY for read-only mode, or use `yc chat --mock`",
		}
	case token == "" && refresh == "":
		return credentialCapability{
			Mode:    credentialModeAPIKey,
			CanRead: true,
			Reason:  "API key mode reads public live chats only; run `yc login` to send messages and moderate",
		}
	}

	capability := credentialCapability{Mode: credentialModeOAuth, CanRead: true}
	// Scopes are only trustworthy when they came from the credential file yc
	// wrote at login. A token pasted into the environment carries no record
	// of its grant, so claiming to know its scopes would be a guess.
	if status.Present && len(status.Record.Scopes) > 0 && !status.TokenShadowed {
		capability.ScopesKnown = true
		capability.Granted = append([]auth.Scope(nil), status.Record.Scopes...)
		missingWrite := auth.MissingScopes(capability.Granted, auth.SendScopes())
		capability.CanSend = len(missingWrite) == 0
		capability.CanModerate = capability.CanSend
		if !capability.CanSend {
			capability.Reason = "granted scopes cover reading only; re-run `yc login` to grant " +
				strings.Join(auth.ScopeValues(auth.SendScopes()), ", ") + " for sending and moderation"
		}
		return capability
	}

	capability.CanSend = true
	capability.CanModerate = true
	capability.Reason = "granted scopes are unknown for a token supplied through the environment or config file; run `yc doctor` to validate it"
	return capability
}

// applyStoredCredentials fills empty credential fields from the credential
// store.
//
// Saved credentials sit at the bottom of the precedence order on purpose: an
// operator who exports a token for one run must be able to do so without
// deleting their saved login, and the reverse - a stale saved token quietly
// overriding an explicit export - is the failure that is hard to diagnose.
func applyStoredCredentials(ctx context.Context, cfg *config.Config) (credentialLoadStatus, error) {
	store, err := newCredentialStore()
	status := credentialLoadStatus{}
	if store != nil {
		status.Path = credentialStorePath(store)
		status.Label = credentialStoreLabel(store)
		status.Location = credentialStoreLocation(store)
		status.Store = store
	}
	if err != nil {
		status.Err = err
		// An unsupported platform is a known limitation, not a failure:
		// environment and config credentials still work there.
		if errors.Is(err, storage.ErrCredentialsUnsupported) {
			return status, nil
		}
		return status, err
	}
	if store == nil {
		return status, nil
	}

	record, ok, err := store.LoadCredentials(ctx)
	if err != nil {
		status.Err = err
		if errors.Is(err, storage.ErrCredentialsUnsupported) {
			return status, nil
		}
		return status, err
	}
	status.Present = ok
	if !ok {
		return status, nil
	}

	status.Record = record.Clone()
	status.TokenShadowed = record.AccessToken.Present() && strings.TrimSpace(cfg.Google.AccessToken) != ""
	applyCredentialRecord(cfg, record)
	return status, nil
}

// applyCredentialRecord copies stored values into the fields still empty after
// file and environment merging.
func applyCredentialRecord(cfg *config.Config, record storage.CredentialRecord) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.Google.ClientID) == "" {
		cfg.Google.ClientID = strings.TrimSpace(record.ClientID)
	}
	if strings.TrimSpace(cfg.Google.AccessToken) == "" {
		cfg.Google.AccessToken = record.AccessToken.Reveal()
	}
	if strings.TrimSpace(cfg.Google.RefreshToken) == "" {
		cfg.Google.RefreshToken = record.RefreshToken.Reveal()
	}
	if strings.TrimSpace(cfg.YouTube.APIKey) == "" {
		cfg.YouTube.APIKey = record.APIKey.Reveal()
	}
	if strings.TrimSpace(cfg.YouTube.ChannelID) == "" {
		cfg.YouTube.ChannelID = strings.TrimSpace(record.ChannelID)
	}
}

// loadConfigWithStoredCredentials is the standard startup load: config first,
// then the saved credentials beneath it.
func loadConfigWithStoredCredentials(ctx context.Context, environ []string, overrides config.Overrides) (config.Config, credentialLoadStatus, error) {
	cfg, err := config.Load(environ, overrides)
	if err != nil {
		return cfg, credentialLoadStatus{}, err
	}
	status, err := applyStoredCredentials(ctx, &cfg)
	return cfg, status, err
}

// credentialPrecedenceHint explains the shadowing case in the one place it
// matters: after a credential failure the user is about to retry.
func credentialPrecedenceHint(status credentialLoadStatus) string {
	if !status.TokenShadowed {
		return ""
	}
	return "Saved login credentials are present but were not used because an environment or config token takes precedence; " +
		"unset YC_GOOGLE_ACCESS_TOKEN/GOOGLE_ACCESS_TOKEN or remove google_access_token from config.toml, then retry."
}

// credentialStorePath reports the store's file path when it has one.
func credentialStorePath(store storage.CredentialStore) string {
	if withPath, ok := store.(interface{ Path() string }); ok {
		return strings.TrimSpace(withPath.Path())
	}
	return ""
}

// credentialStoreLabel reports a short human name for the store.
func credentialStoreLabel(store storage.CredentialStore) string {
	if withLabel, ok := store.(interface{ StoreLabel() string }); ok {
		if label := strings.TrimSpace(withLabel.StoreLabel()); label != "" {
			return label
		}
	}
	if credentialStorePath(store) != "" {
		return "credential file"
	}
	return "credential store"
}

// credentialStoreLocation reports a display-safe location for the store.
func credentialStoreLocation(store storage.CredentialStore) string {
	if withLocation, ok := store.(interface{ StoreLocation() string }); ok {
		if location := strings.TrimSpace(withLocation.StoreLocation()); location != "" {
			return location
		}
	}
	if path := credentialStorePath(store); path != "" {
		return path
	}
	return credentialStoreLabel(store)
}

// credentialFileDoctorCheck renders the credential store's state as a doctor
// line. It is produced here rather than in internal/app because the store is a
// startup concern the app layer never sees.
func credentialFileDoctorCheck(status credentialLoadStatus) app.DoctorCheck {
	label := strings.TrimSpace(status.Label)
	if label == "" {
		label = "credential store"
	}
	location := strings.TrimSpace(status.Location)
	if location == "" {
		location = strings.TrimSpace(status.Path)
	}
	if location == "" {
		location = label
	}
	display := config.RedactDisplayValue(location)

	switch {
	case status.Err != nil && errors.Is(status.Err, storage.ErrCredentialsUnsupported):
		return app.DoctorCheck{
			Name:   label,
			Status: app.DoctorStatusWarn,
			Detail: "saved credentials are unsupported on this platform; use environment variables or a private config file",
		}
	case status.Err != nil:
		// The store error is reported verbatim apart from redaction, and a
		// credential store's failures are the ones most likely to quote file
		// contents. config.RedactDisplayValue only knows credential shapes, so
		// the record's own secrets are removed by value as well - the same
		// two-layer defence safeStartupError applies on the startup path.
		detail := config.RedactDisplayValue(status.Record.Redactor().Redact(status.Err.Error()))
		return app.DoctorCheck{
			Name:   label,
			Status: app.DoctorStatusWarn,
			Detail: fmt.Sprintf("%s load failed: %s; using env/config/defaults", display, detail),
		}
	case status.Present && status.TokenShadowed:
		return app.DoctorCheck{
			Name:   label,
			Status: app.DoctorStatusWarn,
			Detail: display + " loaded but shadowed by an environment or config token",
		}
	case status.Present:
		return app.DoctorCheck{
			Name:   label,
			Status: app.DoctorStatusOK,
			Detail: display + " loaded",
		}
	default:
		return app.DoctorCheck{
			Name:   label,
			Status: app.DoctorStatusWarn,
			Detail: display + " not found; run `yc login`, or set YC_YOUTUBE_API_KEY for read-only mode",
		}
	}
}

// capabilityDoctorCheck reports what the effective credentials can do.
func capabilityDoctorCheck(capability credentialCapability) app.DoctorCheck {
	check := app.DoctorCheck{Name: "credentials"}
	switch capability.Mode {
	case credentialModeNone:
		check.Status = app.DoctorStatusWarn
		check.Detail = capability.Reason
		return check
	case credentialModeAPIKey:
		check.Status = app.DoctorStatusWarn
		check.Detail = "API key (read-only): " + capability.Reason
		return check
	default:
		detail := "OAuth token: read"
		if capability.CanSend {
			detail += ", send"
		}
		if capability.CanModerate {
			detail += ", moderate"
		}
		if capability.ScopesKnown {
			detail += " (granted: " + strings.Join(auth.ScopeValues(capability.Granted), ", ") + ")"
			check.Status = app.DoctorStatusOK
			if !capability.CanSend {
				check.Status = app.DoctorStatusWarn
				detail += "; " + capability.Reason
			}
		} else {
			check.Status = app.DoctorStatusWarn
			detail += "; " + capability.Reason
		}
		check.Detail = detail
		return check
	}
}
