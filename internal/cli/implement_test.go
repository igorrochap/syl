package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	commitgenadapter "github.com/igorrochap/syl/internal/adapters/commitgen"
	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/codex"
	"github.com/igorrochap/syl/internal/usage"
)

func TestImplementOutputMatchesPlainAndStyledGolden(t *testing.T) {
	for _, test := range []struct {
		name   string
		styled bool
	}{
		{name: "plain"},
		{name: "styled", styled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.styled {
				t.Setenv("NO_COLOR", "")
			} else {
				t.Setenv("NO_COLOR", "1")
			}
			fixture := newImplementLoopFixture(t)
			loop := &loopHarness{
				root: fixture.root,
				streams: [][]harness.Event{
					{
						{Type: harness.EventSession, SessionID: "implement-session"},
						{Type: harness.EventAssistantText, Text: "Implementer is working.\n"},
					},
					{
						{Type: harness.EventSession, SessionID: "review-session"},
						{Type: harness.EventAssistantText, Text: approveVerdictText},
					},
				},
			}
			fixture.harnesses["codex"] = loop
			fixture.harnesses["claude"] = loop
			fixture.app.deps.GH = fixedGH(&loopGHRunner{})

			var output io.Writer = &strings.Builder{}
			if test.styled {
				output = newStyledTerminalCapture(t)
			}
			code := fixture.app.Run(context.Background(), []string{"implement", "--verbose", "#42"}, output, &fixture.stderr)
			if code != 0 {
				t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
			}
			actual := normalizeImplementGoldenOutput(output.(interface{ String() string }).String())
			assertCLIGolden(t, test.name+"-implement.golden", []byte(actual))
		})
	}
}

func normalizeImplementGoldenOutput(value string) string {
	const (
		pathPrefix = ".syl/runs/"
		pathSuffix = "-42"
	)
	for {
		start := strings.Index(value, pathPrefix)
		if start == -1 {
			return value
		}
		suffixOffset := strings.Index(value[start:], pathSuffix)
		if suffixOffset == -1 {
			return value
		}
		end := start + suffixOffset + len(pathSuffix)
		value = value[:start] + pathPrefix + "<run-dir>" + value[end:]
	}
}

func TestImplementLoopApprovesOnFirstIteration(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Implemented the ticket.\n"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	code := fixture.app.Run(context.Background(), []string{"implement", "42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}

	if got := gitOutput(t, fixture.root, "branch", "--show-current"); got != "feat/add-resilient-workflow" {
		t.Fatalf("branch = %q, want feat/add-resilient-workflow", got)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != branchPoint {
		t.Fatalf("HEAD = %q, want unchanged branch point %q", got, branchPoint)
	}
	if status := gitOutput(t, fixture.root, "status", "--porcelain"); !strings.Contains(status, "change.txt") {
		t.Fatalf("git status = %q, want uncommitted implementer change", status)
	}

	if len(implementer.requests) != 2 {
		t.Fatalf("harness requests = %d, want implement and review", len(implementer.requests))
	}
	implementPrompt := implementer.requests[0].Prompt
	for _, expected := range []string{"/implement", "Add resilient workflow", "Acceptance criteria: leave a working implementation.", "QUESTION:", "Ambiguity should have been resolved during planning"} {
		if !strings.Contains(implementPrompt, expected) {
			t.Fatalf("implement prompt = %q, want %q", implementPrompt, expected)
		}
	}
	for _, expected := range []string{
		branchPoint,
		"Do not read or write review documents; the invoking tool records the verdict.",
		"The verdict block you print is the only record.",
	} {
		if !strings.Contains(implementer.requests[1].Prompt, expected) {
			t.Fatalf("review prompt = %q, want %q", implementer.requests[1].Prompt, expected)
		}
	}
	if !strings.Contains(fixture.stdout.String(), "Iterations: 1") || !strings.Contains(fixture.stdout.String(), "Final verdict: approve") {
		t.Fatalf("stdout = %q, want final loop summary", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "Nit findings:\n- (none)") {
		t.Fatalf("stdout = %q, want empty nit findings in final summary", fixture.stdout.String())
	}

	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	artifactText := readAllFiles(t, runDirs[0])
	for _, expected := range []string{"implement-session", "review-session", "VERDICT: approve"} {
		if !strings.Contains(artifactText, expected) {
			t.Fatalf("run artifacts = %q, want %q", artifactText, expected)
		}
	}
}

func TestImplementPrOnApproveRejectsLocalTrackerBeforeHarnessStarts(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	configPath := filepath.Join(fixture.root, ".syl", "config.toml")
	configContents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configContents = []byte(strings.Replace(string(configContents), `issues = "github"`, `issues = "local"`, 1))
	if err := os.WriteFile(configPath, configContents, 0o644); err != nil {
		t.Fatal(err)
	}
	ticketPath := filepath.Join(fixture.root, ".scratch", "feature-a", "issues", "42-local-workflow.md")
	if err := os.MkdirAll(filepath.Dir(ticketPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ticketPath, []byte("# 42 — Add resilient workflow\n\n**Status:** todo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, fixture.root, "local tracker fixture")
	loop := &loopHarness{root: fixture.root}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "GitHub or GitLab Tracker") {
		t.Fatalf("implement code = %d, stderr = %q, want supported Tracker explanation", code, fixture.stderr.String())
	}
	if len(loop.requests) != 0 {
		t.Fatalf("harness requests = %d, want none", len(loop.requests))
	}
}

func TestImplementPrOnApproveRequiresOriginBeforeHarnessStarts(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{root: fixture.root}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "--pr-on-approve requires an origin remote") {
		t.Fatalf("implement code = %d, stderr = %q, want missing-origin error", code, fixture.stderr.String())
	}
	if len(loop.requests) != 0 {
		t.Fatalf("harness requests = %d, want none", len(loop.requests))
	}
}

func TestImplementPrOnApprovePublishesApprovedChanges(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	bareRemote := createBareOrigin(t, fixture.root)
	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	startBranch := gitOutput(t, fixture.root, "symbolic-ref", "--quiet", "--short", "HEAD")
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictWithNitText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	gh := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(gh)
	notifier := &recordingNotifier{}
	fixture.app.deps.Notifier = notifier

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	branch := "feat/add-resilient-workflow"
	if got := gitOutput(t, fixture.root, "rev-list", "--count", branchPoint+"..HEAD"); got != "1" {
		t.Fatalf("commits since branch point = %q, want exactly one", got)
	}
	if got := gitOutput(t, fixture.root, "log", "-1", "--format=%s"); got != "feat: Add resilient workflow (#42)" {
		t.Fatalf("commit message = %q, want fallback conventional message", got)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); got != "origin/"+branch {
		t.Fatalf("upstream = %q, want origin/%s", got, branch)
	}
	if got := gitOutput(t, fixture.root, "--git-dir="+bareRemote, "show-ref", "--verify", "refs/heads/"+branch); got == "" {
		t.Fatal("bare origin does not contain the implementation branch")
	}
	if got := gitOutput(t, fixture.root, "status", "--porcelain"); got != "" {
		t.Fatalf("working tree = %q, want clean after publication", got)
	}
	if !strings.Contains(fixture.stdout.String(), "Pull request: https://github.com/example/project/pull/23") {
		t.Fatalf("stdout = %q, want pull request URL in summary", fixture.stdout.String())
	}
	if len(notifier.messages) != 1 || !strings.Contains(notifier.messages[0], "https://github.com/example/project/pull/23") {
		t.Fatalf("notifications = %v, want pull request URL", notifier.messages)
	}
	if len(gh.calls) == 0 || !strings.HasPrefix(gh.calls[len(gh.calls)-1], "pr create --draft ") {
		t.Fatalf("GitHub calls = %#v, want draft PR as final Tracker operation", gh.calls)
	}
	prCall := gh.calls[len(gh.calls)-1]
	for _, expected := range []string{
		"--title Add resilient workflow",
		"Closes #42\n\nThe implementation meets the ticket.\n\n- [ ] internal/example.go:9 — Rename this helper.",
		"--head " + branch,
		"--base " + startBranch,
	} {
		if !strings.Contains(prCall, expected) {
			t.Errorf("draft PR call = %q, want %q", prCall, expected)
		}
	}
	for index, call := range gh.calls {
		if strings.Contains(call, "issue edit 42") && index > 0 && index >= len(gh.calls)-1 {
			t.Fatalf("GitHub calls = %#v, want no status update after PR creation", gh.calls)
		}
	}
	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("Run directories = %v, error = %v; want one Run", runDirs, err)
	}
	metadata, err := os.ReadFile(filepath.Join(runDirs[0], "metadata.txt"))
	if err != nil || !strings.Contains(string(metadata), "Publish on approve: true") {
		t.Fatalf("metadata = %q, error = %v; want Publish on approve", metadata, err)
	}
	summary, err := os.ReadFile(filepath.Join(runDirs[0], "summary.txt"))
	if err != nil || !strings.Contains(string(summary), "Pull request: https://github.com/example/project/pull/23") {
		t.Fatalf("summary = %q, error = %v; want pull request URL", summary, err)
	}
}

func TestImplementPrOnApprovePublishesGitLabDraftMergeRequest(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	setIssueTracker(t, fixture.root, config.TrackerGitLab)
	commitWorkingTree(t, fixture.root, "GitLab tracker fixture")
	createBareOrigin(t, fixture.root)
	branch := gitOutput(t, fixture.root, "symbolic-ref", "--quiet", "--short", "HEAD")
	glab := configureApprovedGitLabPublicationHarness(fixture)
	notifier := &recordingNotifier{}
	fixture.app.deps.Notifier = notifier

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	const mergeRequestURL = "https://gitlab.com/group/project/-/merge_requests/23"
	if !strings.Contains(fixture.stdout.String(), "Pull request: "+mergeRequestURL) {
		t.Fatalf("stdout = %q, want merge request URL in summary", fixture.stdout.String())
	}
	if len(notifier.messages) != 1 || !strings.Contains(notifier.messages[0], mergeRequestURL) {
		t.Fatalf("notifications = %v, want merge request URL", notifier.messages)
	}
	createCall := "mr create --draft --title Add resilient workflow --description Closes #42\n\nThe implementation meets the ticket.\n\n- [ ] internal/example.go:9 — Rename this helper. --source-branch feat/add-resilient-workflow --target-branch " + branch + " --yes"
	if !glab.hasCall(createCall) {
		t.Fatalf("GitLab calls = %#v, want draft merge request arguments", glab.calls)
	}
	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("Run directories = %v, error = %v; want one Run", runDirs, err)
	}
	summary, err := os.ReadFile(filepath.Join(runDirs[0], "summary.txt"))
	if err != nil || !strings.Contains(string(summary), "Pull request: "+mergeRequestURL) ||
		!strings.Contains(string(summary), "Publication: published") {
		t.Fatalf("summary = %q, error = %v; want published Run record with merge request URL", summary, err)
	}
}

func TestImplementPrOnApproveReportsExistingGitLabMergeRequest(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	setIssueTracker(t, fixture.root, config.TrackerGitLab)
	commitWorkingTree(t, fixture.root, "GitLab tracker fixture")
	createBareOrigin(t, fixture.root)
	const mergeRequestURL = "https://gitlab.com/group/project/-/merge_requests/17"
	glab := configureApprovedGitLabPublicationHarness(fixture)
	glab.existingURL = mergeRequestURL

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 || !strings.Contains(fixture.stdout.String(), mergeRequestURL) {
		t.Fatalf("implement code = %d, stdout = %q, stderr = %q; want successful existing merge request URL", code, fixture.stdout.String(), fixture.stderr.String())
	}
	if glab.hasCreateCall() {
		t.Fatalf("GitLab calls = %#v, want existing merge request lookup without creation", glab.calls)
	}
}

