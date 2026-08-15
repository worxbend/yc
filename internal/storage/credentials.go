package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/worxbend/yc/internal/auth"
)

// ErrCredentialsUnsupported is returned by the credential store on platforms
// where yc cannot make the same file-permission guarantees. It is a sentinel so
// callers can degrade with errors.Is instead of matching a message.
var ErrCredentialsUnsupported = errors.New("saved credentials are not supported on this platform")

// ErrCredentialsMalformed is returned when a credential file exists but cannot
// be parsed. It never carries the file's contents.
var ErrCredentialsMalformed = errors.New("credential file is malformed")

// ErrCredentialsInsecure reports a credential file or directory whose type or
// mode makes it unsafe to hold token material.
//
// yc rejects rather than repairs: silently tightening a world-readable
// credential file would hide the fact that something else already had time to
// read it.
var ErrCredentialsInsecure = errors.New("insecure credential permissions")

// ErrCredentialsUnsupportedVersion reports a credential file written by a
// future yc.
var ErrCredentialsUnsupportedVersion = errors.New("unsupported credential file version")

// CredentialRecordVersion is the on-disk schema version yc writes and accepts.
const CredentialRecordVersion = 1

// maxCredentialFileBytes bounds a credential file read. The real file is well
// under a kilobyte; anything larger is a mistake or an attack, not a credential.
const maxCredentialFileBytes = 1 << 20

// Credential file permissions. These are exact, not minimums: a directory or
// file that is more permissive than this is rejected rather than tightened,
// because silently fixing it hides the fact that something else already had
// access.
const (
	CredentialDirMode  fs.FileMode = 0o700
	CredentialFileMode fs.FileMode = 0o600
)

// CredentialStore persists the user's credentials.
type CredentialStore interface {
	LoadCredentials(ctx context.Context) (CredentialRecord, bool, error)
	SaveCredentials(ctx context.Context, record CredentialRecord) error
	DeleteCredentials(ctx context.Context) error
}

// CredentialRecord is the persisted credential set.
//
// Every secret field is an auth.Secret, so formatting or marshaling the record
// anywhere outside the store's own explicit reveal path produces placeholders
// rather than credentials.
type CredentialRecord struct {
	// Version identifies the on-disk schema.
	Version int
	// ClientID is not a secret, but it identifies which OAuth client the
	// tokens belong to and must match before they are reused.
	ClientID     string
	AccessToken  auth.Secret
	RefreshToken auth.Secret
	// APIKey backs the key-only read mode.
	APIKey    auth.Secret
	TokenType string
	ExpiresAt time.Time
	Scopes    []auth.Scope
	// ChannelID and DisplayName are the resolved identity, cached so startup
	// does not have to spend a channels.list call before the UI appears.
	ChannelID   string
	DisplayName string
	UpdatedAt   time.Time
}

// Redactor returns a redactor configured with every secret in the record.
func (r CredentialRecord) Redactor() auth.Redactor {
	return auth.NewRedactor(r.AccessToken, r.RefreshToken, r.APIKey)
}

// Clone returns a deep copy so a caller cannot mutate a stored record's slices.
func (r CredentialRecord) Clone() CredentialRecord {
	r.Scopes = cloneCredentialScopes(r.Scopes)
	return r
}

// Credentials returns the capability view of the record, which is what decides
// whether the composer and the moderation keys are live.
func (r CredentialRecord) Credentials() auth.Credentials {
	return auth.Credentials{
		ClientID:     r.ClientID,
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		APIKey:       r.APIKey,
		Scopes:       cloneCredentialScopes(r.Scopes),
		ExpiresAt:    r.ExpiresAt,
	}
}

// Empty reports whether the record holds no credential material at all.
func (r CredentialRecord) Empty() bool {
	return !r.AccessToken.Present() && !r.RefreshToken.Present() && !r.APIKey.Present()
}

