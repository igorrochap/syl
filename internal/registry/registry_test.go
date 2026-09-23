package registry_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/registry"
)

func TestUpsertNormalizesProjectAndPreservesEntryShape(t *testing.T) {
	projectRoot := t.TempDir()
	symlink := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(projectRoot, symlink); err != nil {
		t.Fatal(err)
	}
	sylHome := t.TempDir()

	if err := registry.Upsert(sylHome, symlink); err != nil {
		t.Fatal(err)
	}

	entries := readEntries(t, registry.Path(sylHome))
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if len(entry) != 3 {
		t.Fatalf("entry fields = %#v, want exactly path, first_seen, last_seen", entry)
	}
	expectedPath, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if string(entry["path"]) != `"`+expectedPath+`"` {
		t.Fatalf("path = %s, want %q", entry["path"], expectedPath)
	}
	for _, field := range []string{"first_seen", "last_seen"} {
		var timestamp time.Time
		if err := json.Unmarshal(entry[field], &timestamp); err != nil {
			t.Fatalf("%s = %s: %v", field, entry[field], err)
		}
		if timestamp.IsZero() {
			t.Fatalf("%s is zero", field)
		}
	}
}

func TestUpsertKeepsFirstSeenAndUpdatesLastSeen(t *testing.T) {
	projectRoot := t.TempDir()
	sylHome := t.TempDir()

	if err := registry.Upsert(sylHome, projectRoot); err != nil {
		t.Fatal(err)
	}
	first := readEntries(t, registry.Path(sylHome))[0]
	time.Sleep(time.Millisecond)
	if err := registry.Upsert(sylHome, projectRoot); err != nil {
		t.Fatal(err)
	}
	second := readEntries(t, registry.Path(sylHome))[0]

	if string(first["first_seen"]) != string(second["first_seen"]) {
		t.Fatalf("first_seen changed from %s to %s", first["first_seen"], second["first_seen"])
	}
	if string(first["last_seen"]) == string(second["last_seen"]) {
		t.Fatalf("last_seen = %s, want an update", second["last_seen"])
	}
}

func TestUpsertRecoversFromCorruptRegistry(t *testing.T) {
	sylHome := t.TempDir()
	path := registry.Path(sylHome)
	contents := []byte("not json\n")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	projectRoot := t.TempDir()
	if err := registry.Upsert(sylHome, projectRoot); err == nil {
		t.Fatal("Upsert() error = nil, want corrupt registry error after repair")
	}
	expectedPath, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	entries := readEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want one repaired entry", len(entries))
	}
	if string(entries[0]["path"]) != `"`+expectedPath+`"` {
		t.Fatalf("path = %s, want %q", entries[0]["path"], expectedPath)
	}
}

func TestUpsertPreservesValidEntriesWhenRegistryEntryIsMalformed(t *testing.T) {
	sylHome := t.TempDir()
	preservedRoot := t.TempDir()
	newRoot := t.TempDir()
	contents := `[
  {"path": "` + preservedRoot + `", "first_seen": "2026-01-01T00:00:00Z", "last_seen": "2026-01-02T00:00:00Z"},
  {"path": 42}
]
`
	if err := os.WriteFile(registry.Path(sylHome), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := registry.Upsert(sylHome, newRoot); err == nil {
		t.Fatal("Upsert() error = nil, want malformed-entry error after repair")
	}
	expectedNewPath, err := filepath.EvalSymlinks(newRoot)
	if err != nil {
		t.Fatal(err)
	}

	entries := readEntries(t, registry.Path(sylHome))
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want preserved and new entries", len(entries))
	}
	paths := map[string]bool{}
	for _, entry := range entries {
		paths[string(entry["path"])] = true
	}
	if !paths[`"`+preservedRoot+`"`] || !paths[`"`+expectedNewPath+`"`] {
		t.Fatalf("paths = %#v, want %q and %q", paths, preservedRoot, expectedNewPath)
	}
}

func TestUpsertReportsSylHomeWriteFailure(t *testing.T) {
	sylHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(sylHome, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := registry.Upsert(sylHome, t.TempDir()); err == nil {
		t.Fatal("Upsert() error = nil, want write error")
	}
}

func readEntries(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(contents, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}
