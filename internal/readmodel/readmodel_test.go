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
	"github.com/igorrochap/syl/internal/usage"
)

func TestReaderReadsProjectRunHistoryNewestFirst(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeRegistryEntry(t, sylHome, registry.Entry{
		Path: project, FirstSeen: time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC),
	})

	writeHistoryRun(t, project, "20260924T120000.000000000Z-running", runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, Iteration: 2, MaxIterations: 3,
		PID: os.Getpid(), Hostname: hostname(t), StartedAt: time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC),
		Kind: runstate.Implement, TicketRef: "#184",
	}, "Implementer harness: codex\n", usage.Artifact{Entries: []usage.Entry{{
		Tracked: true, Metrics: &usage.Metrics{TotalTokens: 900000},
	}}})

	ended := time.Date(2026, time.September, 24, 11, 30, 0, 0, time.UTC)
	writeHistoryRun(t, project, "20260924T110000.000000000Z-approved", runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 3, EndedAt: &ended,
		StartedAt: time.Date(2026, time.September, 24, 11, 0, 0, 0, time.UTC),
		Kind:      runstate.Implement, TicketRef: "#183",
	}, "Implementer harness: codex\n", usage.Artifact{Entries: []usage.Entry{{
		Tracked: true, Metrics: &usage.Metrics{TotalTokens: 2200000},
	}}})
	if err := os.WriteFile(filepath.Join(project, ".syl", "runs", "20260924T110000.000000000Z-approved", "summary.txt"), []byte("Final verdict: approve\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeHistoryRun(t, project, "20260924T100000.000000000Z-review", runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 1, EndedAt: &ended,
		StartedAt: time.Date(2026, time.September, 24, 10, 0, 0, 0, time.UTC),
		Kind:      runstate.Review, TicketRef: "release-candidate",
	}, "Ticket: release-candidate\nReviewer harness: claude\n", usage.Artifact{})

	writeLegacyHistoryRun(t, project, "20260924T090000.000000000Z-182", "Branch: feat/legacy\n", "Iterations: 2\nFinal verdict: approve\n", nil)
	writeLegacyHistoryRun(t, project, "20260924T080000.000000000Z-181", "Branch: feat/unknown\n", "", []byte("not usage json"))
	writeLegacyHistoryRun(t, project, "20260924T070000.000000000Z-180", "", "", nil)

	page, err := readmodel.NewReader(sylHome).ReadProject(project)
	if err != nil {
		t.Fatalf("ReadProject() error = %v", err)
	}
	if page.Project.Health != readmodel.HealthOK || page.Project.IssueTracker != config.TrackerGitHub || page.Project.ReviewLog != config.TrackerLocal {
		t.Fatalf("Project = %#v, want config and healthy project", page.Project)
	}
	if len(page.Runs) != 6 {
		t.Fatalf("Runs = %d, want 6", len(page.Runs))
	}
	if got := page.Runs[0].TicketRef; got != "#184" {
		t.Fatalf("newest Run ticket = %q, want #184", got)
	}
	if page.Runs[0].Status != "running" || page.Runs[0].Activity != "implementing" || page.Runs[0].Iteration != 2 || page.Runs[0].TotalTokens != 900000 {
		t.Fatalf("running Run = %#v, want live state and usage", page.Runs[0])
	}
	if !page.Runs[0].DurationKnown {
		t.Fatal("running Run duration is unknown")
	}
	if page.Runs[1].Status != "approved" || page.Runs[1].Verdict != "approve" || page.Runs[1].TotalTokens != 2200000 {
		t.Fatalf("approved Run = %#v, want final state, verdict, and usage", page.Runs[1])
	}
	if page.Runs[2].Kind != runstate.Review || page.Runs[2].TicketRef != "release-candidate" {
		t.Fatalf("standalone review = %#v, want non-numeric reference", page.Runs[2])
	}
	if page.Runs[3].Status != "completed" || page.Runs[3].TicketRef != "#182" || page.Runs[3].Iteration != 2 || page.Runs[3].Verdict != "approve" {
		t.Fatalf("legacy completed Run = %#v, want derived values", page.Runs[3])
	}
	if page.Runs[4].Status != "unknown" || page.Runs[4].TokensKnown {
		t.Fatalf("legacy unknown Run = %#v, want unknown status and missing tokens", page.Runs[4])
	}
	if page.Runs[5].TicketRef != "" || page.Runs[5].Kind != "" || page.Runs[5].Status != "unknown" {
		t.Fatalf("incomplete Run = %#v, want missing values", page.Runs[5])
	}
}

