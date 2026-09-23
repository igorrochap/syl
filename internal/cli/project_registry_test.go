package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/updater"
)

func TestPlanRegistersProjectAfterLoadingConfig(t *testing.T) {
	fixture := newPlanFixture(t)
	fixture.harnesses["claude"] = &planHarness{}

	if code := fixture.app.Run(context.Background(), []string{"plan", "add registry"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}

	entries := readProjectEntries(t, fixture.sylHome)
	if len(entries) != 1 {
		t.Fatalf("registry entries = %d, want 1", len(entries))
	}
	if entries[0].Path != resolvedProjectPath(t, fixture.root) {
		t.Fatalf("registry path = %q, want resolved %q", entries[0].Path, fixture.root)
	}
}

func TestProjectRegistryUpsertKeepsFirstSeenAndOneEntry(t *testing.T) {
	fixture := newPlanFixture(t)
	fixture.harnesses["claude"] = &planHarness{}

	if code := fixture.app.Run(context.Background(), []string{"plan", "add registry"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("first plan code = %d, stderr = %q", code, fixture.stderr.String())
	}
	first := readProjectEntries(t, fixture.sylHome)[0]
	time.Sleep(time.Millisecond)
	fixture.stdout.Reset()
	fixture.stderr.Reset()
	if code := fixture.app.Run(context.Background(), []string{"plan", "add registry"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("second plan code = %d, stderr = %q", code, fixture.stderr.String())
	}

	entries := readProjectEntries(t, fixture.sylHome)
	if len(entries) != 1 {
		t.Fatalf("registry entries = %d, want 1", len(entries))
	}
	if entries[0].FirstSeen != first.FirstSeen {
		t.Fatalf("first_seen changed from %s to %s", first.FirstSeen, entries[0].FirstSeen)
	}
	if !entries[0].LastSeen.After(first.LastSeen) {
		t.Fatalf("last_seen = %s, want after %s", entries[0].LastSeen, first.LastSeen)
	}
}

func TestProjectRegistryResolvesProjectSymlink(t *testing.T) {
	fixture := newPlanFixture(t)
	projectLink := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(fixture.root, projectLink); err != nil {
		t.Fatal(err)
	}
	app := New(projectLink, projectLink, fixture.sylHome, fixture.app.deps)

	if code := app.Run(context.Background(), []string{"plan", "add registry"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}

	entries := readProjectEntries(t, fixture.sylHome)
	if len(entries) != 1 || entries[0].Path != resolvedProjectPath(t, fixture.root) {
		t.Fatalf("registry entries = %#v, want one resolved entry for %q", entries, fixture.root)
	}
}

func TestInitRegistersNewProject(t *testing.T) {
	root := t.TempDir()
	sylHome := t.TempDir()
	app := New(root, root, sylHome, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d, stderr = %q", code, stderr.String())
	}
	entries := readProjectEntries(t, sylHome)
	if len(entries) != 1 || entries[0].Path != resolvedProjectPath(t, root) {
		t.Fatalf("registry entries = %#v, want one entry for %q", entries, root)
	}
}

func TestNonConfigCommandsLeaveProjectRegistryUntouched(t *testing.T) {
	fixture := newTopSeamFixture(t)
	fixture.app.deps.Updater = &fakeUpdater{result: updater.Result{LatestVersion: "v1.0.0"}}

	for _, args := range [][]string{{"update"}, {"version"}, {"--help"}} {
		fixture.stdout.Reset()
		fixture.stderr.Reset()
		if code := fixture.app.Run(context.Background(), args, &fixture.stdout, &fixture.stderr); code != 0 {
			t.Fatalf("%v code = %d, stderr = %q", args, code, fixture.stderr.String())
		}
	}
	if _, err := os.Stat(registry.Path(fixture.sylHome)); !os.IsNotExist(err) {
		t.Fatalf("registry stat error = %v, want no registry", err)
	}
}

func TestConfigLoadFailureDoesNotRegisterProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".syl", "config.toml"), []byte("not = [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	sylHome := t.TempDir()
	app := New(root, root, sylHome, Dependencies{})
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"plan", "add registry"}, &stdout, &stderr); code == 0 {
		t.Fatal("plan code = 0, want config failure")
	}
	if _, err := os.Stat(registry.Path(sylHome)); !os.IsNotExist(err) {
		t.Fatalf("registry stat error = %v, want no registry", err)
	}
}

func TestResumeConfigLoadFailureDoesNotRegisterProject(t *testing.T) {
	root := t.TempDir()
	sylHome := t.TempDir()
	app := New(root, root, sylHome, Dependencies{})
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"resume", "implement"}, &stdout, &stderr); code == 0 {
		t.Fatal("resume code = 0, want config failure")
	}
	if _, err := os.Stat(registry.Path(sylHome)); !os.IsNotExist(err) {
		t.Fatalf("registry stat error = %v, want no registry", err)
	}
}

func TestProjectRegistryWriteFailureWarnsWithoutChangingCommandResult(t *testing.T) {
	fixture := newPlanFixture(t)
	badSylHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badSylHome, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.app = New(fixture.root, fixture.root, badSylHome, fixture.app.deps)
	fixture.harnesses["claude"] = &planHarness{}

	if code := fixture.app.Run(context.Background(), []string{"plan", "add registry"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("plan code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stderr.String(), "warning") || !strings.Contains(fixture.stderr.String(), "project registry") {
		t.Fatalf("stderr = %q, want project-registry warning", fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "No tickets created.") {
		t.Fatalf("stdout = %q, want normal plan output", fixture.stdout.String())
	}
}

func TestProjectRegistryReadFailureWarnsWithoutChangingCommandResult(t *testing.T) {
	fixture := newPlanFixture(t)
	if err := os.WriteFile(registry.Path(fixture.sylHome), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.harnesses["claude"] = &planHarness{}

	if code := fixture.app.Run(context.Background(), []string{"plan", "add registry"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("plan code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stderr.String(), "warning") || !strings.Contains(fixture.stderr.String(), "project registry") {
		t.Fatalf("stderr = %q, want project-registry warning", fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "No tickets created.") {
		t.Fatalf("stdout = %q, want normal plan output", fixture.stdout.String())
	}
	entries := readProjectEntries(t, fixture.sylHome)
	if len(entries) != 1 || entries[0].Path != resolvedProjectPath(t, fixture.root) {
		t.Fatalf("registry entries = %#v, want repaired entry for %q", entries, fixture.root)
	}
}

type projectEntry struct {
	Path      string    `json:"path"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

func readProjectEntries(t *testing.T, sylHome string) []projectEntry {
	t.Helper()
	contents, err := os.ReadFile(registry.Path(sylHome))
	if err != nil {
		t.Fatal(err)
	}
	var entries []projectEntry
	if err := json.Unmarshal(contents, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

func resolvedProjectPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