func TestImplementPrOnApproveOmitsGitLabTargetForDetachedStart(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	setIssueTracker(t, fixture.root, config.TrackerGitLab)
	commitWorkingTree(t, fixture.root, "GitLab tracker fixture")
	createBareOrigin(t, fixture.root)
	gitOutput(t, fixture.root, "switch", "--detach", "HEAD")
	glab := configureApprovedGitLabPublicationHarness(fixture)

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	createCall := glab.createCall()
	if !strings.HasPrefix(createCall, "mr create --draft ") || strings.Contains(createCall, "--target-branch ") {
		t.Fatalf("draft merge request call = %q, want detached start without target branch", createCall)
	}
}

func TestImplementPrOnApproveUsesGitLabDefaultBaseWithCommitgenFromDetachedStart(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	setIssueTracker(t, fixture.root, config.TrackerGitLab)
	commitWorkingTree(t, fixture.root, "GitLab tracker fixture")
	createBareOrigin(t, fixture.root)
	gitOutput(t, fixture.root, "switch", "--detach", "HEAD")
	glab := configureApprovedGitLabPublicationHarness(fixture)
	commitgen := &fakeCommitgenRunner{
		available: true,
		prOutput:  "Title: feat: generated GitLab title\n\nGenerated GitLab description.",
		commit: func(ctx context.Context, workRoot string) error {
			_, err := fixture.app.gitRunner(workRoot).Run(ctx, "commit", "-m", "feat: generated publication")
			return err
		},
	}
	fixture.app.deps.Commitgen = commitgen

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(commitgen.calls) != 2 || !reflect.DeepEqual(commitgen.calls[1].args, []string{
		"pr", "--agent", "--base", "main", "--head", "feat/add-resilient-workflow",
	}) {
		t.Fatalf("commitgen calls = %#v, want GitLab forge default base", commitgen.calls)
	}
	createCall := glab.createCall()
	if !strings.Contains(createCall, "--title feat: generated GitLab title") || !strings.Contains(createCall, "--target-branch main") {
		t.Fatalf("draft merge request call = %q, want generated title and forge default base", createCall)
	}
}

func TestImplementPrOnApproveReportsGitLabMergeRequestFailureCommand(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	setIssueTracker(t, fixture.root, config.TrackerGitLab)
	commitWorkingTree(t, fixture.root, "GitLab tracker fixture")
	createBareOrigin(t, fixture.root)
	glab := configureApprovedGitLabPublicationHarness(fixture)
	glab.createError = exec.ErrNotFound

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "publication failed during pull request") ||
		!strings.Contains(fixture.stderr.String(), "glab is not installed") ||
		!strings.Contains(fixture.stderr.String(), "glab mr create --draft") {
		t.Fatalf("implement code = %d, stderr = %q, want GitLab merge request recovery command", code, fixture.stderr.String())
	}
	if strings.Contains(fixture.stderr.String(), "gh pr create") {
		t.Fatalf("stderr = %q, want GitLab recovery command only", fixture.stderr.String())
	}
}

func TestImplementPrOnApproveUsesCommitgenForCommitAndPullRequestText(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	createBareOrigin(t, fixture.root)
	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	gh := configureApprovedPublicationHarness(fixture)
	commitgen := &fakeCommitgenRunner{
		available: true,
		prOutput:  "Title: feat: add x\n\nGenerated details.\n\nCloses #42",
		commit: func(ctx context.Context, workRoot string) error {
			_, err := fixture.app.gitRunner(workRoot).Run(ctx, "commit", "-m", "feat: generated publication")
			return err
		},
	}
	fixture.app.deps.Commitgen = commitgen

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if got := gitOutput(t, fixture.root, "rev-list", "--count", branchPoint+"..HEAD"); got != "1" {
		t.Fatalf("commits since branch point = %q, want exactly one commitgen commit", got)
	}
	if got := gitOutput(t, fixture.root, "log", "-1", "--format=%s"); got != "feat: generated publication" {
		t.Fatalf("commit message = %q, want commitgen-created message", got)
	}
	if len(commitgen.calls) != 2 || commitgen.calls[0].workRoot != fixture.root || commitgen.calls[1].workRoot != fixture.root {
		t.Fatalf("commitgen calls = %#v, want commit and PR in the work root", commitgen.calls)
	}
	if !reflect.DeepEqual(commitgen.calls[0].args, []string{"--agent", "--context", "Ticket #42: Add resilient workflow"}) {
		t.Fatalf("commitgen commit arguments = %#v", commitgen.calls[0].args)
	}
	if !reflect.DeepEqual(commitgen.calls[1].args, []string{"pr", "--agent", "--base", "main", "--head", "feat/add-resilient-workflow"}) {
		t.Fatalf("commitgen PR arguments = %#v", commitgen.calls[1].args)
	}
	for _, forbidden := range []string{"--provider", "--model", "--language"} {
		if slices.Contains(commitgen.calls[0].args, forbidden) || slices.Contains(commitgen.calls[1].args, forbidden) {
			t.Fatalf("commitgen arguments contain %q: %#v", forbidden, commitgen.calls)
		}
	}
	prCall := gh.calls[len(gh.calls)-1]
	for _, expected := range []string{
		"--title feat: add x",
		"Generated details.\n\nCloses #42\n\n- [ ] internal/example.go:9 — Rename this helper.",
		"--base main",
	} {
		if !strings.Contains(prCall, expected) {
			t.Errorf("draft PR call = %q, want %q", prCall, expected)
		}
	}
	bodyEnd := strings.Index(prCall, " --head ")
	if strings.Count(prCall, "Closes #42") != 1 || bodyEnd < 0 ||
		!strings.HasSuffix(prCall[:bodyEnd], "- [ ] internal/example.go:9 — Rename this helper.") {
		t.Fatalf("draft PR call = %q, want one close reference and nit checklist at the end", prCall)
	}
	if strings.Contains(fixture.stdout.String(), "commitgen") {
		t.Fatalf("stdout = %q, want no fallback notice when commitgen succeeds", fixture.stdout.String())
	}
}

func TestImplementPrOnApprovePassesProjectLanguageToCommitgen(t *testing.T) {
	for _, test := range []struct {
		name     string
		language string
	}{
		{name: "commitgen default"},
		{name: "project override", language: "pt_br"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImplementLoopFixture(t)
			createBareOrigin(t, fixture.root)
			configureApprovedPublicationHarness(fixture)
			if test.language != "" {
				projectConfig, err := config.Load(fixture.root)
				if err != nil {
					t.Fatal(err)
				}
				projectConfig.Publish.Language = test.language
				if _, err := config.Write(fixture.root, projectConfig, config.OverwriteExisting); err != nil {
					t.Fatal(err)
				}
				commitWorkingTree(t, fixture.root, "configure publication language")
			}
			logPath := installCommitgenScript(t, false)
			fixture.app.deps.Commitgen = commitgenadapter.Runner{}

			code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
			}
			calls := readCommitgenScriptCalls(t, logPath)
			if len(calls) != 2 {
				t.Fatalf("commitgen calls = %#v, want commit and PR calls", calls)
			}
			for index, args := range calls {
				languageIndex := slices.Index(args, "--language")
				if test.language == "" {
					if languageIndex >= 0 {
						t.Errorf("commitgen call %d arguments = %#v, want no --language", index, args)
					}
					continue
				}
				if languageIndex < 0 || languageIndex+1 >= len(args) || args[languageIndex+1] != test.language {
					t.Errorf("commitgen call %d arguments = %#v, want --language %s", index, args, test.language)
				}
			}
		})
	}
}

func TestImplementPrOnApproveIncludesCommitgenLanguageFailureInFallbackNotice(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	createBareOrigin(t, fixture.root)
	gh := configureApprovedPublicationHarness(fixture)
	gh.issueTitle = "Adicionar suporte à publicação"
	projectConfig, err := config.Load(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	projectConfig.Publish.Language = "xx"
	if _, err := config.Write(fixture.root, projectConfig, config.OverwriteExisting); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, fixture.root, "configure publication language")
	logPath := installCommitgenScript(t, true)
	fixture.app.deps.Commitgen = commitgenadapter.Runner{}

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if got := gitOutput(t, fixture.root, "log", "-1", "--format=%s"); got != "feat: Adicionar suporte à publicação (#42)" {
		t.Fatalf("fallback commit message = %q, want ticket title preserved", got)
	}
	for _, expected := range []string{
		"used fallback commit message",
		"used fallback pull request text",
		"language xx not supported",
	} {
		if !strings.Contains(fixture.stdout.String(), expected) {
			t.Errorf("stdout = %q, want %q", fixture.stdout.String(), expected)
		}
	}
	prCall := gh.calls[len(gh.calls)-1]
	if !strings.Contains(prCall, "--title Adicionar suporte à publicação") ||
		!strings.Contains(prCall, "The implementation meets the ticket.") {
		t.Fatalf("draft PR call = %q, want original ticket title and English fallback body", prCall)
	}
	if calls := readCommitgenScriptCalls(t, logPath); len(calls) != 2 {
		t.Fatalf("commitgen calls = %#v, want failed commit and PR calls", calls)
	}
}

func installCommitgenScript(t *testing.T, failUnsupported bool) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commitgen-args.log")
	script := `#!/bin/sh
printf 'CALL\n' >> "$COMMITGEN_LOG"
printf '%s\n' "$@" >> "$COMMITGEN_LOG"
printf 'END\n' >> "$COMMITGEN_LOG"
if [ "$COMMITGEN_FAIL_UNSUPPORTED" = "true" ]; then
  case " $* " in
    *" --language xx "*) printf '%s\n' 'language xx not supported' >&2; exit 1 ;;
  esac
fi
if [ "$1" = "pr" ]; then
  printf '%s\n' 'Title: feat: generated PR' '' 'Generated details.'
  exit 0
fi
git commit -m 'feat: generated publication' >/dev/null
`
	path := filepath.Join(bin, "commitgen")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMMITGEN_LOG", logPath)
	if failUnsupported {
		t.Setenv("COMMITGEN_FAIL_UNSUPPORTED", "true")
	} else {
		t.Setenv("COMMITGEN_FAIL_UNSUPPORTED", "false")
	}
	return logPath
}

func readCommitgenScriptCalls(t *testing.T, path string) [][]string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var current []string
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		switch line {
		case "CALL":
			current = []string{}
		case "END":
			calls = append(calls, current)
		default:
			current = append(current, line)
		}
	}
	return calls
}

func TestImplementPrOnApproveFallsBackWhenCommitgenCannotCommit(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "exits non-zero", mode: "fail"},
		{name: "commits then exits non-zero", mode: "fail-after-commit"},
		{name: "leaves HEAD unchanged", mode: "noop"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImplementLoopFixture(t)
			createBareOrigin(t, fixture.root)
			branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
			gh := configureApprovedPublicationHarness(fixture)
			commitgen := &fakeCommitgenRunner{
				available: true,
				prOutput:  "Title: feat: generated PR\n\nGenerated description.",
			}
			if test.mode == "fail" {
				commitgen.commitErr = fmt.Errorf("simulated commitgen failure")
			}
			if test.mode == "fail-after-commit" {
				commitgen.commit = func(ctx context.Context, workRoot string) error {
					if _, err := fixture.app.gitRunner(workRoot).Run(ctx, "commit", "-m", "feat: generated publication"); err != nil {
						return err
					}
					return fmt.Errorf("simulated commitgen failure after commit")
				}
			}
			fixture.app.deps.Commitgen = commitgen

			code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
			}
			if got := gitOutput(t, fixture.root, "rev-list", "--count", branchPoint+"..HEAD"); got != "1" {
				t.Fatalf("commits since branch point = %q, want one fallback commit", got)
			}
			if got := gitOutput(t, fixture.root, "log", "-1", "--format=%s"); got != "feat: Add resilient workflow (#42)" {
				t.Fatalf("commit message = %q, want fallback commit message", got)
			}
			if !strings.Contains(fixture.stdout.String(), "used fallback commit message") {
				t.Fatalf("stdout = %q, want commitgen fallback notice", fixture.stdout.String())
			}
			if strings.Contains(fixture.stdout.String(), "used fallback pull request text") {
				t.Fatalf("stdout = %q, want generated pull request text", fixture.stdout.String())
			}
			if len(gh.calls) == 0 || !strings.Contains(gh.calls[len(gh.calls)-1], "--title feat: generated PR") {
				t.Fatalf("GitHub calls = %#v, want commitgen pull request title", gh.calls)
			}
			prCall := gh.calls[len(gh.calls)-1]
			bodyEnd := strings.Index(prCall, " --head ")
			if strings.Count(prCall, "Closes #42") != 1 || bodyEnd < 0 ||
				!strings.HasSuffix(prCall[:bodyEnd], "- [ ] internal/example.go:9 — Rename this helper.") {
				t.Fatalf("draft PR call = %q, want one appended close reference and nit checklist at the end", prCall)
			}
		})
	}
}

