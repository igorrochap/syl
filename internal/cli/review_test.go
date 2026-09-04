package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/usage"
)

func TestReviewOutputMatchesPlainAndStyledGolden(t *testing.T) {
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
			harness := &scriptedHarness{first: []harness.Event{
				{Type: harness.EventSession, SessionID: "review-session"},
				{Type: harness.EventAssistantText, Text: "Reviewing the diff...\n"},
				{Type: harness.EventToolUse, ToolName: "Bash", ArgumentGist: `{"command":"cat review.diff"}`},
				{Type: harness.EventAssistantText, Text: "Done.\n" + approveVerdictText},
			}}
			fixture := newReviewFixture(t, harness)
			var output io.Writer = &strings.Builder{}
			if test.styled {
				output = newStyledTerminalCapture(t)
			}
			code := fixture.app.Run(context.Background(), []string{"review", "--verbose"}, output, &fixture.stderr)
			if code != 0 {
				t.Fatalf("review code = %d, stderr = %q", code, fixture.stderr.String())
			}
			actual := output.(interface{ String() string }).String()
			assertCLIGolden(t, test.name+"-review.golden", []byte(actual))
		})
	}
}

func TestReviewTopSeamApprovePrintsFeedAndWritesLog(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-approve"},
		{Type: harness.EventAssistantText, Text: "Reviewing the diff...\n"},
		{Type: harness.EventToolUse, ToolName: "Bash", ArgumentGist: `{"command":"cat review.diff"}`},
		{Type: harness.EventAssistantText, Text: "Done.\n" + approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	if err := os.WriteFile(filepath.Join(fixture.root, "change.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := fixture.app.Run(context.Background(), []string{"review", "--verbose"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "Reviewing the diff...") || !strings.Contains(fixture.stdout.String(), "tool: Bash — {\"command\":\"cat review.diff\"}") {
		t.Fatalf("stdout = %q, want parsed feed", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "VERDICT: approve") {
		t.Fatalf("stdout = %q, want verdict", fixture.stdout.String())
	}
	for _, expected := range []string{
		"/code-review",
		"pre-computed diff",
		"authoritative",
		"Do not read or write review documents; the invoking tool records the verdict.",
		"The verdict block you print is the only record.",
		"QUESTION:",
	} {
		if !strings.Contains(harness.runRequest.Prompt, expected) {
			t.Fatalf("review prompt = %q, want %q", harness.runRequest.Prompt, expected)
		}
	}
	if strings.Contains(harness.runRequest.Prompt, "## Additional context") {
		t.Fatalf("review prompt = %q, want no additional context section", harness.runRequest.Prompt)
	}
	if harness.runRequest.Model != "claude-sonnet-5" || harness.runRequest.Effort != "medium" {
		t.Fatalf("review request = %#v, want configured model and effort", harness.runRequest)
	}
	if harness.runRequest.MCP {
		t.Fatal("review request MCP = true, want lean default false")
	}
	logContents := readSingleReviewLog(t, fixture.root)
	if !strings.Contains(logContents, "VERDICT: approve") || !strings.Contains(logContents, "Reviewed ref: branch-point") || !strings.Contains(logContents, "Timestamp:") {
		t.Fatalf("review log = %q, want verdict, ref, and timestamp", logContents)
	}
}

func TestReviewWritesTranscriptArtifacts(t *testing.T) {
	run := runCompletedClaudeReview(t)
	if run.claude.runRequest.Prompt == "" {
		t.Fatal("review did not run the Claude harness")
	}

	wantArtifacts := map[string]string{
		"review.feed":       "Reviewing the transcript.\ntool: Read — {\"file_path\":\"review.diff\"}\nVERDICT: approve\nSUMMARY: The PTY review is ready\nFINDINGS:\n",
		"review.transcript": "Reviewing the transcript.\nVERDICT: approve\nSUMMARY: The PTY review is ready\nFINDINGS:\n",
		"sessions.txt":      "iteration 0 review: " + ptyReviewSessionID + "\n",
	}
	for name, want := range wantArtifacts {
		contents, err := os.ReadFile(filepath.Join(run.dir, name))
		if err != nil {
			t.Fatalf("read PTY artifact %s: %v", name, err)
		}
		if string(contents) != want {
			t.Fatalf("PTY artifact %s = %q, want %q", name, contents, want)
		}
	}
}

func TestReviewWritesReviewLogFromTranscript(t *testing.T) {
	run := runCompletedClaudeReview(t)
	logContents := readSingleReviewLog(t, run.fixture.root)
	if !strings.Contains(logContents, "VERDICT: approve") ||
		!strings.Contains(logContents, "SUMMARY: The PTY review is ready") {
		t.Fatalf("PTY review log = %q, want transcript verdict", logContents)
	}
}

func TestReviewRecordsCompleteClaudeTranscriptUsage(t *testing.T) {
	run := runCompletedClaudeReview(t)
	artifact, err := usage.ReadArtifact(filepath.Join(run.dir, "usage.json"))
	if err != nil {
		t.Fatalf("read PTY usage artifact: %v", err)
	}
	entry := findUsageEntry(t, artifact, 0, "review")
	if !entry.Tracked || entry.Metrics == nil || entry.Metrics.WeightedEstimate != 17.5 {
		t.Fatalf("PTY review usage = %#v, want complete transcript metrics", entry)
	}
}

func TestReviewClaudeUsageMatchesRecomputation(t *testing.T) {
	run := runCompletedClaudeReview(t)
	var recordedOutput, recordedError strings.Builder
	if code := run.fixture.app.Run(
		context.Background(),
		[]string{"usage", filepath.Base(run.dir)},
		&recordedOutput,
		&recordedError,
	); code != 0 {
		t.Fatalf("PTY usage code = %d, stderr = %q", code, recordedError.String())
	}
	if err := os.Remove(filepath.Join(run.dir, "usage.json")); err != nil {
		t.Fatalf("remove PTY usage artifact: %v", err)
	}
	var recomputedOutput, recomputedError strings.Builder
	if code := run.fixture.app.Run(
		context.Background(),
		[]string{"usage", filepath.Base(run.dir)},
		&recomputedOutput,
		&recomputedError,
	); code != 0 {
		t.Fatalf("recomputed PTY usage code = %d, stderr = %q", code, recomputedError.String())
	}
	wantRecomputed := "recomputed from transcripts — usage.json not found\n" + recordedOutput.String()
	if recomputedOutput.String() != wantRecomputed {
		t.Fatalf("recomputed PTY usage = %q, want recorded value %q", recomputedOutput.String(), wantRecomputed)
	}
}

const ptyReviewSessionID = "pty-session"

type completedClaudeReview struct {
	fixture *reviewFixture
	dir     string
	claude  *scriptedHarness
}

func runCompletedClaudeReview(t *testing.T) completedClaudeReview {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claude := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventAssistantText, Text: "Reviewing the transcript.\n"},
		{Type: harness.EventToolUse, ToolName: "Read", ArgumentGist: `{"file_path":"review.diff"}`},
		{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: The PTY review is ready\nFINDINGS:\n"},
	}, knownSessionID: ptyReviewSessionID}
	fixture := newReviewFixture(t, claude)
	writePTYReviewTranscriptFixture(t, home, fixture.root, ptyReviewSessionID)

	code := fixture.app.Run(
		context.Background(),
		[]string{"review"},
		&fixture.stdout,
		&fixture.stderr,
	)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	runDirs := reviewRunArtifactPaths(t, fixture.root)
	if len(runDirs) != 1 {
		t.Fatalf("run artifact directories = %v, want one", runDirs)
	}
	return completedClaudeReview{
		fixture: fixture,
		dir:     runDirs[0],
		claude:  claude,
	}
}

func TestReviewRejectsRemovedPTYFlag(t *testing.T) {
	fixture := newReviewFixture(t, &scriptedHarness{})

	code := fixture.app.Run(
		context.Background(),
		[]string{"review", "--pty"},
		&fixture.stdout,
		&fixture.stderr,
	)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "unknown flag: --pty") {
		t.Fatalf("review code = %d, stderr = %q, want removed flag error", code, fixture.stderr.String())
	}
}

func writePTYReviewTranscriptFixture(t *testing.T, home, projectRoot, sessionID string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "pty-review-session.jsonl"))
	if err != nil {
		t.Fatalf("read PTY transcript fixture: %v", err)
	}
	projectDir := filepath.Join(
		home,
		".claude",
		"projects",
		strings.ReplaceAll(projectRoot, string(filepath.Separator), "-"),
	)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create Claude transcript fixture directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), contents, 0o644); err != nil {
		t.Fatalf("write PTY transcript fixture: %v", err)
	}
}

