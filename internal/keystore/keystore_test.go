package keystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAbsentIsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "keys.json"))
	if err != nil {
		t.Fatalf("Open absent: %v", err)
	}
	if s.Count() != 0 {
		t.Errorf("absent file should yield empty store, got %d", s.Count())
	}
}

func TestOpenMalformedIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("malformed file should be a hard error, not silent data loss")
	}
}

func TestMergePersistsAndDedupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	s, _ := Open(path)

	n, err := s.Merge(map[string]string{"aa": "11", "bb": "22"})
	if err != nil || n != 2 {
		t.Fatalf("first merge: n=%d err=%v, want 2/nil", n, err)
	}
	// Re-adding the same pairs changes nothing and must not rewrite.
	if n, _ := s.Merge(map[string]string{"aa": "11"}); n != 0 {
		t.Errorf("re-merge identical: n=%d, want 0", n)
	}
	// Overwriting an existing KID counts as a change.
	if n, _ := s.Merge(map[string]string{"aa": "99"}); n != 1 {
		t.Errorf("overwrite: n=%d, want 1", n)
	}
	// Blank kid/key are skipped; nil is a no-op.
	if n, _ := s.Merge(map[string]string{"": "x", "cc": ""}); n != 0 {
		t.Errorf("blank pairs: n=%d, want 0", n)
	}
	if n, _ := s.Merge(nil); n != 0 {
		t.Errorf("nil merge: n=%d, want 0", n)
	}

	// All returns a copy — mutating it must not touch the store.
	got := s.All()
	got["aa"] = "tampered"
	if s.All()["aa"] != "99" {
		t.Error("All() must return a defensive copy")
	}

	// Reopening from disk recovers the persisted state.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Count() != 2 || s2.All()["aa"] != "99" || s2.All()["bb"] != "22" {
		t.Errorf("persisted state wrong after reopen: %+v", s2.All())
	}
}
