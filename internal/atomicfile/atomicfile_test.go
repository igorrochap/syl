package atomicfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/atomicfile"
)

func TestWriteReplacesFileWithContentsAndRequestedMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.Write(path, []byte("new contents"), 0o640); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "new contents" {
		t.Fatalf("contents = %q, want %q", contents, "new contents")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want %o", got, 0o640)
	}
}

func TestWriteLeavesTargetAndCleansTemporaryFileWhenRenameFails(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "target")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	err := atomicfile.Write(path, []byte("new contents"), 0o644)
	if err == nil {
		t.Fatal("Write() error = nil, want rename failure")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("Write() error = %v, want target path %q", err, path)
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !info.IsDir() {
		t.Fatal("target was replaced after rename failure")
	}
	assertOnlyEntry(t, directory, "target")
}

func TestWriteReportsReadOnlyDirectoryAndCleansTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Errorf("restore temporary directory permissions: %v", err)
		}
	})

	path := filepath.Join(directory, "target")
	err := atomicfile.Write(path, []byte("contents"), 0o644)
	if err == nil {
		t.Fatal("Write() error = nil, want read-only directory failure")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("Write() error = %v, want target path %q", err, path)
	}
	assertDirectoryEmpty(t, directory)
}

func TestWriteDoesNotCreateMissingParentDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	path := filepath.Join(directory, "target")

	err := atomicfile.Write(path, []byte("contents"), 0o644)
	if err == nil {
		t.Fatal("Write() error = nil, want missing-parent failure")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("Write() error = %v, want target path %q", err, path)
	}
	if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
		t.Fatalf("parent directory stat error = %v, want directory to remain absent", statErr)
	}
}

func assertOnlyEntry(t *testing.T, directory, name string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("directory entries = %#v, want only %q", entries, name)
	}
}

func assertDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory entries = %#v, want none", entries)
	}
}