func TestReviewPrecomputesAuthoritativeDiffOnceAndPassesArtifactPath(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-diff"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	git := fixture.app.deps.Git(fixture.app.workRoot).(*reviewGitRunner)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}

	runDirs := reviewRunArtifactPaths(t, fixture.root)
	if len(runDirs) != 1 {
		t.Fatalf("run artifact directories = %v, want one", runDirs)
	}
	diffPath := filepath.Join(runDirs[0], "review.diff")
	diff, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("read pre-computed diff: %v", err)
	}
	if string(diff) != git.diff {
		t.Fatalf("review diff = %q, want %q", diff, git.diff)
	}
	sessions, err := os.ReadFile(filepath.Join(runDirs[0], "sessions.txt"))
	if err != nil {
		t.Fatalf("read standalone review sessions: %v", err)
	}
	if string(sessions) != "iteration 0 review: session-diff\n" {
		t.Fatalf("standalone review sessions = %q, want recorded reviewer session", sessions)
	}
	for _, expected := range []string{
		diffPath,
		"branch-point",
		"authoritative diff",
		"Do not run Git to re-derive the diff",
	} {
		if !strings.Contains(harness.runRequest.Prompt, expected) {
			t.Fatalf("review prompt = %q, want %q", harness.runRequest.Prompt, expected)
		}
	}
	if got := strings.Join(git.calls, "\n"); got != "rev-parse HEAD\ndiff branch-point\nls-files --others --exclude-standard -z" {
		t.Fatalf("git calls = %q, want one ref lookup and one complete diff", got)
	}
}

