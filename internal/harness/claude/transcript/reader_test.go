package transcript

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderFindsSessionAndReadsEntriesInSequence(t *testing.T) {
	reader := New(filepath.Join("testdata", "home"))

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
	reader := New(filepath.Join("testdata", "home"))

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
	reader := New(filepath.Join("testdata", "home"))

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
	reader := New(filepath.Join("testdata", "home"))

	path, err := reader.Find("/workspace/project", "missing-session")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	_, err = reader.Read(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read() error = %v, want os.ErrNotExist", err)
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
