// Package store persists the set of announcements we have already delivered so
// that a restart never re-sends old news. It is a small, dependency-free JSON
// file written atomically (temp file + rename), guarded by a mutex.
//
// The workload is tiny (a set of string keys + a few per-source flags), so a
// flat file loaded into memory is more than adequate and avoids any embedded
// database dependency — which keeps the Docker image trivial and the build
// hermetic.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store is a concurrency-safe persistent set of delivered announcement keys.
type Store struct {
	path string

	mu    sync.Mutex
	state state
	dirty bool
}

type state struct {
	// Seen maps DedupKey -> first-seen unix seconds. The timestamp lets us
	// prune very old entries so the file cannot grow unbounded.
	Seen map[string]int64 `json:"seen"`
	// Initialized marks a source name as having completed its first poll, so
	// we only backfill history once.
	Initialized map[string]bool `json:"initialized"`
}

// Open loads the store from path, creating parent directories if needed. A
// missing or empty file yields an empty store (not an error).
func Open(path string) (*Store, error) {
	s := &Store{
		path: path,
		state: state{
			Seen:        map[string]int64{},
			Initialized: map[string]bool{},
		},
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		// A corrupt state file must not crash the bot; log-and-reset is safer
		// than refusing to start. The caller logs the returned error context.
		return s, fmt.Errorf("state file corrupt, starting fresh: %w", err)
	}
	if st.Seen == nil {
		st.Seen = map[string]int64{}
	}
	if st.Initialized == nil {
		st.Initialized = map[string]bool{}
	}
	s.state = st
	return s, nil
}

// IsSeen reports whether key has already been delivered.
func (s *Store) IsSeen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.state.Seen[key]
	return ok
}

// MarkSeen records key as delivered (at time now). It marks the store dirty;
// call Flush (or rely on the periodic flusher) to persist.
func (s *Store) MarkSeen(key string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.Seen[key]; !ok {
		s.state.Seen[key] = now.Unix()
		s.dirty = true
	}
}

// IsInitialized reports whether the named source has completed a first poll.
func (s *Store) IsInitialized(source string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Initialized[source]
}

// MarkInitialized flags a source as having completed its first poll.
func (s *Store) MarkInitialized(source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Initialized[source] {
		s.state.Initialized[source] = true
		s.dirty = true
	}
}

// Prune removes seen entries older than maxAge to bound file growth.
func (s *Store) Prune(maxAge time.Duration, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-maxAge).Unix()
	removed := 0
	for k, ts := range s.state.Seen {
		if ts < cutoff {
			delete(s.state.Seen, k)
			removed++
		}
	}
	if removed > 0 {
		s.dirty = true
	}
	return removed
}

// Flush atomically writes the state to disk if it has changed since the last
// flush. Writing to a temp file and renaming guarantees the on-disk file is
// never left half-written even if the process is killed mid-write.
func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("marshal state: %w", err)
	}
	s.dirty = false
	s.mu.Unlock()

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}

// Count returns the number of remembered keys (used for logging/health).
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.state.Seen)
}