func TestImplementPrOnApproveFallsBackWhenCommitgenPRTextIsUnusable(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		exit   string
	}{
		{name: "command fails", output: "Title: ignored", exit: "1"},
		{name: "no title", output: " \n\t\n", exit: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImplementLoopFixture(t)
			createBareOrigin(t, fixture.root)
			gh := configureApprovedPublicationHarness(fixture)
			commitgen := &fakeCommitgenRunner{
				available: true,
				prOutput:  test.output,
				commit: func(ctx context.Context, workRoot string) error {
					_, err := fixture.app.gitRunner(workRoot).Run(ctx, "commit", "-m", "feat: generated publication")
					return err
				},
			}
			if test.exit == "1" {
				commitgen.prErr = fmt.Errorf("simulated commitgen pr failure")
			}
			fixture.app.deps.Commitgen = commitgen

			code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
			}
			if !strings.Contains(fixture.stdout.String(), "used fallback pull request text") {
				t.Fatalf("stdout = %q, want pull request fallback notice", fixture.stdout.String())
			}
			prCall := gh.calls[len(gh.calls)-1]
			bodyEnd := strings.Index(prCall, " --head ")
			if !strings.Contains(prCall, "--title Add resilient workflow") ||
				!strings.Contains(prCall, "Closes #42\n\nThe implementation meets the ticket.") ||
				bodyEnd < 0 || !strings.HasSuffix(prCall[:bodyEnd], "- [ ] internal/example.go:9 — Rename this helper.") {
				t.Fatalf("draft PR call = %q, want fallback title, body, and nit checklist", prCall)
			}
		})
	}
}

func TestImplementPrOnApproveUsesForgeDefaultBaseForCommitgenFromDetachedStart(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	createBareOrigin(t, fixture.root)
	gitOutput(t, fixture.root, "switch", "--detach", "HEAD")
	loopGH := configureApprovedPublicationHarness(fixture)
	commitgen := &fakeCommitgenRunner{
		available: true,
		prOutput:  "Titulo: feat: add x\n\nGenerated description.",
		commit: func(ctx context.Context, workRoot string) error {
			_, err := fixture.app.gitRunner(workRoot).Run(ctx, "commit", "-m", "feat: generated publication")
			return err
		},
	}
	fixture.app.deps.Commitgen = commitgen

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(commitgen.calls) != 2 || !reflect.DeepEqual(commitgen.calls[1].args, []string{
		"pr", "--agent", "--base", "main", "--head", "feat/add-resilient-workflow",
	}) {
		t.Fatalf("commitgen calls = %#v, want forge default base", commitgen.calls)
	}
	prCall := loopGH.calls[len(loopGH.calls)-1]
	if !strings.Contains(prCall, "--base main") || !strings.Contains(prCall, "--title feat: add x") {
		t.Fatalf("draft PR call = %q, want generated title and the same forge default base", prCall)
	}
}

func TestImplementPrOnApproveFallsBackWhenCommitgenIsUnavailable(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	createBareOrigin(t, fixture.root)
	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	configureApprovedPublicationHarness(fixture)
	commitgen := &fakeCommitgenRunner{}
	fixture.app.deps.Commitgen = commitgen

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if got := gitOutput(t, fixture.root, "rev-list", "--count", branchPoint+"..HEAD"); got != "1" {
		t.Fatalf("commits since branch point = %q, want one fallback commit", got)
	}
	if got := gitOutput(t, fixture.root, "log", "-1", "--format=%s"); got != "feat: Add resilient workflow (#42)" {
		t.Fatalf("commit message = %q, want fallback commit message", got)
	}
	for _, notice := range []string{
		"commitgen unavailable; used fallback commit message",
		"commitgen unavailable; used fallback pull request text",
	} {
		if !strings.Contains(fixture.stdout.String(), notice) {
			t.Errorf("stdout = %q, want %q", fixture.stdout.String(), notice)
		}
	}
	if len(commitgen.calls) != 0 {
		t.Fatalf("commitgen calls = %#v, want none when the runner is unavailable", commitgen.calls)
	}
}

func TestImplementPrOnApproveSkipsPublicationForEmptyDiff(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	bareRemote := createBareOrigin(t, fixture.root)
	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	loop := &loopHarness{
		root: fixture.root, noChanges: true,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	gh := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(gh)

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if got := gitOutput(t, fixture.root, "rev-list", "--count", branchPoint+"..HEAD"); got != "0" {
		t.Fatalf("commits since branch point = %q, want zero", got)
	}
	if got := gitOutput(t, fixture.root, "status", "--porcelain"); got != "" {
		t.Fatalf("working tree = %q, want clean", got)
	}
	if strings.Contains(strings.Join(gh.calls, "\n"), "pr create") {
		t.Fatalf("GitHub calls = %#v, want no pull request operation", gh.calls)
	}
	if strings.Contains(gitOutput(t, fixture.root, "--git-dir="+bareRemote, "for-each-ref", "--format=%(refname)", "refs/heads"), "feat/add-resilient-workflow") {
		t.Fatal("bare origin contains an implementation branch after an empty diff")
	}
	if !strings.Contains(fixture.stdout.String(), "No changes to publish; skipped commit, push, and pull request.") {
		t.Fatalf("stdout = %q, want empty-diff notice", fixture.stdout.String())
	}
}

func TestImplementPrOnApproveUsesNoBaseForDetachedStart(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	createBareOrigin(t, fixture.root)
	gitOutput(t, fixture.root, "switch", "--detach", "HEAD")
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	gh := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(gh)

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	prCall := gh.calls[len(gh.calls)-1]
	if !strings.HasPrefix(prCall, "pr create --draft ") || strings.Contains(prCall, "--base ") {
		t.Fatalf("draft PR call = %q, want no base for detached start", prCall)
	}
}

func TestImplementPrOnApproveUsesExplicitWorktreeBase(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	bareRemote := createBareOrigin(t, fixture.root)
	gitOutput(t, fixture.root, "branch", "-M", "main")
	gitOutput(t, fixture.root, "push", "-u", "origin", "main")
	gitOutput(t, fixture.root, "fetch", "origin", "main")
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeRoot(t, fixture.root, worktreeRoot)
	loop := &loopHarness{
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "worktree-implement"}},
			{{Type: harness.EventSession, SessionID: "worktree-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	var worktreePath string
	fixture.app.deps.Harnesses = func(root string) map[string]harness.Adapter {
		worktreePath = root
		loop.root = root
		return map[string]harness.Adapter{"codex": loop, "claude": loop}
	}
	gh := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(gh)

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve", "--worktree", "--base", "origin/main"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement worktree code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if worktreePath == "" {
		t.Fatal("worktree harness did not run")
	}
	prCall := gh.calls[len(gh.calls)-1]
	if !strings.Contains(prCall, "--base main") || strings.Contains(prCall, "--base origin/main") {
		t.Fatalf("draft PR call = %q, want normalized base main", prCall)
	}
	branch := "feat/add-resilient-workflow"
	if got := gitOutput(t, worktreePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); got != "origin/"+branch {
		t.Fatalf("worktree upstream = %q, want origin/%s", got, branch)
	}
	if got := gitOutput(t, fixture.root, "--git-dir="+bareRemote, "show-ref", "--verify", "refs/heads/"+branch); got == "" {
		t.Fatal("bare origin does not contain the worktree implementation branch")
	}
}

func TestImplementPrOnApproveDoesNotPublishExhaustedRun(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	bareRemote := createBareOrigin(t, fixture.root)
	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "implement-3"}},
			{{Type: harness.EventSession, SessionID: "review-3"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	gh := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(gh)

	code := fixture.app.Run(context.Background(), []string{"implement", "42", "--pr-on-approve"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || len(loop.requests) != 6 {
		t.Fatalf("implement code = %d, harness requests = %d, want exhausted failure after three iterations", code, len(loop.requests))
	}
	if got := gitOutput(t, fixture.root, "rev-list", "--count", branchPoint+"..HEAD"); got != "0" {
		t.Fatalf("commits since branch point = %q, want zero", got)
	}
	if strings.Contains(strings.Join(gh.calls, "\n"), "pr create") {
		t.Fatalf("GitHub calls = %#v, want no pull request operation", gh.calls)
	}
	if refs := gitOutput(t, fixture.root, "--git-dir="+bareRemote, "for-each-ref", "--format=%(refname)", "refs/heads"); strings.Contains(refs, "feat/add-resilient-workflow") {
		t.Fatalf("bare origin refs = %q, want no implementation branch", refs)
	}
}

func createBareOrigin(t *testing.T, root string) string {
	t.Helper()
	bareRemote := filepath.Join(t.TempDir(), "origin.git")
	command := exec.Command("git", "init", "--bare", "--quiet", bareRemote)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create bare origin: %v\n%s", err, output)
	}
	gitOutput(t, root, "config", "user.name", "syl test")
	gitOutput(t, root, "config", "user.email", "syl@example.test")
	gitOutput(t, root, "remote", "add", "origin", bareRemote)
	return bareRemote
}

func configureApprovedPublicationHarness(fixture *implementLoopFixture) *loopGHRunner {
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictWithNitText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	gh := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(gh)
	return gh
}

func configureApprovedGitLabPublicationHarness(fixture *implementLoopFixture) *publishingGitLabRunner {
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictWithNitText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	glab := &publishingGitLabRunner{}
	fixture.app.deps.GLab = fixedGLab(glab)
	return glab
}

type publishingGitLabRunner struct {
	calls       []string
	existingURL string
	createError error
}

func (runner *publishingGitLabRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	runner.calls = append(runner.calls, call)
	switch call {
	case "label list --output json --per-page 100":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue view 42 --output json":
		return `{"id":9042,"iid":42,"title":"Add resilient workflow","description":"Acceptance criteria: leave a working implementation.","state":"opened","labels":["todo"]}`, nil
	case "api --method PUT projects/:fullpath/issues/42 --raw-field labels=doing":
		return "", nil
	case "mr list --source-branch feat/add-resilient-workflow --output json --per-page 1":
		if runner.existingURL != "" {
			return fmt.Sprintf(`[{"web_url":%q}]`, runner.existingURL), nil
		}
		return "[]", nil
	case "repo view --output json --jq .default_branch":
		return "main\n", nil
	default:
		if strings.HasPrefix(call, "mr create --draft ") {
			if runner.createError != nil {
				return "", runner.createError
			}
			return "!23 Draft: Add resilient workflow (feat/add-resilient-workflow)\n https://gitlab.com/group/project/-/merge_requests/23\n", nil
		}
		return "", fmt.Errorf("unexpected glab command %q", call)
	}
}

func (runner *publishingGitLabRunner) hasCall(want string) bool {
	for _, call := range runner.calls {
		if call == want {
			return true
		}
	}
	return false
}

func (runner *publishingGitLabRunner) hasCreateCall() bool {
	return runner.createCall() != ""
}

func (runner *publishingGitLabRunner) createCall() string {
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "mr create --draft ") {
			return call
		}
	}
	return ""
}

