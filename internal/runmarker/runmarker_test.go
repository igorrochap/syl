package runmarker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCreateAndListResolvesPointerPathsAndKeepsExactFieldSet(t *testing.T) {
	projectTarget := t.TempDir()
	projectLink := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(projectTarget, projectLink); err != nil {
		t.Fatal(err)
	}
	resolvedProjectTarget, err := filepath.EvalSymlinks(projectTarget)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(projectLink, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sylHome := filepath.Join(t.TempDir(), "syl")

	marker, err := Create(sylHome, projectLink, runDir, "#181", 1234, "host-a")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = marker.Remove() })

	activeEntries, err := os.ReadDir(filepath.Join(sylHome, "active"))
	if err != nil {
		t.Fatalf("read active directory: %v", err)
	}
	if len(activeEntries) != 1 || !strings.HasSuffix(activeEntries[0].Name(), ".json") {
		t.Fatalf("active entries = %#v, want one JSON marker", activeEntries)
	}
	contents, err := os.ReadFile(filepath.Join(sylHome, "active", activeEntries[0].Name()))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(contents, &fields); err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	wantFields := map[string]any{
		"project_path": filepath.Clean(resolvedProjectTarget),
		"run_dir":      filepath.Join(filepath.Clean(resolvedProjectTarget), ".syl", "runs", "run-1"),
		"ticket_ref":   "#181",
		"pid":          float64(1234),
		"host":         "host-a",
	}
	if !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("marker fields = %#v, want %#v", fields, wantFields)
	}

	pointers, err := List(sylHome)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantPointers := []Pointer{{
		ProjectPath: filepath.Clean(resolvedProjectTarget),
		RunDir:      filepath.Join(filepath.Clean(resolvedProjectTarget), ".syl", "runs", "run-1"),
		TicketRef:   "#181",
		PID:         1234,
		Host:        "host-a",
	}}
	if !reflect.DeepEqual(pointers, wantPointers) {
		t.Fatalf("List() = %#v, want %#v", pointers, wantPointers)
	}
}

func TestCreateReportsInvalidRootsAndMarkerPath(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("", project, runDir, "", 1, "host"); err == nil {
		t.Fatal("Create() with empty syl home succeeded")
	}
	if _, err := Create(t.TempDir(), filepath.Join(project, "missing"), runDir, "", 1, "host"); err == nil {
		t.Fatal("Create() with missing Project succeeded")
	}
	if _, err := Create(t.TempDir(), project, filepath.Join(project, "missing-run"), "", 1, "host"); err == nil {
		t.Fatal("Create() with missing Run directory succeeded")
	}

	sylHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(sylHome, "active"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(sylHome, project, runDir, "", 1, "host"); err == nil {
		t.Fatal("Create() with invalid active directory succeeded")
	}
}

func TestCreateReportsAtomicReplacementFailure(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sylHome := t.TempDir()
	activeDir := filepath.Join(sylHome, "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(activeDir, markerFileName("run-1", resolvedProject))
	if err := os.Mkdir(markerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(sylHome, project, runDir, "#181", 1, "host"); err == nil {
		t.Fatal("Create() with an existing marker directory succeeded")
	}
}

func TestCreateUsesDistinctMarkerNamesForRunsAcrossProjects(t *testing.T) {
	sylHome := t.TempDir()
	projects := []string{t.TempDir(), t.TempDir()}
	resolvedProjects := make([]string, len(projects))
	markers := make([]*Marker, 0, len(projects))
	for index, project := range projects {
		resolvedProject, err := filepath.EvalSymlinks(project)
		if err != nil {
			t.Fatal(err)
		}
		resolvedProjects[index] = resolvedProject
		runDir := filepath.Join(project, ".syl", "runs", "same-run-id")
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker, err := Create(sylHome, project, runDir, "#181", index+1, "host")
		if err != nil {
			t.Fatalf("Create(%d) error = %v", index, err)
		}
		markers = append(markers, marker)
	}
	t.Cleanup(func() {
		for _, marker := range markers {
			_ = marker.Remove()
		}
	})

	entries, err := os.ReadDir(filepath.Join(sylHome, "active"))
	if err != nil {
		t.Fatalf("read active directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	wantNames := []string{
		"same-run-id-" + projectHash(resolvedProjects[0]) + ".json",
		"same-run-id-" + projectHash(resolvedProjects[1]) + ".json",
	}
	sort.Strings(wantNames)
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("marker names = %#v, want distinct project-qualified names", names)
	}
}

func TestCreateKeepsTwoRunsInOneProjectDistinct(t *testing.T) {
	project := t.TempDir()
	sylHome := t.TempDir()
	markers := make([]*Marker, 0, 2)
	for index, runID := range []string{"run-a", "run-b"} {
		runDir := filepath.Join(project, ".syl", "runs", runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker, err := Create(sylHome, project, runDir, "#181", index+1, "host")
		if err != nil {
			t.Fatalf("Create(%s) error = %v", runID, err)
		}
		markers = append(markers, marker)
	}
	t.Cleanup(func() {
		for _, marker := range markers {
			_ = marker.Remove()
		}
	})

	pointers, err := List(sylHome)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(pointers) != 2 || pointers[0].RunDir == pointers[1].RunDir {
		t.Fatalf("pointers = %#v, want two distinct Runs", pointers)
	}
}

func TestListReturnsEmptyWhenActiveDirectoryDoesNotExist(t *testing.T) {
	pointers, err := List(filepath.Join(t.TempDir(), "syl"))
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(pointers) != 0 {
		t.Fatalf("List() = %#v, want no pointers", pointers)
	}
}

func TestListReportsInvalidHomeAndMarker(t *testing.T) {
	if _, err := List(""); err == nil {
		t.Fatal("List() with empty syl home succeeded")
	}
	sylHome := t.TempDir()
	activeDir := filepath.Join(sylHome, "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "broken.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(sylHome); err == nil {
		t.Fatal("List() with malformed marker succeeded")
	}
	if err := os.Remove(filepath.Join(activeDir, "broken.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(activeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activeDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(sylHome); err == nil {
		t.Fatal("List() with invalid active directory succeeded")
	}
}

func TestRemoveIsIdempotentAndReportsRemovalFailure(t *testing.T) {
	var nilMarker *Marker
	if err := nilMarker.Remove(); err != nil {
		t.Fatalf("nil Marker.Remove() error = %v", err)
	}
	if err := (&Marker{}).Remove(); err != nil {
		t.Fatalf("empty Marker.Remove() error = %v", err)
	}
	if err := (&Marker{path: filepath.Join(t.TempDir(), "missing")}).Remove(); err != nil {
		t.Fatalf("missing Marker.Remove() error = %v", err)
	}

	markerPath := filepath.Join(t.TempDir(), "marker.json")
	if err := os.Mkdir(markerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerPath, "child"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&Marker{path: markerPath}).Remove(); err == nil {
		t.Fatal("Marker.Remove() on non-empty directory succeeded")
	}
}