func TestStandaloneReviewUsageRecomputesFromRecordedSession(t *testing.T) {
	const sessionID = "standalone-review-session"
	t.Setenv("HOME", t.TempDir())
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: sessionID},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	writeFallbackClaudeTranscript(t, fixture.root, sessionID, []string{
		`{"type":"assistant","timestamp":"2026-08-20T12:00:05Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2}}}`,
	})

	if code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("standalone review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	runDirs := reviewRunArtifactPaths(t, fixture.root)
	if len(runDirs) != 1 {
		t.Fatalf("run artifact directories = %v, want one", runDirs)
	}
	if err := os.Remove(filepath.Join(runDirs[0], "usage.json")); err != nil {
		t.Fatalf("remove usage artifact: %v", err)
	}

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage", filepath.Base(runDirs[0])}, &stdout, &stderr); code != 0 {
		t.Fatalf("recomputed standalone usage code = %d, stderr = %q", code, stderr.String())
	}
	for _, expected := range []string{
		"recomputed from transcripts — usage.json not found",
		"review (claude, claude-sonnet-5):  weighted_estimate=12.00 input_tokens=10 output_tokens=2",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("recomputed standalone usage = %q, want %q", stdout.String(), expected)
		}
	}
}

func TestStandaloneReviewRecordsUsageDuringRun(t *testing.T) {
	const sessionID = "standalone-review-session"
	home := t.TempDir()
	t.Setenv("HOME", home)
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: sessionID},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	harness.beforeRun = func() error {
		projectDir := filepath.Join(
			home,
			".claude",
			"projects",
			strings.ReplaceAll(fixture.root, string(filepath.Separator), "-"),
		)
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			return err
		}
		transcript := fmt.Sprintf(
			`{"type":"assistant","timestamp":%q,"message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}`+"\n",
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		return os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(transcript), 0o644)
	}

	if code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr); code != 0 {
		t.Fatalf("standalone review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	runDirs := reviewRunArtifactPaths(t, fixture.root)
	if len(runDirs) != 1 {
		t.Fatalf("run artifact directories = %v, want one", runDirs)
	}
	artifact, err := usage.ReadArtifact(filepath.Join(runDirs[0], "usage.json"))
	if err != nil {
		t.Fatalf("read standalone review usage: %v", err)
	}
	entry := findUsageEntry(t, artifact, 0, "review")
	if entry.Harness != "claude" || entry.Model != "claude-sonnet-5" || !entry.Tracked || entry.Metrics == nil {
		t.Fatalf("standalone review usage = %#v, want tracked Claude review entry", entry)
	}
	if entry.Metrics.InputTokens != 10 || entry.Metrics.OutputTokens != 2 ||
		entry.Metrics.CacheWriteTokens != 4 || entry.Metrics.CacheReadTokens != 5 ||
		entry.Metrics.WeightedEstimate != 17.5 {
		t.Fatalf("standalone review metrics = %#v, want transcript token metrics", *entry.Metrics)
	}

	var stdout, stderr strings.Builder
	if code := fixture.app.Run(context.Background(), []string{"usage"}, &stdout, &stderr); code != 0 {
		t.Fatalf("standalone usage code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review (claude, claude-sonnet-5):  weighted_estimate=17.50") {
		t.Fatalf("standalone usage output = %q, want recorded review usage", stdout.String())
	}
	if strings.Contains(stdout.String(), "recomputed from transcripts") {
		t.Fatalf("standalone usage output = %q, want live capture without recompute", stdout.String())
	}
}

func TestReviewRejectsEmptyPrecomputedDiffBeforeRunningHarness(t *testing.T) {
	fixture := newReviewFixture(t, &scriptedHarness{})
	fixture.app.deps.Git(fixture.app.workRoot).(*reviewGitRunner).diff = "  \n"

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "pre-computed diff") || !strings.Contains(fixture.stderr.String(), "empty") {
		t.Fatalf("review code = %d, stderr = %q, want empty-diff failure", code, fixture.stderr.String())
	}
	if fixture.harnesses["claude"].(*scriptedHarness).runRequest.Prompt != "" {
		t.Fatal("harness ran despite an empty pre-computed diff")
	}
}

func TestReviewReportsDiffComputationFailureBeforeRunningHarness(t *testing.T) {
	fixture := newReviewFixture(t, &scriptedHarness{})
	fixture.app.deps.Git(fixture.app.workRoot).(*reviewGitRunner).diffErr = errors.New("git unavailable")

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "compute review diff") || !strings.Contains(fixture.stderr.String(), "git unavailable") {
		t.Fatalf("review code = %d, stderr = %q, want diff-computation failure", code, fixture.stderr.String())
	}
	if fixture.harnesses["claude"].(*scriptedHarness).runRequest.Prompt != "" {
		t.Fatal("harness ran despite a diff-computation failure")
	}
}

