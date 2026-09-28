// Package sylhome owns the user-level Project registry and Live-run markers.
package sylhome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/igorrochap/syl/internal/atomicfile"
)

const (
	projectsFileName = "projects.json"
	activeDirectory  = "active"
)

// Dir is an opened Syl home directory.
type Dir struct {
	path string
}

// Open opens path as a Syl home directory value.
func Open(path string) (Dir, error) {
	if path == "" {
		return Dir{}, errors.New("syl home is required")
	}
	return Dir{path: filepath.Clean(path)}, nil
}

// String returns the path represented by the directory.
func (d Dir) String() string {
	return d.path
}

// RegisteredProject is one Project in the user-level registry.
type RegisteredProject struct {
	Path      string    `json:"path"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// RegisterProject records root in the registry, preserving its first-seen time.
// A corrupt registry is repaired with its valid entries and the current
// Project, while the read error is returned after a successful write.
func (d Dir) RegisterProject(root string) error {
	projectPath, err := normalizeProjectPath(root)
	if err != nil {
		return err
	}

	entries, readErr := d.readProjects()
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
		entries = append(entries, RegisteredProject{Path: projectPath, FirstSeen: now, LastSeen: now})
		sortProjects(entries)
	}
	if err := d.writeProjects(entries); err != nil {
		return err
	}
	return readErr
}

// Projects returns the Projects recorded in the registry.
func (d Dir) Projects() ([]RegisteredProject, error) {
	return d.readProjects()
}

// ForgetProject removes root from the registry without touching the Project.
// The registry replacement is atomic when an entry is removed.
func (d Dir) ForgetProject(root string) error {
	projectPath, err := normalizeForgetPath(root)
	if err != nil {
		return err
	}

	entries, readErr := d.readProjects()
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

	if err := d.writeProjects(remaining); err != nil {
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

func (d Dir) projectsPath() string {
	return filepath.Join(d.path, projectsFileName)
}

func (d Dir) readProjects() ([]RegisteredProject, error) {
	path := d.projectsPath()
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []RegisteredProject{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project registry %s: %w", path, err)
	}

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(contents, &rawEntries); err != nil {
		return []RegisteredProject{}, fmt.Errorf("decode project registry %s: %w", path, err)
	}

	entries := make([]RegisteredProject, 0, len(rawEntries))
	var decodeErr error
	for index, rawEntry := range rawEntries {
		var entry RegisteredProject
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

func (d Dir) writeProjects(entries []RegisteredProject) error {
	if err := os.MkdirAll(d.path, 0o755); err != nil {
		return fmt.Errorf("create syl home %s: %w", d.path, err)
	}

	contents, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project registry: %w", err)
	}
	contents = append(contents, '\n')

	if err := atomicfile.Write(d.projectsPath(), contents, 0o644); err != nil {
		return fmt.Errorf("replace project registry %s: %w", d.projectsPath(), err)
	}
	return nil
}

func sortProjects(entries []RegisteredProject) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
}

// LiveRun points to a Run that has not finished.
type LiveRun struct {
	ProjectPath string `json:"project_path"`
	RunDir      string `json:"run_dir"`
	TicketRef   string `json:"ticket_ref"`
	Host        string `json:"host"`
	PID         int    `json:"pid"`
	markerPath  string
}

// MarkLive resolves the Run paths and atomically writes its live-run marker.
func (d Dir) MarkLive(run LiveRun) (LiveRun, error) {
	resolvedProjectPath, err := resolvePath(run.ProjectPath)
	if err != nil {
		return LiveRun{}, fmt.Errorf("resolve Project path: %w", err)
	}
	resolvedRunDir, err := resolvePath(run.RunDir)
	if err != nil {
		return LiveRun{}, fmt.Errorf("resolve Run directory: %w", err)
	}
	runID := filepath.Base(resolvedRunDir)
	if runID == "." || runID == string(filepath.Separator) || runID == "" {
		return LiveRun{}, errors.New("run directory name is required")
	}

	activeDir := filepath.Join(d.path, activeDirectory)
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		return LiveRun{}, fmt.Errorf("create active marker directory: %w", err)
	}
	markedRun := LiveRun{
		ProjectPath: resolvedProjectPath,
		RunDir:      resolvedRunDir,
		TicketRef:   run.TicketRef,
		Host:        run.Host,
		PID:         run.PID,
	}
	markedRun.markerPath = filepath.Join(activeDir, markerFileName(runID, resolvedProjectPath))
	if err := writeMarker(markedRun.markerPath, markedRun); err != nil {
		return LiveRun{}, err
	}
	return markedRun, nil
}

// LiveRuns returns the live-run markers recorded in the Syl home.
func (d Dir) LiveRuns() ([]LiveRun, error) {
	activeDir := filepath.Join(d.path, activeDirectory)
	entries, err := os.ReadDir(activeDir)
	if errors.Is(err, os.ErrNotExist) {
		return []LiveRun{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read active marker directory: %w", err)
	}

	runs := make([]LiveRun, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		run, err := readMarker(filepath.Join(activeDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(left, right int) bool {
		if runs[left].ProjectPath != runs[right].ProjectPath {
			return runs[left].ProjectPath < runs[right].ProjectPath
		}
		return runs[left].RunDir < runs[right].RunDir
	})
	return runs, nil
}

// Unmark removes exactly this LiveRun's marker. An absent marker is success.
func (run LiveRun) Unmark() error {
	if run.markerPath == "" {
		return nil
	}
	if err := os.Remove(run.markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove live-run marker %s: %w", run.markerPath, err)
	}
	return nil
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

type markerFile struct {
	ProjectPath string `json:"project_path"`
	RunDir      string `json:"run_dir"`
	TicketRef   string `json:"ticket_ref"`
	PID         int    `json:"pid"`
	Host        string `json:"host"`
}

func writeMarker(path string, run LiveRun) error {
	contents, err := json.MarshalIndent(markerFile{
		ProjectPath: run.ProjectPath,
		RunDir:      run.RunDir,
		TicketRef:   run.TicketRef,
		PID:         run.PID,
		Host:        run.Host,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode live-run marker: %w", err)
	}
	contents = append(contents, '\n')

	if err := atomicfile.Write(path, contents, 0o644); err != nil {
		return fmt.Errorf("replace live-run marker %s: %w", path, err)
	}
	return nil
}

func readMarker(path string) (LiveRun, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return LiveRun{}, fmt.Errorf("read live-run marker %s: %w", path, err)
	}
	var file markerFile
	if err := json.Unmarshal(contents, &file); err != nil {
		return LiveRun{}, fmt.Errorf("decode live-run marker %s: %w", path, err)
	}
	return LiveRun{
		ProjectPath: file.ProjectPath,
		RunDir:      file.RunDir,
		TicketRef:   file.TicketRef,
		Host:        file.Host,
		PID:         file.PID,
		markerPath:  path,
	}, nil
}

func projectHash(projectPath string) string {
	hash := sha256.Sum256([]byte(projectPath))
	return hex.EncodeToString(hash[:])[:12]
}
