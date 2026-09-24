// Package runmarker stores user-level pointers to Runs that have not finished.
package runmarker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const activeDirectory = "active"

// Pointer is the information needed to find a Run without reading Project history.
type Pointer struct {
	ProjectPath string `json:"project_path"`
	RunDir      string `json:"run_dir"`
	TicketRef   string `json:"ticket_ref"`
	PID         int    `json:"pid"`
	Host        string `json:"host"`
}

// Marker is a live-run marker that can be removed when its Run ends.
type Marker struct {
	path string
}

// Create atomically writes one live-run marker under sylHome/active.
func Create(sylHome, projectPath, runDir, ticketRef string, pid int, host string) (*Marker, error) {
	if sylHome == "" {
		return nil, errors.New("syl home is required")
	}
	resolvedProjectPath, err := resolvePath(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve Project path: %w", err)
	}
	resolvedRunDir, err := resolvePath(runDir)
	if err != nil {
		return nil, fmt.Errorf("resolve Run directory: %w", err)
	}
	runID := filepath.Base(resolvedRunDir)
	if runID == "." || runID == string(filepath.Separator) || runID == "" {
		return nil, errors.New("run directory name is required")
	}

	activeDir := filepath.Join(sylHome, activeDirectory)
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create active marker directory: %w", err)
	}
	markerPath := filepath.Join(activeDir, markerFileName(runID, resolvedProjectPath))
	pointer := Pointer{
		ProjectPath: resolvedProjectPath,
		RunDir:      resolvedRunDir,
		TicketRef:   ticketRef,
		PID:         pid,
		Host:        host,
	}
	if err := writeAtomically(markerPath, pointer); err != nil {
		return nil, err
	}
	return &Marker{path: markerPath}, nil
}

// Remove removes the marker. Removing an already absent marker succeeds.
func (m *Marker) Remove() error {
	if m == nil || m.path == "" {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove live-run marker %s: %w", m.path, err)
	}
	return nil
}

// Remove deletes the marker for pointer. Removing an already absent marker succeeds.
func Remove(sylHome string, pointer Pointer) error {
	if sylHome == "" {
		return errors.New("syl home is required")
	}
	markerPath := filepath.Join(
		sylHome,
		activeDirectory,
		markerFileName(filepath.Base(pointer.RunDir), filepath.Clean(pointer.ProjectPath)),
	)
	return (&Marker{path: markerPath}).Remove()
}

// List returns the pointers recorded in sylHome/active.
func List(sylHome string) ([]Pointer, error) {
	if sylHome == "" {
		return nil, errors.New("syl home is required")
	}
	activeDir := filepath.Join(sylHome, activeDirectory)
	entries, err := os.ReadDir(activeDir)
	if errors.Is(err, os.ErrNotExist) {
		return []Pointer{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read active marker directory: %w", err)
	}

	pointers := make([]Pointer, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		pointer, err := readPointer(filepath.Join(activeDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		pointers = append(pointers, pointer)
	}
	sort.Slice(pointers, func(left, right int) bool {
		if pointers[left].ProjectPath != pointers[right].ProjectPath {
			return pointers[left].ProjectPath < pointers[right].ProjectPath
		}
		return pointers[left].RunDir < pointers[right].RunDir
	})
	return pointers, nil
}

func resolvePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", absolutePath, err)
	}
	return filepath.Clean(resolvedPath), nil
}

func markerFileName(runID, projectPath string) string {
	return runID + "-" + projectHash(projectPath) + ".json"
}

func writeAtomically(path string, pointer Pointer) error {
	contents, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return fmt.Errorf("encode live-run marker: %w", err)
	}
	contents = append(contents, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".run-marker-*")
	if err != nil {
		return fmt.Errorf("create temporary live-run marker: %w", err)
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
		return fmt.Errorf("set temporary live-run marker permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary live-run marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary live-run marker: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace live-run marker %s: %w", path, err)
	}
	keepTemporary = true
	return nil
}

func readPointer(path string) (Pointer, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Pointer{}, fmt.Errorf("read live-run marker %s: %w", path, err)
	}
	var pointer Pointer
	if err := json.Unmarshal(contents, &pointer); err != nil {
		return Pointer{}, fmt.Errorf("decode live-run marker %s: %w", path, err)
	}
	return pointer, nil
}

func projectHash(projectPath string) string {
	hash := sha256.Sum256([]byte(projectPath))
	return hex.EncodeToString(hash[:])[:12]
}
