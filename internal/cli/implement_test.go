package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/codex"
)

func TestImplementLoopApprovesOnFirstIteration(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	implementer := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Implemented the ticket.\n"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.app.deps.Harnesses["codex"] = implementer
	fixture.app.deps.Harnesses["claude"] = implementer
	fixture.app.deps.GH = &loopGHRunner{}

	branchPoint := gitOutput(t, fixture.root, "rev-parse", "HEAD")
	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
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

func TestImplementFailsAfterOneUnparseableReviewReaskAndSavesTranscript(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	harness := &unparseableReviewImplementHarness{root: fixture.root}
	fixture.app.deps.Harnesses["codex"] = harness
	fixture.app.deps.Harnesses["claude"] = harness
	fixture.app.deps.GH = &loopGHRunner{}

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "reviewer produced no parseable verdict") {
		t.Fatalf("implement code = %d, stderr = %q, want clear unparseable-verdict failure", code, fixture.stderr.String())
	}
	if harness.resumeCount != 1 {
		t.Fatalf("resume count = %d, want exactly one review re-ask", harness.resumeCount)
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
				{Type: harness.EventToolUse, ToolName: "git diff", ArgumentGist: "branch point"},
				{Type: harness.EventAssistantText, Text: "VER"},
				{Type: harness.EventAssistantText, Text: strings.TrimPrefix(approveVerdictText, "VER")},
			},
		},
	}
	fixture.app.deps.Harnesses["codex"] = loop
	fixture.app.deps.Harnesses["claude"] = loop
	fixture.app.deps.GH = &loopGHRunner{}

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
		"tool: git diff — branch point",
	} {
		index := strings.Index(output, expected)
		if index < 0 || index > summaryIndex {
			t.Fatalf("stdout = %q, want %q before final summary", output, expected)
		}
	}
	if !strings.Contains(output, "VERDICT: approve") {
		t.Fatalf("stdout = %q, want the rendered reviewer verdict", output)
	}
	for _, expected := range []string{
		"[implement] Implementer is working.",
		"[implement] tool: edit — change.txt",
		"[review] Reviewer is checking the diff.",
		"[review] Verify the diff first.",
		"[review] tool: git diff — branch point",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("stdout = %q, want line labeled %q", output, expected)
		}
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
				{Type: harness.EventToolUse, ToolName: "git diff", ArgumentGist: "branch point"},
				{Type: harness.EventAssistantText, Text: approveVerdictText},
			},
		},
	}
	fixture.app.deps.Harnesses["codex"] = loop
	fixture.app.deps.Harnesses["claude"] = loop
	fixture.app.deps.GH = &loopGHRunner{}

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}

	output := fixture.stdout.String()
	for _, expected := range []string{
		"iteration 1/3 — implementing",
		"iteration 1/3 — reviewing",
		"[implement] Implementer internal prose.",
		"[review] Reviewer internal prose.",
		"VERDICT: approve",
		"SUMMARY: The working tree is ready",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("stdout = %q, want %q", output, expected)
		}
	}
	for _, suppressed := range []string{"tool: edit — change.txt", "tool: git diff — branch point"} {
		if strings.Contains(output, suppressed) {
			t.Fatalf("stdout = %q, want tool call %q suppressed", output, suppressed)
		}
	}
}

