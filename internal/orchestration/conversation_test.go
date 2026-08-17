package orchestration

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/verdict"
)

const conversationTestVerdict = "VERDICT: approve\nSUMMARY: The working tree is ready\nFINDINGS:\n"

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
	questions := NewQuestionHandler(strings.NewReader("Use SQLite.\n\n"), io.Discard, "review", nil)

	result, err := runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
		return adapter.Run(ctx, harness.Request{})
	}, conversationOptions{
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
	if result.Transcript != "Before the decision.\n"+conversationTestVerdict {
		t.Fatalf("transcript = %q, want one question prefix and resumed assistant message", result.Transcript)
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
	runs        [][]harness.Event
	resumes     [][]harness.Event
	runCalls    int
	resumeCalls []conversationResumeCall
}

type conversationResumeCall struct {
	sessionID string
	prompt    string
}

func (a *scriptedConversationAdapter) Run(context.Context, harness.Request) (harness.Stream, error) {
	if a.runCalls >= len(a.runs) {
		return nil, &scriptedConversationError{message: "no scripted run remains"}
	}
	events := a.runs[a.runCalls]
	a.runCalls++
	return scriptedConversationStream{events: events}, nil
}

func (a *scriptedConversationAdapter) Resume(_ context.Context, sessionID, prompt string) (harness.Stream, error) {
	a.resumeCalls = append(a.resumeCalls, conversationResumeCall{sessionID: sessionID, prompt: prompt})
	index := len(a.resumeCalls) - 1
	if index >= len(a.resumes) {
		return nil, &scriptedConversationError{message: "no scripted resume remains"}
	}
	return scriptedConversationStream{events: a.resumes[index]}, nil
}

func (*scriptedConversationAdapter) Attach(context.Context, harness.Request) error { return nil }

type scriptedConversationStream struct {
	events []harness.Event
}

func (s scriptedConversationStream) Events() <-chan harness.Event {
	channel := make(chan harness.Event, len(s.events))
	for _, event := range s.events {
		channel <- event
	}
	close(channel)
	return channel
}

func (scriptedConversationStream) Wait() error { return nil }

type scriptedConversationError struct {
	message string
}

func (e *scriptedConversationError) Error() string { return e.message }
