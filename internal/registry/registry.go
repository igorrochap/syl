// Package registry records the Projects syl has successfully used.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const fileName = "projects.json"

// Entry is one Project in the user-level registry.
type Entry struct {
	Path      string    `json:"path"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Path returns the registry file path under sylHome.
func Path(sylHome string) string {
	return filepath.Join(sylHome, fileName)
}

// List returns the Projects recorded in sylHome.
func List(sylHome string) ([]Entry, error) {
	if sylHome == "" {
		return nil, fmt.Errorf("syl home is required")
	}

	return read(Path(sylHome))
}

// Upsert records projectRoot in the registry, preserving its first-seen time.
// A corrupt registry is repaired with its valid entries and the current
// Project, while the read error is returned after a successful write.
func Upsert(sylHome, projectRoot string) error {
	if sylHome == "" {
		return fmt.Errorf("syl home is required")
	}

	projectPath, err := normalizeProjectPath(projectRoot)
	if err != nil {
		return err
	}

	registryPath := Path(sylHome)
	entries, readErr := read(registryPath)
	if readErr != nil && entries == nil {
		return readErr
	}

	now := time.Now().UTC()
	updated := false
	for index := range entries {
		if entries[index].Path != projectPath {
			continue
		}
		entries[index].LastSeen = now
		updated = true
		break
	}

	if !updated {
		entries = append(entries, Entry{Path: projectPath, FirstSeen: now, LastSeen: now})
		sortEntries(entries)
	}
	if err := write(sylHome, registryPath, entries); err != nil {
		return err
	}
	return readErr
}

// Forget removes projectRoot from the registry without touching the Project.
// The registry replacement is atomic when an entry is removed.
func Forget(sylHome, projectRoot string) error {
	if sylHome == "" {
		return fmt.Errorf("syl home is required")
	}
	if projectRoot == "" {
		return fmt.Errorf("project path is required")
	}

	projectPath, err := normalizeForgetPath(projectRoot)
	if err != nil {
		return err
	}

	registryPath := Path(sylHome)
	entries, readErr := read(registryPath)
	if readErr != nil && entries == nil {
		return readErr
	}

	remaining := entries[:0]
	removed := false
	for _, entry := range entries {
		if pathsMatch(entry.Path, projectPath) {
			removed = true
			continue
		}
		remaining = append(remaining, entry)
	}
	if !removed {
		return readErr
	}

	if err := write(sylHome, registryPath, remaining); err != nil {
		return err
	}
	return readErr
}

func pathsMatch(entryPath, projectPath string) bool {
	if entryPath == projectPath {
		return true
	}
	normalizedEntryPath, err := normalizeForgetPath(entryPath)
	return err == nil && normalizedEntryPath == projectPath
}

func normalizeProjectPath(projectRoot string) (string, error) {
	absolutePath, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("make Project path absolute: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve Project path %s: %w", absolutePath, err)
	}
	return filepath.Clean(resolvedPath), nil
}

func normalizeForgetPath(projectRoot string) (string, error) {
	absolutePath, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("make Project path absolute: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err == nil {
		return filepath.Clean(resolvedPath), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve Project path %s: %w", absolutePath, err)
	}
	return filepath.Clean(absolutePath), nil
}

func read(path string) ([]Entry, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project registry %s: %w", path, err)
	}

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(contents, &rawEntries); err != nil {
		return []Entry{}, fmt.Errorf("decode project registry %s: %w", path, err)
	}

	entries := make([]Entry, 0, len(rawEntries))
	var decodeErr error
	for index, rawEntry := range rawEntries {
		var entry Entry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			if decodeErr == nil {
				decodeErr = fmt.Errorf("decode project registry %s entry %d: %w", path, index, err)
			}
			continue
		}
		if entry.Path == "" {
			if decodeErr == nil {
				decodeErr = fmt.Errorf("decode project registry %s entry %d: path is required", path, index)
			}
			continue
		}
		entries = append(entries, entry)
	}
	return entries, decodeErr
}

func write(sylHome, registryPath string, entries []Entry) error {
	if err := os.MkdirAll(sylHome, 0o755); err != nil {
		return fmt.Errorf("create syl home %s: %w", sylHome, err)
	}

	contents, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project registry: %w", err)
	}
	contents = append(contents, '\n')

	temporary, err := os.CreateTemp(sylHome, ".projects.json-*")
	if err != nil {
		return fmt.Errorf("create temporary project registry: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary project registry permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary project registry: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary project registry: %w", err)
	}
	if err := os.Rename(temporaryPath, registryPath); err != nil {
		return fmt.Errorf("replace project registry %s: %w", registryPath, err)
	}
	keepTemporary = true
	return nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
}
