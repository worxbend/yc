package storagetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/worxbend/yc/internal/auth"
	"github.com/worxbend/yc/internal/storage"
)

const testAccessToken = auth.FakeTokenMarker + "-access"

var testUpdatedAt = time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)

func testRecord() storage.CredentialRecord {
	return storage.CredentialRecord{
		Version:     storage.CredentialRecordVersion,
		ClientID:    "yc-test-client.apps.googleusercontent.com",
		AccessToken: auth.NewSecret(testAccessToken),
		TokenType:   "Bearer",
		ExpiresAt:   testUpdatedAt.Add(time.Hour),
		Scopes:      auth.LoginScopes(),
		ChannelID:   "UCtest",
		DisplayName: "yc test channel",
		UpdatedAt:   testUpdatedAt,
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

// SetCredentials is the test seam the whole suite depends on, so its own
// behavior - a deep copy, marked loaded - is pinned rather than assumed.
func TestMemoryCredentialStoreSetCredentialsCopiesDeeply(t *testing.T) {
	store := NewMemoryCredentialStore()
	record := storage.CredentialRecord{
		AccessToken: auth.NewSecret(testAccessToken),
		Scopes:      auth.LoginScopes(),
		ChannelID:   "UC123",
	}
	store.SetCredentials(record)

	// Mutating the caller's copy must not reach the store.
	record.Scopes[0] = auth.Scope("mutated")
	record.ChannelID = "UC-changed"

	got, ok, err := store.LoadCredentials(context.Background())
	if err != nil || !ok {
		t.Fatalf("LoadCredentials: ok=%v err=%v", ok, err)
	}
	if got.ChannelID != "UC123" {
		t.Errorf("channel ID = %q, want the seeded value", got.ChannelID)
	}
	if string(got.Scopes[0]) == "mutated" {
		t.Error("SetCredentials shared the caller's scope slice")
	}
}
