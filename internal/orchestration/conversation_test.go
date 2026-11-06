package orchestration

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/verdict"
)

const conversationTestVerdict = "VERDICT: approve\nSUMMARY: The working tree is ready\nFINDINGS:\n"

func TestRunReviewExecutionKeepsPartialOutputWhenHarnessWaitFails(t *testing.T) {
	adapter := &failedConversationAdapter{
		events: []harness.Event{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "Partial reviewer response."},
		},
		err: errors.New("idle timeout\nSession state: Subagents: 2 total, 2 finished"),
	}

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{}, io.Discard, ParsedHarnessOutput, nil)
	if err == nil || !strings.Contains(err.Error(), "Subagents: 2 total, 2 finished") {
		t.Fatalf("RunReviewExecution() error = %v, want failed stream error", err)
	}
	if review.Transcript != "Partial reviewer response." || strings.TrimSpace(review.Feed) != "Partial reviewer response." ||
		len(review.SessionIDs) != 1 || review.SessionIDs[0] != "review-session" {
		t.Fatalf("partial review = %#v, want captured transcript, feed, and session", review)
	}
}

func TestRunReviewExecutionRecoversStalledSessionWithFinishedSubagentReports(t *testing.T) {
	stall := &harness.SessionStalledError{
		SessionID:    "review-session",
		StateSummary: "Task notifications: 2 enqueued, 0 dequeued or removed\nSubagents: 2 total, 2 finished",
		Reports: []harness.SubagentReport{
			{Path: "/claude/session/subagents/agent-1.jsonl", Text: "Standards report: no findings."},
			{Path: "/claude/session/subagents/agent-2.jsonl", Text: "Spec report: missing validation."},
		},
	}
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "Partial reviewer response."},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: conversationTestVerdict},
		}},
		runWaitErrors:    []error{stall},
		resumeWaitErrors: []error{nil},
	}

	review, err := RunReviewExecution(
		context.Background(), adapter, harness.Request{Prompt: "original review"}, io.Discard, QuietHarnessOutput, nil,
	)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve {
		t.Fatalf("verdict = %#v, want recovered approve verdict", review.Verdict)
	}
	if len(adapter.resumeCalls) != 1 || adapter.resumeCalls[0].sessionID != "review-session" {
		t.Fatalf("resume calls = %#v, want one resume of stalled session", adapter.resumeCalls)
	}
	for _, want := range []string{
		"Standards report: no findings.",
		"Spec report: missing validation.",
		"Merge the reports",
		"mandatory verdict block",
	} {
		if !strings.Contains(adapter.resumeCalls[0].prompt, want) {
			t.Errorf("recovery prompt = %q, want %q", adapter.resumeCalls[0].prompt, want)
		}
	}
	if len(review.SessionIDs) != 2 || review.SessionIDs[0] != "review-session" || review.SessionIDs[1] != "review-session" {
		t.Fatalf("session ids = %v, want one id recorded for each turn", review.SessionIDs)
	}
	for _, want := range []string{"Partial reviewer response.", conversationTestVerdict} {
		if !strings.Contains(review.Transcript, want) || !strings.Contains(review.Feed, want) {
			t.Errorf("review output omitted %q: %#v", want, review)
		}
	}
}

func TestRunReviewExecutionFailsWhenStalledRecoveryAlsoStalls(t *testing.T) {
	firstStall := &harness.SessionStalledError{
		SessionID: "review-session", StateSummary: "first session summary",
	}
	secondStall := &harness.SessionStalledError{
		SessionID: "review-session", StateSummary: "second session summary",
	}
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "First turn."},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "Recovery turn."},
		}},
		runWaitErrors:    []error{firstStall},
		resumeWaitErrors: []error{secondStall},
	}

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{}, io.Discard, QuietHarnessOutput, nil)
	if err == nil || !strings.Contains(err.Error(), "second session summary") {
		t.Fatalf("RunReviewExecution() error = %v, want second stalled-session summary", err)
	}
	if len(adapter.resumeCalls) != 1 {
		t.Fatalf("resume calls = %d, want exactly one recovery attempt", len(adapter.resumeCalls))
	}
	for _, want := range []string{"First turn.", "Recovery turn."} {
		if !strings.Contains(review.Transcript, want) || !strings.Contains(review.Feed, want) {
			t.Errorf("failed review output omitted %q: %#v", want, review)
		}
	}
	if len(review.SessionIDs) != 2 {
		t.Fatalf("session ids = %v, want both turn ids", review.SessionIDs)
	}
}