func TestReviewTopSeamQuietSuppressesToolCalls(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-quiet"},
		{Type: harness.EventAssistantText, Text: "Reviewer internal prose.\n"},
		{Type: harness.EventToolUse, ToolName: "Bash", ArgumentGist: `{"command":"cat review.diff"}`},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	output := fixture.stdout.String()
	if !strings.Contains(output, "Reviewer internal prose.") || !strings.Contains(output, "VERDICT: approve") {
		t.Fatalf("stdout = %q, want assistant prose and rendered verdict", output)
	}
	if strings.Contains(output, "tool: Bash — {\"command\":\"cat review.diff\"}") {
		t.Fatalf("stdout = %q, want tool call suppressed", output)
	}
}

func TestReviewRawAndVerboseAreMutuallyExclusive(t *testing.T) {
	fixture := newReviewFixture(t, &scriptedHarness{})

	code := fixture.app.Run(context.Background(), []string{"review", "--raw", "--verbose"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "[raw verbose]") {
		t.Fatalf("review code = %d, stderr = %q, want mutually-exclusive flag error", code, fixture.stderr.String())
	}
	if fixture.stdout.String() != "" {
		t.Fatalf("stdout = %q, want no banner for invalid flag combination", fixture.stdout.String())
	}
}

func TestReviewWithTicketIncludesTicketInPromptAndReviewLog(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-ticket"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	configureLocalIssues(t, fixture.root)
	writeReviewTicket(t, fixture.root, "feature-a", "07", "Improve the tracker", "Implement the requested tracker behavior.")

	code := fixture.app.Run(context.Background(), []string{"review", "07"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	for _, expected := range []string{
		"Review the current diff against this ticket",
		"Improve the tracker",
		"Implement the requested tracker behavior.",
	} {
		if !strings.Contains(harness.runRequest.Prompt, expected) {
			t.Fatalf("review prompt = %q, want %q", harness.runRequest.Prompt, expected)
		}
	}
	if logContents := readSingleReviewLog(t, fixture.root); !strings.Contains(logContents, "Ticket: #07") {
		t.Fatalf("review log = %q, want ticket reference", logContents)
	}
}

func TestReviewWithContextAppendsContextAfterTicket(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-context"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	configureLocalIssues(t, fixture.root)
	ticketBody := "Only review the parser behavior."
	writeReviewTicket(t, fixture.root, "feature-context", "42", "Parser review", ticketBody)

	contextText := "  only the parser changes matter  "
	code := fixture.app.Run(
		context.Background(),
		[]string{"review", "42", "--context", contextText},
		&fixture.stdout,
		&fixture.stderr,
	)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	prompt := harness.runRequest.Prompt
	if !strings.Contains(prompt, "## Additional context supplied by the user for this run\n\nonly the parser changes matter") {
		t.Fatalf("review prompt = %q, want trimmed additional context", prompt)
	}
	bodyIndex := strings.Index(prompt, ticketBody)
	contextIndex := strings.Index(prompt, "## Additional context")
	if bodyIndex < 0 || contextIndex <= bodyIndex {
		t.Fatalf("review prompt = %q, want context after ticket body", prompt)
	}
}

func TestReviewWithMissingTicketFailsClearlyBeforeRunningHarness(t *testing.T) {
	harness := &scriptedHarness{}
	fixture := newReviewFixture(t, harness)
	configureLocalIssues(t, fixture.root)

	code := fixture.app.Run(context.Background(), []string{"review", "#42"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "local ticket #42 not found") {
		t.Fatalf("review code = %d, stderr = %q, want clear missing-ticket failure", code, fixture.stderr.String())
	}
	if harness.runRequest.Prompt != "" {
		t.Fatalf("harness prompt = %q, want no harness run after resolution failure", harness.runRequest.Prompt)
	}
}

func TestReviewWithGitHubReviewLogPostsFencedVerdictWithoutClosingIssue(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "github-review"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	configureGitHubReviewLog(t, fixture.root)
	github := &reviewGitHubRunner{}
	fixture.app.deps.GH = fixedGH(github)

	code := fixture.app.Run(context.Background(), []string{"review", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(github.commentBody, "```text") || !strings.Contains(github.commentBody, approveVerdictText) {
		t.Fatalf("GitHub comment = %q, want fenced verbatim verdict", github.commentBody)
	}
	if github.hasCall("issue close 42") || github.hasCall("issue edit 42 --state closed") {
		t.Fatalf("GitHub calls = %#v, want no close operation", github.calls)
	}
}

func TestReviewWithGitLabReviewLogPostsVerdictAsNote(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "gitlab-review"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	configureGitLabReviewLog(t, fixture.root)
	glab := &reviewGitLabRunner{}
	fixture.app.deps.GLab = fixedGLab(glab)

	code := fixture.app.Run(context.Background(), []string{"review", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(glab.noteBody, "```text") || !strings.Contains(glab.noteBody, approveVerdictText) {
		t.Fatalf("GitLab note = %q, want fenced verdict", glab.noteBody)
	}
	if !glab.hasCallPrefix("issue note 42 --message") {
		t.Fatalf("GitLab calls = %#v, want issue note", glab.calls)
	}
}

func TestReviewWithGitHubReviewLogRequiresIssueNumber(t *testing.T) {
	harness := &scriptedHarness{}
	fixture := newReviewFixture(t, harness)
	configureGitHubReviewLog(t, fixture.root)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "remote review logging requires an issue reference") {
		t.Fatalf("review code = %d, stderr = %q, want clear issue-reference failure", code, fixture.stderr.String())
	}
	if harness.runRequest.Prompt != "" {
		t.Fatalf("harness prompt = %q, want no harness run", harness.runRequest.Prompt)
	}
}

func TestReviewWithGitHubIssueWritesLocalReviewLog(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "github-issue-local-log"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	github := &reviewGitHubRunner{}
	fixture.app.deps.GH = fixedGH(github)

	code := fixture.app.Run(context.Background(), []string{"review", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(readSingleReviewLog(t, fixture.root), "Ticket: #42") {
		t.Fatalf("review log missing GitHub ticket reference")
	}
	if github.hasCall("issue comment 42 --body") {
		t.Fatalf("GitHub calls = %#v, want no GitHub review comment in local-log mode", github.calls)
	}
}

func TestReviewTopSeamRevisePrintsVerdictAndExitsNonZero(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-revise"},
		{Type: harness.EventAssistantText, Text: reviseVerdictText},
	}}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("review code = 0, want non-zero for revise")
	}
	if !strings.Contains(fixture.stdout.String(), "VERDICT: revise") || !strings.Contains(fixture.stderr.String(), "review verdict is revise") {
		t.Fatalf("stdout = %q, stderr = %q, want revise verdict and failure", fixture.stdout.String(), fixture.stderr.String())
	}
	if logContents := readSingleReviewLog(t, fixture.root); !strings.Contains(logContents, "[blocking] internal/orchestration/review.go:42") {
		t.Fatalf("review log = %q, want blocking finding", logContents)
	}
}

func TestReviewUnparseableVerdictIsReaskedOnceInSameSession(t *testing.T) {
	harness := &scriptedHarness{
		first: []harness.Event{
			{Type: harness.EventSession, SessionID: "same-session"},
			{Type: harness.EventAssistantText, Text: "I reviewed it but forgot the contract."},
		},
		retry: []harness.Event{
			{Type: harness.EventSession, SessionID: "same-session"},
			{Type: harness.EventAssistantText, Text: approveVerdictText},
		},
	}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if harness.resumeCount != 1 || harness.resumedSession != "same-session" || harness.resumePrompt != "emit the verdict block" {
		t.Fatalf("resume = count %d, session %q, prompt %q; want one same-session re-ask", harness.resumeCount, harness.resumedSession, harness.resumePrompt)
	}
}

func TestReviewUnparseableVerdictFailsAfterOneReaskWithoutWritingReviewLog(t *testing.T) {
	harness := &scriptedHarness{
		first: []harness.Event{
			{Type: harness.EventSession, SessionID: "fallback-session"},
			{Type: harness.EventAssistantText, Text: "not a verdict"},
		},
		retry: []harness.Event{
			{Type: harness.EventAssistantText, Text: "still not a verdict"},
		},
	}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("review code = 0, want non-zero for unparseable verdict")
	}
	if harness.resumeCount != 1 || !strings.Contains(fixture.stderr.String(), "reviewer produced no parseable verdict") {
		t.Fatalf("resume count = %d, stderr = %q, want one retry and clear failure", harness.resumeCount, fixture.stderr.String())
	}
	if strings.Contains(fixture.stdout.String(), "reviewer — verdict was unparseable") {
		t.Fatalf("stdout = %q, want no synthetic finding", fixture.stdout.String())
	}
	if logs := reviewLogPaths(t, fixture.root); len(logs) != 0 {
		t.Fatalf("review logs = %v, want none after an unparseable verdict", logs)
	}
	runDirs := reviewRunArtifactPaths(t, fixture.root)
	if len(runDirs) != 1 {
		t.Fatalf("run artifact directories = %v, want one failed-review artifact directory", runDirs)
	}
	artifacts := readAllFiles(t, runDirs[0])
	if !strings.Contains(artifacts, "not a verdict") || !strings.Contains(artifacts, "still not a verdict") {
		t.Fatalf("run artifacts = %q, want the full failed review transcript", artifacts)
	}
}

func TestGitHubReviewUnparseableVerdictDoesNotPostComment(t *testing.T) {
	harness := &scriptedHarness{
		first: []harness.Event{
			{Type: harness.EventSession, SessionID: "github-fallback-session"},
			{Type: harness.EventAssistantText, Text: "not a verdict"},
		},
		retry: []harness.Event{
			{Type: harness.EventAssistantText, Text: "still not a verdict"},
		},
	}
	fixture := newReviewFixture(t, harness)
	configureGitHubReviewLog(t, fixture.root)
	github := &reviewGitHubRunner{}
	fixture.app.deps.GH = fixedGH(github)

	code := fixture.app.Run(context.Background(), []string{"review", "#42"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "reviewer produced no parseable verdict") {
		t.Fatalf("review code = %d, stderr = %q, want clear unparseable-verdict failure", code, fixture.stderr.String())
	}
	if github.hasCall("issue comment 42 --body") {
		t.Fatalf("GitHub calls = %#v, want no review comment after an unparseable verdict", github.calls)
	}
}

func TestReviewUnparseableVerdictWithoutSessionFailsWithoutWritingReviewLog(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventAssistantText, Text: "not a verdict and no session"},
	}}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("review code = 0, want non-zero for unparseable verdict")
	}
	if harness.resumeCount != 0 || !strings.Contains(fixture.stderr.String(), "reviewer produced no parseable verdict") {
		t.Fatalf("resume count = %d, stderr = %q, want clear failure without a session", harness.resumeCount, fixture.stderr.String())
	}
	if logs := reviewLogPaths(t, fixture.root); len(logs) != 0 {
		t.Fatalf("review logs = %v, want none after an unparseable verdict", logs)
	}
	if dirs := reviewRunArtifactPaths(t, fixture.root); len(dirs) != 1 {
		t.Fatalf("run artifact directories = %v, want one failed-review artifact directory", dirs)
	}
}

func TestReviewRawPassesHarnessOutputWithoutFeedRendering(t *testing.T) {
	rawLine := "{\"type\":\"assistant\",\"message\":\"raw\"}\n"
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventAssistantText, Text: approveVerdictText, Raw: rawLine},
	}}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review", "--raw"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	got := fixture.stdout.String()
	if !strings.HasPrefix(got, "syl review — working tree\n  reviewer:  claude · claude-sonnet-5 · effort medium\n") || !strings.HasSuffix(got, rawLine) {
		t.Fatalf("raw stdout = %q, want the identification banner followed by exact raw output %q", got, rawLine)
	}
	if strings.Contains(got, "tool:") || strings.Contains(got, "VERDICT:") {
		t.Fatalf("raw stdout = %q, want no parsed rendering", got)
	}
}

