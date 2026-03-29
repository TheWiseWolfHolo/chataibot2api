package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type AccountStore interface {
	Load() ([]*Account, error)
	Save(accounts []*Account) error
	Path() string
}

type fileAccountStore struct {
	path string
	mu   sync.Mutex
}

type persistedAccountsSnapshot struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updated_at"`
	Accounts  []persistedAccount `json:"accounts"`
}

type persistedAccount struct {
	JWT   string `json:"jwt"`
	Quota int    `json:"quota"`
}

func NewFileAccountStore(path string) AccountStore {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &fileAccountStore{path: path}
}

func (s *fileAccountStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *fileAccountStore) Load() ([]*Account, error) {
	if s == nil {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var snapshot persistedAccountsSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}

	accounts := make([]*Account, 0, len(snapshot.Accounts))
	seen := make(map[string]struct{}, len(snapshot.Accounts))
	for _, entry := range snapshot.Accounts {
		jwt := strings.TrimSpace(entry.JWT)
		if jwt == "" {
			continue
		}
		if _, ok := seen[jwt]; ok {
			continue
		}
		seen[jwt] = struct{}{}

		quota := entry.Quota
		if quota < 2 {
			continue
		}
		accounts = append(accounts, &Account{
			JWT:   jwt,
			Quota: quota,
		})
	}

	return accounts, nil
}

func (s *fileAccountStore) Save(accounts []*Account) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries := make([]persistedAccount, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		jwt := strings.TrimSpace(acc.JWT)
		if jwt == "" || acc.Quota < 2 {
			continue
		}
		if _, ok := seen[jwt]; ok {
			continue
		}
		seen[jwt] = struct{}{}
		entries = append(entries, persistedAccount{
			JWT:   jwt,
			Quota: acc.Quota,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Quota == entries[j].Quota {
			return entries[i].JWT < entries[j].JWT
		}
		return entries[i].Quota > entries[j].Quota
	})

	snapshot := persistedAccountsSnapshot{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Accounts:  entries,
	}
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, s.path)
}