func TestRunReviewExecutionUsesReportPathsWhenRecoveryReportsAreTooLarge(t *testing.T) {
	prompt := stalledReviewRecoveryPrompt([]harness.SubagentReport{{
		Path:     "/claude/session/subagents/agent-large.jsonl",
		Text:     "must not be embedded",
		TooLarge: true,
	}})
	if !strings.Contains(prompt, "/claude/session/subagents/agent-large.jsonl") {
		t.Fatalf("recovery prompt = %q, want report path", prompt)
	}
	if strings.Contains(prompt, "must not be embedded") {
		t.Fatalf("recovery prompt = %q, want path in place of report text", prompt)
	}
	if !strings.Contains(prompt, "merge the finished subagent reports") || !strings.Contains(prompt, "mandatory verdict block") {
		t.Fatalf("recovery prompt = %q, want report merge and verdict instructions", prompt)
	}
}

func TestRunReviewExecutionFallsBackWhenStalledSessionResumeCannotStart(t *testing.T) {
	stall := &harness.SessionStalledError{SessionID: "review-session", StateSummary: "finished reports queued"}
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "Partial first review."},
		}, {
			{Type: harness.EventSession, SessionID: "fresh-review-session"},
			{Type: harness.EventAssistantText, Text: conversationTestVerdict},
		}},
		runWaitErrors:     []error{stall, nil},
		resumeStartErrors: []error{errors.New("session is unavailable")},
	}

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{Prompt: "original review"}, io.Discard, QuietHarnessOutput, nil)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve || adapter.runCalls != 2 || len(adapter.resumeCalls) != 1 {
		t.Fatalf("review = %#v; run calls = %d; resume calls = %d, want fresh-run fallback verdict", review, adapter.runCalls, len(adapter.resumeCalls))
	}
	if !strings.Contains(review.Transcript, "Partial first review.") || !strings.Contains(review.Transcript, conversationTestVerdict) {
		t.Fatalf("transcript = %q, want stalled and fallback turns", review.Transcript)
	}
}

func TestRunReviewExecutionDoesNotDuplicateResultAfterAssistantDeltas(t *testing.T) {
	adapter := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: conversationTestVerdict},
		{Type: harness.EventResult, Text: conversationTestVerdict},
	}}}
	var output strings.Builder

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{}, &output, ParsedHarnessOutput, nil)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve {
		t.Fatalf("verdict status = %q, want approve", review.Verdict.Status)
	}
	if review.Transcript != conversationTestVerdict {
		t.Fatalf("transcript = %q, want one assistant message %q", review.Transcript, conversationTestVerdict)
	}
	if output.String() != conversationTestVerdict {
		t.Fatalf("parsed output = %q, want one assistant message %q", output.String(), conversationTestVerdict)
	}
}

func TestConsumeHarnessStreamSeparatesToolsWithoutSplittingAssistantChunks(t *testing.T) {
	stream := scriptedConversationStream{events: []harness.Event{
		{Type: harness.EventAssistantText, Text: "running the full quality gate"},
		{Type: harness.EventAssistantText, Text: " now"},
		{Type: harness.EventToolUse, ToolName: "Bash", ArgumentGist: "go test ./..."},
	}}
	var output strings.Builder

	if _, err := consumeHarnessStream(stream, &output, ParsedHarnessOutput); err != nil {
		t.Fatalf("consumeHarnessStream() error = %v", err)
	}

	want := "running the full quality gate now\ntool: Bash — go test ./...\n"
	if output.String() != want {
		t.Fatalf("parsed output = %q, want %q", output.String(), want)
	}
}

func TestRunReviewExecutionUsesResultTextWhenNoAssistantDeltasArrive(t *testing.T) {
	adapter := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventResult, Text: conversationTestVerdict},
	}}}

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{}, io.Discard, ParsedHarnessOutput, nil)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve {
		t.Fatalf("verdict status = %q, want approve", review.Verdict.Status)
	}
	if review.Transcript != conversationTestVerdict {
		t.Fatalf("transcript = %q, want result fallback %q", review.Transcript, conversationTestVerdict)
	}
}

