package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// offsetStore persists the highest update_id we've handed off to the
// broker's per-route workers. On restart, the pollLoop seeds its `offset`
// from this so Telegram doesn't redeliver updates we already accepted.
//
// Note on the durability boundary: we persist after dispatch (host.Emit),
// not after the worker forwards to a CLI. Worst case on a crash mid-flush:
// we lose the messages currently sitting in worker queues but Telegram
// won't redeliver them either. That's the same exposure as today;
// persisting offset here only fixes the "broker restart re-runs the last
// 24h of updates" footgun. A stronger guarantee (per-update completion
// tracking) is OpenClaw's bot-update-tracker design — bigger change,
// deferred.
//
// File: $XDG_STATE_HOME/c3/<channel>-offset.json (mode 0600).
type offsetStore struct {
	path string
	mu   sync.Mutex
}

// newOffsetStore returns a store keyed by channel name. The directory is
// created on first use.
func newOffsetStore(channelName string) (*offsetStore, error) {
	dir := xdgStateHomeC3()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("offsetStore: mkdir %s: %w", dir, err)
	}
	return &offsetStore{
		path: filepath.Join(dir, fmt.Sprintf("%s-offset.json", channelName)),
	}, nil
}

type offsetRecord struct {
	HighestCompletedUpdateID int64 `json:"highest_completed_update_id"`
}

// Load reads the persisted offset; returns 0 (and no error) if the file
// doesn't exist yet.
func (s *offsetStore) Load() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("offsetStore: read %s: %w", s.path, err)
	}
	var rec offsetRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return 0, fmt.Errorf("offsetStore: parse %s: %w", s.path, err)
	}
	return rec.HighestCompletedUpdateID, nil
}

// Save atomically writes offset to disk. Idempotent; safe across restarts.
func (s *offsetStore) Save(offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(offsetRecord{HighestCompletedUpdateID: offset})
	if err != nil {
		return fmt.Errorf("offsetStore: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("offsetStore: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("offsetStore: rename %s → %s: %w", tmp, s.path, err)
	}
	return nil
}

// xdgStateHomeC3 returns $XDG_STATE_HOME/c3 (or ~/.local/state/c3 fallback).
func xdgStateHomeC3() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "c3")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "c3")
}