// CredentialRecordFromLoginResult builds a record from a completed login.
func CredentialRecordFromLoginResult(result auth.LoginResult, clientID string, updatedAt time.Time) CredentialRecord {
	scopes := result.Scopes
	if len(scopes) == 0 {
		scopes = result.Tokens.Scopes
	}
	return CredentialRecord{
		Version:      CredentialRecordVersion,
		ClientID:     strings.TrimSpace(clientID),
		AccessToken:  result.Tokens.AccessToken,
		RefreshToken: result.Tokens.RefreshToken,
		TokenType:    strings.TrimSpace(result.Tokens.TokenType),
		ExpiresAt:    result.Tokens.ExpiresAt,
		Scopes:       cloneCredentialScopes(scopes),
		ChannelID:    strings.TrimSpace(result.Identity.ChannelID),
		DisplayName:  strings.TrimSpace(result.Identity.DisplayName),
		UpdatedAt:    updatedAt,
	}
}

// DefaultCredentialFilePath returns the platform credential file path.
func DefaultCredentialFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve credential directory: %w", err)
	}
	return filepath.Join(dir, "yc", "credentials.json"), nil
}

// CredentialFilePlan is a validated plan for one credential file: its path, its
// directory, and the modes both must have.
type CredentialFilePlan struct {
	Path    string
	Dir     string
	DirMode fs.FileMode
	Mode    fs.FileMode
}

// NewCredentialFilePlan validates path and returns the plan for it. An empty
// path uses the platform default.
func NewCredentialFilePlan(path string) (CredentialFilePlan, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		defaultPath, err := DefaultCredentialFilePath()
		if err != nil {
			return CredentialFilePlan{}, err
		}
		path = defaultPath
	}
	path = filepath.Clean(path)
	plan := CredentialFilePlan{
		Path:    path,
		Dir:     filepath.Dir(path),
		DirMode: CredentialDirMode,
		Mode:    CredentialFileMode,
	}
	return plan, plan.Validate()
}

// Validate checks the plan's own invariants.
func (p CredentialFilePlan) Validate() error {
	if strings.TrimSpace(p.Path) == "" || filepath.Clean(p.Path) == "." {
		return errors.New("credential file path is required")
	}
	if filepath.Base(p.Path) == string(filepath.Separator) || filepath.Base(p.Path) == "." {
		return errors.New("credential file path must name a file")
	}
	if strings.TrimSpace(p.Dir) == "" {
		return errors.New("credential directory is required")
	}
	if err := validateCredentialMode("credential directory", p.DirMode, CredentialDirMode); err != nil {
		return err
	}
	return validateCredentialMode("credential file", p.Mode, CredentialFileMode)
}

// CredentialFileStore is the hardened Unix credential file.
//
// It enforces a 0700 directory and a 0600 file with exact modes, rejects
// symlinks, opens with O_NOFOLLOW, and replaces atomically. On non-Unix builds
// every method returns ErrCredentialsUnsupported rather than writing a file
// whose permissions yc cannot vouch for.
type CredentialFileStore struct {
	plan CredentialFilePlan
}

var _ CredentialStore = (*CredentialFileStore)(nil)

// NewDefaultCredentialFileStore returns a store at the default path.
func NewDefaultCredentialFileStore() (*CredentialFileStore, error) {
	plan, err := NewCredentialFilePlan("")
	if err != nil {
		return nil, err
	}
	return NewCredentialFileStore(plan)
}

// NewCredentialFileStore returns a store for a validated plan.
func NewCredentialFileStore(plan CredentialFilePlan) (*CredentialFileStore, error) {
	if err := plan.Validate(); err != nil {
		return nil, credentialError("validate credential file plan", plan.Path, err, auth.Redactor{})
	}
	if err := credentialPlatformSupported(); err != nil {
		return nil, credentialError("prepare credential storage", plan.Path, err, auth.Redactor{})
	}
	return &CredentialFileStore{plan: plan}, nil
}

// Plan returns the immutable plan this store enforces.
func (s *CredentialFileStore) Plan() CredentialFilePlan {
	if s == nil {
		return CredentialFilePlan{}
	}
	return s.plan
}

// Path returns the credential file path.
func (s *CredentialFileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.plan.Path
}

// StoreLabel returns a short human name for diagnostics, such as
// "credential file".
func (s *CredentialFileStore) StoreLabel() string {
	return "credential file"
}

// StoreLocation returns a display-safe location for diagnostics.
//
// The path is the one credential-adjacent string yc does print, so it still
// goes through the pattern redactor in case a user pointed --config at
// something with a token-shaped component.
func (s *CredentialFileStore) StoreLocation() string {
	return auth.NewRedactor().Redact(s.Path())
}

