package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderFindsSessionAndReadsEntriesInSequence(t *testing.T) {
	reader := newFixtureReader(t)

	path, err := reader.Find("/workspace/project", "parent-session")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}

	entries, err := reader.Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := entryIDs(t, entries); !equalStrings(got, []string{"parent-first", "parent-second"}) {
		t.Fatalf("entry IDs = %v, want transcript order", got)
	}
	if entries[0].Line != 1 || entries[1].Line != 3 {
		t.Fatalf("entry lines = [%d %d], want physical lines [1 3]", entries[0].Line, entries[1].Line)
	}
}

func TestReaderFindsSubagentTranscriptsInStableOrder(t *testing.T) {
	reader := newFixtureReader(t)

	paths, err := reader.FindSubagents("/workspace/project", "parent-session")
	if err != nil {
		t.Fatalf("FindSubagents() error = %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("FindSubagents() paths = %v, want two transcripts", paths)
	}

	var got []string
	for _, path := range paths {
		entries, readErr := reader.Read(path)
		if readErr != nil {
			t.Fatalf("Read(%q) error = %v", path, readErr)
		}
		got = append(got, entryIDs(t, entries)...)
	}
	if !equalStrings(got, []string{"agent-a", "agent-b"}) {
		t.Fatalf("subagent entry IDs = %v, want path order", got)
	}
}

func TestReaderReportsMalformedTranscriptEntry(t *testing.T) {
	reader := newFixtureReader(t)

	path, err := reader.Find("/workspace/project", "malformed-session")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	_, err = reader.Read(path)
	if !errors.Is(err, ErrParse) {
		t.Fatalf("Read() error = %v, want ErrParse", err)
	}
}

func TestReaderReportsMissingTranscriptWhenRead(t *testing.T) {
	reader := newFixtureReader(t)

	path, err := reader.Find("/workspace/project", "missing-session")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	_, err = reader.Read(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() error = %v, want os.ErrNotExist", err)
	}
}

func newFixtureReader(t *testing.T) Reader {
	t.Helper()
	reader := New(t.TempDir())
	parentPath, err := reader.Find("/workspace/project", "parent-session")
	if err != nil {
		t.Fatalf("find parent transcript path: %v", err)
	}
	malformedPath, err := reader.Find("/workspace/project", "malformed-session")
	if err != nil {
		t.Fatalf("find malformed transcript path: %v", err)
	}

	writeFixture(t, filepath.Join("testdata", "parent-session.jsonl"), parentPath)
	writeFixture(t, filepath.Join("testdata", "malformed-session.jsonl"), malformedPath)
	subagentsDir := filepath.Join(filepath.Dir(parentPath), "parent-session", "subagents")
	writeFixture(
		t,
		filepath.Join("testdata", "subagents", "agent-a.jsonl"),
		filepath.Join(subagentsDir, "agent-a.jsonl"),
	)
	writeFixture(
		t,
		filepath.Join("testdata", "subagents", "agent-b.jsonl"),
		filepath.Join(subagentsDir, "agent-b.jsonl"),
	)
	return reader
}

func writeFixture(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture %q: %v", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(destination, contents, 0o644); err != nil {
		t.Fatalf("write fixture %q: %v", destination, err)
	}
}

func entryIDs(t *testing.T, entries []Entry) []string {
	t.Helper()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		var record struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(entry.Raw, &record); err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		ids = append(ids, record.ID)
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