func TestImplementLoopUsesCodexAdapterAtProcessBoundary(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	argsPath := filepath.Join(fixture.root, "codex-args")
	commandDir := t.TempDir()
	command := filepath.Join(commandDir, "codex")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("for arg in \"$@\"; do\n")
	script.WriteString("printf '%s' \"$arg\" | tr '\\n' '\\034'\n")
	script.WriteString("printf '\\n'\ndone > ")
	script.WriteString(shellQuote(argsPath))
	script.WriteString("\nprintf '%s\\n' 'implemented' > change.txt\n")
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

	fixture.app.deps.Harnesses["codex"] = codex.New(fixture.root)
	fixture.app.deps.Harnesses["claude"] = &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture.app.deps.GH = &loopGHRunner{}

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
	fixture.app.deps.Harnesses["codex"] = implementer
	fixture.app.deps.Harnesses["claude"] = implementer
	fixture.app.deps.GH = &loopGHRunner{}

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(implementer.requests) != 4 {
		t.Fatalf("harness requests = %d, want two implement and two review requests", len(implementer.requests))
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
	if !strings.Contains(fixture.stdout.String(), "Iterations: 2") || !strings.Contains(fixture.stdout.String(), "Final verdict: approve") {
		t.Fatalf("stdout = %q, want two-iteration approval summary", fixture.stdout.String())
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
	fixture.app.deps.Harnesses["codex"] = implementer
	fixture.app.deps.Harnesses["claude"] = implementer
	fixture.app.deps.GH = &loopGHRunner{}

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
		t.Fatalf("stdout = %q, want accumulated nit in final summary", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "iteration 2/3 — revising 0 blocking finding(s)") {
		t.Fatalf("stdout = %q, want revision transition with zero blocking findings", fixture.stdout.String())
	}
	if got := strings.Count(fixture.stdout.String(), "style could be clearer"); got != 2 {
		t.Fatalf("stdout = %q, want the per-iteration verdict and final summary", fixture.stdout.String())
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
		fixture.app.deps.Harnesses["codex"] = loop
		fixture.app.deps.Harnesses["claude"] = loop
		fixture.app.deps.GH = &loopGHRunner{}
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
		return readAllFiles(t, runDirs[0])
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
	fixture.app.deps.Harnesses["codex"] = loop
	fixture.app.deps.Harnesses["claude"] = loop

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
	fixture.app.deps.Harnesses["codex"] = implementer
	fixture.app.deps.Harnesses["claude"] = implementer
	fixture.app.deps.GH = &loopGHRunner{}

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
	fixture.app.deps.Harnesses["codex"] = implementer
	fixture.app.deps.Harnesses["claude"] = implementer
	github := &loopGHRunner{}
	fixture.app.deps.GH = github
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

type implementLoopFixture struct {
	root   string
	app    *App
	stdout strings.Builder
	stderr strings.Builder
}

func newImplementLoopFixture(t *testing.T) *implementLoopFixture {
	t.Helper()
	base := newTopSeamFixtureWithGit(t, true)
	if err := os.WriteFile(filepath.Join(base.root, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitWorkingTree(t, base.root, "fixture")
	return &implementLoopFixture{
		root: base.root,
		app: New(base.root, Dependencies{
			Harnesses: map[string]harness.Adapter{},
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
	root     string
	streams  [][]harness.Event
	requests []harness.Request
}

type unparseableReviewImplementHarness struct {
	root        string
	requests    []harness.Request
	resumeCount int
}

func (h *unparseableReviewImplementHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.requests = append(h.requests, request)
	switch len(h.requests) {
	case 1:
		if err := os.WriteFile(filepath.Join(h.root, "change.txt"), []byte("implemented\n"), 0o644); err != nil {
			return nil, err
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

func (h *unparseableReviewImplementHarness) Resume(context.Context, string, string) (harness.Stream, error) {
	h.resumeCount++
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventAssistantText, Text: "The re-ask also omitted the verdict."},
	}}, nil
}

func (*unparseableReviewImplementHarness) Attach(context.Context, harness.Request) error { return nil }

func (h *loopHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.requests = append(h.requests, request)
	index := len(h.requests) - 1
	if index == 0 {
		if err := os.WriteFile(filepath.Join(h.root, "change.txt"), []byte("implemented\n"), 0o644); err != nil {
			return nil, err
		}
	}
	if index >= len(h.streams) {
		return nil, fmt.Errorf("no scripted harness stream for request %d", index+1)
	}
	return scriptedHarnessStream{events: h.streams[index]}, nil
}

func (*loopHarness) Resume(context.Context, string, string) (harness.Stream, error) {
	return nil, fmt.Errorf("unexpected harness resume")
}

func (*loopHarness) Attach(context.Context, harness.Request) error {
	return fmt.Errorf("unexpected harness attach")
}

type loopGHRunner struct {
	calls []string
}

func (r *loopGHRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch call {
	case "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue view 42 --json number,title,body,state,labels":
		return `{"number":42,"title":"Add resilient workflow","body":"Acceptance criteria: leave a working implementation.","state":"OPEN","labels":[{"name":"todo"}]}`, nil
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