// LoadCredentials reads and validates the credential file. A missing file is
// (zero, false, nil), not an error.
func (s *CredentialFileStore) LoadCredentials(ctx context.Context) (CredentialRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CredentialRecord{}, false, err
	}
	if s == nil {
		return CredentialRecord{}, false, nil
	}
	if err := s.plan.Validate(); err != nil {
		return CredentialRecord{}, false, credentialError("validate credential file plan", s.plan.Path, err, auth.Redactor{})
	}
	if err := credentialPlatformSupported(); err != nil {
		return CredentialRecord{}, false, credentialError("load credential file", s.plan.Path, err, auth.Redactor{})
	}

	info, err := os.Lstat(s.plan.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return CredentialRecord{}, false, nil
	}
	if err != nil {
		return CredentialRecord{}, false, credentialError("load credential file", s.plan.Path, err, auth.Redactor{})
	}
	if err := validateCredentialDir(s.plan.Dir); err != nil {
		return CredentialRecord{}, false, credentialError("load credential file", s.plan.Path, err, auth.Redactor{})
	}
	if err := validateCredentialFileInfo(info); err != nil {
		return CredentialRecord{}, false, credentialError("load credential file", s.plan.Path, err, auth.Redactor{})
	}

	// O_NOFOLLOW closes the window between the Lstat above and the open: a
	// symlink swapped in after the check still fails here.
	file, err := openCredentialFileNoFollow(s.plan.Path)
	if err != nil {
		return CredentialRecord{}, false, credentialError("load credential file", s.plan.Path, err, auth.Redactor{})
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil {
		return CredentialRecord{}, false, credentialError("load credential file", s.plan.Path, err, auth.Redactor{})
	}
	if len(data) > maxCredentialFileBytes {
		return CredentialRecord{}, false, credentialMalformedError("load credential file", s.plan.Path, ErrCredentialsMalformed)
	}

	record, err := ParseCredentialFile(data)
	if err != nil {
		return CredentialRecord{}, false, credentialMalformedError("load credential file", s.plan.Path, err)
	}
	return record, true, nil
}

// SaveCredentials writes the record atomically with exact private permissions.
func (s *CredentialFileStore) SaveCredentials(ctx context.Context, record CredentialRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	redactor := record.Redactor()
	if err := s.plan.Validate(); err != nil {
		return credentialError("validate credential file plan", s.plan.Path, err, redactor)
	}
	if err := credentialPlatformSupported(); err != nil {
		return credentialError("save credential file", s.plan.Path, err, redactor)
	}

	data, err := MarshalCredentialFile(record)
	if err != nil {
		return credentialError("encode credential file", s.plan.Path, err, redactor)
	}
	if err := ensureCredentialDir(s.plan.Dir); err != nil {
		return credentialError("prepare credential directory", s.plan.Dir, err, redactor)
	}
	if err := validateCredentialReplacementTarget(s.plan.Path); err != nil {
		return credentialError("prepare credential file", s.plan.Path, err, redactor)
	}
	if err := writeCredentialFileAtomically(ctx, s.plan.Path, data, s.plan.Mode); err != nil {
		return credentialError("save credential file", s.plan.Path, err, redactor)
	}
	return nil
}

// DeleteCredentials removes the credential file. A missing file is not an error.
func (s *CredentialFileStore) DeleteCredentials(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	if err := s.plan.Validate(); err != nil {
		return credentialError("validate credential file plan", s.plan.Path, err, auth.Redactor{})
	}
	if err := credentialPlatformSupported(); err != nil {
		return credentialError("delete credential file", s.plan.Path, err, auth.Redactor{})
	}

	info, err := os.Lstat(s.plan.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return credentialError("delete credential file", s.plan.Path, err, auth.Redactor{})
	}
	// Refusing to unlink a symlink or a device node keeps `yc logout` from
	// being turned into a delete-arbitrary-file primitive.
	if err := validateCredentialFileInfo(info); err != nil {
		return credentialError("delete credential file", s.plan.Path, err, auth.Redactor{})
	}
	if err := os.Remove(s.plan.Path); err != nil {
		return credentialError("delete credential file", s.plan.Path, err, auth.Redactor{})
	}
	if err := syncCredentialDir(s.plan.Dir); err != nil {
		return credentialError("sync credential directory", s.plan.Dir, err, auth.Redactor{})
	}
	return nil
}

