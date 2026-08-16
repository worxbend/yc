package storagetest

import (
	"context"
	"sync"

	"github.com/worxbend/yc/internal/storage"
)

// MemoryCredentialStore is an in-memory store for tests. It never touches disk.
type MemoryCredentialStore struct {
	mu        sync.RWMutex
	record    storage.CredentialRecord
	loaded    bool
	loadErr   error
	saveErr   error
	deleteErr error
	saves     []storage.CredentialRecord
	deletes   int
}

var _ storage.CredentialStore = (*MemoryCredentialStore)(nil)

// NewMemoryCredentialStore returns an empty in-memory store.
func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{}
}

// SetCredentials seeds the store with a record.
func (s *MemoryCredentialStore) SetCredentials(record storage.CredentialRecord) {
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
func (s *MemoryCredentialStore) SavedRecords() []storage.CredentialRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.saves) == 0 {
		return nil
	}
	records := make([]storage.CredentialRecord, len(s.saves))
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
func (s *MemoryCredentialStore) LoadCredentials(ctx context.Context) (storage.CredentialRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return storage.CredentialRecord{}, false, err
	}
	if s == nil {
		return storage.CredentialRecord{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.loadErr != nil {
		return storage.CredentialRecord{}, false, s.loadErr
	}
	return s.record.Clone(), s.loaded, nil
}

// SaveCredentials stores a copy of the record.
func (s *MemoryCredentialStore) SaveCredentials(ctx context.Context, record storage.CredentialRecord) error {
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
	s.record = storage.CredentialRecord{}
	s.loaded = false
	s.deletes++
	return nil
}
