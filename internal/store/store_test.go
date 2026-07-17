package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSeenPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now()
	s.MarkSeen("binance:1", now)
	s.MarkInitialized("binance:api-updates")
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !s2.IsSeen("binance:1") {
		t.Errorf("expected key to persist")
	}
	if !s2.IsInitialized("binance:api-updates") {
		t.Errorf("expected initialized flag to persist")
	}
	if s2.IsSeen("binance:2") {
		t.Errorf("did not expect unknown key")
	}
}

func TestFlushNoopWhenClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	// Flushing an empty, never-dirtied store should not error or create a file.
	if err := s.Flush(); err != nil {
		t.Fatalf("flush clean: %v", err)
	}
}

func TestPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	old := time.Now().Add(-100 * 24 * time.Hour)
	recent := time.Now()
	s.MarkSeen("old", old)
	s.MarkSeen("recent", recent)

	removed := s.Prune(60*24*time.Hour, time.Now())
	if removed != 1 {
		t.Fatalf("want 1 pruned, got %d", removed)
	}
	if s.IsSeen("old") {
		t.Errorf("old key should be pruned")
	}
	if !s.IsSeen("recent") {
		t.Errorf("recent key should survive")
	}
}

func TestOpenCorruptFileStartsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil {
		t.Fatalf("expected a non-nil (informational) error for corrupt file")
	}
	if s == nil {
		t.Fatalf("expected a usable store even on corrupt file")
	}
	// Must be usable.
	s.MarkSeen("x", time.Now())
	if !s.IsSeen("x") {
		t.Errorf("store should be usable after corrupt-file recovery")
	}
}