// MemoryCredentialStore is an in-memory store for tests. It never touches disk.
type MemoryCredentialStore struct {
	mu        sync.RWMutex
	record    CredentialRecord
	loaded    bool
	loadErr   error
	saveErr   error
	deleteErr error
	saves     []CredentialRecord
	deletes   int
}

var _ CredentialStore = (*MemoryCredentialStore)(nil)

// NewMemoryCredentialStore returns an empty in-memory store.
func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{}
}

// SetCredentials seeds the store with a record.
func (s *MemoryCredentialStore) SetCredentials(record CredentialRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record = record.Clone()
	s.loaded = true
}

// SetErrors configures the errors returned by load, save, and delete.
func (s *MemoryCredentialStore) SetErrors(loadErr, saveErr, deleteErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadErr, s.saveErr, s.deleteErr = loadErr, saveErr, deleteErr
}

// SavedRecords returns copies of every record passed to SaveCredentials.
func (s *MemoryCredentialStore) SavedRecords() []CredentialRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.saves) == 0 {
		return nil
	}
	records := make([]CredentialRecord, len(s.saves))
	for i, record := range s.saves {
		records[i] = record.Clone()
	}
	return records
}

// DeleteCount returns how many successful deletes the store has seen.
func (s *MemoryCredentialStore) DeleteCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deletes
}

// LoadCredentials returns the stored record.
func (s *MemoryCredentialStore) LoadCredentials(ctx context.Context) (CredentialRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CredentialRecord{}, false, err
	}
	if s == nil {
		return CredentialRecord{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return CredentialRecord{}, false, s.loadErr
	}
	return s.record.Clone(), s.loaded, nil
}

// SaveCredentials stores a copy of the record.
func (s *MemoryCredentialStore) SaveCredentials(ctx context.Context, record CredentialRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.record = record.Clone()
	s.loaded = true
	s.saves = append(s.saves, record.Clone())
	return nil
}

// DeleteCredentials clears the stored record.
func (s *MemoryCredentialStore) DeleteCredentials(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.record = CredentialRecord{}
	s.loaded = false
	s.deletes++
	return nil
}

// UnsupportedCredentialStore is the store handed to callers on platforms with
// no hardened backend. Every operation reports the redacted sentinel, and none
// of them write anything.
type UnsupportedCredentialStore struct{}

var _ CredentialStore = UnsupportedCredentialStore{}

// LoadCredentials reports that no credential backend exists on this platform.
func (UnsupportedCredentialStore) LoadCredentials(ctx context.Context) (CredentialRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CredentialRecord{}, false, err
	}
	return CredentialRecord{}, false, unsupportedCredentialsError()
}

// SaveCredentials refuses to persist anything on an unsupported platform.
func (UnsupportedCredentialStore) SaveCredentials(ctx context.Context, _ CredentialRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unsupportedCredentialsError()
}

// DeleteCredentials reports the same unsupported-platform error; there is
// never anything to delete.
func (UnsupportedCredentialStore) DeleteCredentials(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unsupportedCredentialsError()
}

// unsupportedCredentialsError renders the platform sentinel with the action a
// user can actually take.
func unsupportedCredentialsError() error {
	return fmt.Errorf("%w; set YC_GOOGLE_ACCESS_TOKEN and YC_GOOGLE_REFRESH_TOKEN, or use a private config file", ErrCredentialsUnsupported)
}