type fakeCommitgenCall struct {
	workRoot string
	args     []string
}

type fakeCommitgenRunner struct {
	available bool
	calls     []fakeCommitgenCall
	commit    func(context.Context, string) error
	commitErr error
	prOutput  string
	prErr     error
}

func (r *fakeCommitgenRunner) Available() bool { return r.available }

func (r *fakeCommitgenRunner) Run(ctx context.Context, workRoot string, args ...string) (string, error) {
	r.calls = append(r.calls, fakeCommitgenCall{workRoot: workRoot, args: append([]string(nil), args...)})
	if len(args) > 0 && args[0] == "pr" {
		return r.prOutput, r.prErr
	}
	if r.commit != nil {
		if err := r.commit(ctx, workRoot); err != nil {
			return "", err
		}
	}
	return "", r.commitErr
}

func TestBareImplementDoesNotRunWorktreeSetup(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeSetup(t, fixture.root, worktreeRoot, "printf bare-setup-should-not-run >&2; exit 17")

	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "bare-implement"}},
			{{Type: harness.EventSession, SessionID: "bare-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.app.deps.Harnesses = func(string) map[string]harness.Adapter {
		return map[string]harness.Adapter{"codex": loop, "claude": loop}
	}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("bare implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if strings.Contains(fixture.stderr.String(), "bare-setup-should-not-run") {
		t.Fatalf("stderr = %q, want no worktree setup output", fixture.stderr.String())
	}
}

func TestImplementWritesClaudeUsagePerIterationIncludingSubagents(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
		},
	}
	reviewer := &usageTranscriptHarness{projectRoot: fixture.root, home: home}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = reviewer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	artifact, err := usage.ReadArtifact(filepath.Join(runDirs[0], "usage.json"))
	if err != nil {
		t.Fatalf("read usage artifact: %v", err)
	}
	if len(artifact.Entries) != 4 {
		t.Fatalf("usage entries = %#v, want implement and review for two iterations", artifact.Entries)
	}
	firstReview := findUsageEntry(t, artifact, 1, "review")
	if !firstReview.Tracked || firstReview.Metrics == nil {
		t.Fatalf("first review usage = %#v, want tracked metrics", firstReview)
	}
	if firstReview.Metrics.InputTokens != 15 || firstReview.Metrics.OutputTokens != 4 || firstReview.Metrics.CacheWriteTokens != 6 || firstReview.Metrics.CacheReadTokens != 3 {
		t.Fatalf("first review metrics = %#v, want parent plus sub-agent usage", *firstReview.Metrics)
	}
	secondReview := findUsageEntry(t, artifact, 2, "review")
	if !secondReview.Tracked || secondReview.Metrics == nil {
		t.Fatalf("second review usage = %#v, want tracked metrics", secondReview)
	}
	if secondReview.Metrics.InputTokens != 20 || secondReview.Metrics.OutputTokens != 3 || secondReview.Metrics.CacheWriteTokens != 8 || secondReview.Metrics.CacheReadTokens != 10 {
		t.Fatalf("second review metrics = %#v, want only resumed invocation usage", *secondReview.Metrics)
	}
	if implementEntry := findUsageEntry(t, artifact, 1, "implement"); implementEntry.Tracked || implementEntry.Reason == "" {
		t.Fatalf("Codex usage = %#v, want untracked reason", implementEntry)
	}
}

func findUsageEntry(t *testing.T, artifact usage.Artifact, iteration int, role string) usage.Entry {
	t.Helper()
	for _, entry := range artifact.Entries {
		if entry.Iteration == iteration && entry.Role == role {
			return entry
		}
	}
	t.Fatalf("usage artifact has no iteration %d %s entry: %#v", iteration, role, artifact.Entries)
	return usage.Entry{}
}

type usageTranscriptHarness struct {
	projectRoot string
	home        string
	resumeCount int
}

func (h *usageTranscriptHarness) Run(_ context.Context, _ harness.Request) (harness.Stream, error) {
	if err := h.appendTranscript("review-session", "review-message-1", 10, 2, 4, 1); err != nil {
		return nil, err
	}
	if err := h.writeSubagentTranscript("review-session", "sub-message-1", 5, 2, 2, 2); err != nil {
		return nil, err
	}
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: reviseVerdictText},
	}}, nil
}

func (h *usageTranscriptHarness) Resume(_ context.Context, _ string, _ harness.Request) (harness.Stream, error) {
	h.resumeCount++
	if err := h.appendTranscript("review-session", "review-message-2", 20, 3, 8, 10); err != nil {
		return nil, err
	}
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}, nil
}

func (*usageTranscriptHarness) Attach(context.Context, harness.Request) error {
	return fmt.Errorf("unexpected harness attach")
}

func (*usageTranscriptHarness) AttachSession(context.Context, string, harness.Request) error {
	return fmt.Errorf("unexpected harness session attach")
}

func (h *usageTranscriptHarness) appendTranscript(sessionID, messageID string, input, output, cacheWrite, cacheRead int) error {
	return h.writeTranscript(sessionID, messageID, input, output, cacheWrite, cacheRead, false)
}

func (h *usageTranscriptHarness) writeSubagentTranscript(sessionID, messageID string, input, output, cacheWrite, cacheRead int) error {
	return h.writeTranscript(sessionID, messageID, input, output, cacheWrite, cacheRead, true)
}

func (h *usageTranscriptHarness) writeTranscript(sessionID, messageID string, input, output, cacheWrite, cacheRead int, subagent bool) error {
	slug := strings.ReplaceAll(h.projectRoot, string(filepath.Separator), "-")
	projectDir := filepath.Join(h.home, ".claude", "projects", slug)
	path := filepath.Join(projectDir, sessionID+".jsonl")
	if subagent {
		path = filepath.Join(projectDir, sessionID, "subagents", messageID+".jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"role":"assistant","usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), messageID, input, output, cacheWrite, cacheRead)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(line)
	return err
}

func TestImplementFailsAfterOneUnparseableReviewReaskAndSavesTranscript(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	harness := &unparseableReviewImplementHarness{root: fixture.root}
	fixture.harnesses["codex"] = harness
	fixture.harnesses["claude"] = harness
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--review-context", "reviewer-only context"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "reviewer produced no parseable verdict") {
		t.Fatalf("implement code = %d, stderr = %q, want clear unparseable-verdict failure", code, fixture.stderr.String())
	}
	report := "Uncommitted changes are kept in the work root: " + fixture.root + "\n  A  change.txt"
	if !strings.Contains(fixture.stderr.String(), report) {
		t.Fatalf("stderr = %q, want uncommitted work report %q", fixture.stderr.String(), report)
	}
	if strings.Index(fixture.stderr.String(), report) < strings.Index(fixture.stderr.String(), "reviewer produced no parseable verdict") {
		t.Fatalf("stderr = %q, want work report after original error", fixture.stderr.String())
	}
	if harness.resumeCount != 1 {
		t.Fatalf("resume count = %d, want exactly one review re-ask", harness.resumeCount)
	}
	if len(harness.resumeRequests) != 1 || harness.resumeRequests[0].Prompt != "emit the verdict block" {
		t.Fatalf("resume requests = %#v, want exactly the context-free verdict re-ask", harness.resumeRequests)
	}
	if len(harness.requests) != 2 {
		t.Fatalf("harness requests = %d, want one implement and one review request", len(harness.requests))
	}
	if strings.Contains(fixture.stdout.String(), "reviewer — verdict was unparseable") {
		t.Fatalf("stdout = %q, want no synthetic finding", fixture.stdout.String())
	}
	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	if !strings.Contains(fixture.stderr.String(), runDirs[0]) {
		t.Fatalf("stderr = %q, want the failed run artifact directory %q", fixture.stderr.String(), runDirs[0])
	}
	artifacts := readAllFiles(t, runDirs[0])
	if !strings.Contains(artifacts, "The review did not produce a verdict.") || !strings.Contains(artifacts, "The re-ask also omitted the verdict.") {
		t.Fatalf("run artifacts = %q, want the full failed review transcript", artifacts)
	}
}

func TestImplementReviewContextReachesReviewerPromptsOnly(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	reviewerContext := "Ignore the vendored skills directory."
	loop := &resumingLoopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
		},
		resumes: [][]harness.Event{
			{{Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(
		context.Background(),
		[]string{"implement", "42", "--review-context", reviewerContext},
		&fixture.stdout,
		&fixture.stderr,
	)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.requests) != 2 || len(loop.implementResumeCalls) != 1 || len(loop.resumeCalls) != 1 {
		t.Fatalf("harness requests = %d, implement resumes = %d, review resumes = %d, want two fresh requests and one resume per role", len(loop.requests), len(loop.implementResumeCalls), len(loop.resumeCalls))
	}
	if !strings.Contains(loop.requests[1].Prompt, reviewerContext) {
		t.Fatalf("first reviewer prompt = %q, want reviewer context %q", loop.requests[1].Prompt, reviewerContext)
	}
	if strings.Contains(loop.requests[0].Prompt, reviewerContext) || strings.Contains(loop.implementResumeCalls[0].request.Prompt, reviewerContext) {
		t.Fatalf("implementer prompts = %q and %q, want no reviewer context %q", loop.requests[0].Prompt, loop.implementResumeCalls[0].request.Prompt, reviewerContext)
	}
	resumePrompt := loop.resumeCalls[0].request.Prompt
	for _, expected := range []string{
		reviewerContext,
		"internal/orchestration/review.go:42",
		"handle a missing session",
	} {
		if !strings.Contains(resumePrompt, expected) {
			t.Fatalf("resume prompt = %q, want %q", resumePrompt, expected)
		}
	}
	if strings.Contains(resumePrompt, "/code-review") {
		t.Fatalf("resume prompt = %q, want incremental review prompt", resumePrompt)
	}
}

func TestImplementContextsReachTheirRolesOnly(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	implementerContext := "Keep the existing adapter seam."
	reviewerContext := "Ignore the vendored skills directory."
	loop := &resumingLoopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
		},
		resumes: [][]harness.Event{
			{{Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(
		context.Background(),
		[]string{"implement", "42", "--implement-context", implementerContext, "--review-context", reviewerContext},
		&fixture.stdout,
		&fixture.stderr,
	)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.requests) != 2 || len(loop.implementResumeCalls) != 1 || len(loop.resumeCalls) != 1 {
		t.Fatalf("harness requests = %d, implement resumes = %d, review resumes = %d, want two fresh requests and one resume per role", len(loop.requests), len(loop.implementResumeCalls), len(loop.resumeCalls))
	}
	for _, request := range []harness.Request{loop.requests[0], loop.implementResumeCalls[0].request} {
		if !strings.Contains(request.Prompt, implementerContext) || strings.Contains(request.Prompt, reviewerContext) {
			t.Fatalf("implementer prompt = %q, want implementer context only", request.Prompt)
		}
	}
	if !strings.Contains(loop.requests[1].Prompt, reviewerContext) || strings.Contains(loop.requests[1].Prompt, implementerContext) {
		t.Fatalf("reviewer prompt = %q, want reviewer context only", loop.requests[1].Prompt)
	}
	resumePrompt := loop.resumeCalls[0].request.Prompt
	if !strings.Contains(resumePrompt, reviewerContext) || strings.Contains(resumePrompt, implementerContext) {
		t.Fatalf("resume prompt = %q, want reviewer context only", resumePrompt)
	}
}

func TestImplementLoopStreamsImplementerAndReviewerProgress(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{
				{Type: harness.EventSession, SessionID: "implement-session"},
				{Type: harness.EventAssistantText, Text: "Implementer is working.\n"},
				{Type: harness.EventToolUse, ToolName: "edit", ArgumentGist: "change.txt"},
			},
			{
				{Type: harness.EventSession, SessionID: "review-session"},
				{Type: harness.EventAssistantText, Text: "Reviewer is checking the diff.\n"},
				{Type: harness.EventAssistantText, Text: "Verify the diff first.\n"},
				{Type: harness.EventToolUse, ToolName: "cat", ArgumentGist: "iteration review diff"},
				{Type: harness.EventAssistantText, Text: "VER"},
				{Type: harness.EventAssistantText, Text: strings.TrimPrefix(approveVerdictText, "VER")},
			},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "--verbose", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}

	output := fixture.stdout.String()
	summaryIndex := strings.Index(output, "Iterations: 1")
	if summaryIndex < 0 {
		t.Fatalf("stdout = %q, want final summary", output)
	}
	for _, expected := range []string{
		"Implementer is working.",
		"tool: edit — change.txt",
		"Reviewer is checking the diff.",
		"Verify the diff first.",
		"tool: cat — iteration review diff",
	} {
		index := strings.Index(output, expected)
		if index < 0 || index > summaryIndex {
			t.Fatalf("stdout = %q, want %q before final summary", output, expected)
		}
	}
	if !strings.Contains(output, "VERDICT: approve") {
		t.Fatalf("stdout = %q, want the rendered reviewer verdict", output)
	}
	if strings.Contains(output, "│ ") || strings.Contains(output, "◆ Implementer") || strings.Contains(output, "◆ Reviewer") {
		t.Fatalf("stdout = %q, want plain output without role decoration", output)
	}
}

func TestImplementLoopQuietlyShowsProgressWithoutToolCalls(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{
				{Type: harness.EventSession, SessionID: "implement-session"},
				{Type: harness.EventAssistantText, Text: "Implementer internal prose.\n"},
				{Type: harness.EventToolUse, ToolName: "edit", ArgumentGist: "change.txt"},
			},
			{
				{Type: harness.EventSession, SessionID: "review-session"},
				{Type: harness.EventAssistantText, Text: "Reviewer internal prose.\n"},
				{Type: harness.EventToolUse, ToolName: "cat", ArgumentGist: "iteration review diff"},
				{Type: harness.EventAssistantText, Text: approveVerdictText},
			},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}

	output := fixture.stdout.String()
	for _, expected := range []string{
		"iteration 1/3 — implementing",
		"iteration 1/3 — reviewing",
		"Implementer internal prose.",
		"Reviewer internal prose.",
		"VERDICT: approve",
		"SUMMARY: The working tree is ready",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("stdout = %q, want %q", output, expected)
		}
	}
	if strings.Contains(output, "│ ") || strings.Contains(output, "◆ Implementer") || strings.Contains(output, "◆ Reviewer") {
		t.Fatalf("stdout = %q, want plain output without role decoration", output)
	}
	for _, suppressed := range []string{"tool: edit — change.txt", "tool: cat — iteration review diff"} {
		if strings.Contains(output, suppressed) {
			t.Fatalf("stdout = %q, want tool call %q suppressed", output, suppressed)
		}
	}
}