const approveVerdictText = "VERDICT: approve\nSUMMARY: The working tree is ready\nFINDINGS:\n"

const reviseVerdictText = "VERDICT: revise\nSUMMARY: Fix the remaining issue\nFINDINGS:\n- [blocking] internal/orchestration/review.go:42 — handle a missing session\n"

type reviewFixture struct {
	root      string
	app       *App
	harnesses map[string]harness.Adapter
	stdout    strings.Builder
	stderr    strings.Builder
}

func newReviewFixture(t *testing.T, adapter harness.Adapter) *reviewFixture {
	t.Helper()
	base := newTopSeamFixture(t)
	base.harnesses = map[string]harness.Adapter{"claude": adapter}
	base.app = New(base.root, base.root, Dependencies{
		Harnesses: harnessFactories(base.harnesses),
		Git:       fixedGit(&reviewGitRunner{diff: "diff --git a/tracked.txt b/tracked.txt\n+reviewed\n"}),
	})
	return &reviewFixture{root: base.root, app: base.app, harnesses: base.harnesses}
}

func readSingleReviewLog(t *testing.T, root string) string {
	t.Helper()
	matches := reviewLogPaths(t, root)
	if len(matches) != 1 {
		t.Fatalf("found %d review logs, want 1: %v", len(matches), matches)
	}
	contents, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read review log: %v", err)
	}
	return string(contents)
}