// MarshalCredentialFile encodes the on-disk credential file.
//
// This is the one deliberate reveal path for auth.Secret values. Nothing else
// in yc may call it: not diagnostics, not debug logging, not `yc config show`.
func MarshalCredentialFile(record CredentialRecord) ([]byte, error) {
	data, err := json.MarshalIndent(credentialFileFromRecord(record), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseCredentialFile decodes the on-disk credential file. Its errors describe
// the shape of the problem and never quote the file's contents.
func ParseCredentialFile(data []byte) (CredentialRecord, error) {
	var file credentialFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return CredentialRecord{}, ErrCredentialsMalformed
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CredentialRecord{}, ErrCredentialsMalformed
	}
	if file.Version != CredentialRecordVersion {
		return CredentialRecord{}, fmt.Errorf("%w: %d", ErrCredentialsUnsupportedVersion, file.Version)
	}
	return file.toRecord()
}

// credentialFile is the versioned JSON envelope. The nested object keeps room
// for a second provider without a schema break.
type credentialFile struct {
	Version int                  `json:"version"`
	Google  credentialFileGoogle `json:"google"`
}

type credentialFileGoogle struct {
	ClientID     string   `json:"client_id,omitempty"`
	AccessToken  string   `json:"access_token,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	APIKey       string   `json:"api_key,omitempty"`
	TokenType    string   `json:"token_type,omitempty"`
	ExpiresAt    string   `json:"expires_at,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	ChannelID    string   `json:"channel_id,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	UpdatedAt    string   `json:"updated_at,omitempty"`
}

func credentialFileFromRecord(record CredentialRecord) credentialFile {
	return credentialFile{
		Version: CredentialRecordVersion,
		Google: credentialFileGoogle{
			ClientID:     record.ClientID,
			AccessToken:  record.AccessToken.Reveal(),
			RefreshToken: record.RefreshToken.Reveal(),
			APIKey:       record.APIKey.Reveal(),
			TokenType:    record.TokenType,
			ExpiresAt:    formatCredentialTime(record.ExpiresAt),
			Scopes:       auth.ScopeValues(record.Scopes),
			ChannelID:    record.ChannelID,
			DisplayName:  record.DisplayName,
			UpdatedAt:    formatCredentialTime(record.UpdatedAt),
		},
	}
}

func (f credentialFile) toRecord() (CredentialRecord, error) {
	expiresAt, err := parseCredentialTime(f.Google.ExpiresAt)
	if err != nil {
		return CredentialRecord{}, err
	}
	updatedAt, err := parseCredentialTime(f.Google.UpdatedAt)
	if err != nil {
		return CredentialRecord{}, err
	}
	return CredentialRecord{
		Version:      f.Version,
		ClientID:     f.Google.ClientID,
		AccessToken:  auth.NewSecret(f.Google.AccessToken),
		RefreshToken: auth.NewSecret(f.Google.RefreshToken),
		APIKey:       auth.NewSecret(f.Google.APIKey),
		TokenType:    f.Google.TokenType,
		ExpiresAt:    expiresAt,
		Scopes:       auth.Scopes(f.Google.Scopes...),
		ChannelID:    f.Google.ChannelID,
		DisplayName:  f.Google.DisplayName,
		UpdatedAt:    updatedAt,
	}, nil
}

func formatCredentialTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseCredentialTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, ErrCredentialsMalformed
	}
	return parsed, nil
}

// validateCredentialMode rejects a mode that is not exactly want, and rejects
// setuid/setgid/sticky bits outright.
func validateCredentialMode(label string, mode, want fs.FileMode) error {
	if mode.Perm() != want {
		return fmt.Errorf("%w: %s mode is %s, want %s", ErrCredentialsInsecure, label, mode.Perm(), want)
	}
	if special := mode & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); special != 0 {
		return fmt.Errorf("%w: %s has special mode bits %s", ErrCredentialsInsecure, label, special)
	}
	return nil
}

func validateCredentialDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	return validateCredentialDirInfo(info)
}

func validateCredentialDirInfo(info fs.FileInfo) error {
	mode := info.Mode()
	if mode&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: credential directory is a symlink", ErrCredentialsInsecure)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: credential directory is not a directory", ErrCredentialsInsecure)
	}
	return validateCredentialMode("credential directory", mode, CredentialDirMode)
}

// ensureCredentialDir creates the credential directory when it is missing and
// verifies it every time.
//
// A directory yc did not create is validated, never chmod'd: tightening
// someone else's directory would mask the fact that it was open.
func ensureCredentialDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, CredentialDirMode); err != nil {
			return err
		}
		// MkdirAll honors the umask, so the mode is set explicitly after
		// creation rather than trusted from the create call.
		if err := os.Chmod(dir, CredentialDirMode); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	return validateCredentialDirInfo(info)
}

func validateCredentialReplacementTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateCredentialFileInfo(info)
}

func validateCredentialFileInfo(info fs.FileInfo) error {
	mode := info.Mode()
	if mode&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: credential file is a symlink", ErrCredentialsInsecure)
	}
	if !mode.IsRegular() {
		return fmt.Errorf("%w: credential file is not a regular file", ErrCredentialsInsecure)
	}
	return validateCredentialMode("credential file", mode, CredentialFileMode)
}