func TestImplementLoopUsesCodexAdapterAtProcessBoundary(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	argsPath := filepath.Join(fixture.root, "codex-args")
	home := t.TempDir()
	t.Setenv("HOME", home)
	rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "19")
	rolloutPath := filepath.Join(rolloutDir, "rollout-20260820T010203-codex-implement.jsonl")
	commandDir := t.TempDir()
	command := filepath.Join(commandDir, "codex")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("for arg in \"$@\"; do\n")
	script.WriteString("printf '%s' \"$arg\" | tr '\\n' '\\034'\n")
	script.WriteString("printf '\\n'\ndone > ")
	script.WriteString(shellQuote(argsPath))
	script.WriteString("\nprintf '%s\\n' 'implemented' > change.txt\n")
	script.WriteString("git add change.txt\n")
	script.WriteString("mkdir -p ")
	script.WriteString(shellQuote(rolloutDir))
	script.WriteByte('\n')
	script.WriteString("printf '%s\\n' ")
	script.WriteString(shellQuote(`{"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":5,"output_tokens":10,"reasoning_output_tokens":4,"total_tokens":110}}}}`))
	script.WriteString(" > ")
	script.WriteString(shellQuote(rolloutPath))
	script.WriteByte('\n')
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"codex-implement"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"Implemented the ticket."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":1,"output_tokens":1}}`,
	} {
		script.WriteString("printf '%s\\n' ")
		script.WriteString(shellQuote(line))
		script.WriteByte('\n')
	}
	if err := os.WriteFile(command, []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fixture.harnesses["codex"] = codex.New(fixture.root)
	fixture.harnesses["claude"] = &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}

	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(contents)), "\n")
	for i := range args {
		args[i] = strings.ReplaceAll(args[i], "\x1c", "\n")
	}
	for _, want := range []string{"exec", "--json", "gpt-5.6-luna", `model_reasoning_effort="xhigh"`} {
		if !containsArg(args, want) {
			t.Fatalf("Codex args = %#v, want argument %q", args, want)
		}
	}
	prompt := args[len(args)-1]
	if !strings.Contains(prompt, "$implement") || !strings.Contains(prompt, "Add resilient workflow") || !strings.Contains(prompt, "Acceptance criteria: leave a working implementation.") {
		t.Fatalf("Codex prompt = %q, want composed implement skill and ticket", prompt)
	}

	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	artifact, err := usage.ReadArtifact(filepath.Join(runDirs[0], "usage.json"))
	if err != nil {
		t.Fatalf("read usage artifact: %v", err)
	}
	implementEntry := findUsageEntry(t, artifact, 1, "implement")
	if !implementEntry.Tracked || implementEntry.Metrics == nil {
		t.Fatalf("Codex usage = %#v, want tracked metrics", implementEntry)
	}
	if implementEntry.Metrics.InputTokens != 100 || implementEntry.Metrics.CachedInputTokens != 80 ||
		implementEntry.Metrics.OutputTokens != 10 || implementEntry.Metrics.ReasoningOutputTokens != 4 ||
		implementEntry.Metrics.TotalTokens != 110 || implementEntry.Metrics.WeightedEstimate != 0 {
		t.Fatalf("Codex metrics = %#v, want raw rollout totals without weighted estimate", *implementEntry.Metrics)
	}
}

func TestImplementLoopFeedsBlockingFindingsIntoSecondImplementerPrompt(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "implement-2"}, {Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(implementer.requests) != 4 {
		t.Fatalf("harness requests = %d, want two implement and two review requests", len(implementer.requests))
	}
	for i, request := range implementer.requests {
		wantMCP := i%2 == 0
		if request.MCP != wantMCP {
			t.Fatalf("request %d MCP = %t, want %t", i+1, request.MCP, wantMCP)
		}
	}
	secondPrompt := implementer.requests[2].Prompt
	if !strings.Contains(secondPrompt, "/fix-review") || !strings.Contains(secondPrompt, "Address ONLY") || !strings.Contains(secondPrompt, "- [blocking] internal/orchestration/review.go:42 — handle a missing session") {
		t.Fatalf("second implementer prompt = %q, want only the verbatim blocking finding", secondPrompt)
	}
	if strings.Contains(secondPrompt, "Acceptance criteria: leave a working implementation.") {
		t.Fatalf("second implementer prompt = %q, want reviewer findings prompt instead of first-pass ticket prompt", secondPrompt)
	}
	for _, reviewRequest := range []harness.Request{implementer.requests[1], implementer.requests[3]} {
		for _, expected := range []string{
			"Do not read or write review documents; the invoking tool records the verdict.",
			"The verdict block you print is the only record.",
		} {
			if !strings.Contains(reviewRequest.Prompt, expected) {
				t.Fatalf("review prompt = %q, want %q", reviewRequest.Prompt, expected)
			}
		}
	}
	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	firstDiff, err := os.ReadFile(filepath.Join(runDirs[0], "iteration-01-review.diff"))
	if err != nil {
		t.Fatalf("read first review diff: %v", err)
	}
	secondDiff, err := os.ReadFile(filepath.Join(runDirs[0], "iteration-02-review.diff"))
	if err != nil {
		t.Fatalf("read second review diff: %v", err)
	}
	if string(firstDiff) == string(secondDiff) {
		t.Fatalf("review diffs = %q and %q, want recomputation after the second implementation", firstDiff, secondDiff)
	}
	diffNames := []string{"iteration-01-review.diff", "iteration-02-review.diff"}
	for iteration, reviewRequest := range []harness.Request{implementer.requests[1], implementer.requests[3]} {
		for _, expected := range []string{
			diffNames[iteration],
			"authoritative diff",
			"Do not run Git to re-derive the diff",
		} {
			if !strings.Contains(reviewRequest.Prompt, expected) {
				t.Fatalf("review prompt = %q, want %q", reviewRequest.Prompt, expected)
			}
		}
	}
	if !strings.Contains(fixture.stdout.String(), "Iterations: 2") || !strings.Contains(fixture.stdout.String(), "Final verdict: approve") {
		t.Fatalf("stdout = %q, want two-iteration approval summary", fixture.stdout.String())
	}
}

