package sylhome_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/sylhome"
)

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := sylhome.Open(""); err == nil {
		t.Fatal("Open(\"\") succeeded")
	}
}

func TestRegisterProjectResolvesSymlinkAndPreservesFirstSeen(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	resolvedProject := resolvedPath(t, project)
	dir := openDir(t)

	if err := dir.RegisterProject(link); err != nil {
		t.Fatal(err)
	}
	first, err := dir.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Path != resolvedProject {
		t.Fatalf("Projects() = %#v, want resolved project %q", first, resolvedProject)
	}

	time.Sleep(time.Millisecond)
	if err := dir.RegisterProject(project); err != nil {
		t.Fatal(err)
	}
	second, err := dir.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if !second[0].FirstSeen.Equal(first[0].FirstSeen) || !second[0].LastSeen.After(first[0].LastSeen) {
		t.Fatalf("upsert timestamps = %#v, want first seen unchanged and last seen updated", second[0])
	}
}

func TestProjectsReturnsValidEntriesAndCorruption(t *testing.T) {
	dir := openDir(t)
	validProject := t.TempDir()
	path := filepath.Join(dir.String(), "projects.json")
	contents := `[
  {"path":"` + validProject + `"},
  {"path":42}
]`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	projects, err := dir.Projects()
	if err == nil {
		t.Fatal("Projects() error = nil, want corrupt-entry error")
	}
	if len(projects) != 1 || projects[0].Path != validProject {
		t.Fatalf("Projects() = %#v, want the valid entry plus an error", projects)
	}
}

func TestRegisterProjectRepairsCorruptRegistry(t *testing.T) {
	dir := openDir(t)
	path := filepath.Join(dir.String(), "projects.json")
	if err := os.WriteFile(path, []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	resolvedProject := resolvedPath(t, project)

	if err := dir.RegisterProject(project); err == nil {
		t.Fatal("RegisterProject() error = nil, want original corruption error")
	}
	projects, readErr := dir.Projects()
	if readErr != nil || len(projects) != 1 || projects[0].Path != resolvedProject {
		t.Fatalf("repaired Projects() = %#v, %v", projects, readErr)
	}
}

func TestForgetProjectRemovesMissingProject(t *testing.T) {
	dir := openDir(t)
	missingProject := filepath.Join(t.TempDir(), "missing")
	contents := `[{"path":"` + missingProject + `"}]`
	if err := os.WriteFile(filepath.Join(dir.String(), "projects.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := dir.ForgetProject(missingProject); err != nil {
		t.Fatal(err)
	}
	projects, err := dir.Projects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("Projects() after forget = %#v, %v", projects, err)
	}
}

func TestMarkLiveKeepsPreviousMarkerFormatAndUnmarksExactFile(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := openDir(t)
	resolvedProject := resolvedPath(t, project)
	resolvedRunDir := resolvedPath(t, runDir)
	marked, err := dir.MarkLive(sylhome.LiveRun{
		ProjectPath: project,
		RunDir:      runDir,
		TicketRef:   "#202",
		Host:        "host-a",
		PID:         1234,
	})
	if err != nil {
		t.Fatal(err)
	}

	markerEntries, err := os.ReadDir(filepath.Join(dir.String(), "active"))
	if err != nil || len(markerEntries) != 1 {
		t.Fatalf("marker entries = %#v, %v", markerEntries, err)
	}
	contents, err := os.ReadFile(filepath.Join(dir.String(), "active", markerEntries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(contents, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := map[string]any{
		"project_path": resolvedProject,
		"run_dir":      resolvedRunDir,
		"ticket_ref":   "#202",
		"pid":          float64(1234),
		"host":         "host-a",
	}
	if !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("marker fields = %#v, want %#v", fields, wantFields)
	}

	if err := marked.Unmark(); err != nil {
		t.Fatal(err)
	}
	if runs, err := dir.LiveRuns(); err != nil || len(runs) != 0 {
		t.Fatalf("LiveRuns() after Unmark = %#v, %v", runs, err)
	}
	if err := marked.Unmark(); err != nil {
		t.Fatalf("second Unmark() error = %v", err)
	}
}

func TestLiveRunFromPreviousMarkerUnmarksItsOwnFile(t *testing.T) {
	dir := openDir(t)
	activeDir := filepath.Join(dir.String(), "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(activeDir, "legacy-name.json")
	contents := []byte("{\n  \"project_path\": \"/old/project\",\n  \"run_dir\": \"/old/project/.syl/runs/run-1\",\n  \"ticket_ref\": \"#179\",\n  \"pid\": 1234,\n  \"host\": \"host-a\"\n}\n")
	if err := os.WriteFile(markerPath, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	runs, err := dir.LiveRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("LiveRuns() = %#v, %v", runs, err)
	}
	if err := runs[0].Unmark(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("legacy marker stat = %v, want absent", err)
	}
}

func TestMarkLiveResolvesSymlinkedPaths(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(link, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := openDir(t)
	run, err := dir.MarkLive(sylhome.LiveRun{ProjectPath: link, RunDir: runDir})
	if err != nil {
		t.Fatal(err)
	}
	resolvedProject := resolvedPath(t, project)
	if run.ProjectPath != resolvedProject || run.RunDir != filepath.Join(resolvedProject, ".syl", "runs", "run-1") {
		t.Fatalf("marked run = %#v, want resolved paths", run)
	}
}

func TestLiveRunsReportsMalformedMarker(t *testing.T) {
	dir := openDir(t)
	activeDir := filepath.Join(dir.String(), "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "broken.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.LiveRuns(); err == nil || !strings.Contains(err.Error(), "decode live-run marker") {
		t.Fatalf("LiveRuns() error = %v, want decode error", err)
	}
}

func openDir(t *testing.T) sylhome.Dir {
	t.Helper()
	dir, err := sylhome.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(resolved)
}