// writeCredentialFileAtomically writes through a private temp file in the same
// directory and renames over the target, so a reader never sees a half-written
// credential and a crash never truncates the old one.
func writeCredentialFileAtomically(ctx context.Context, path string, data []byte, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	closed := false
	closeFile := func() error {
		if closed {
			return nil
		}
		closed = true
		return file.Close()
	}

	// CreateTemp already makes the file 0600, but setting it explicitly
	// keeps the guarantee independent of the standard library's choice.
	if err := file.Chmod(mode); err != nil {
		_ = closeFile()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = closeFile()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = closeFile()
		return err
	}
	if err := closeFile(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCredentialTempFile(tempPath, mode); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	removeTemp = false
	return syncCredentialDir(dir)
}

func validateCredentialTempFile(path string, mode fs.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := validateCredentialFileInfo(info); err != nil {
		return err
	}
	if info.Mode().Perm() != mode.Perm() {
		return fmt.Errorf("%w: credential temp file mode is %s", ErrCredentialsInsecure, info.Mode().Perm())
	}
	return nil
}

// syncCredentialDir flushes the directory entry so the rename survives a crash.
func syncCredentialDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	// Directory fsync is not supported everywhere; an EINVAL there is not a
	// failure of the write that already landed.
	if err := file.Sync(); err != nil && !errors.Is(err, fs.ErrInvalid) && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}

// credentialStoreError is an already-redacted store error that still answers
// errors.Is for the states callers act on.
type credentialStoreError struct {
	message string
	matches []error
}

func (e credentialStoreError) Error() string { return e.message }

// GoString keeps %#v from printing the struct fields verbatim.
func (e credentialStoreError) GoString() string { return e.message }

func (e credentialStoreError) Is(target error) bool {
	for _, match := range e.matches {
		if errors.Is(match, target) {
			return true
		}
	}
	return false
}

// credentialError renders a store failure with the path and a redacted detail.
func credentialError(action, location string, err error, redactor auth.Redactor) error {
	if err == nil {
		return nil
	}
	return credentialStoreError{
		message: sanitizedCredentialMessage(action, location, err.Error(), redactor),
		matches: credentialErrorMatches(err),
	}
}

// credentialMalformedError reports an unparsable file without ever echoing what
// was in it - a malformed credential file is still a file full of credentials.
func credentialMalformedError(action, location string, err error) error {
	matches := []error{ErrCredentialsMalformed}
	if errors.Is(err, ErrCredentialsUnsupportedVersion) {
		matches = append(matches, ErrCredentialsUnsupportedVersion)
	}
	return credentialStoreError{
		message: sanitizedCredentialMessage(action, location, ErrCredentialsMalformed.Error(), auth.Redactor{}),
		matches: matches,
	}
}

func sanitizedCredentialMessage(action, location, detail string, redactor auth.Redactor) string {
	var builder strings.Builder
	builder.WriteString(action)
	if strings.TrimSpace(location) != "" {
		builder.WriteString(" ")
		builder.WriteString(location)
	}
	if strings.TrimSpace(detail) != "" {
		builder.WriteString(": ")
		builder.WriteString(detail)
	}
	// The caller's secrets first, then the pattern redactor, so a value the
	// caller did not know about is still caught by shape.
	return auth.NewRedactor().Redact(redactor.Redact(builder.String()))
}

// credentialErrorMatches curates which sentinels survive wrapping. The original
// error is deliberately dropped so an os.PathError cannot re-introduce a path
// or a syscall detail into a formatted message downstream.
func credentialErrorMatches(err error) []error {
	var matches []error
	for _, candidate := range []error{
		ErrCredentialsUnsupported,
		ErrCredentialsMalformed,
		ErrCredentialsInsecure,
		ErrCredentialsUnsupportedVersion,
		ErrPathIsDirectory,
		fs.ErrPermission,
		fs.ErrExist,
		fs.ErrNotExist,
	} {
		if errors.Is(err, candidate) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func cloneCredentialScopes(scopes []auth.Scope) []auth.Scope {
	if len(scopes) == 0 {
		return nil
	}
	return append([]auth.Scope(nil), scopes...)
}