func TestImplementLoopSummaryUsesFinalVerdictNitsAndPreservesHistory(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	configureBannerMaxIterations(t, fixture.root, 2)
	commitWorkingTree(t, fixture.root, "configure iteration cap")
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{
				Type: harness.EventAssistantText,
				Text: "VERDICT: revise\nSUMMARY: First pass findings\nFINDINGS:\n" +
					"- [nit] first.go:1 — nit A\n" +
					"- [nit] shared.go:2 — nit B initial wording\n",
			}},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
			{{
				Type: harness.EventAssistantText,
				Text: "VERDICT: revise\nSUMMARY: Final pass findings\nFINDINGS:\n" +
					"- [nit] shared.go:2 — nit B final wording\n" +
					"- [nit] final.go:3 — nit C\n",
			}},
		},
	}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("implement code = 0, want non-zero when the final verdict reaches the iteration cap")
	}

	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	summary, err := os.ReadFile(filepath.Join(runDirs[0], "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summaryText := string(summary)
	if strings.Contains(summaryText, "nit A") || strings.Contains(summaryText, "nit B initial wording") {
		t.Fatalf("summary = %q, want only final iteration nit findings", summaryText)
	}
	for _, expected := range []string{"nit B final wording", "nit C"} {
		if !strings.Contains(summaryText, expected) {
			t.Fatalf("summary = %q, want final iteration finding %q", summaryText, expected)
		}
	}
	if strings.Count(summaryText, "- [nit]") != 2 {
		t.Fatalf("summary = %q, want exactly two final iteration nit findings", summaryText)
	}
	if strings.Index(summaryText, "nit B final wording") > strings.Index(summaryText, "nit C") {
		t.Fatalf("summary = %q, want final findings in reviewer order", summaryText)
	}

	firstVerdict, err := os.ReadFile(filepath.Join(runDirs[0], "iteration-01-verdict.txt"))
	if err != nil {
		t.Fatalf("read first verdict: %v", err)
	}
	if !strings.Contains(string(firstVerdict), "nit A") || !strings.Contains(string(firstVerdict), "nit B initial wording") {
		t.Fatalf("first verdict = %q, want complete first iteration verdict", firstVerdict)
	}
	finalVerdict, err := os.ReadFile(filepath.Join(runDirs[0], "iteration-02-verdict.txt"))
	if err != nil {
		t.Fatalf("read final verdict: %v", err)
	}
	if !strings.Contains(string(finalVerdict), "nit B final wording") || !strings.Contains(string(finalVerdict), "nit C") {
		t.Fatalf("final verdict = %q, want complete final iteration verdict", finalVerdict)
	}
}

func TestImplementContextReachesImplementerPromptsOnly(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	additionalContext := "Use the existing GitRunner seam.\nDo not add a new adapter."
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "implement-2"}, {Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(
		context.Background(),
		[]string{"implement", "42", "--implement-context", additionalContext},
		&fixture.stdout,
		&fixture.stderr,
	)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(implementer.requests) != 4 {
		t.Fatalf("harness requests = %d, want two implement and two review requests", len(implementer.requests))
	}

	for _, implementRequest := range []harness.Request{implementer.requests[0], implementer.requests[2]} {
		if !strings.Contains(implementRequest.Prompt, additionalContext) {
			t.Fatalf("implementer prompt = %q, want context %q", implementRequest.Prompt, additionalContext)
		}
	}
	secondPrompt := implementer.requests[2].Prompt
	for _, expected := range []string{
		"- [blocking] internal/orchestration/review.go:42 — handle a missing session",
		additionalContext,
	} {
		if !strings.Contains(secondPrompt, expected) {
			t.Fatalf("second implementer prompt = %q, want %q", secondPrompt, expected)
		}
	}
	for _, reviewRequest := range []harness.Request{implementer.requests[1], implementer.requests[3]} {
		if strings.Contains(reviewRequest.Prompt, additionalContext) {
			t.Fatalf("reviewer prompt = %q, want no implementer context %q", reviewRequest.Prompt, additionalContext)
		}
	}
}

func TestImplementLoopReviewDiffIncludesUntrackedFiles(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{
		root:                  fixture.root,
		leaveChangesUntracked: true,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	diff, err := os.ReadFile(filepath.Join(runDirs[0], "iteration-01-review.diff"))
	if err != nil {
		t.Fatalf("read review diff: %v", err)
	}
	for _, want := range []string{
		"diff --git a/change.txt b/change.txt",
		"new file mode",
		"--- /dev/null",
		"+++ b/change.txt",
		"+implemented-1",
	} {
		if !strings.Contains(string(diff), want) {
			t.Errorf("review diff does not contain %q:\n%s", want, diff)
		}
	}
	if staged := gitOutput(t, fixture.root, "diff", "--cached"); staged != "" {
		t.Fatalf("staged diff = %q, want untracked implementation file", staged)
	}
}

func TestImplementLoopResumesPreviousReviewerSession(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &resumingLoopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventSession, SessionID: "review-1a"}, {Type: harness.EventSession, SessionID: "review-1b"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
		},
		resumes: [][]harness.Event{
			{{Type: harness.EventAssistantText, Text: "The blocking finding is resolved, but I omitted the structured verdict."}},
			{{Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.requests) != 2 {
		t.Fatalf("harness Run requests = %d, want fresh implement and review turns only", len(loop.requests))
	}
	if len(loop.implementResumeCalls) != 1 || loop.implementResumeCalls[0].sessionID != "implement-1" {
		t.Fatalf("implement Resume calls = %#v, want iteration 2 to resume implement-1", loop.implementResumeCalls)
	}
	if len(loop.resumeCalls) != 2 {
		t.Fatalf("harness Resume calls = %d, want warm re-review plus one verdict re-ask", len(loop.resumeCalls))
	}
	if got := loop.resumeCalls[0].sessionID; got != "review-1b" {
		t.Fatalf("resumed session = %q, want last review session review-1b", got)
	}
	if got := loop.resumeCalls[1].request.Prompt; got != "emit the verdict block" {
		t.Fatalf("verdict re-ask prompt = %q, want mandatory verdict prompt", got)
	}

	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	resumePrompt := loop.resumeCalls[0].request.Prompt
	for _, expected := range []string{
		filepath.Join(runDirs[0], "iteration-02-review.diff"),
		"internal/orchestration/review.go:42 — handle a missing session",
		"incremental re-review",
		"Do not spawn fresh Standards/Spec sub-agents",
		"mandatory verdict block",
	} {
		if !strings.Contains(resumePrompt, expected) {
			t.Fatalf("resume prompt = %q, want %q", resumePrompt, expected)
		}
	}
	if strings.Contains(resumePrompt, "/code-review") {
		t.Fatalf("resume prompt = %q, want no fresh review skill invocation", resumePrompt)
	}

	sessions, err := os.ReadFile(filepath.Join(runDirs[0], "sessions.txt"))
	if err != nil {
		t.Fatalf("read sessions.txt: %v", err)
	}
	for _, expected := range []string{"iteration 1 implement: implement-1", "iteration 2 implement: implement-1", "iteration 1 review: review-1a", "iteration 1 review: review-1b", "iteration 2 review: review-1b"} {
		if !strings.Contains(string(sessions), expected) {
			t.Fatalf("sessions.txt = %q, want %q", sessions, expected)
		}
	}
	for _, artifact := range []string{
		"iteration-02-review.feed",
		"iteration-02-review.transcript",
		"iteration-02-verdict.txt",
	} {
		if _, err := os.Stat(filepath.Join(runDirs[0], artifact)); err != nil {
			t.Fatalf("stat resumed review artifact %s: %v", artifact, err)
		}
	}
}

func TestImplementLoopFallsBackToFreshReviewWhenResumeCannotStart(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &resumingLoopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
		resumeErr: fmt.Errorf("review session is unavailable"),
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumeCalls) != 1 {
		t.Fatalf("harness Resume calls = %d, want one attempted warm re-review", len(loop.resumeCalls))
	}
	if len(loop.requests) != 3 {
		t.Fatalf("harness Run requests = %d, want fresh review fallback", len(loop.requests))
	}
	if !strings.Contains(loop.requests[2].Prompt, "/code-review") {
		t.Fatalf("fallback review prompt = %q, want full review prompt", loop.requests[2].Prompt)
	}
	if !strings.Contains(fixture.stdout.String(), "Iterations: 2") || !strings.Contains(fixture.stdout.String(), "Final verdict: approve") {
		t.Fatalf("stdout = %q, want successful two-iteration fallback summary", fixture.stdout.String())
	}
}

func TestImplementLoopFallsBackWhenResumedReviewStreamFails(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &resumingLoopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
		resumes: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: "The resumed review started, then failed."}},
		},
		resumeWaitErr: fmt.Errorf("review process exited during streaming"),
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumeCalls) != 1 {
		t.Fatalf("harness Resume calls = %d, want one failed warm re-review", len(loop.resumeCalls))
	}
	if len(loop.requests) != 3 || !strings.Contains(loop.requests[2].Prompt, "/code-review") {
		t.Fatalf("harness requests = %#v, want a fresh review after stream failure", loop.requests)
	}
	if !strings.Contains(fixture.stdout.String(), "Final verdict: approve") {
		t.Fatalf("stdout = %q, want successful fallback verdict", fixture.stdout.String())
	}
}

func TestImplementLoopStartsFreshReviewWithoutUsableSessionID(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &resumingLoopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}, {Type: harness.EventAssistantText, Text: "First pass.\n"}},
			{{Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumeCalls) != 0 {
		t.Fatalf("harness Resume calls = %d, want no resume without a usable session ID", len(loop.resumeCalls))
	}
	if len(loop.implementResumeCalls) != 1 {
		t.Fatalf("implement Resume calls = %d, want iteration 2 implement resume", len(loop.implementResumeCalls))
	}
	if len(loop.requests) != 3 || !strings.Contains(loop.requests[2].Prompt, "/code-review") {
		t.Fatalf("harness requests = %#v, want a fresh full review on iteration 2", loop.requests)
	}
}

func TestImplementLoopStillIteratesForNitOnlyRevisionAndSummarizesNits(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{
				{Type: harness.EventSession, SessionID: "review-1"},
				{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Polish the implementation\n"},
				{Type: harness.EventAssistantText, Text: "FINDINGS:\n- [nit] docs/example.md:3 — style could be clearer\n"},
			},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(implementer.requests[2].Prompt, "Address ONLY") || !strings.Contains(implementer.requests[2].Prompt, "Blocking findings:\n(none)") {
		t.Fatalf("second implementer prompt = %q, want a second iteration with no blocking findings", implementer.requests[2].Prompt)
	}
	if strings.Contains(implementer.requests[2].Prompt, "style could be clearer") {
		t.Fatalf("second implementer prompt = %q, want nit excluded from implementer instructions", implementer.requests[2].Prompt)
	}
	if !strings.Contains(fixture.stdout.String(), "Iterations: 2") || !strings.Contains(fixture.stdout.String(), "[nit] docs/example.md:3 — style could be clearer") {
		t.Fatalf("stdout = %q, want per-iteration nit verdict", fixture.stdout.String())
	}
	summaryStart := strings.Index(fixture.stdout.String(), "Iterations: 2")
	if summaryStart < 0 {
		t.Fatalf("stdout = %q, want final summary", fixture.stdout.String())
	}
	finalSummary := fixture.stdout.String()[summaryStart:]
	if !strings.Contains(finalSummary, "Nit findings:\n- (none)") {
		t.Fatalf("final summary = %q, want no nit findings", finalSummary)
	}
	if strings.Contains(finalSummary, "style could be clearer") {
		t.Fatalf("final summary = %q, want earlier nit omitted", finalSummary)
	}
	if !strings.Contains(fixture.stdout.String(), "iteration 2/3 — revising 0 blocking finding(s)") {
		t.Fatalf("stdout = %q, want revision transition with zero blocking findings", fixture.stdout.String())
	}
	if got := strings.Count(fixture.stdout.String(), "style could be clearer"); got != 1 {
		t.Fatalf("stdout = %q, want only the first iteration verdict to contain the nit", fixture.stdout.String())
	}
}

func TestImplementQuietAndVerboseRunsPreserveRunArtifacts(t *testing.T) {
	t.Setenv("GIT_AUTHOR_DATE", "2020-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2020-01-01T00:00:00Z")

	run := func(verbose bool) string {
		t.Helper()
		fixture := newImplementLoopFixture(t)
		loop := &loopHarness{
			root: fixture.root,
			streams: [][]harness.Event{
				{
					{Type: harness.EventSession, SessionID: "implement-session"},
					{Type: harness.EventAssistantText, Text: "Implementer prose.\n"},
					{Type: harness.EventToolUse, ToolName: "edit", ArgumentGist: "change.txt"},
				},
				{
					{Type: harness.EventSession, SessionID: "review-session"},
					{Type: harness.EventAssistantText, Text: "Reviewer prose.\n" + approveVerdictText},
				},
			},
		}
		fixture.harnesses["codex"] = loop
		fixture.harnesses["claude"] = loop
		fixture.app.deps.GH = fixedGH(&loopGHRunner{})
		args := []string{"implement", "#42"}
		if verbose {
			args = []string{"implement", "--verbose", "#42"}
		}
		if code := fixture.app.Run(context.Background(), args, &fixture.stdout, &fixture.stderr); code != 0 {
			t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
		}
		runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
		if err != nil || len(runDirs) != 1 {
			t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
		}
		artifacts := readAllFiles(t, runDirs[0])
		return strings.ReplaceAll(artifacts, "Work root: "+fixture.root+"\n", "Work root: <work root>\n")
	}

	quietArtifacts := run(false)
	verboseArtifacts := run(true)
	if quietArtifacts != verboseArtifacts {
		t.Fatalf("quiet artifacts = %q, verbose artifacts = %q; want byte-identical content", quietArtifacts, verboseArtifacts)
	}
}

func TestImplementLoopRunsAgainstLocalTracker(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	configPath := filepath.Join(fixture.root, ".syl", "config.toml")
	configContents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configContents = []byte(strings.Replace(string(configContents), `issues = "github"`, `issues = "local"`, 1))
	if err := os.WriteFile(configPath, configContents, 0o644); err != nil {
		t.Fatal(err)
	}
	ticketPath := filepath.Join(fixture.root, ".scratch", "feature-a", "issues", "42-local-workflow.md")
	if err := os.MkdirAll(filepath.Dir(ticketPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ticketPath, []byte("# 42 — Add resilient workflow\n\n**What to build:** Acceptance criteria: leave a working implementation.\n\n**Blocked by:** None — can start immediately.\n\n**Status:** todo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, fixture.root, "local tracker fixture")

	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "local-implement"}},
			{{Type: harness.EventSession, SessionID: "local-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	updatedTicket, err := os.ReadFile(ticketPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedTicket), "**Status:** doing") {
		t.Fatalf("local ticket = %q, want doing status", updatedTicket)
	}
	if len(loop.requests) != 2 || !strings.Contains(loop.requests[0].Prompt, "Add resilient workflow") {
		t.Fatalf("local tracker harness requests = %#v, want implement and review with ticket", loop.requests)
	}
}

func TestImplementLoopCapReturnsNonZeroAfterThreeRevisions(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
			{{Type: harness.EventSession, SessionID: "implement-3"}},
			{{Type: harness.EventSession, SessionID: "review-3"}, {Type: harness.EventAssistantText, Text: reviseVerdictText}},
		},
	}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("implement code = 0, want non-zero when the iteration cap is reached")
	}
	if len(implementer.requests) != 6 {
		t.Fatalf("harness requests = %d, want three implement and three review requests", len(implementer.requests))
	}
	if !strings.Contains(fixture.stdout.String(), "Iterations: 3") || !strings.Contains(fixture.stdout.String(), "Final verdict: revise") {
		t.Fatalf("stdout = %q, want capped revise summary", fixture.stdout.String())
	}
	for _, expected := range []string{
		"iteration 2/3 — revising 1 blocking finding(s)",
		"iteration 3/3 — revising 1 blocking finding(s)",
	} {
		if !strings.Contains(fixture.stdout.String(), expected) {
			t.Fatalf("stdout = %q, want %q", fixture.stdout.String(), expected)
		}
	}
	if !strings.Contains(fixture.stderr.String(), "max iterations") {
		t.Fatalf("stderr = %q, want iteration-cap error", fixture.stderr.String())
	}
}

