package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
)

const (
	testAccessToken  = auth.FakeTokenMarker + "-access"
	testRefreshToken = auth.FakeTokenMarker + "-refresh"
	testAPIKey       = auth.FakeTokenMarker + "-api-key"
)

var testUpdatedAt = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

func testRecord() CredentialRecord {
	return CredentialRecord{
		Version:      CredentialRecordVersion,
		ClientID:     "yc-test-client.apps.googleusercontent.com",
		AccessToken:  auth.NewSecret(testAccessToken),
		RefreshToken: auth.NewSecret(testRefreshToken),
		APIKey:       auth.NewSecret(testAPIKey),
		TokenType:    "Bearer",
		ExpiresAt:    testUpdatedAt.Add(time.Hour),
		Scopes:       auth.LoginScopes(),
		ChannelID:    "UCtest",
		DisplayName:  "yc test channel",
		UpdatedAt:    testUpdatedAt,
	}
}

// newTestStore builds a store under a temp directory, skipping on platforms
// where the hardened backend is deliberately unavailable.
func newTestStore(t *testing.T) (*CredentialFileStore, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the hardened credential file is a Unix-only backend")
	}

	path := filepath.Join(t.TempDir(), "yc", "credentials.json")
	plan, err := NewCredentialFilePlan(path)
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	store, err := NewCredentialFileStore(plan)
	if err != nil {
		t.Fatalf("NewCredentialFileStore: %v", err)
	}
	return store, path
}

func TestCredentialFileRoundTrip(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	if _, ok, err := store.LoadCredentials(ctx); err != nil || ok {
		t.Fatalf("a missing file must load as (zero, false, nil), got ok=%v err=%v", ok, err)
	}

	want := testRecord()
	if err := store.SaveCredentials(ctx, want); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	got, ok, err := store.LoadCredentials(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadCredentials: ok=%v err=%v", ok, err)
	}
	if got.AccessToken.Reveal() != testAccessToken ||
		got.RefreshToken.Reveal() != testRefreshToken ||
		got.APIKey.Reveal() != testAPIKey {
		t.Fatal("credential values did not survive the round trip")
	}
	if got.ClientID != want.ClientID || got.ChannelID != want.ChannelID || got.DisplayName != want.DisplayName {
		t.Fatalf("identity did not survive the round trip: %+v", got)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("timestamps did not survive the round trip: %v / %v", got.ExpiresAt, got.UpdatedAt)
	}
	if len(auth.MissingScopes(got.Scopes, auth.LoginScopes())) != 0 {
		t.Fatalf("scopes did not survive the round trip: %v", got.Scopes)
	}
}

// TestCredentialFileModesAreExact is the permission invariant: 0700 directory,
// 0600 file, no exceptions and no "at most" reading.
func TestCredentialFileModesAreExact(t *testing.T) {
	store, path := newTestStore(t)
	if err := store.SaveCredentials(context.Background(), testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	fileInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != CredentialFileMode {
		t.Fatalf("credential file mode = %s, want %s", got, CredentialFileMode)
	}

	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat credential directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != CredentialDirMode {
		t.Fatalf("credential directory mode = %s, want %s", got, CredentialDirMode)
	}
}

// TestCredentialFileRejectsLoosePermissions checks that yc reports rather than
// repairs: quietly tightening a world-readable file would hide the fact that
// something else already had time to read it.
func TestCredentialFileRejectsLoosePermissions(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	if err := store.SaveCredentials(ctx, testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod credential file: %v", err)
	}
	_, _, err := store.LoadCredentials(ctx)
	if !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("expected ErrCredentialsInsecure for a 0644 file, got %v", err)
	}
	assertRedacted(t, err)

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("restore credential file mode: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("chmod credential directory: %v", err)
	}
	if _, _, err := store.LoadCredentials(ctx); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("expected ErrCredentialsInsecure for a 0755 directory, got %v", err)
	}
}

