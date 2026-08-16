package cli

import (
	"context"
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
