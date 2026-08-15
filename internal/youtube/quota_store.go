package youtube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ledgerRecordVersion is stamped into every persisted record so a future
// format change can be recognized rather than mis-parsed.
const ledgerRecordVersion = 1

// ledgerFileMode and ledgerDirMode keep the ledger private. It holds no
// credential, but it is keyed by a credential fingerprint and it reports what
// an account did today, which is nobody else's business on a shared machine.
const (
	ledgerFileMode fs.FileMode = 0o600
	ledgerDirMode  fs.FileMode = 0o700
)

// LedgerRetentionDays is how many Pacific days of ledger records yc keeps.
//
// The meter itself only ever reads today's record. The window exists so a
// session running across the Pacific reset still finds yesterday where it left
// it, and so a user can see what a long weekend of streaming cost. Without a
// caller for Prune these files accumulate at one per credential per day
// forever, which is a leak in the only directory yc writes to unprompted.
//
// It is a constant rather than a config knob because the records are a few
// hundred bytes each and a knob could only ever buy a larger pile of them. If
// one is ever wanted it belongs on config.QuotaConfig as
// `ledger_retention_days`, documented there and in docs/quota.md alongside the
// rest of the estimated-meter behavior, and passed to Prune in place of this.
const LedgerRetentionDays = 7

// anonymousFingerprint keys the ledger for a run with no credential identity,
// such as an API-key-only session before the channel is known.
const anonymousFingerprint = "anonymous"

// ledgerDayPattern is the only day shape a record filename may take. Anything
// else is refused rather than sanitized, because a filename assembled from
// unvalidated input is how a path traversal starts.
var ledgerDayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ledgerFingerprintPattern is the hex shape CredentialFingerprint produces.
var ledgerFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// ErrLedgerUnsafeKey means a fingerprint or day would not make a safe filename.
var ErrLedgerUnsafeKey = errors.New("quota ledger: unsafe record key")

// ledgerRecord is the on-disk form.
type ledgerRecord struct {
	Version     int            `json:"version"`
	Day         string         `json:"day"`
	Fingerprint string         `json:"fingerprint"`
	ByEndpoint  map[string]int `json:"by_endpoint"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// FileLedgerStore persists the estimated quota ledger through internal/storage.
//
// Persistence matters because the meter is the only thing standing between a
// user and an exhausted day: a restart that zeroes it hands them a false sense
// of budget, and they find out by having chat stop mid-stream. Records are keyed
// by credential fingerprint and Pacific date so two accounts on one machine do
// not share a meter and yesterday's spend never counts against today.
type FileLedgerStore struct {
	root string
}

var _ LedgerStore = (*FileLedgerStore)(nil)

// NewFileLedgerStore returns a store rooted at dir.
func NewFileLedgerStore(dir string) *FileLedgerStore {
	return &FileLedgerStore{root: strings.TrimSpace(dir)}
}

// LoadLedger returns the persisted per-endpoint spend for a fingerprint and
// Pacific day. A missing record is an empty tally, not an error.
func (s *FileLedgerStore) LoadLedger(fingerprint, day string) (map[string]int, error) {
	path, err := s.recordPath(fingerprint, day)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("quota ledger: read record: %w", err)
	}

	var record ledgerRecord
	if err := json.Unmarshal(data, &record); err != nil {
		// A corrupt record is treated as no record. Refusing to start over a
		// damaged estimate would make an advisory meter fatal.
		return map[string]int{}, nil
	}
	if record.Version != ledgerRecordVersion || record.Day != day {
		return map[string]int{}, nil
	}
	if record.ByEndpoint == nil {
		return map[string]int{}, nil
	}
	return record.ByEndpoint, nil
}

// SaveLedger writes the per-endpoint spend atomically.
func (s *FileLedgerStore) SaveLedger(fingerprint, day string, byEndpoint map[string]int) error {
	path, err := s.recordPath(fingerprint, day)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), ledgerDirMode); err != nil {
		return fmt.Errorf("quota ledger: create directory: %w", err)
	}

	data, err := json.Marshal(ledgerRecord{
		Version:     ledgerRecordVersion,
		Day:         day,
		Fingerprint: normalizeFingerprint(fingerprint),
		ByEndpoint:  byEndpoint,
		UpdatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("quota ledger: encode record: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("quota ledger: create temp file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	if err := temp.Chmod(ledgerFileMode); err != nil {
		temp.Close()
		return fmt.Errorf("quota ledger: set permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("quota ledger: write record: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("quota ledger: close record: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("quota ledger: replace record: %w", err)
	}
	return nil
}

// Prune removes ledger records older than the current Pacific day.
//
// keepDays of zero or less keeps only today, which is all the meter needs; a
// larger window exists so a user can look back at what a long stream cost.
func (s *FileLedgerStore) Prune(ctx context.Context, keepDays int) error {
	if strings.TrimSpace(s.root) == "" {
		return nil
	}
	if keepDays < 0 {
		keepDays = 0
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("quota ledger: read directory: %w", err)
	}

	oldest := time.Now().In(pacificLocation()).AddDate(0, 0, -keepDays).Format("2006-01-02")
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		day, ok := ledgerDayFromName(entry.Name())
		if !ok || day >= oldest {
			continue
		}
		_ = os.Remove(filepath.Join(s.root, entry.Name()))
	}
	return nil
}

// recordPath builds the validated path for one record.
func (s *FileLedgerStore) recordPath(fingerprint, day string) (string, error) {
	if strings.TrimSpace(s.root) == "" {
		return "", fmt.Errorf("quota ledger: no root directory: %w", ErrLedgerUnsafeKey)
	}
	if !ledgerDayPattern.MatchString(day) {
		return "", fmt.Errorf("quota ledger: bad day key: %w", ErrLedgerUnsafeKey)
	}
	key := normalizeFingerprint(fingerprint)
	if key != anonymousFingerprint && !ledgerFingerprintPattern.MatchString(key) {
		return "", fmt.Errorf("quota ledger: bad fingerprint key: %w", ErrLedgerUnsafeKey)
	}
	return filepath.Join(s.root, key+"-"+day+".json"), nil
}

// ledgerDayFromName recovers the day from a record filename.
func ledgerDayFromName(name string) (string, bool) {
	base := strings.TrimSuffix(name, ".json")
	if len(base) < len("0000-00-00") {
		return "", false
	}
	day := base[len(base)-len("0000-00-00"):]
	if !ledgerDayPattern.MatchString(day) {
		return "", false
	}
	return day, true
}

// normalizeFingerprint maps an empty fingerprint onto the anonymous key.
func normalizeFingerprint(fingerprint string) string {
	trimmed := strings.ToLower(strings.TrimSpace(fingerprint))
	if trimmed == "" {
		return anonymousFingerprint
	}
	return trimmed
}

// CredentialFingerprint returns a stable, non-reversible key for a credential.
// It must be a hash: the fingerprint appears in a filename, and a filename is
// not a place a credential may ever appear.
func CredentialFingerprint(clientID, accountID string) string {
	clientID = strings.TrimSpace(clientID)
	accountID = strings.TrimSpace(accountID)
	if clientID == "" && accountID == "" {
		return anonymousFingerprint
	}
	// The separator is a byte that cannot appear in either input, so two
	// different pairs cannot hash to the same preimage by concatenation.
	sum := sha256.Sum256([]byte(clientID + "\x00" + accountID))
	return hex.EncodeToString(sum[:])[:16]
}
