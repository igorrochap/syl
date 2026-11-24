package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestImplementQuestionPausesAndResumesTheSameSession(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	notifier := &recordingNotifier{}
	loop := &questionHarness{
		root: fixture.root,
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Before the decision.\nQUESTION:\nWhich database should this use?\nEND QUESTION\nignored"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
		resumeStreams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Continuing after the answer."}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})
	fixture.app.deps.Input = strings.NewReader("Use SQLite.\\\nwith WAL.\n")
	fixture.app.deps.Notifier = notifier

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumes) != 1 || loop.resumes[0].sessionID != "implement-session" || loop.resumes[0].prompt != "Use SQLite.\nwith WAL." {
		t.Fatalf("resumes = %#v, want one answer on the original implement session", loop.resumes)
	}
	wantNotification := "[project: " + filepath.Base(fixture.root) + " | branch: feat/add-resilient-workflow] syl is waiting for your answer on #42"
	if len(notifier.messages) < 1 || notifier.messages[0] != wantNotification {
		t.Fatalf("notifications = %v, want %q", notifier.messages, wantNotification)
	}
	stdout := fixture.stdout.String()
	if !strings.Contains(stdout, "awaiting answer") || !strings.Contains(stdout, "Which database should this use?") || !strings.Contains(stdout, "answer> ") {
		t.Fatalf("stdout = %q, want the full question and terminal answer prompt", stdout)
	}
	if strings.Contains(stdout, "Before the decision.") || strings.Contains(stdout, "--- implement output resumes ---") || strings.Contains(stdout, "answer sent — resuming implementer…") {
		t.Fatalf("stdout = %q, want compact question output without preceding prose or resume framing", stdout)
	}
	if !strings.Contains(stdout, "Iterations: 1") || !strings.Contains(stdout, "Final verdict: approve") {
		t.Fatalf("stdout = %q, want one-iteration approval", stdout)
	}
}

func TestImplementVerboseQuestionKeepsExistingFraming(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &questionHarness{
		root: fixture.root,
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Before the decision.\nQUESTION:\nWhich database should this use?\nEND QUESTION"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
		resumeStreams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Continuing after the answer."}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})
	fixture.app.deps.Input = strings.NewReader("Use SQLite.\n")

	code := fixture.app.Run(context.Background(), []string{"implement", "#42", "--verbose"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	stdout := fixture.stdout.String()
	for _, expected := range []string{
		"Before the decision.",
		"implement question on #42",
		"Which database should this use?",
		"answer> ",
		"answer sent — resuming implementer…",
		"--- implement output resumes ---",
		"Continuing after the",
		"answer.",
	} {
		if !strings.Contains(stdout, expected) {
			t.Fatalf("verbose stdout = %q, want %q", stdout, expected)
		}
	}
	if strings.Contains(stdout, "    awaiting answer") {
		t.Fatalf("verbose stdout = %q, want the existing verbose question framing", stdout)
	}
}

func TestReviewerQuestionPausesAndResumesTheSameSession(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	notifier := &recordingNotifier{}
	loop := &questionHarness{
		root: fixture.root,
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: "QUESTION:\nShould this be treated as a blocker?\nEND QUESTION"}},
		},
		resumeStreams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})
	fixture.app.deps.Input = strings.NewReader("Yes, it is a blocker.\n")
	fixture.app.deps.Notifier = notifier

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumes) != 1 || loop.resumes[0].sessionID != "review-session" || loop.resumes[0].prompt != "Yes, it is a blocker." {
		t.Fatalf("resumes = %#v, want one answer on the original review session", loop.resumes)
	}
	wantNotification := "[project: " + filepath.Base(fixture.root) + " | branch: feat/add-resilient-workflow] syl is waiting for your answer on #42"
	if len(notifier.messages) < 1 || notifier.messages[0] != wantNotification {
		t.Fatalf("notifications = %v, want %q", notifier.messages, wantNotification)
	}
}

func TestTwoQuestionsPauseTwiceWithoutRestartingTheIteration(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &questionHarness{
		root: fixture.root,
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "QUESTION:\nFirst question\nEND QUESTION"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
		resumeStreams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "QUESTION:\nSecond question\nEND QUESTION"}},
			{{Type: harness.EventSession, SessionID: "implement-session"}, {Type: harness.EventAssistantText, Text: "Done implementing."}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})
	fixture.app.deps.Input = strings.NewReader("First answer\nSecond answer\n")

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumes) != 2 {
		t.Fatalf("resume count = %d, want two sequential question resumes", len(loop.resumes))
	}
	for i, want := range []string{"First answer", "Second answer"} {
		if loop.resumes[i].sessionID != "implement-session" || loop.resumes[i].prompt != want {
			t.Fatalf("resume %d = %#v, want original session and prompt %q", i, loop.resumes[i], want)
		}
	}
	if !strings.Contains(fixture.stdout.String(), "Iterations: 1") {
		t.Fatalf("stdout = %q, want questions not to count as iterations", fixture.stdout.String())
	}
}