func reviewLogPaths(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".scratch", "*", "reviews", "*.md"))
	if err != nil {
		t.Fatalf("glob review logs: %v", err)
	}
	return matches
}

func reviewRunArtifactPaths(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".syl", "runs", "*"))
	if err != nil {
		t.Fatalf("glob review run artifacts: %v", err)
	}
	return matches
}

func configureLocalIssues(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), `issues = "github"`, `issues = "local"`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func configureGitHubReviewLog(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), `reviews = "local"`, `reviews = "github"`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func configureGitLabReviewLog(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), `issues = "github"`, `issues = "gitlab"`, 1)
	updated = strings.Replace(updated, `reviews = "local"`, `reviews = "gitlab"`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeReviewTicket(t *testing.T, root, feature, number, title, body string) {
	t.Helper()
	path := filepath.Join(root, ".scratch", feature, "issues", number+"-ticket.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "# " + number + " — " + title + "\n\n" +
		"**What to build:** " + body + "\n\n" +
		"**Blocked by:** None — can start immediately.\n\n" +
		"**Status:** todo\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

type scriptedHarness struct {
	first          []harness.Event
	retry          []harness.Event
	knownSessionID string
	beforeRun      func() error
	runRequest     harness.Request
	resumeCount    int
	resumedSession string
	resumePrompt   string
}

func (h *scriptedHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.runRequest = request
	if h.beforeRun != nil {
		if err := h.beforeRun(); err != nil {
			return nil, err
		}
	}
	return scriptedHarnessStream{events: h.first, sessionID: h.knownSessionID}, nil
}

func (h *scriptedHarness) Resume(_ context.Context, sessionID string, request harness.Request) (harness.Stream, error) {
	h.resumeCount++
	h.resumedSession = sessionID
	h.resumePrompt = request.Prompt
	return scriptedHarnessStream{events: h.retry, sessionID: h.knownSessionID}, nil
}

func (*scriptedHarness) Attach(context.Context, harness.Request) error { return nil }

type scriptedHarnessStream struct {
	events    []harness.Event
	sessionID string
}

func (s scriptedHarnessStream) Events() <-chan harness.Event {
	channel := make(chan harness.Event, len(s.events))
	for _, event := range s.events {
		channel <- event
	}
	close(channel)
	return channel
}

func (scriptedHarnessStream) Wait() error { return nil }

func (s scriptedHarnessStream) SessionID() string { return s.sessionID }

type reviewGitHubRunner struct {
	calls       []string
	commentBody string
}

type reviewGitLabRunner struct {
	calls    []string
	noteBody string
}

type reviewGitRunner struct {
	calls   []string
	diff    string
	diffErr error
}

func (r *reviewGitRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch call {
	case "rev-parse HEAD":
		return "branch-point\n", nil
	case "branch --show-current":
		return "review-branch\n", nil
	case "diff branch-point":
		return r.diff, r.diffErr
	case "ls-files --others --exclude-standard -z":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected git command %q", call)
	}
}

func (r *reviewGitHubRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch {
	case call == "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case call == "issue view 42 --json number,title,body,state,labels":
		return `{"number":42,"title":"Review this","body":"Review body","state":"OPEN","labels":[{"name":"doing"}]}`, nil
	case len(args) >= 5 && args[0] == "issue" && args[1] == "comment":
		r.commentBody = args[len(args)-1]
		return "", nil
	default:
		return "", fmt.Errorf("unexpected gh command %q", call)
	}
}

func (r *reviewGitHubRunner) hasCall(call string) bool {
	for _, got := range r.calls {
		if got == call {
			return true
		}
	}
	return false
}

func (r *reviewGitLabRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch {
	case call == "label list --output json --per-page 100":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case call == "issue view 42 --output json":
		return `{"iid":42,"title":"Review this","description":"Review body","state":"opened","labels":["doing"]}`, nil
	case len(args) >= 5 && args[0] == "issue" && args[1] == "note":
		r.noteBody = args[len(args)-1]
		return "", nil
	default:
		return "", fmt.Errorf("unexpected glab command %q", call)
	}
}

func (r *reviewGitLabRunner) hasCallPrefix(prefix string) bool {
	for _, call := range r.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}
