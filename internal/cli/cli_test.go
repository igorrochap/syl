package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/updater"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/version"
)

func TestRunHelpListsAllCommands(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"--help"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	for _, command := range []string{"init", "sync", "plan", "implement", "review", "usage", "version", "update"} {
		if !strings.Contains(fixture.stdout.String(), command) {
			t.Errorf("help output %q does not list %q", fixture.stdout.String(), command)
		}
	}
	if !strings.Contains(fixture.stdout.String(), "print the running binary's version") {
		t.Fatalf("help output %q, want version description", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "update syl to the latest release") {
		t.Fatalf("help output %q, want update description", fixture.stdout.String())
	}
}

func TestImplementHelpExplainsWorktreeDependencySetup(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"implement", "--help"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	for _, expected := range []string{
		"only the tracked tree",
		"[worktree] setup",
		"provision its dependencies",
	} {
		if !strings.Contains(fixture.stdout.String(), expected) {
			t.Fatalf("implement help = %q, want %q", fixture.stdout.String(), expected)
		}
	}
}

func TestRootsRouteCollaboratorsToTheirOwningRoot(t *testing.T) {
	originRoot := t.TempDir()
	workRoot := t.TempDir()
	configContents := `[tracker]
issues = "local"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-origin-planner"
effort = "high"

[roles.implement]
harness = "codex"
model = "gpt-origin-implementer"
effort = "high"

[roles.review]
harness = "claude"
model = "claude-origin-reviewer"
effort = "medium"
`
	if err := os.MkdirAll(filepath.Join(originRoot, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originRoot, ".syl", "config.toml"), []byte(configContents), 0o644); err != nil {
		t.Fatal(err)
	}
	writeReviewTicket(t, originRoot, "feature-a", "07", "Origin ticket", "Track the origin root.")

	adapter := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "root-split-review"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	gitRunner := &reviewGitRunner{diff: "diff --git a/work.txt b/work.txt\n+changed\n"}
	var harnessRoots []string
	var gitRoots []string
	var ghRoots []string
	app := New(originRoot, workRoot, Dependencies{
		Harnesses: func(root string) map[string]harness.Adapter {
			harnessRoots = append(harnessRoots, root)
			return map[string]harness.Adapter{"claude": adapter}
		},
		Git: func(root string) orchestration.GitRunner {
			gitRoots = append(gitRoots, root)
			return gitRunner
		},
		GH: func(root string) tracker.GHRunner {
			ghRoots = append(ghRoots, root)
			return fakeGHRunner{}
		},
	})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"review"}, &stdout, &stderr); code != 0 {
		t.Fatalf("review code = %d, stderr = %q", code, stderr.String())
	}
	if got := harnessRoots; len(got) != 1 || got[0] != workRoot {
		t.Fatalf("harness roots = %v, want [%s]", got, workRoot)
	}
	if got := gitRoots; len(got) != 1 || got[0] != workRoot {
		t.Fatalf("git roots = %v, want [%s]", got, workRoot)
	}
	if !strings.Contains(stdout.String(), "claude-origin-reviewer") {
		t.Fatalf("review output = %q, want configuration loaded from origin root", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(originRoot, ".syl", "runs")); err != nil {
		t.Fatalf("origin run artifacts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workRoot, ".syl")); !os.IsNotExist(err) {
		t.Fatalf("work-root syl state error = %v, want no work-root artifacts", err)
	}

	projectConfig, err := config.Load(originRoot)
	if err != nil {
		t.Fatal(err)
	}
	issueTracker, err := app.newIssueTracker(projectConfig)
	if err != nil {
		t.Fatal(err)
	}
	tickets, err := issueTracker.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Number != 7 {
		t.Fatalf("local tickets = %#v, want ticket #7 from origin root", tickets)
	}
	if got := ghRoots; len(got) != 0 {
		t.Fatalf("GitHub roots = %v, want no GitHub construction for local tracker", got)
	}
	if _, err := app.newIssueTracker(config.Config{Tracker: config.TrackerConfig{Issues: config.TrackerGitHub}}); err != nil {
		t.Fatal(err)
	}
	if got := ghRoots; len(got) != 1 || got[0] != originRoot {
		t.Fatalf("GitHub roots = %v, want [%s]", got, originRoot)
	}
}

func TestUsageRendersLatestAndNamedRunWithoutCrossHarnessTotal(t *testing.T) {
	root := t.TempDir()
	runsDir := filepath.Join(root, ".syl", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldRun := filepath.Join(runsDir, "20260820T120000.000000000Z-41")
	latestRun := filepath.Join(runsDir, "20260820T130000.000000000Z-42")
	for _, runDir := range []string{oldRun, latestRun} {
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := usage.WriteArtifact(filepath.Join(oldRun, "usage.json"), usage.Artifact{
		SchemaVersion: usage.SchemaVersion,
		Disclaimer:    usage.Disclaimer,
		Entries: []usage.Entry{{Iteration: 1, Role: "review", Harness: "claude", Model: "claude-sonnet-5", Tracked: true, Metrics: &usage.Metrics{
			InputTokens: 2, OutputTokens: 3, WeightedEstimate: 5,
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := usage.WriteArtifact(filepath.Join(latestRun, "usage.json"), usage.Artifact{
		SchemaVersion: usage.SchemaVersion,
		Disclaimer:    usage.Disclaimer,
		Entries: []usage.Entry{
			{Iteration: 1, Role: "implement", Harness: "codex", Model: "gpt-5.6-luna", Tracked: true, Metrics: &usage.Metrics{
				InputTokens: 2_000_000, CachedInputTokens: 1_920_000,
				CacheWriteInputTokens: 10, OutputTokens: 24_200, ReasoningOutputTokens: 13_700,
				TotalTokens: 2_024_200, WeightedEstimate: 999,
			}},
			{Iteration: 1, Role: "review", Harness: "claude", Model: "claude-sonnet-5", Tracked: true, Metrics: &usage.Metrics{
				InputTokens: 20, OutputTokens: 3, CacheWriteTokens: 8, CacheReadTokens: 10, WeightedEstimate: 34,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	app := New(root, root, Dependencies{})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"usage"}, &stdout, &stderr); code != 0 {
		t.Fatalf("latest usage code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{
		"iteration 1",
		"implement (codex, gpt-5.6-luna): input 2.0M (96% cached) · output 24.2k (13.7k reasoning)",
		"review (claude, claude-sonnet-5): weighted_estimate=34.00",
		"input_tokens=20",
		"cache_write_tokens=8",
		"cache_read_tokens=10",
		usage.Disclaimer,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("latest usage output = %q, want %q", output, expected)
		}
	}
	if strings.Contains(output, "implement (codex, gpt-5.6-luna): weighted_estimate") {
		t.Fatalf("latest usage output = %q, want raw Codex totals without weighted estimate", output)
	}
	if strings.Contains(output, "input_tokens=2 output_tokens=3") || strings.Contains(output, "total") {
		t.Fatalf("latest usage output = %q, want latest run only and no total", output)
	}

	stdout.Reset()
	stderr.Reset()
	if code := app.Run(context.Background(), []string{"usage", filepath.Base(oldRun)}, &stdout, &stderr); code != 0 {
		t.Fatalf("named usage code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review (claude, claude-sonnet-5): weighted_estimate=5.00") {
		t.Fatalf("named usage output = %q, want named run", stdout.String())
	}
}

func TestUsageRecomputesClaudeTranscriptWhenArtifactIsMissing(t *testing.T) {
	fixture := newTopSeamFixture(t)
	runDir := filepath.Join(fixture.root, ".syl", "runs", "20260820T120000.000000000Z-41")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte("Branch: feat/usage\nBranch point: abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "sessions.txt"), []byte("iteration 1 review: review-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "iteration-01-review.feed"), []byte("review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(fixture.root, string(filepath.Separator), "-"))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := `{"type":"assistant","timestamp":"2026-08-20T12:00:05Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, "review-session.jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(runDir, "iteration-01-review.feed"), mustUsageTime(t, "2026-08-20T12:00:10Z"), mustUsageTime(t, "2026-08-20T12:00:10Z")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("recomputed usage code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{
		"recomputed from transcripts — usage.json not found",
		"iteration 1",
		"review (claude, claude-sonnet-5): weighted_estimate=17.50 input_tokens=10 output_tokens=2 cache_write_tokens=4 cache_read_tokens=5",
		usage.Disclaimer,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("recomputed usage output = %q, want %q", output, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(runDir, "usage.json")); !os.IsNotExist(err) {
		t.Fatalf("usage.json stat error = %v, want artifact to remain absent", err)
	}
}

func TestUsageReportsRunDirectoryWhenArtifactIsMissingAndNoTranscriptsExist(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, ".syl", "runs", "20260820T120000.000000000Z-41")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	app := New(root, root, Dependencies{})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("missing usage code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "recomputed from transcripts — usage.json not found") {
		t.Fatalf("missing usage output = %q, want recomputed marker", stdout.String())
	}
}

func TestUsageRecomputesResumedClaudeSessionUsingArtifactWindows(t *testing.T) {
	fixture := newTopSeamFixture(t)
	runDir := createFallbackRun(t, fixture.root, "iteration 1 review: review-session\niteration 2 review: review-session\n")
	t.Setenv("HOME", t.TempDir())
	writeFallbackClaudeTranscript(t, fixture.root, "review-session", []string{
		`{"type":"assistant","timestamp":"2026-08-20T12:00:05Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"assistant","timestamp":"2026-08-20T12:00:15Z","message":{"id":"message-2","role":"assistant","usage":{"input_tokens":20,"output_tokens":2}}}`,
	})
	for _, artifact := range []string{"iteration-01-review.feed", "iteration-01-review.transcript"} {
		writeFallbackArtifactAt(t, runDir, artifact, "first\n", "2026-08-20T12:00:10Z")
	}
	for _, artifact := range []string{"iteration-02-review.feed", "iteration-02-review.transcript"} {
		writeFallbackArtifactAt(t, runDir, artifact, "second\n", "2026-08-20T12:00:20Z")
	}

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("resumed usage code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{
		"recomputed from transcripts — usage.json not found",
		"iteration 1\nreview (claude, claude-sonnet-5): weighted_estimate=11.00 input_tokens=10 output_tokens=1",
		"iteration 2\nreview (claude, claude-sonnet-5): weighted_estimate=22.00 input_tokens=20 output_tokens=2",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("resumed usage output = %q, want %q", output, expected)
		}
	}
	if strings.Contains(output, "iterations 1–2 combined") {
		t.Fatalf("resumed usage output = %q, want artifact-mtime-derived split", output)
	}
}

func TestUsageLabelsResumedClaudeSessionCombinedWhenArtifactWindowsAreUnavailable(t *testing.T) {
	fixture := newTopSeamFixture(t)
	runDir := createFallbackRun(t, fixture.root, "iteration 1 review: review-session\niteration 2 review: review-session\n")
	t.Setenv("HOME", t.TempDir())
	writeFallbackClaudeTranscript(t, fixture.root, "review-session", []string{
		`{"type":"assistant","timestamp":"2026-08-20T12:00:05Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"assistant","timestamp":"2026-08-20T12:00:15Z","message":{"id":"message-2","role":"assistant","usage":{"input_tokens":20,"output_tokens":2}}}`,
	})

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("combined usage code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "review, iterations 1–2 combined (claude, claude-sonnet-5): weighted_estimate=33.00 input_tokens=30 output_tokens=3") {
		t.Fatalf("combined usage output = %q, want one labeled combined row", output)
	}
	if strings.Contains(output, "iteration 2\nreview (") {
		t.Fatalf("combined usage output = %q, want no guessed iteration split", output)
	}
}

func TestUsageReportsUnavailableFallbackRolesWithoutFailing(t *testing.T) {
	fixture := newTopSeamFixture(t)
	runDir := createFallbackRun(t, fixture.root, "iteration 1 implement: missing-codex\niteration 1 review: malformed-claude\n")
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(fixture.root, string(filepath.Separator), "-"))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "malformed-claude.jsonl"), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("unavailable usage code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{
		"implement (codex, gpt-5.6-luna): usage unavailable",
		"review (claude, claude-sonnet-5): usage unavailable",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("unavailable usage output = %q, want %q", output, expected)
		}
	}
}

func TestUsageRecomputesCodexRolloutWhenArtifactIsMissing(t *testing.T) {
	fixture := newTopSeamFixture(t)
	runDir := createFallbackRun(t, fixture.root, "iteration 1 implement: codex-session\n")
	home := t.TempDir()
	t.Setenv("HOME", home)
	rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "20")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := `{"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":900,"cache_write_input_tokens":25,"output_tokens":80,"reasoning_output_tokens":40,"total_tokens":1080}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-20260820T010203-codex-session.jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("Codex fallback code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "implement (codex, gpt-5.6-luna): input 1.0k (90% cached) · output 80 (40 reasoning)") {
		t.Fatalf("Codex fallback output = %q, want rollout usage", stdout.String())
	}
}

func TestUsageReportsNonexistentRunDirectoryAsAnError(t *testing.T) {
	fixture := newTopSeamFixture(t)
	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", "does-not-exist"}, &stdout, &stderr); code == 0 {
		t.Fatal("nonexistent usage code = 0, want error")
	}
	if !strings.Contains(stderr.String(), "run directory") || !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("nonexistent usage stderr = %q, want clear run-directory error", stderr.String())
	}
}

func createFallbackRun(t *testing.T, root, sessions string) string {
	t.Helper()
	runDir := filepath.Join(root, ".syl", "runs", "20260820T120000.000000000Z-41")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte("Branch: feat/usage\nBranch point: abc123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "sessions.txt"), []byte(sessions), 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func writeFallbackClaudeTranscript(t *testing.T, root, sessionID string, lines []string) {
	t.Helper()
	home := os.Getenv("HOME")
	projectDir := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(root, string(filepath.Separator), "-"))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFallbackArtifactAt(t *testing.T, runDir, name, contents, modifiedAt string) {
	t.Helper()
	path := filepath.Join(runDir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	modified := mustUsageTime(t, modifiedAt)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func mustUsageTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestRunUpdateReportsVersionChange(t *testing.T) {
	originalVersion := version.Version
	version.Version = "v1.10.0"
	t.Cleanup(func() { version.Version = originalVersion })

	update := &fakeUpdater{result: updater.Result{
		CurrentVersion: "v1.10.0",
		LatestVersion:  "v1.11.0",
		Updated:        true,
	}}
	root := t.TempDir()
	app := New(root, root, Dependencies{Updater: update})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"update"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "updated v1.10.0 -> v1.11.0\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if update.currentVersion != "v1.10.0" {
		t.Fatalf("updater current version = %q, want v1.10.0", update.currentVersion)
	}
}

func TestRunUpdateReportsAlreadyUpToDate(t *testing.T) {
	originalVersion := version.Version
	version.Version = "v1.11.0"
	t.Cleanup(func() { version.Version = originalVersion })

	update := &fakeUpdater{result: updater.Result{
		CurrentVersion: "v1.11.0",
		LatestVersion:  "v1.11.0",
	}}
	root := t.TempDir()
	app := New(root, root, Dependencies{Updater: update})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"update"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "syl is already up to date (v1.11.0)\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunUpdateReportsInstallerFailure(t *testing.T) {
	originalVersion := version.Version
	version.Version = "v1.10.0"
	t.Cleanup(func() { version.Version = originalVersion })

	update := &fakeUpdater{err: fmt.Errorf("checksum verification failed for syl_Linux_amd64.tar.gz")}
	root := t.TempDir()
	app := New(root, root, Dependencies{Updater: update})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"update"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("Run() code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "checksum verification failed") {
		t.Fatalf("stderr = %q, want checksum error", stderr.String())
	}
}

type fakeUpdater struct {
	result         updater.Result
	err            error
	currentVersion string
}

func (u *fakeUpdater) Update(_ context.Context, currentVersion string) (updater.Result, error) {
	u.currentVersion = currentVersion
	return u.result, u.err
}

func TestRunVersionPrintsBuildMetadata(t *testing.T) {
	originalVersion, originalCommit := version.Version, version.Commit
	version.Version = "v1.2.3"
	version.Commit = "abc1234"
	t.Cleanup(func() {
		version.Version = originalVersion
		version.Commit = originalCommit
	})

	root := t.TempDir()
	app := New(root, root, Dependencies{})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "v1.2.3 (commit abc1234)\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunSubcommandHelpIsAvailableWithoutConfig(t *testing.T) {
	root := t.TempDir()
	app := New(root, root, Dependencies{})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"sync", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "synchronize installed skills") {
		t.Fatalf("stdout = %q, want sync help", stdout.String())
	}
}

func TestQuestionAnswerInputProtocolIsDocumentedInHelp(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"review", "--help"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review help code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "single-line answer from stdin") || !strings.Contains(fixture.stdout.String(), "trailing backslash") {
		t.Fatalf("review help = %q, want QUESTION input protocol", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "--context string") || !strings.Contains(fixture.stdout.String(), "additional context for the reviewer Role") {
		t.Fatalf("review help = %q, want reviewer context flag", fixture.stdout.String())
	}
}

func TestRunRefusesCommandsWithoutConfig(t *testing.T) {
	for _, command := range []string{"plan", "implement", "review"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			app := New(root, root, Dependencies{})
			var stdout, stderr strings.Builder

			code := app.Run(context.Background(), []string{command}, &stdout, &stderr)
			if code == 0 {
				t.Fatal("Run() code = 0, want failure")
			}
			if !strings.Contains(stderr.String(), "run syl init") {
				t.Fatalf("stderr = %q, want init guidance", stderr.String())
			}
		})
	}
}

func TestImplementResolvesAndMarksGitHubTicketDoing(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement"}},
			{{Type: harness.EventSession, SessionID: "review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	github := &implementGitHubRunner{}
	fixture.app.deps.GH = fixedGH(github)

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !github.hasCall("issue edit 42 --add-label doing --remove-label todo") {
		t.Fatalf("GitHub calls = %#v, want todo to doing transition", github.calls)
	}
	if github.hasCall("issue close 42") || github.hasCall("issue edit 42 --state closed") {
		t.Fatalf("GitHub calls = %#v, want no close operation", github.calls)
	}
}

func TestRunRejectsUnexpectedCommandArguments(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"sync", "unexpected"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("Run() code = 0, want argument validation failure")
	}
	if !strings.Contains(fixture.stderr.String(), `unknown command "unexpected"`) {
		t.Fatalf("stderr = %q, want Cobra argument error", fixture.stderr.String())
	}
}

func TestRunInitCreatesConfig(t *testing.T) {
	root := t.TempDir()
	app := New(root, root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".syl", "config.toml")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if _, err := config.Load(root); err != nil {
		t.Fatalf("init wrote invalid config: %v", err)
	}
	if !strings.Contains(stdout.String(), ".syl/config.toml") {
		t.Fatalf("stdout = %q, want config path", stdout.String())
	}
}

func TestNewDefaultsToCurrentDirectoryWhenOriginRootIsEmpty(t *testing.T) {
	root := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	app := New("", "", Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".syl", "config.toml")); err != nil {
		t.Fatalf("default project root config: %v", err)
	}
}

type implementGitHubRunner struct {
	calls []string
}

func (r *implementGitHubRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch call {
	case "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue view 42 --json number,title,body,state,labels":
		return `{"number":42,"title":"Implement this","body":"Details","state":"OPEN","labels":[{"name":"todo"}]}`, nil
	case "issue edit 42 --add-label doing --remove-label todo":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected gh command %q", call)
	}
}

func (r *implementGitHubRunner) hasCall(call string) bool {
	for _, got := range r.calls {
		if got == call {
			return true
		}
	}
	return false
}