func TestOneShotReviewHonorsQuestionProtocol(t *testing.T) {
	notifier := &recordingNotifier{}
	loop := &questionHarness{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "QUESTION:\nWhich files are in scope?\nEND QUESTION"},
		}},
		resumeStreams: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: approveVerdictText},
		}},
	}
	fixture := newReviewFixture(t, loop)
	fixture.app.deps.Input = strings.NewReader("The current working-tree diff.\n")
	fixture.app.deps.Notifier = notifier

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if len(loop.resumes) != 1 || loop.resumes[0].sessionID != "review-session" || loop.resumes[0].prompt != "The current working-tree diff." {
		t.Fatalf("resumes = %#v, want one same-session review resume", loop.resumes)
	}
	wantNotification := "[project: " + filepath.Base(fixture.root) + " | branch: review-branch] syl is waiting for your answer on review"
	if len(notifier.messages) != 1 || notifier.messages[0] != wantNotification {
		t.Fatalf("notifications = %v, want %q", notifier.messages, wantNotification)
	}
	if !strings.Contains(fixture.stdout.String(), "VERDICT: approve") {
		t.Fatalf("stdout = %q, want approval verdict", fixture.stdout.String())
	}
}

func TestOneShotReviewRawQuestionRendersWithoutProtocolMarkers(t *testing.T) {
	loop := &questionHarness{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{
				Type: harness.EventAssistantText,
				Text: "QUESTION:\nWhich files are in scope?\nEND QUESTION",
				Raw:  "{\"type\":\"assistant\",\"text\":\"QUESTION:\\nWhich files are in scope?\\nEND QUESTION\"}\n",
			},
		}},
		resumeStreams: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: approveVerdictText, Raw: "{\"type\":\"assistant\",\"text\":\"approved\"}\n"},
		}},
	}
	fixture := newReviewFixture(t, loop)
	fixture.app.deps.Input = strings.NewReader("The current working-tree diff.\n")

	code := fixture.app.Run(context.Background(), []string{"review", "--raw"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if strings.Contains(fixture.stdout.String(), "QUESTION:") || strings.Contains(fixture.stdout.String(), "END QUESTION") {
		t.Fatalf("stdout = %q, want raw QUESTION markers hidden in raw mode", fixture.stdout.String())
	}
	if !strings.Contains(fixture.stdout.String(), "review question on review") {
		t.Fatalf("stdout = %q, want rendered review question in raw mode", fixture.stdout.String())
	}
}

func TestQuestionDoesNotCancelHarnessContextBeforeResume(t *testing.T) {
	adapter := &contextAwareQuestionHarness{}
	questions := orchestration.NewQuestionHandler(strings.NewReader("Use the existing schema.\n"), io.Discard, "review", nil, nil)

	review, err := orchestration.RunReviewExecution(context.Background(), adapter, harness.Request{}, io.Discard, orchestration.ParsedHarnessOutput, questions)
	if err != nil {
		t.Fatalf("run review execution: %v", err)
	}
	if review.Verdict.Status != verdict.Approve {
		t.Fatalf("verdict = %q, want approve", review.Verdict.Status)
	}
	if adapter.cancelledBeforeResume {
		t.Fatal("harness context was cancelled before Resume; QUESTION should let the turn finish naturally")
	}
}

type questionHarness struct {
	root          string
	runs          [][]harness.Event
	resumeStreams [][]harness.Event
	resumes       []questionResume
	streams       [][]harness.Event
}

type questionResume struct {
	sessionID string
	prompt    string
}

func (h *questionHarness) Run(_ context.Context, _ harness.Request) (harness.Stream, error) {
	if len(h.streams) >= len(h.runs) {
		return nil, fmt.Errorf("no scripted run stream remains")
	}
	if h.root != "" && len(h.streams) == 0 {
		changePath := filepath.Join(h.root, "change.txt")
		if err := os.WriteFile(changePath, []byte("implemented\n"), 0o644); err != nil {
			return nil, err
		}
		if output, err := exec.Command("git", "-C", h.root, "add", changePath).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("stage fixture change: %v\n%s", err, output)
		}
	}
	events := h.runs[len(h.streams)]
	h.streams = append(h.streams, events)
	return scriptedHarnessStream{events: events}, nil
}

func (h *questionHarness) Resume(_ context.Context, sessionID string, request harness.Request) (harness.Stream, error) {
	h.resumes = append(h.resumes, questionResume{sessionID: sessionID, prompt: request.Prompt})
	index := len(h.resumes) - 1
	if index >= len(h.resumeStreams) {
		return nil, fmt.Errorf("no scripted resume stream remains")
	}
	return scriptedHarnessStream{events: h.resumeStreams[index]}, nil
}

func (*questionHarness) Attach(context.Context, harness.Request) error { return nil }

func (*questionHarness) AttachSession(context.Context, string, harness.Request) error { return nil }

type contextAwareQuestionHarness struct {
	runContext            context.Context
	cancelledBeforeResume bool
}

func (h *contextAwareQuestionHarness) Run(ctx context.Context, _ harness.Request) (harness.Stream, error) {
	h.runContext = ctx
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: "QUESTION:\nWhich schema?\nEND QUESTION"},
	}}, nil
}

func (h *contextAwareQuestionHarness) Resume(_ context.Context, sessionID string, _ harness.Request) (harness.Stream, error) {
	select {
	case <-h.runContext.Done():
		h.cancelledBeforeResume = true
	default:
	}
	if sessionID != "review-session" {
		return nil, fmt.Errorf("resumed session %q, want review-session", sessionID)
	}
	return scriptedHarnessStream{events: []harness.Event{
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}, nil
}

func (*contextAwareQuestionHarness) Attach(context.Context, harness.Request) error { return nil }

func (*contextAwareQuestionHarness) AttachSession(context.Context, string, harness.Request) error {
	return nil
}
