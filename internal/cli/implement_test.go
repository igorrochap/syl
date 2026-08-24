package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/codex"
	"github.com/igorrochap/syl/internal/usage"
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
	for _, expected := range []string{
		"[implement] Implementer is working.",
		"[implement] tool: edit — change.txt",
		"[review] Reviewer is checking the diff.",
		"[review] Verify the diff first.",
		"[review] tool: cat — iteration review diff",
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
		"[implement] Implementer internal prose.",
		"[review] Reviewer internal prose.",
		"VERDICT: approve",
		"SUMMARY: The working tree is ready",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("stdout = %q, want %q", output, expected)
		}
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
			{{Type: harness.EventSession, SessionID: "implement-2"}, {Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"}},
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
	if len(loop.requests) != 3 {
		t.Fatalf("harness Run requests = %d, want two implement turns and one review turn", len(loop.requests))
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
	for _, expected := range []string{"iteration 1 review: review-1a", "iteration 1 review: review-1b", "iteration 2 review: review-1b"} {
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
			{{Type: harness.EventSession, SessionID: "implement-2"}, {Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"}},
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
	if len(loop.requests) != 4 {
		t.Fatalf("harness Run requests = %d, want fresh review fallback", len(loop.requests))
	}
	if !strings.Contains(loop.requests[3].Prompt, "/code-review") {
		t.Fatalf("fallback review prompt = %q, want full review prompt", loop.requests[3].Prompt)
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
			{{Type: harness.EventSession, SessionID: "implement-2"}, {Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"}},
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
	if len(loop.requests) != 4 || !strings.Contains(loop.requests[3].Prompt, "/code-review") {
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
			{{Type: harness.EventSession, SessionID: "implement-2"}, {Type: harness.EventAssistantText, Text: "Blocking finding fixed.\n"}},
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
	if len(loop.requests) != 4 || !strings.Contains(loop.requests[3].Prompt, "/code-review") {
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
	harnesses := map[string]harness.Adapter{
		"claude":   nil,
		"codex":    nil,
		"opencode": nil,
	}
	return &implementLoopFixture{
		root:      base.root,
		harnesses: harnesses,
		app: New(base.root, base.root, Dependencies{
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
}

type resumingLoopHarness struct {
	root          string
	streams       [][]harness.Event
	resumes       [][]harness.Event
	requests      []harness.Request
	resumeCalls   []resumeCall
	resumeErr     error
	resumeWaitErr error
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

func (h *unparseableReviewImplementHarness) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	h.resumeCount++
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventAssistantText, Text: "The re-ask also omitted the verdict."},
	}}, nil
}

func (*unparseableReviewImplementHarness) Attach(context.Context, harness.Request) error { return nil }

func (h *loopHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.requests = append(h.requests, request)
	index := len(h.requests) - 1
	if index%2 == 0 {
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