func TestImplementRefusesDirtyTreeBeforeBranchOrStatusChange(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, "preexisting.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	implementer := &loopHarness{root: fixture.root}
	fixture.harnesses["codex"] = implementer
	fixture.harnesses["claude"] = implementer
	github := &loopGHRunner{}
	fixture.app.deps.GH = fixedGH(github)
	initialBranch := gitOutput(t, fixture.root, "branch", "--show-current")

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "working tree is dirty") {
		t.Fatalf("implement code = %d, stderr = %q, want dirty-tree refusal", code, fixture.stderr.String())
	}
	if got := gitOutput(t, fixture.root, "branch", "--show-current"); got != initialBranch {
		t.Fatalf("branch = %q, want %q after dirty-tree refusal", got, initialBranch)
	}
	if len(implementer.requests) != 0 {
		t.Fatalf("harness requests = %d, want none", len(implementer.requests))
	}
	for _, call := range github.calls {
		if strings.Contains(call, "issue edit") {
			t.Fatalf("GitHub calls = %#v, want no status update", github.calls)
		}
	}
}

func TestImplementWorktreeLeavesOriginUntouchedAndReviewsDiffInsideWorktree(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	notifier := &recordingNotifier{}
	fixture.app.deps.Notifier = notifier
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeRoot(t, fixture.root, worktreeRoot)
	if err := os.WriteFile(filepath.Join(fixture.root, "preexisting.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loop := &loopHarness{
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "worktree-implement"}},
			{{Type: harness.EventSession, SessionID: "worktree-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	harnessRoots := make([]string, 0, 2)
	fixture.app.deps.Harnesses = func(root string) map[string]harness.Adapter {
		harnessRoots = append(harnessRoots, root)
		loop.root = root
		return map[string]harness.Adapter{"codex": loop, "claude": loop}
	}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	initialBranch := gitOutput(t, fixture.root, "branch", "--show-current")
	initialHead := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	initialStatus := gitOutput(t, fixture.root, "status", "--porcelain", "--untracked-files=all")

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--worktree"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement worktree code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(harnessRoots) != 2 || harnessRoots[0] != harnessRoots[1] {
		t.Fatalf("harness roots = %v, want two calls for one worktree", harnessRoots)
	}
	worktreePath := harnessRoots[0]
	if worktreePath == fixture.root || !strings.HasPrefix(worktreePath, worktreeRoot+string(filepath.Separator)) {
		t.Fatalf("worktree path = %q, want a dedicated path below %q", worktreePath, worktreeRoot)
	}

	if got := gitOutput(t, fixture.root, "branch", "--show-current"); got != initialBranch {
		t.Fatalf("origin branch = %q, want %q", got, initialBranch)
	}
	if got := gitOutput(t, fixture.root, "rev-parse", "HEAD"); got != initialHead {
		t.Fatalf("origin HEAD = %q, want %q", got, initialHead)
	}
	if got := gitOutput(t, fixture.root, "status", "--porcelain", "--untracked-files=all"); got != initialStatus {
		t.Fatalf("origin status = %q, want %q", got, initialStatus)
	}
	if got := gitOutput(t, worktreePath, "branch", "--show-current"); got != "feat/add-resilient-workflow" {
		t.Fatalf("worktree branch = %q, want implementation branch", got)
	}

	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("origin run directories = %v, err = %v; want one run artifact", runDirs, err)
	}
	diffPath := filepath.Join(worktreePath, ".syl", "runs", filepath.Base(runDirs[0]), "iteration-01-review.diff")
	diff, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("read worktree review diff: %v", err)
	}
	if !strings.Contains(string(diff), "diff --git a/change.txt b/change.txt") {
		t.Fatalf("worktree review diff = %q, want implementation change", diff)
	}
	summary, err := os.ReadFile(filepath.Join(runDirs[0], "summary.txt"))
	if err != nil {
		t.Fatalf("read origin summary: %v", err)
	}
	if !strings.Contains(string(summary), "Worktree: "+worktreePath) ||
		!strings.Contains(string(summary), "git worktree remove --force "+worktreePath) {
		t.Fatalf("origin summary = %q, want path and cleanup command", summary)
	}
	if !strings.Contains(fixture.stdout.String(), "Worktree: "+worktreePath) ||
		!strings.Contains(fixture.stdout.String(), "git worktree remove --force "+worktreePath) {
		t.Fatalf("worktree output = %q, want path and cleanup command", fixture.stdout.String())
	}
	if !strings.Contains(loop.requests[1].Prompt, diffPath) {
		t.Fatalf("review prompt = %q, want worktree diff path %q", loop.requests[1].Prompt, diffPath)
	}
	wantNotification := "[project: " + filepath.Base(fixture.root) + " | branch: feat/add-resilient-workflow] implement #42 finished: approve"
	if len(notifier.messages) != 1 || notifier.messages[0] != wantNotification {
		t.Fatalf("notifications = %v, want %q", notifier.messages, wantNotification)
	}
}

func TestImplementWorktreeCleansUpWhenHarnessSetupFails(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeRoot(t, fixture.root, worktreeRoot)

	var worktreePath string
	fixture.app.deps.Harnesses = func(root string) map[string]harness.Adapter {
		worktreePath = root
		return map[string]harness.Adapter{}
	}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--worktree"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), `implement harness "codex" is not configured`) {
		t.Fatalf("implement worktree code = %d, stderr = %q; want harness setup failure", code, fixture.stderr.String())
	}
	if worktreePath == "" {
		t.Fatal("harness factory was not called")
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree path stat error = %v, want path removed", err)
	}
	if got := strings.TrimSpace(gitOutput(t, fixture.root, "branch", "--list", "--format=%(refname:short)", "feat/add-resilient-workflow")); got != "" {
		t.Fatalf("implementation branch = %q, want branch removed", got)
	}
	if got := gitOutput(t, fixture.root, "worktree", "list", "--porcelain"); strings.Contains(got, worktreePath) {
		t.Fatalf("registered worktrees = %q, want failed worktree removed", got)
	}
}

func TestImplementWorktreeRunsSetupBeforeHarnessInWorktree(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeSetup(t, fixture.root, worktreeRoot, "printf setup-stdout; printf setup-stderr >&2; pwd > setup-cwd")

	loop := &loopHarness{
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "setup-implement"}},
			{{Type: harness.EventSession, SessionID: "setup-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	checkingHarness := &setupCheckingLoopHarness{loopHarness: loop}
	var worktreePath string
	fixture.app.deps.Harnesses = func(root string) map[string]harness.Adapter {
		worktreePath = root
		loop.root = root
		checkingHarness.marker = filepath.Join(root, "setup-cwd")
		return map[string]harness.Adapter{"codex": checkingHarness, "claude": checkingHarness}
	}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--worktree"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement worktree code = %d, stderr = %q", code, fixture.stderr.String())
	}
	setupCWD, err := os.ReadFile(filepath.Join(worktreePath, "setup-cwd"))
	if err != nil {
		t.Fatalf("read setup cwd: %v", err)
	}
	if got := string(setupCWD); got != worktreePath+"\n" {
		t.Fatalf("setup cwd = %q, want worktree path %q", got, worktreePath)
	}
	if !strings.Contains(fixture.stdout.String(), "setup-stdout") {
		t.Fatalf("stdout = %q, want setup stdout streamed to the user", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stderr.String(), "setup-stderr") {
		t.Fatalf("stderr = %q, want setup stderr streamed to the user", fixture.stderr.String())
	}
}

func TestImplementWorktreeSetupFailureRollsBackBeforeHarnessStarts(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeSetup(t, fixture.root, worktreeRoot, "printf setup-failed >&2; exit 17")

	harnessCalled := false
	fixture.app.deps.Harnesses = func(string) map[string]harness.Adapter {
		harnessCalled = true
		return map[string]harness.Adapter{}
	}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--worktree"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "run worktree setup") ||
		!strings.Contains(fixture.stderr.String(), "setup-failed") {
		t.Fatalf("implement worktree code = %d, stderr = %q, want setup failure", code, fixture.stderr.String())
	}
	if harnessCalled {
		t.Fatal("harness factory was called after setup failed")
	}

	worktreePath := filepath.Join(worktreeRoot, filepath.Base(fixture.root), "feat-add-resilient-workflow")
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree path stat error = %v, want path removed", err)
	}
	if got := strings.TrimSpace(gitOutput(t, fixture.root, "branch", "--list", "--format=%(refname:short)", "feat/add-resilient-workflow")); got != "" {
		t.Fatalf("implementation branch = %q, want branch removed", got)
	}
	if got := gitOutput(t, fixture.root, "worktree", "list", "--porcelain"); strings.Contains(got, worktreePath) {
		t.Fatalf("registered worktrees = %q, want failed worktree removed", got)
	}
}

func TestImplementWorktreeSetupCancellationRollsBack(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeSetup(t, fixture.root, worktreeRoot, "touch setup-started; sleep 30")
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})
	worktreePath := filepath.Join(worktreeRoot, filepath.Base(fixture.root), "feat-add-resilient-workflow")

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	go func() {
		result <- fixture.app.Run(ctx, []string{"implement", "#42", "--worktree"}, &fixture.stdout, &fixture.stderr)
	}()
	startedPath := filepath.Join(worktreePath, "setup-started")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case code := <-result:
		if code == 0 {
			t.Fatal("implement worktree code = 0, want cancellation failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("implement worktree did not stop after setup cancellation")
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree path stat error = %v, want path removed after cancellation", err)
	}
}

func configureWorktreeRoot(t *testing.T, root, worktreeRoot string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(contents) + "\n[worktree]\nroot = \"" + worktreeRoot + "\"\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, root, "configure worktree")
}

