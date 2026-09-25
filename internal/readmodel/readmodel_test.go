package readmodel_test

import (
	"encoding/json"
	"errors"
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

func TestReaderReadsRunDetailsFromRecordedArtifacts(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(project, ".syl", "runs", "20260920T195033.518469000Z-173")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, time.September, 20, 20, 31, 0, 0, time.UTC)
	if err := runstate.Write(runstate.Path(runDir), runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 3,
		StartedAt: time.Date(2026, time.September, 20, 19, 50, 0, 0, time.UTC), EndedAt: &ended,
		Kind: runstate.Implement, TicketRef: "#173",
	}); err != nil {
		t.Fatal(err)
	}
	writeRunArtifact(t, runDir, "metadata.txt", "Branch: feat/implementer-context-rollover\nBranch point: 30de5905ca30\nWork root: /worktree\nImplementer harness: codex\nReviewer harness: claude\n")
	writeRunArtifact(t, runDir, "sessions.txt", "iteration 1 implement: implement-session\niteration 1 review: review-session\n")
	writeRunArtifact(t, runDir, "summary.txt", "Iterations: 1\nFinal verdict: approve\nSummary: Ready for review\nDiff stat:\n file.go | 2 ++\n")
	writeRunArtifact(t, runDir, "iteration-01-implement.feed", "implement feed")
	writeRunArtifact(t, runDir, "iteration-01-implement.transcript", "implement transcript")
	writeRunArtifact(t, runDir, "iteration-01-review.diff", "diff")
	writeRunArtifact(t, runDir, "iteration-01-review.feed", "review feed")
	writeRunArtifact(t, runDir, "iteration-01-review.transcript", "review transcript")
	writeRunArtifact(t, runDir, "iteration-01-verdict.txt", "VERDICT: approve\nSUMMARY: Ready for review\nFINDINGS:\n- [nit] file.go:1 — Consider a smaller helper\n- [blocking] file.go:2 — Fix this before merging\n")
	if err := usage.WriteArtifact(filepath.Join(runDir, "usage.json"), usage.Artifact{Entries: []usage.Entry{
		{Iteration: 1, Role: "implement", Harness: "codex", Model: "gpt-5.6", Tracked: true, Metrics: &usage.Metrics{
			InputTokens: 2000, CachedInputTokens: 1000, CacheWriteInputTokens: 20, OutputTokens: 80, TotalTokens: 2080,
		}},
		{Iteration: 1, Role: "review", Harness: "claude", Model: "claude-sonnet", Tracked: true, Metrics: &usage.Metrics{
			InputTokens: 100, CacheReadTokens: 40, CacheWriteTokens: 10, OutputTokens: 30, TotalTokens: 130,
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	page, err := readmodel.NewReader(t.TempDir()).ReadRun(runDir)
	if err != nil {
		t.Fatalf("ReadRun() error = %v", err)
	}
	if page.Run.TicketRef != "#173" || page.Run.Branch != "feat/implementer-context-rollover" || page.Run.Status != "approved" {
		t.Fatalf("Run = %#v, want ticket, branch, and status", page.Run)
	}
	if page.Run.Duration != 41*time.Minute || page.Run.TotalTokens != 2210 || !page.Run.TokensKnown {
		t.Fatalf("Run timing/tokens = %#v, want duration and total tokens", page.Run)
	}
	if page.Metadata.BranchPoint != "30de5905ca30" || page.Metadata.WorkRoot != "/worktree" || len(page.Metadata.Sessions) != 2 {
		t.Fatalf("Metadata = %#v, want branch point, work root, and sessions", page.Metadata)
	}
	if page.Summary != "Ready for review" || page.DiffStat != "file.go | 2 ++" {
		t.Fatalf("summary/diff stat = %q/%q", page.Summary, page.DiffStat)
	}
	if len(page.Iterations) != 1 || len(page.Iterations[0].Roles) != 2 || len(page.Iterations[0].Roles[1].Artifacts) != 3 {
		t.Fatalf("Iterations = %#v, want two roles and three review artifacts", page.Iterations)
	}
	iteration := page.Iterations[0]
	if !iteration.HasVerdict || len(iteration.FindingGroups) != 2 || iteration.FindingGroups[0].Count != 1 || iteration.FindingGroups[1].Count != 1 {
		t.Fatalf("verdict groups = %#v, want one finding in each severity", iteration)
	}
	if len(page.Usage) != 2 || page.Usage[0].Role != "implement" || page.Usage[0].CachedTokens != 1020 {
		t.Fatalf("Usage = %#v, want per-role cached totals", page.Usage)
	}
	if len(page.Resume) != 2 || page.Resume[0].Command != "syl resume implement #173" || page.Resume[1].Command != "syl resume review #173" {
		t.Fatalf("Resume = %#v, want implement and review commands", page.Resume)
	}
}

func TestReaderShowsCurrentActivityAndInterruptedRun(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), ".syl", "runs", "running-173")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runstate.Write(runstate.Path(runDir), runstate.State{
		Status: runstate.Running, Activity: runstate.Reviewing, Iteration: 2, MaxIterations: 3,
		PID: 999999, Hostname: hostname(t), Kind: runstate.Implement, TicketRef: "#173",
		StartedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	writeRunArtifact(t, runDir, "metadata.txt", "Branch: feat/example\n")
	writeRunArtifact(t, runDir, "iteration-01-verdict.txt", "VERDICT: revise\nSUMMARY: Needs work\nFINDINGS:\n")

	page, err := readmodel.NewReader(t.TempDir()).ReadRun(runDir)
	if err != nil {
		t.Fatalf("ReadRun() error = %v", err)
	}
	if !page.Run.Interrupted || page.Run.Status != "Interrupted" || page.Run.Refreshing {
		t.Fatalf("Run = %#v, want interrupted and not refreshing", page.Run)
	}
	if page.Run.Activity != "reviewing" || len(page.Iterations) != 2 || page.Iterations[1].Activity != "reviewing" {
		t.Fatalf("activity iterations = %#v, want current review activity", page.Iterations)
	}
}

func TestReaderReadsLegacyRunValues(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), ".syl", "runs", "20260920T195033.518469000Z-173")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunArtifact(t, runDir, "metadata.txt", "Branch: feat/legacy\nBranch point: abc123\nWork root: /legacy\n")
	writeRunArtifact(t, runDir, "summary.txt", "Iterations: 2\nFinal verdict: approve\nSummary: Legacy summary\nDiff stat:\n old.go | 1 +\n")

	page, err := readmodel.NewReader(t.TempDir()).ReadRun(runDir)
	if err != nil {
		t.Fatalf("ReadRun() error = %v", err)
	}
	if page.Run.Status != "completed" || page.Run.TicketRef != "#173" || page.Run.Kind != runstate.Implement {
		t.Fatalf("legacy Run = %#v, want completed implement Run", page.Run)
	}
	if page.Run.StartedAt.IsZero() || page.Run.EndedAt != nil || page.Run.DurationKnown {
		t.Fatalf("legacy timing = %#v, want timestamp only", page.Run)
	}
}

func writeRunArtifact(t *testing.T, runDir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runDir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
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

func TestReaderCountsLiveRunsForProject(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	otherProject := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeRegistryEntry(t, sylHome, registry.Entry{Path: project})
	host := hostname(t)

	liveRun := createRun(t, project, "live", runstate.State{
		Status: runstate.Running, PID: os.Getpid(), Hostname: host, StartedAt: time.Now(), Kind: runstate.Implement,
	}, "/worktrees/live", "codex", "claude")
	createMarker(t, sylHome, project, liveRun, "#live", os.Getpid(), host)

	finishedRun := createRun(t, project, "finished", runstate.State{
		Status: runstate.Approved, StartedAt: time.Now(), Kind: runstate.Implement,
	}, "/worktrees/finished", "codex", "claude")
	createMarker(t, sylHome, project, finishedRun, "#finished", 999999, host)

	staleRun := createRun(t, project, "stale", runstate.State{
		Status: runstate.Running, PID: 999999, Hostname: host, StartedAt: time.Now(), Kind: runstate.Implement,
	}, "/worktrees/stale", "codex", "claude")
	createMarker(t, sylHome, project, staleRun, "#stale", 999999, host)

	remoteRun := createRun(t, project, "remote", runstate.State{
		Status: runstate.Running, PID: 999999, Hostname: "other-host", StartedAt: time.Now(), Kind: runstate.Implement,
	}, "/worktrees/remote", "codex", "claude")
	createMarker(t, sylHome, project, remoteRun, "#remote", 999999, "other-host")

	otherRun := createRun(t, otherProject, "other", runstate.State{
		Status: runstate.Running, PID: os.Getpid(), Hostname: host, StartedAt: time.Now(), Kind: runstate.Implement,
	}, "/worktrees/other", "codex", "claude")
	createMarker(t, sylHome, otherProject, otherRun, "#other", os.Getpid(), host)

	missingStateRun := filepath.Join(project, ".syl", "runs", "missing-state")
	if err := os.MkdirAll(missingStateRun, 0o755); err != nil {
		t.Fatal(err)
	}
	createMarker(t, sylHome, project, missingStateRun, "#missing-state", os.Getpid(), host)

	page, err := readmodel.NewReader(sylHome).ReadProject(project)
	if err != nil {
		t.Fatalf("ReadProject() error = %v", err)
	}
	if page.Project.LiveRunCount != 2 {
		t.Fatalf("LiveRunCount = %d, want live and remote Runs", page.Project.LiveRunCount)
	}
}

func TestReaderHandlesMissingProjectWhenCountingLiveRuns(t *testing.T) {
	sylHome := t.TempDir()
	missingProject := filepath.Join(t.TempDir(), "missing")
	writeRegistryEntry(t, sylHome, registry.Entry{Path: missingProject})

	page, err := readmodel.NewReader(sylHome).ReadProject(missingProject)
	if err != nil {
		t.Fatalf("ReadProject() error = %v", err)
	}
	if page.Project.Health != readmodel.HealthMissing || page.Project.LiveRunCount != 0 {
		t.Fatalf("Project = %#v, want missing project with no live Runs", page.Project)
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

func TestDismissRemovesOnlyInterruptedMarkerAndLeavesRunUntouched(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "interrupted")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, PID: 999999,
		Hostname: hostname(t), StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#183",
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	beforeState, err := os.ReadFile(runstate.Path(runDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runmarker.Create(sylHome, project, runDir, state.TicketRef, state.PID, state.Hostname); err != nil {
		t.Fatal(err)
	}

	if err := readmodel.Dismiss(sylHome, runDir); err != nil {
		t.Fatalf("Dismiss() error = %v", err)
	}
	markers, err := runmarker.List(sylHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(markers) != 0 {
		t.Fatalf("markers = %#v, want empty", markers)
	}
	afterState, err := os.ReadFile(runstate.Path(runDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterState) != string(beforeState) {
		t.Fatalf("run-state.json changed from %q to %q", beforeState, afterState)
	}
}

func TestDismissRefusesLiveRunAndKeepsMarker(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "live")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, PID: os.Getpid(),
		Hostname: hostname(t), StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#183",
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	if _, err := runmarker.Create(sylHome, project, runDir, state.TicketRef, state.PID, state.Hostname); err != nil {
		t.Fatal(err)
	}

	if err := readmodel.Dismiss(sylHome, runDir); !errors.Is(err, readmodel.ErrRunNotInterrupted) {
		t.Fatalf("Dismiss() error = %v, want ErrRunNotInterrupted", err)
	}
	markers, err := runmarker.List(sylHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(markers) != 1 {
		t.Fatalf("markers = %#v, want live marker preserved", markers)
	}
}

func TestForgetDelegatesToRegistry(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	writeRegistry(t, sylHome, project)

	if err := readmodel.Forget(sylHome, project); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	entries, err := registry.List(sylHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("registry entries = %#v, want empty", entries)
	}
}

func TestDismissReportsInvalidAndUnknownRuns(t *testing.T) {
	sylHome := t.TempDir()
	if err := readmodel.Dismiss(sylHome, " "); err == nil {
		t.Fatal("Dismiss() with blank Run directory succeeded")
	}
	if err := readmodel.Dismiss(sylHome, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("Dismiss() with missing Run directory succeeded")
	}
	runDir := t.TempDir()
	if err := readmodel.Dismiss(sylHome, runDir); !errors.Is(err, readmodel.ErrRunMarkerNotFound) {
		t.Fatalf("Dismiss() error = %v, want ErrRunMarkerNotFound", err)
	}
	if err := readmodel.Dismiss("", runDir); err == nil {
		t.Fatal("Dismiss() with invalid syl home succeeded")
	}
}

func TestDismissReportsUnreadableRunState(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "corrupt")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runstate.Path(runDir), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	createMarker(t, sylHome, project, runDir, "#183", 999999, hostname(t))

	if err := readmodel.Dismiss(sylHome, runDir); err == nil {
		t.Fatal("Dismiss() with corrupt Run state succeeded")
	}
}

func TestDismissRefusesFinishedAndRemoteRuns(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     runstate.Status
		markerHost string
	}{
		{name: "finished", status: runstate.Approved, markerHost: hostname(t)},
		{name: "remote", status: runstate.Running, markerHost: "remote-host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sylHome := t.TempDir()
			project := t.TempDir()
			runDir := filepath.Join(project, ".syl", "runs", test.name)
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatal(err)
			}
			state := runstate.State{
				Status: test.status, Activity: runstate.Implementing, PID: 999999,
				Hostname: hostname(t), StartedAt: time.Now().UTC(), Kind: runstate.Implement,
			}
			if err := runstate.Write(runstate.Path(runDir), state); err != nil {
				t.Fatal(err)
			}
			createMarker(t, sylHome, project, runDir, "#183", state.PID, test.markerHost)

			if err := readmodel.Dismiss(sylHome, runDir); !errors.Is(err, readmodel.ErrRunNotInterrupted) {
				t.Fatalf("Dismiss() error = %v, want ErrRunNotInterrupted", err)
			}
		})
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