func TestRunReviewExecutionFailsAfterOneUnparseableVerdictReask(t *testing.T) {
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "The review is complete, but the verdict is missing."},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "Still no structured verdict."},
		}},
	}

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{}, io.Discard, ParsedHarnessOutput, nil)
	if err == nil || !strings.Contains(err.Error(), "reviewer produced no parseable verdict") {
		t.Fatalf("RunReviewExecution() error = %v, want unparseable-verdict failure", err)
	}
	if len(adapter.resumeCalls) != 1 || adapter.resumeCalls[0].prompt != "emit the verdict block" {
		t.Fatalf("resume calls = %#v, want exactly one verdict re-ask", adapter.resumeCalls)
	}
	if !strings.Contains(review.Transcript, "Still no structured verdict.") {
		t.Fatalf("transcript = %q, want the full failed review transcript", review.Transcript)
	}
}

func TestConsumeHarnessStreamPreservesResultErrorsWithAndWithoutDeltas(t *testing.T) {
	for _, tt := range []struct {
		name        string
		assistant   string
		resultError string
	}{
		{name: "deltas present", assistant: "partial response", resultError: "Claude failed after streaming"},
		{name: "deltas absent", resultError: "Claude failed before streaming"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []harness.Event{{Type: harness.EventResult, Text: tt.resultError, IsError: true}}
			if tt.assistant != "" {
				events = append([]harness.Event{{Type: harness.EventAssistantText, Text: tt.assistant}}, events...)
			}

			_, err := consumeHarnessStream(scriptedConversationStream{events: events}, io.Discard, ParsedHarnessOutput)
			if err == nil || !strings.Contains(err.Error(), "harness returned an error: "+tt.resultError) {
				t.Fatalf("consumeHarnessStream() error = %v, want harness error %q", err, tt.resultError)
			}
		})
	}
}

func TestRunHarnessConversationPausesOnceForQuestionWithDuplicatedResult(t *testing.T) {
	question := "Before the decision.\nQUESTION:\nWhich database should this use?\nEND QUESTION"
	request := harness.Request{MCP: true}
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: question},
			{Type: harness.EventResult, Text: question},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: conversationTestVerdict},
		}},
	}
	questions := NewQuestionHandler(strings.NewReader("Use SQLite.\n"), io.Discard, "review", nil, nil)

	result, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
		return adapter.Run(ctx, request)
	}, conversationOptions{
		request:   request,
		output:    io.Discard,
		mode:      ParsedHarnessOutput,
		questions: questions,
	})
	if err != nil {
		t.Fatalf("runHarnessConversation() error = %v", err)
	}
	if len(adapter.resumeCalls) != 1 {
		t.Fatalf("resume calls = %d, want exactly one pause/resume cycle", len(adapter.resumeCalls))
	}
	if !adapter.resumeCalls[0].mcp {
		t.Fatal("resume request MCP = false, want original request setting true")
	}
	if result.Transcript != "Before the decision.\n"+conversationTestVerdict {
		t.Fatalf("transcript = %q, want one question prefix and resumed assistant message", result.Transcript)
	}
}

func TestRunHarnessConversationKeepsQuestionOutputWhenAnswerCannotBeProvided(t *testing.T) {
	for _, test := range []struct {
		name      string
		questions *QuestionHandler
		wantError string
	}{
		{name: "missing handler", wantError: "no terminal question handler"},
		{
			name:      "unavailable input",
			questions: NewQuestionHandler(strings.NewReader(""), io.Discard, "review", nil, nil),
			wantError: "terminal input is unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &scriptedConversationAdapter{runs: [][]harness.Event{{
				{Type: harness.EventSession, SessionID: "review-session"},
				{Type: harness.EventAssistantText, Text: "Before the question.\nQUESTION:\nWhich database?\nEND QUESTION"},
			}}}
			result, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
				return adapter.Run(ctx, harness.Request{})
			}, conversationOptions{
				output: io.Discard, mode: ParsedHarnessOutput, questions: test.questions, role: "review",
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runHarnessConversation() error = %v, want %q", err, test.wantError)
			}
			if !strings.Contains(result.Transcript, "Before the question.") ||
				len(result.SessionIDs) != 1 || result.SessionIDs[0] != "review-session" {
				t.Fatalf("partial question transcript = %#v, want captured output and session", result)
			}
		})
	}
}