func configureWorktreeSetup(t *testing.T, root, worktreeRoot, setup string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := fmt.Sprintf("%s\n[worktree]\nroot = %s\nsetup = %s\n", contents, strconv.Quote(worktreeRoot), strconv.Quote(setup))
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, root, "configure worktree setup")
}

func TestImplementWorktreeUsesExplicitBaseRef(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeRoot(t, fixture.root, worktreeRoot)
	base := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(fixture.root, "tracked.txt"), []byte("later commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, fixture.root, "later commit")

	loop := &loopHarness{
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "base-implement"}},
			{{Type: harness.EventSession, SessionID: "base-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	var worktreePath string
	fixture.app.deps.Harnesses = func(root string) map[string]harness.Adapter {
		worktreePath = root
		loop.root = root
		return map[string]harness.Adapter{"codex": loop, "claude": loop}
	}
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--worktree", "--base", base}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement worktree with base code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if got := gitOutput(t, worktreePath, "rev-parse", "HEAD"); got != base {
		t.Fatalf("worktree HEAD = %q, want explicit base %q", got, base)
	}
	contents, err := os.ReadFile(filepath.Join(worktreePath, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(contents); got != "initial\n" {
		t.Fatalf("worktree tracked file = %q, want content from explicit base", got)
	}
}

func TestImplementWorktreeUpdatesLocalTicketOnlyInsideWorktree(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	configureWorktreeRoot(t, fixture.root, worktreeRoot)

	configPath := filepath.Join(fixture.root, ".syl", "config.toml")
	configContents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configContents = []byte(strings.Replace(string(configContents), `issues = "github"`, `issues = "local"`, 1))
	if err := os.WriteFile(configPath, configContents, 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, fixture.root, "local worktree config")
	ticketPath := filepath.Join(fixture.root, ".scratch", "feature-a", "issues", "42-local-worktree.md")
	if err := os.MkdirAll(filepath.Dir(ticketPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ticketPath, []byte("# 42 — Add resilient workflow\n\n**What to build:** Use the worktree.\n\n**Blocked by:** None — can start immediately.\n\n**Status:** todo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loop := &loopHarness{
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "local-worktree-implement"}},
			{{Type: harness.EventSession, SessionID: "local-worktree-review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	var worktreePath string
	fixture.app.deps.Harnesses = func(root string) map[string]harness.Adapter {
		worktreePath = root
		loop.root = root
		return map[string]harness.Adapter{"codex": loop, "claude": loop}
	}

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--worktree"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("local worktree implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	originTicket, err := os.ReadFile(ticketPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(originTicket), "**Status:** todo") {
		t.Fatalf("origin ticket = %q, want unchanged todo status", originTicket)
	}
	worktreeTicket, err := os.ReadFile(filepath.Join(worktreePath, ".scratch", "feature-a", "issues", "42-local-worktree.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worktreeTicket), "**Status:** doing") {
		t.Fatalf("worktree ticket = %q, want doing status", worktreeTicket)
	}
}

type implementLoopFixture struct {
	root      string
	app       *App
	harnesses map[string]harness.Adapter
	stdout    strings.Builder
	stderr    strings.Builder
}

func newImplementLoopFixture(t *testing.T) *implementLoopFixture {
	t.Helper()
	base := newTopSeamFixtureWithGit(t, true)
	if err := os.WriteFile(filepath.Join(base.root, ".gitignore"), []byte(".syl/runs/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base.root, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, base.root, "fixture")
	gitOutput(t, base.root, "branch", "-M", "main")
	harnesses := map[string]harness.Adapter{
		"claude":   nil,
		"codex":    nil,
		"opencode": nil,
	}
	return &implementLoopFixture{
		root:      base.root,
		harnesses: harnesses,
		app: New(base.root, base.root, testSylHome(t, t.TempDir()), Dependencies{
			Harnesses: harnessFactories(harnesses),
			Notifier:  fakeNotifier{},
		}),
	}
}

func commitWorkingTree(t *testing.T, root, message string) {
	t.Helper()
	command := exec.Command("git", "-C", root, "add", ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	command = exec.Command("git", "-C", root, "-c", "user.name=syl test", "-c", "user.email=syl@example.test", "commit", "--quiet", "-m", message)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, output)
	}
}

type loopHarness struct {
	root                  string
	streams               [][]harness.Event
	requests              []harness.Request
	leaveChangesUntracked bool
	noChanges             bool
}

const approveVerdictWithNitText = "VERDICT: approve\nSUMMARY: The implementation meets the ticket.\nFINDINGS:\n- [nit] internal/example.go:9 — Rename this helper.\n"

type setupCheckingLoopHarness struct {
	*loopHarness
	marker string
}

func (h *setupCheckingLoopHarness) Run(ctx context.Context, request harness.Request) (harness.Stream, error) {
	if len(h.requests) == 0 {
		if _, err := os.Stat(h.marker); err != nil {
			return nil, fmt.Errorf("setup marker is missing before harness start: %w", err)
		}
	}
	return h.loopHarness.Run(ctx, request)
}

type resumingLoopHarness struct {
	root                 string
	streams              [][]harness.Event
	resumes              [][]harness.Event
	requests             []harness.Request
	implementResumeCalls []resumeCall
	resumeCalls          []resumeCall
	resumeErr            error
	resumeWaitErr        error
}

type resumeCall struct {
	sessionID string
	request   harness.Request
}

func (h *resumingLoopHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.requests = append(h.requests, request)
	index := len(h.requests) - 1
	if strings.Contains(request.Prompt, "/implement") || strings.Contains(request.Prompt, "/fix-review") {
		if err := os.WriteFile(filepath.Join(h.root, "change.txt"), []byte(fmt.Sprintf("implemented-%d\n", index)), 0o644); err != nil {
			return nil, err
		}
		if output, err := exec.Command("git", "-C", h.root, "add", "change.txt").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("stage fixture change: %v\n%s", err, output)
		}
	}
	if index >= len(h.streams) {
		return nil, fmt.Errorf("no scripted harness stream for request %d", index+1)
	}
	return scriptedHarnessStream{events: h.streams[index]}, nil
}

func (h *resumingLoopHarness) Resume(_ context.Context, sessionID string, request harness.Request) (harness.Stream, error) {
	if strings.Contains(request.Prompt, "/implement") || strings.Contains(request.Prompt, "/fix-review") {
		h.implementResumeCalls = append(h.implementResumeCalls, resumeCall{sessionID: sessionID, request: request})
		if err := os.WriteFile(filepath.Join(h.root, "change.txt"), []byte(fmt.Sprintf("implemented-resume-%d\n", len(h.implementResumeCalls))), 0o644); err != nil {
			return nil, err
		}
		if output, err := exec.Command("git", "-C", h.root, "add", "change.txt").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("stage fixture change: %v\n%s", err, output)
		}
		return scriptedHarnessStream{events: []harness.Event{
			{Type: harness.EventSession, SessionID: sessionID},
			{Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"},
		}}, nil
	}
	h.resumeCalls = append(h.resumeCalls, resumeCall{sessionID: sessionID, request: request})
	if h.resumeErr != nil {
		return nil, h.resumeErr
	}
	index := len(h.resumeCalls) - 1
	if index >= len(h.resumes) {
		return nil, fmt.Errorf("no scripted resume stream for call %d", index+1)
	}
	if h.resumeWaitErr != nil {
		return failingHarnessStream{events: h.resumes[index], err: h.resumeWaitErr}, nil
	}
	return scriptedHarnessStream{events: h.resumes[index]}, nil
}

func (*resumingLoopHarness) Attach(context.Context, harness.Request) error {
	return fmt.Errorf("unexpected harness attach")
}

func (*resumingLoopHarness) AttachSession(context.Context, string, harness.Request) error {
	return fmt.Errorf("unexpected harness session attach")
}

type failingHarnessStream struct {
	events []harness.Event
	err    error
}

func (s failingHarnessStream) Events() <-chan harness.Event {
	channel := make(chan harness.Event, len(s.events))
	for _, event := range s.events {
		channel <- event
	}
	close(channel)
	return channel
}

func (s failingHarnessStream) Wait() error { return s.err }

type unparseableReviewImplementHarness struct {
	root           string
	requests       []harness.Request
	resumeCount    int
	resumeRequests []harness.Request
}

func (h *unparseableReviewImplementHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.requests = append(h.requests, request)
	switch len(h.requests) {
	case 1:
		if err := os.WriteFile(filepath.Join(h.root, "change.txt"), []byte("implemented\n"), 0o644); err != nil {
			return nil, err
		}
		if output, err := exec.Command("git", "-C", h.root, "add", "change.txt").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("stage fixture change: %v\n%s", err, output)
		}
		return scriptedHarnessStream{events: []harness.Event{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Implemented the ticket.\n"},
		}}, nil
	case 2:
		return scriptedHarnessStream{events: []harness.Event{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "The review did not produce a verdict."},
		}}, nil
	default:
		return nil, fmt.Errorf("unexpected harness request %d", len(h.requests))
	}
}

func (h *unparseableReviewImplementHarness) Resume(_ context.Context, _ string, request harness.Request) (harness.Stream, error) {
	h.resumeCount++
	h.resumeRequests = append(h.resumeRequests, request)
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventAssistantText, Text: "The re-ask also omitted the verdict."},
	}}, nil
}

func (*unparseableReviewImplementHarness) Attach(context.Context, harness.Request) error { return nil }

func (*unparseableReviewImplementHarness) AttachSession(context.Context, string, harness.Request) error {
	return nil
}

func (h *loopHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.requests = append(h.requests, request)
	index := len(h.requests) - 1
	if index%2 == 0 && !h.noChanges {
		if err := os.WriteFile(filepath.Join(h.root, "change.txt"), []byte(fmt.Sprintf("implemented-%d\n", index/2+1)), 0o644); err != nil {
			return nil, err
		}
		if !h.leaveChangesUntracked {
			if output, err := exec.Command("git", "-C", h.root, "add", "change.txt").CombinedOutput(); err != nil {
				return nil, fmt.Errorf("stage fixture change: %v\n%s", err, output)
			}
		}
	}
	if index >= len(h.streams) {
		return nil, fmt.Errorf("no scripted harness stream for request %d", index+1)
	}
	return scriptedHarnessStream{events: h.streams[index]}, nil
}

func (*loopHarness) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return nil, fmt.Errorf("unexpected harness resume")
}

func (*loopHarness) Attach(context.Context, harness.Request) error {
	return fmt.Errorf("unexpected harness attach")
}

func (*loopHarness) AttachSession(context.Context, string, harness.Request) error {
	return fmt.Errorf("unexpected harness session attach")
}

type loopGHRunner struct {
	calls      []string
	issueTitle string
}

func (r *loopGHRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if strings.HasPrefix(call, "pr create --draft ") {
		return "https://github.com/example/project/pull/23\n", nil
	}
	if strings.HasPrefix(call, "pr list --head ") {
		return "[]", nil
	}
	switch call {
	case "repo view --json defaultBranchRef --jq .defaultBranchRef.name":
		return "main\n", nil
	case "pr list --head feat/add-resilient-workflow --state open --json url --limit 1":
		return "[]", nil
	case "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue view 42 --json number,title,body,state,labels":
		title := r.issueTitle
		if title == "" {
			title = "Add resilient workflow"
		}
		return fmt.Sprintf(`{"number":42,"title":%q,"body":"Acceptance criteria: leave a working implementation.","state":"OPEN","labels":[{"name":"todo"}]}`, title), nil
	case "issue edit 42 --add-label doing --remove-label todo":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected gh command %q", call)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func readAllFiles(t *testing.T, root string) string {
	t.Helper()
	var contents strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Lifecycle timestamps and process metadata are intentionally run-specific;
		// this helper compares the stable role artifacts.
		if filepath.Base(path) == "run-state.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("read artifacts: %v", err)
	}
	return contents.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