// TestCredentialFileRejectsSymlink covers the classic attack: point the
// credential path at someone else's file and let yc read or overwrite it.
func TestCredentialFileRejectsSymlink(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("create credential directory: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("chmod credential directory: %v", err)
	}

	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"google":{}}`), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	if _, _, err := store.LoadCredentials(ctx); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("load through a symlink must be refused, got %v", err)
	}
	if err := store.SaveCredentials(ctx, testRecord()); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("save over a symlink must be refused, got %v", err)
	}
	if err := store.DeleteCredentials(ctx); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("delete of a symlink must be refused, got %v", err)
	}

	// The symlink target must be untouched: no read-through, no clobber.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(data) != `{"version":1,"google":{}}` {
		t.Fatalf("the symlink target was modified: %s", data)
	}
}

func TestCredentialDirectorySymlinkIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hardened credential file is a Unix-only backend")
	}

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, CredentialDirMode); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "credentials.json"), []byte(`{"version":1,"google":{}}`), CredentialFileMode); err != nil {
		t.Fatalf("write credential file: %v", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	plan, err := NewCredentialFilePlan(filepath.Join(link, "credentials.json"))
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	store, err := NewCredentialFileStore(plan)
	if err != nil {
		t.Fatalf("NewCredentialFileStore: %v", err)
	}
	if _, _, err := store.LoadCredentials(context.Background()); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("a symlinked credential directory must be refused, got %v", err)
	}
}

func TestCredentialFileRejectsNonRegularFile(t *testing.T) {
	store, path := newTestStore(t)
	if err := os.MkdirAll(path, CredentialDirMode); err != nil {
		t.Fatalf("create directory at the credential path: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("chmod credential directory: %v", err)
	}
	if _, _, err := store.LoadCredentials(context.Background()); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("a directory at the credential path must be refused, got %v", err)
	}
}

func TestCredentialFileMalformedNeverEchoesContents(t *testing.T) {
	store, path := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("create credential directory: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), CredentialDirMode); err != nil {
		t.Fatalf("chmod credential directory: %v", err)
	}

	broken := `{"version":1,"google":{"access_token":"` + testAccessToken + `"` // deliberately truncated
	if err := os.WriteFile(path, []byte(broken), CredentialFileMode); err != nil {
		t.Fatalf("write malformed credential file: %v", err)
	}

	_, _, err := store.LoadCredentials(context.Background())
	if !errors.Is(err, ErrCredentialsMalformed) {
		t.Fatalf("expected ErrCredentialsMalformed, got %v", err)
	}
	assertRedacted(t, err)
}

func TestParseCredentialFileRejectsBadPayloads(t *testing.T) {
	tests := map[string]struct {
		data string
		want error
	}{
		"empty":           {data: "", want: ErrCredentialsMalformed},
		"not json":        {data: "not json at all", want: ErrCredentialsMalformed},
		"unknown field":   {data: `{"version":1,"google":{},"surprise":true}`, want: ErrCredentialsMalformed},
		"trailing data":   {data: `{"version":1,"google":{}}{"version":1}`, want: ErrCredentialsMalformed},
		"future version":  {data: `{"version":99,"google":{}}`, want: ErrCredentialsUnsupportedVersion},
		"bad expiry time": {data: `{"version":1,"google":{"expires_at":"yesterday"}}`, want: ErrCredentialsMalformed},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCredentialFile([]byte(test.data)); !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
		})
	}
}

// TestMarshalCredentialFileIsTheOnlyRevealPath documents the asymmetry: the
// file format contains raw tokens on purpose, while the record itself never
// formats them.
func TestMarshalCredentialFileIsTheOnlyRevealPath(t *testing.T) {
	record := testRecord()

	encoded, err := MarshalCredentialFile(record)
	if err != nil {
		t.Fatalf("MarshalCredentialFile: %v", err)
	}
	if !strings.Contains(string(encoded), testAccessToken) {
		t.Fatal("the credential file must contain the real token; it is the deliberate reveal path")
	}

	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		rendered := fmt.Sprintf(format, record)
		if strings.Contains(rendered, auth.FakeTokenMarker) {
			t.Fatalf("record formatted with %s leaked a credential: %s", format, rendered)
		}
	}
	structured, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if strings.Contains(string(structured), auth.FakeTokenMarker) {
		t.Fatalf("default json encoding of the record leaked a credential: %s", structured)
	}
}

func TestCredentialFileAtomicReplacementLeavesNoTemp(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()

	for i := range 3 {
		record := testRecord()
		record.DisplayName = fmt.Sprintf("channel-%d", i)
		if err := store.SaveCredentials(ctx, record); err != nil {
			t.Fatalf("SaveCredentials: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read credential directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("atomic replace left extra files behind: %v", names)
	}

	got, _, err := store.LoadCredentials(ctx)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got.DisplayName != "channel-2" {
		t.Fatalf("the last write did not win: %q", got.DisplayName)
	}
}

func TestCredentialFileDelete(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()

	// Deleting nothing is not an error.
	if err := store.DeleteCredentials(ctx); err != nil {
		t.Fatalf("DeleteCredentials on a missing file: %v", err)
	}
	if err := store.SaveCredentials(ctx, testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := store.DeleteCredentials(ctx); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the credential file survived delete: %v", err)
	}
	if _, ok, err := store.LoadCredentials(ctx); err != nil || ok {
		t.Fatalf("after delete the store must be empty, got ok=%v err=%v", ok, err)
	}
}

func TestCredentialStoreHonorsContextCancellation(t *testing.T) {
	store, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := store.LoadCredentials(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadCredentials: expected context.Canceled, got %v", err)
	}
	if err := store.SaveCredentials(ctx, testRecord()); !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveCredentials: expected context.Canceled, got %v", err)
	}
	if err := store.DeleteCredentials(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteCredentials: expected context.Canceled, got %v", err)
	}
}

func TestCredentialFilePlanValidation(t *testing.T) {
	plan, err := NewCredentialFilePlan(filepath.Join(t.TempDir(), "creds.json"))
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	if plan.DirMode != CredentialDirMode || plan.Mode != CredentialFileMode {
		t.Fatalf("plan modes are wrong: %+v", plan)
	}

	loose := plan
	loose.Mode = 0o644
	if err := loose.Validate(); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("a loose file mode must be rejected, got %v", err)
	}
	loose = plan
	loose.DirMode = 0o777
	if err := loose.Validate(); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("a loose directory mode must be rejected, got %v", err)
	}
	setuid := plan
	setuid.Mode = CredentialFileMode | fs.ModeSetuid
	if err := setuid.Validate(); !errors.Is(err, ErrCredentialsInsecure) {
		t.Fatalf("special mode bits must be rejected, got %v", err)
	}

	empty := CredentialFilePlan{DirMode: CredentialDirMode, Mode: CredentialFileMode}
	if err := empty.Validate(); err == nil {
		t.Fatal("an empty path must be rejected")
	}
}

func TestCredentialRecordFromLoginResult(t *testing.T) {
	result := auth.LoginResult{
		Identity: auth.Identity{ChannelID: "UCtest", DisplayName: "yc test channel", Handle: "@yctest"},
		Tokens: auth.TokenSet{
			AccessToken:  auth.NewSecret(testAccessToken),
			RefreshToken: auth.NewSecret(testRefreshToken),
			TokenType:    "Bearer",
			ExpiresAt:    testUpdatedAt.Add(time.Hour),
			Scopes:       auth.LoginScopes(),
		},
	}

	record := CredentialRecordFromLoginResult(result, " client-id ", testUpdatedAt)
	if record.Version != CredentialRecordVersion {
		t.Fatalf("version = %d", record.Version)
	}
	if record.ClientID != "client-id" {
		t.Fatalf("client ID was not trimmed: %q", record.ClientID)
	}
	if record.ChannelID != "UCtest" || record.DisplayName != "yc test channel" {
		t.Fatalf("identity was not carried: %+v", record)
	}
	// Scopes fall back to the token set when the result did not carry them.
	if len(auth.MissingScopes(record.Scopes, auth.LoginScopes())) != 0 {
		t.Fatalf("scopes were not carried: %v", record.Scopes)
	}
	if !record.Credentials().CanModerate() {
		t.Fatal("a force-ssl record must be able to moderate")
	}
	if record.Empty() {
		t.Fatal("a record with tokens must not read as empty")
	}
}

func TestCredentialRecordCloneIsDeep(t *testing.T) {
	record := testRecord()
	clone := record.Clone()
	clone.Scopes[0] = auth.Scope("mutated")

	if record.Scopes[0] == auth.Scope("mutated") {
		t.Fatal("Clone shared the scope slice with the original")
	}
}

func TestMemoryCredentialStore(t *testing.T) {
	store := NewMemoryCredentialStore()
	ctx := context.Background()

	if _, ok, err := store.LoadCredentials(ctx); err != nil || ok {
		t.Fatalf("an empty store must load as (zero, false, nil), got ok=%v err=%v", ok, err)
	}
	if err := store.SaveCredentials(ctx, testRecord()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	got, ok, err := store.LoadCredentials(ctx)
	if err != nil || !ok {
		t.Fatalf("LoadCredentials: ok=%v err=%v", ok, err)
	}
	got.Scopes[0] = auth.Scope("mutated")
	again, _, err := store.LoadCredentials(ctx)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if again.Scopes[0] == auth.Scope("mutated") {
		t.Fatal("the store handed out a record sharing its own slice")
	}

	if got := len(store.SavedRecords()); got != 1 {
		t.Fatalf("SavedRecords = %d, want 1", got)
	}
	if err := store.DeleteCredentials(ctx); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if store.DeleteCount() != 1 {
		t.Fatalf("DeleteCount = %d, want 1", store.DeleteCount())
	}
	if _, ok, _ := store.LoadCredentials(ctx); ok {
		t.Fatal("delete did not clear the store")
	}

	sentinel := errors.New("boom")
	store.SetErrors(sentinel, sentinel, sentinel)
	if _, _, err := store.LoadCredentials(ctx); !errors.Is(err, sentinel) {
		t.Fatalf("load error was not honored: %v", err)
	}
	if err := store.SaveCredentials(ctx, testRecord()); !errors.Is(err, sentinel) {
		t.Fatalf("save error was not honored: %v", err)
	}
	if err := store.DeleteCredentials(ctx); !errors.Is(err, sentinel) {
		t.Fatalf("delete error was not honored: %v", err)
	}
}

func TestUnsupportedCredentialStore(t *testing.T) {
	var store UnsupportedCredentialStore
	ctx := context.Background()

	if _, _, err := store.LoadCredentials(ctx); !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
	if err := store.SaveCredentials(ctx, testRecord()); !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
	err := store.DeleteCredentials(ctx)
	if !errors.Is(err, ErrCredentialsUnsupported) {
		t.Fatalf("expected ErrCredentialsUnsupported, got %v", err)
	}
	// The sentinel must tell the user what to do instead.
	if !strings.Contains(err.Error(), "YC_GOOGLE_ACCESS_TOKEN") {
		t.Fatalf("the unsupported sentinel should name the workaround: %v", err)
	}
}

func TestStoreLocationIsRedacted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hardened credential file is a Unix-only backend")
	}

	// A path can carry a credential-shaped component when a user points
	// --config somewhere unfortunate; diagnostics must not repeat it.
	path := filepath.Join(t.TempDir(), "access_token=AIzaSyA1234567890abcdefghijklmnopqrstuv", "credentials.json")
	plan, err := NewCredentialFilePlan(path)
	if err != nil {
		t.Fatalf("NewCredentialFilePlan: %v", err)
	}
	store, err := NewCredentialFileStore(plan)
	if err != nil {
		t.Fatalf("NewCredentialFileStore: %v", err)
	}
	if location := store.StoreLocation(); strings.Contains(location, "AIzaSyA1234567890abcdefghijklmnopqrstuv") {
		t.Fatalf("StoreLocation leaked a key-shaped path component: %s", location)
	}
	if store.StoreLabel() != "credential file" {
		t.Fatalf("unexpected store label %q", store.StoreLabel())
	}
}

// assertRedacted fails when an error message carries any test credential.
func assertRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		rendered := fmt.Sprintf(format, err)
		if strings.Contains(rendered, auth.FakeTokenMarker) {
			t.Fatalf("error formatted with %s leaked a credential: %s", format, rendered)
		}
	}
}