func TestRunHarnessConversationRendersQuestionAndConfirmsResume(t *testing.T) {
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Before the decision.\nQUESTION:\nWhich database should this use?\nEND QUESTION"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Continuing after the answer."},
		}},
	}
	var output strings.Builder
	questions := NewQuestionHandler(strings.NewReader("Use SQLite.\n"), &output, "#42", nil, nil)

	result, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
		return adapter.Run(ctx, harness.Request{})
	}, conversationOptions{
		output:    &output,
		mode:      ParsedHarnessOutput,
		questions: questions,
		role:      "implement",
	})
	if err != nil {
		t.Fatalf("runHarnessConversation() error = %v", err)
	}
	if len(adapter.resumeCalls) != 1 || adapter.resumeCalls[0].prompt != "Use SQLite." {
		t.Fatalf("resume calls = %#v, want one single-line answer", adapter.resumeCalls)
	}
	got := output.String()
	for _, unwanted := range []string{"QUESTION:", "END QUESTION"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("output = %q, must not contain raw protocol marker %q", got, unwanted)
		}
	}
	for _, expected := range []string{
		"implement question on #42",
		"Which database should this use?",
		"answer> ",
		"answer sent — resuming implementer…",
		"--- implement output resumes ---",
		"Continuing after the answer.",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("output = %q, want %q", got, expected)
		}
	}
	if strings.Index(got, "answer sent") > strings.Index(got, "Continuing after the answer.") {
		t.Fatalf("output = %q, want confirmation before resumed output", got)
	}
	if result.Transcript != "Before the decision.\nContinuing after the answer." {
		t.Fatalf("transcript = %q, want question prefix and resumed output", result.Transcript)
	}
}

func TestQuestionAnswerSupportsBackslashContinuation(t *testing.T) {
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "session"},
			{Type: harness.EventAssistantText, Text: "QUESTION:\nWhich details?\nEND QUESTION"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: conversationTestVerdict},
		}},
	}
	questions := NewQuestionHandler(strings.NewReader("first line\\\nsecond line\n"), io.Discard, "#42", nil, nil)

	_, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
		return adapter.Run(ctx, harness.Request{})
	}, conversationOptions{
		output:    io.Discard,
		mode:      ParsedHarnessOutput,
		questions: questions,
		role:      "review",
	})
	if err != nil {
		t.Fatalf("runHarnessConversation() error = %v", err)
	}
	if len(adapter.resumeCalls) != 1 || adapter.resumeCalls[0].prompt != "first line\nsecond line" {
		t.Fatalf("resume calls = %#v, want joined multi-line answer", adapter.resumeCalls)
	}
}

func TestQuestionAnswerRepromptsAfterEmptyLine(t *testing.T) {
	adapter := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "session"},
			{Type: harness.EventAssistantText, Text: "QUESTION:\nWhich details?\nEND QUESTION"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: conversationTestVerdict},
		}},
	}
	var output strings.Builder
	questions := NewQuestionHandler(strings.NewReader("\nUseful answer\n"), &output, "#42", nil, nil)

	_, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
		return adapter.Run(ctx, harness.Request{})
	}, conversationOptions{
		output:    &output,
		mode:      ParsedHarnessOutput,
		questions: questions,
		role:      "review",
	})
	if err != nil {
		t.Fatalf("runHarnessConversation() error = %v", err)
	}
	if len(adapter.resumeCalls) != 1 || adapter.resumeCalls[0].prompt != "Useful answer" {
		t.Fatalf("resume calls = %#v, want answer after empty-line retry", adapter.resumeCalls)
	}
	if !strings.Contains(output.String(), "answer cannot be empty; please try again") {
		t.Fatalf("output = %q, want empty-answer hint", output.String())
	}
}

func TestQuestionAnswerEOFWithoutAnswerExplainsUnavailableInput(t *testing.T) {
	var output strings.Builder
	questions := NewQuestionHandler(strings.NewReader(""), &output, "#42", nil, nil)

	_, err := questions.Handle(context.Background(), "Which details?")
	if err == nil || !strings.Contains(err.Error(), "terminal input is unavailable") {
		t.Fatalf("Handle() error = %v, want unavailable terminal input", err)
	}
}

func TestMidLineQuestionMentionDoesNotPauseConversation(t *testing.T) {
	adapter := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: "The protocol mentions QUESTION: in prose.\n" + conversationTestVerdict},
	}}}

	result, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
		return adapter.Run(ctx, harness.Request{})
	}, conversationOptions{
		output: io.Discard,
		mode:   ParsedHarnessOutput,
		role:   "review",
	})
	if err != nil {
		t.Fatalf("runHarnessConversation() error = %v", err)
	}
	if len(adapter.resumeCalls) != 0 {
		t.Fatal("runHarnessConversation() paused for a mid-line QUESTION mention")
	}
	if result.Transcript != "The protocol mentions QUESTION: in prose.\n"+conversationTestVerdict {
		t.Fatalf("transcript = %q, want ordinary assistant output", result.Transcript)
	}
}

