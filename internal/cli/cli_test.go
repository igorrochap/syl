package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
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

	app := New(root, Dependencies{})
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

func TestUsageReportsRunDirectoryWhenArtifactIsMissing(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, ".syl", "runs", "20260820T120000.000000000Z-41")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	app := New(root, Dependencies{})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"usage", filepath.Base(runDir)}, &stdout, &stderr); code != 0 {
		t.Fatalf("missing usage code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no usage.json found") || !strings.Contains(stdout.String(), runDir) {
		t.Fatalf("missing usage output = %q, want clear run directory guidance", stdout.String())
	}
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
	app := New(t.TempDir(), Dependencies{Updater: update})
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
	app := New(t.TempDir(), Dependencies{Updater: update})
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
	app := New(t.TempDir(), Dependencies{Updater: update})
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

	app := New(t.TempDir(), Dependencies{})
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
	app := New(t.TempDir(), Dependencies{})
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
}

func TestRunRefusesCommandsWithoutConfig(t *testing.T) {
	for _, command := range []string{"plan", "implement", "review"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			app := New(root, Dependencies{})
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
	fixture.app.deps.Harnesses["codex"] = loop
	fixture.app.deps.Harnesses["claude"] = loop
	github := &implementGitHubRunner{}
	fixture.app.deps.GH = github

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
	app := New(root, Dependencies{Input: defaultInitInput()})
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

func TestNewDefaultsToCurrentDirectoryWhenProjectRootIsEmpty(t *testing.T) {
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
	app := New("", Dependencies{Input: defaultInitInput()})
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
