package sylhome_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/igorrochap/syl/internal/sylhome"
)

func TestForgetProjectKeepsUnknownProject(t *testing.T) {
	dir := openDir(t)
	registered := t.TempDir()
	if err := dir.RegisterProject(registered); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir.String(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.ForgetProject(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir.String(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("registry changed from %q to %q", before, after)
	}
}

func TestForgetProjectReportsCorruptionAfterRepair(t *testing.T) {
	dir := openDir(t)
	registered := t.TempDir()
	other := t.TempDir()
	contents := `[
  {"path":"` + registered + `"},
  {"path":42}
]`
	if err := os.WriteFile(filepath.Join(dir.String(), "projects.json"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dir.ForgetProject(registered); err == nil {
		t.Fatal("ForgetProject() error = nil, want corruption error")
	}
	projects, err := dir.Projects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("Projects() after repair = %#v, %v", projects, err)
	}
	if err := dir.RegisterProject(other); err != nil {
		t.Fatal(err)
	}
}

func TestProjectOperationsReportInvalidPathsAndUnreadableHome(t *testing.T) {
	dir := openDir(t)
	if err := dir.RegisterProject("bad\x00path"); err == nil {
		t.Fatal("RegisterProject() with invalid path succeeded")
	}
	if err := dir.ForgetProject("bad\x00path"); err == nil {
		t.Fatal("ForgetProject() with invalid path succeeded")
	}
	filePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(filePath, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileDir, err := sylhome.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileDir.Projects(); err == nil {
		t.Fatal("Projects() with unreadable home succeeded")
	}
	if err := fileDir.ForgetProject(t.TempDir()); err == nil {
		t.Fatal("ForgetProject() with unreadable home succeeded")
	}
}

func TestMarkLiveRejectsInvalidRootsAndActiveDirectory(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := openDir(t)
	if _, err := dir.MarkLive(sylhome.LiveRun{ProjectPath: filepath.Join(project, "missing"), RunDir: runDir}); err == nil {
		t.Fatal("MarkLive() with missing Project succeeded")
	}
	if _, err := dir.MarkLive(sylhome.LiveRun{ProjectPath: project, RunDir: filepath.Join(project, "missing")}); err == nil {
		t.Fatal("MarkLive() with missing Run directory succeeded")
	}
	if err := os.WriteFile(filepath.Join(dir.String(), "active"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.MarkLive(sylhome.LiveRun{ProjectPath: project, RunDir: runDir}); err == nil {
		t.Fatal("MarkLive() with invalid active directory succeeded")
	}
}

func TestLiveRunsHandlesEmptyAndInvalidActiveDirectory(t *testing.T) {
	dir := openDir(t)
	runs, err := dir.LiveRuns()
	if err != nil || len(runs) != 0 {
		t.Fatalf("LiveRuns() = %#v, %v, want empty", runs, err)
	}
	if err := os.WriteFile(filepath.Join(dir.String(), "active"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.LiveRuns(); err == nil {
		t.Fatal("LiveRuns() with invalid active directory succeeded")
	}
}

func TestUnmarkReportsRemovalFailureAndZeroValueSucceeds(t *testing.T) {
	var zero sylhome.LiveRun
	if err := zero.Unmark(); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "run-1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := openDir(t)
	run, err := dir.MarkLive(sylhome.LiveRun{ProjectPath: project, RunDir: runDir})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir.String(), "active"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("marker entries = %#v, %v", entries, err)
	}
	markerPath := filepath.Join(dir.String(), "active", entries[0].Name())
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(markerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerPath, "child"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run.Unmark(); err == nil {
		t.Fatal("Unmark() on non-empty directory succeeded")
	}
}