func TestRunReviewExecutionRawOutputStillForwardsBothHarnessLines(t *testing.T) {
	const deltaRaw = "{\"type\":\"stream_event\"}\n"
	const resultRaw = "{\"type\":\"result\"}\n"
	adapter := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventAssistantText, Text: conversationTestVerdict, Raw: deltaRaw},
		{Type: harness.EventResult, Text: conversationTestVerdict, Raw: resultRaw},
	}}}
	var output strings.Builder

	review, err := RunReviewExecution(context.Background(), adapter, harness.Request{}, &output, RawHarnessOutput, nil)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if got, want := output.String(), deltaRaw+resultRaw; got != want {
		t.Fatalf("raw output = %q, want unchanged harness lines %q", got, want)
	}
	if review.Transcript != conversationTestVerdict {
		t.Fatalf("transcript = %q, want one assistant message", review.Transcript)
	}
}

type scriptedConversationAdapter struct {
	runs              [][]harness.Event
	resumes           [][]harness.Event
	runWaitErrors     []error
	resumeWaitErrors  []error
	resumeStartErrors []error
	runHooks          []func(harness.Request)
	resumeHooks       []func(harness.Request)
	runCalls          int
	runRequests       []harness.Request
	resumeCalls       []conversationResumeCall
}

type failedConversationAdapter struct {
	events []harness.Event
	err    error
}

func (a *failedConversationAdapter) Run(context.Context, harness.Request) (harness.Stream, error) {
	return failedConversationStream{events: a.events, err: a.err}, nil
}

func (a *failedConversationAdapter) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return failedConversationStream{events: a.events, err: a.err}, nil
}

func (*failedConversationAdapter) Attach(context.Context, harness.Request) error { return nil }

func (*failedConversationAdapter) AttachSession(context.Context, string, harness.Request) error {
	return nil
}

type failedConversationStream struct {
	events []harness.Event
	err    error
}

func (s failedConversationStream) Events() <-chan harness.Event {
	channel := make(chan harness.Event, len(s.events))
	for _, event := range s.events {
		channel <- event
	}
	close(channel)
	return channel
}

func (s failedConversationStream) Wait() error { return s.err }

type conversationResumeCall struct {
	sessionID string
	prompt    string
	mcp       bool
}

func (a *scriptedConversationAdapter) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	a.runRequests = append(a.runRequests, request)
	if a.runCalls >= len(a.runs) {
		return nil, &scriptedConversationError{message: "no scripted run remains"}
	}
	if a.runCalls < len(a.runHooks) && a.runHooks[a.runCalls] != nil {
		a.runHooks[a.runCalls](request)
	}
	index := a.runCalls
	events := a.runs[index]
	a.runCalls++
	var waitErr error
	if index < len(a.runWaitErrors) {
		waitErr = a.runWaitErrors[index]
	}
	return scriptedConversationStream{events: events, err: waitErr}, nil
}

func (a *scriptedConversationAdapter) Resume(_ context.Context, sessionID string, request harness.Request) (harness.Stream, error) {
	a.resumeCalls = append(a.resumeCalls, conversationResumeCall{sessionID: sessionID, prompt: request.Prompt, mcp: request.MCP})
	index := len(a.resumeCalls) - 1
	if index >= len(a.resumes) {
		return nil, &scriptedConversationError{message: "no scripted resume remains"}
	}
	if index < len(a.resumeHooks) && a.resumeHooks[index] != nil {
		a.resumeHooks[index](request)
	}
	if index < len(a.resumeStartErrors) && a.resumeStartErrors[index] != nil {
		return nil, a.resumeStartErrors[index]
	}
	var waitErr error
	if index < len(a.resumeWaitErrors) {
		waitErr = a.resumeWaitErrors[index]
	}
	return scriptedConversationStream{events: a.resumes[index], err: waitErr}, nil
}

func (*scriptedConversationAdapter) Attach(context.Context, harness.Request) error { return nil }

func (*scriptedConversationAdapter) AttachSession(context.Context, string, harness.Request) error {
	return nil
}

type scriptedConversationStream struct {
	events []harness.Event
	err    error
}

func (s scriptedConversationStream) Events() <-chan harness.Event {
	channel := make(chan harness.Event, len(s.events))
	for _, event := range s.events {
		channel <- event
	}
	close(channel)
	return channel
}

func (s scriptedConversationStream) Wait() error { return s.err }

type scriptedConversationError struct {
	message string
}

func (e *scriptedConversationError) Error() string { return e.message }
