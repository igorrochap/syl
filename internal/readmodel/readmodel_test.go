package readmodel_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/runstate"
)

func TestOverviewReadsProjectHealthAndLiveRuns(t *testing.T) {
	sylHome := t.TempDir()
	okProject := t.TempDir()
	uninitializedProject := t.TempDir()
	invalidProject := t.TempDir()
	missingProject := filepath.Join(t.TempDir(), "missing")
	if _, err := config.Init(okProject); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(invalidProject, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(invalidProject), []byte("invalid = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, sylHome, okProject, uninitializedProject, invalidProject, missingProject)

	awaitingRun := createRun(t, okProject, "awaiting", runstate.State{
		Status: runstate.Running, Activity: runstate.AwaitingAnswer, Iteration: 2, MaxIterations: 3,
		Question: "Which option?", PID: os.Getpid(), Hostname: hostname(t),
		StartedAt: time.Date(2026, time.September, 23, 10, 0, 0, 0, time.UTC),
		Kind:      runstate.Implement, TicketRef: "#181",
	}, "/worktrees/awaiting", "codex", "claude")
	createMarker(t, sylHome, okProject, awaitingRun, "#181", os.Getpid(), hostname(t))

	interruptedRun := createRun(t, okProject, "interrupted", runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, Iteration: 1, MaxIterations: 3,
		PID: 999999, Hostname: hostname(t), StartedAt: time.Date(2026, time.September, 23, 9, 0, 0, 0, time.UTC),
		Kind: runstate.Implement, TicketRef: "#182",
	}, "/worktrees/interrupted", "codex", "claude")
	createMarker(t, sylHome, okProject, interruptedRun, "#182", 999999, hostname(t))

	remoteRun := createRun(t, okProject, "remote", runstate.State{
		Status: runstate.Running, Activity: runstate.Reviewing, Iteration: 2, MaxIterations: 3,
		PID: 1, Hostname: "other-host", StartedAt: time.Date(2026, time.September, 23, 8, 0, 0, 0, time.UTC),
		Kind: runstate.Implement, TicketRef: "#183",
	}, "/worktrees/remote", "codex", "claude")
	createMarker(t, sylHome, okProject, remoteRun, "#183", 1, "other-host")

	overview, err := readmodel.ReadOverview(sylHome)
	if err != nil {
		t.Fatalf("ReadOverview() error = %v", err)
	}
	if len(overview.Projects) != 4 {
		t.Fatalf("Projects = %d, want 4", len(overview.Projects))
	}
	assertProjectHealth(t, overview.Projects, okProject, readmodel.HealthOK)
	assertProjectHealth(t, overview.Projects, uninitializedProject, readmodel.HealthUninitialized)
	assertProjectHealth(t, overview.Projects, invalidProject, readmodel.HealthInvalid)
	assertProjectHealth(t, overview.Projects, missingProject, readmodel.HealthMissing)

	if len(overview.AwaitingAnswer) != 1 || overview.AwaitingAnswer[0].TicketRef != "#181" {
		t.Fatalf("AwaitingAnswer = %#v, want #181 first", overview.AwaitingAnswer)
	}
	awaiting := overview.AwaitingAnswer[0]
	if awaiting.Question != "Which option?" || awaiting.WorkRoot != "/worktrees/awaiting" {
		t.Fatalf("awaiting Run = %#v, want question and work root", awaiting)
	}
	if len(overview.LiveRuns) != 2 {
		t.Fatalf("LiveRuns = %d, want interrupted and remote Runs", len(overview.LiveRuns))
	}
	interrupted := findRun(t, overview.LiveRuns, "#182")
	if !interrupted.Interrupted || interrupted.Activity != string(runstate.Implementing) {
		t.Fatalf("interrupted Run = %#v, want last-seen activity", interrupted)
	}
	remote := findRun(t, overview.LiveRuns, "#183")
	if remote.Interrupted || remote.Hostname != "other-host" {
		t.Fatalf("remote Run = %#v, want remote host without interruption", remote)
	}
}

func TestOverviewShowsUnknownRunWhenStateCannotBeRead(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, sylHome, project)
	runDir := filepath.Join(project, ".syl", "runs", "unknown")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runstate.Path(runDir), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	createMarker(t, sylHome, project, runDir, "#184", os.Getpid(), hostname(t))

	overview, err := readmodel.ReadOverview(sylHome)
	if err != nil {
		t.Fatalf("ReadOverview() error = %v", err)
	}
	if len(overview.LiveRuns) != 1 || !overview.LiveRuns[0].Unknown {
		t.Fatalf("LiveRuns = %#v, want one unknown Run", overview.LiveRuns)
	}
	contents, err := os.ReadFile(runstate.Path(runDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "before" {
		t.Fatalf("run-state.json = %q, want unchanged", contents)
	}
}

func writeRegistry(t *testing.T, sylHome string, paths ...string) {
	t.Helper()
	entries := make([]registry.Entry, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, registry.Entry{Path: path})
	}
	contents, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path(sylHome), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func createRun(t *testing.T, project, name string, state runstate.State, workRoot, implementerHarness, reviewerHarness string) string {
	t.Helper()
	runDir := filepath.Join(project, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	contents := strings.Join([]string{
		"Work root: " + workRoot,
		"Implementer harness: " + implementerHarness,
		"Reviewer harness: " + reviewerHarness,
	}, "\n")
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func createMarker(t *testing.T, sylHome, project, runDir, ticket string, pid int, host string) {
	t.Helper()
	marker, err := runmarker.Create(sylHome, project, runDir, ticket, pid, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = marker.Remove() })
}

func assertProjectHealth(t *testing.T, projects []readmodel.Project, path string, want readmodel.Health) {
	t.Helper()
	for _, project := range projects {
		if project.Path == path {
			if project.Health != want {
				t.Fatalf("Project %q health = %q, want %q", path, project.Health, want)
			}
			return
		}
	}
	t.Fatalf("Project %q not found in %#v", path, projects)
}

func findRun(t *testing.T, runs []readmodel.Run, ticket string) readmodel.Run {
	t.Helper()
	for _, run := range runs {
		if run.TicketRef == ticket {
			return run
		}
	}
	t.Fatalf("Run %q not found in %#v", ticket, runs)
	return readmodel.Run{}
}

func hostname(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return host
}
