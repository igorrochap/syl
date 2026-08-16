package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/rig/internal/harness"
)

func TestReviewTopSeamApprovePrintsFeedAndWritesLog(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "session-approve"},
		{Type: harness.EventAssistantText, Text: "Reviewing the diff...\n"},
		{Type: harness.EventToolUse, ToolName: "Bash", ArgumentGist: `{"command":"git diff"}`},
		{Type: harness.EventAssistantText, Text: "Done.\n" + approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	if err := os.WriteFile(filepath.Join(fixture.root, "change.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "Reviewing the diff...") || !strings.Contains(fixture.stdout.String(), "tool: Bash — {\"command\":\"git diff\"}") {
		t.Fatalf("stdout = %q, want parsed feed", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "VERDICT: approve") {
		t.Fatalf("stdout = %q, want verdict", fixture.stdout.String())
	}
	if !strings.Contains(harness.runRequest.Prompt, "/code-review") || !strings.Contains(harness.runRequest.Prompt, "current working-tree diff") {
		t.Fatalf("review prompt = %q, want named skill and current diff instruction", harness.runRequest.Prompt)
	}
	if harness.runRequest.Model != "sonnet-5" || harness.runRequest.Effort != "medium" {
		t.Fatalf("review request = %#v, want configured model and effort", harness.runRequest)
	}
	logContents := readSingleReviewLog(t, fixture.root)
	if !strings.Contains(logContents, "VERDICT: approve") || !strings.Contains(logContents, "Reviewed ref: working-tree") || !strings.Contains(logContents, "Timestamp:") {
		t.Fatalf("review log = %q, want verdict, ref, and timestamp", logContents)
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

	code := fixture.app.Run(context.Background(), []string{"review", "#07"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	for _, expected := range []string{
		"review the current diff against this ticket",
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
	fixture.app.deps.GH = github

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

func TestReviewWithGitHubReviewLogRequiresIssueNumber(t *testing.T) {
	harness := &scriptedHarness{}
	fixture := newReviewFixture(t, harness)
	configureGitHubReviewLog(t, fixture.root)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "requires an issue reference") {
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
	fixture.app.deps.GH = github

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
	if logContents := readSingleReviewLog(t, fixture.root); !strings.Contains(logContents, "[blocking] internal/cli/review.go:42") {
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

func TestReviewUnparseableVerdictFallsBackToSyntheticBlockingFinding(t *testing.T) {
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
		t.Fatal("review code = 0, want non-zero for synthetic revise")
	}
	if harness.resumeCount != 1 || !strings.Contains(fixture.stdout.String(), "verdict was unparseable") {
		t.Fatalf("resume count = %d, stdout = %q, want one retry and synthetic finding", harness.resumeCount, fixture.stdout.String())
	}
	if logContents := readSingleReviewLog(t, fixture.root); !strings.Contains(logContents, "[blocking] reviewer — verdict was unparseable") {
		t.Fatalf("review log = %q, want synthetic blocking finding", logContents)
	}
}

func TestReviewUnparseableVerdictWithoutSessionFallsBackToSyntheticFinding(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventAssistantText, Text: "not a verdict and no session"},
	}}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("review code = 0, want non-zero for synthetic revise")
	}
	if harness.resumeCount != 0 || !strings.Contains(fixture.stdout.String(), "verdict was unparseable") {
		t.Fatalf("resume count = %d, stdout = %q, want fallback without a session", harness.resumeCount, fixture.stdout.String())
	}
	if logContents := readSingleReviewLog(t, fixture.root); !strings.Contains(logContents, "[blocking] reviewer — verdict was unparseable") {
		t.Fatalf("review log = %q, want synthetic blocking finding", logContents)
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
	if got := fixture.stdout.String(); got != rawLine {
		t.Fatalf("raw stdout = %q, want exact %q", got, rawLine)
	}
	if strings.Contains(fixture.stdout.String(), "tool:") || strings.Contains(fixture.stdout.String(), "VERDICT:") {
		t.Fatalf("raw stdout = %q, want no parsed rendering", fixture.stdout.String())
	}
}

const approveVerdictText = "VERDICT: approve\nSUMMARY: The working tree is ready\nFINDINGS:\n"

const reviseVerdictText = "VERDICT: revise\nSUMMARY: Fix the remaining issue\nFINDINGS:\n- [blocking] internal/cli/review.go:42 — handle a missing session\n"

type reviewFixture struct {
	root   string
	app    *App
	stdout strings.Builder
	stderr strings.Builder
}

func newReviewFixture(t *testing.T, adapter harness.Adapter) *reviewFixture {
	t.Helper()
	base := newTopSeamFixture(t)
	base.app = New(base.root, Dependencies{
		Harnesses: map[string]harness.Adapter{"claude": adapter},
	})
	return &reviewFixture{root: base.root, app: base.app}
}

func readSingleReviewLog(t *testing.T, root string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".scratch", "*", "reviews", "*.md"))
	if err != nil {
		t.Fatalf("glob review logs: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d review logs, want 1: %v", len(matches), matches)
	}
	contents, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read review log: %v", err)
	}
	return string(contents)
}

func configureLocalIssues(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".rig", "config.toml")
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
	path := filepath.Join(root, ".rig", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), `reviews = "local"`, `reviews = "github"`, 1)
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
	runRequest     harness.Request
	resumeCount    int
	resumedSession string
	resumePrompt   string
}

func (h *scriptedHarness) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	h.runRequest = request
	return scriptedHarnessStream{events: h.first}, nil
}

func (h *scriptedHarness) Resume(_ context.Context, sessionID, prompt string) (harness.Stream, error) {
	h.resumeCount++
	h.resumedSession = sessionID
	h.resumePrompt = prompt
	return scriptedHarnessStream{events: h.retry}, nil
}

func (*scriptedHarness) Attach(context.Context, harness.Request) error { return nil }

type scriptedHarnessStream struct {
	events []harness.Event
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

type reviewGitHubRunner struct {
	calls       []string
	commentBody string
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