func TestReaderMemoizesFinalAndLegacyRunsButRefreshesRunningState(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeRegistryEntry(t, sylHome, registry.Entry{Path: project})

	finalRun := filepath.Join(project, ".syl", "runs", "20260924T120000.000000000Z-final")
	runningRun := filepath.Join(project, ".syl", "runs", "20260924T110000.000000000Z-running")
	started := time.Date(2026, time.September, 24, 11, 0, 0, 0, time.UTC)
	writeHistoryRun(t, project, filepath.Base(finalRun), runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 1, StartedAt: started,
		Kind: runstate.Implement, TicketRef: "#1",
	}, "Branch: final\n", usage.Artifact{Entries: []usage.Entry{{Tracked: true, Metrics: &usage.Metrics{TotalTokens: 10}}}})
	writeHistoryRun(t, project, filepath.Base(runningRun), runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, PID: os.Getpid(), Hostname: hostname(t),
		StartedAt: started, Kind: runstate.Implement, TicketRef: "#2",
	}, "Branch: running\n", usage.Artifact{Entries: []usage.Entry{{Tracked: true, Metrics: &usage.Metrics{TotalTokens: 20}}}})

	files := &countingFileSystem{}
	reader := readmodel.NewReaderWithFileSystem(sylHome, files)
	if _, err := reader.ReadProject(project); err != nil {
		t.Fatal(err)
	}
	if err := usage.WriteArtifact(filepath.Join(runningRun, "usage.json"), usage.Artifact{Entries: []usage.Entry{{Tracked: true, Metrics: &usage.Metrics{TotalTokens: 30}}}}); err != nil {
		t.Fatal(err)
	}
	page, err := reader.ReadProject(project)
	if err != nil {
		t.Fatal(err)
	}
	if files.stateReads != 3 {
		t.Fatalf("run-state reads = %d, want final once and running twice", files.stateReads)
	}
	if files.usageReads != 3 {
		t.Fatalf("usage reads = %d, want final once and running twice", files.usageReads)
	}
	if page.Runs[1].TotalTokens != 30 {
		t.Fatalf("running Run = %#v, want refreshed usage", page.Runs[1])
	}

	if err := os.WriteFile(runstate.Path(finalRun), []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err = reader.ReadProject(project)
	if err != nil {
		t.Fatal(err)
	}
	final := page.Runs[0]
	if final.Status != "approved" || final.TotalTokens != 10 {
		t.Fatalf("memoized final Run = %#v, want original values", final)
	}
}

func TestReaderShowsHistoryForInvalidAndUninitializedProjects(t *testing.T) {
	sylHome := t.TempDir()
	invalidProject := t.TempDir()
	uninitializedProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(invalidProject, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(invalidProject), []byte("invalid = ["), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRegistryEntry(t, sylHome, registry.Entry{Path: invalidProject}, registry.Entry{Path: uninitializedProject})
	for _, project := range []string{invalidProject, uninitializedProject} {
		writeLegacyHistoryRun(t, project, "20260924T120000.000000000Z-1", "Branch: feat/history\n", "Final verdict: approve\n", nil)
	}

	reader := readmodel.NewReader(sylHome)
	for _, test := range []struct {
		name   string
		path   string
		health readmodel.Health
	}{
		{name: "invalid", path: invalidProject, health: readmodel.HealthInvalid},
		{name: "uninitialized", path: uninitializedProject, health: readmodel.HealthUninitialized},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, err := reader.ReadProject(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if page.Project.Health != test.health || len(page.Runs) != 1 {
				t.Fatalf("page = %#v, want %s with one Run", page, test.health)
			}
		})
	}
}

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

func writeRegistryEntry(t *testing.T, sylHome string, entries ...registry.Entry) {
	t.Helper()
	contents, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sylHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path(sylHome), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeHistoryRun(t *testing.T, project, name string, state runstate.State, metadata string, artifact usage.Artifact) {
	t.Helper()
	runDir := filepath.Join(project, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	if metadata != "" {
		if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte(metadata), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := usage.WriteArtifact(filepath.Join(runDir, "usage.json"), artifact); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyHistoryRun(t *testing.T, project, name, metadata, summary string, usageContents []byte) {
	t.Helper()
	runDir := filepath.Join(project, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if metadata != "" {
		if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte(metadata), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if summary != "" {
		if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte(summary), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if usageContents != nil {
		if err := os.WriteFile(filepath.Join(runDir, "usage.json"), usageContents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type countingFileSystem struct {
	stateReads int
	usageReads int
}

func (files *countingFileSystem) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (files *countingFileSystem) ReadFile(name string) ([]byte, error) {
	if filepath.Base(name) == "run-state.json" {
		files.stateReads++
	}
	if filepath.Base(name) == "usage.json" {
		files.usageReads++
	}
	return os.ReadFile(name)
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
