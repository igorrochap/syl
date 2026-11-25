package orchestration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
)

func TestActivitySpanRecordsFailedAndCancelledOutcomes(t *testing.T) {
	for _, test := range []struct {
		name         string
		operationErr error
		want         activitySpanOutcome
	}{
		{name: "failed", operationErr: errors.New("operation failed"), want: activitySpanFailed},
		{name: "cancelled", operationErr: context.Canceled, want: activitySpanCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMemoryRunRecorder()
			state := newRunStateTracker(recorder, newRunState(RunSpec{Kind: runrecord.Implement}))
			span, err := startActivitySpan(
				state, io.Discard, RawHarnessOutput, nil, 1, runrecord.Implementing, "1/1 — implementing",
			)
			if err != nil {
				t.Fatalf("startActivitySpan() error = %v", err)
			}
			if err := span.finish(context.Background(), test.operationErr); !errors.Is(err, test.operationErr) {
				t.Fatalf("finish() error = %v, want original operation error %v", err, test.operationErr)
			}
			if len(recorder.activities) != 2 || recorder.activities[1].End == nil {
				t.Fatalf("Activity events = %#v, want start and end", recorder.activities)
			}
			if got := activitySpanOutcome(recorder.activities[1].End.Outcome); got != test.want {
				t.Fatalf("span outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunRecordsSubagentEventsUnderEachRoleParent(t *testing.T) {
	root := t.TempDir()
	recorder, err := openDiskRun(root, sylhome.Dir{}, io.Discard, RunSpec{
		Kind: runrecord.Implement, TicketRef: "#298", WorkRoot: root, MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("open Run record: %v", err)
	}
	state := newRunStateTracker(recorder, newRunState(RunSpec{Kind: runrecord.Implement, TicketRef: "#298", MaxIterations: 1}))
	runRole := func(activity runrecord.Activity, label string, events []harness.Event, operationErr error) *activitySpan {
		t.Helper()
		span, err := startActivitySpan(state, io.Discard, RawHarnessOutput, nil, 1, activity, label)
		if err != nil {
			t.Fatalf("start %s span: %v", label, err)
		}
		adapter := &scriptedConversationAdapter{runs: [][]harness.Event{events}}
		_, err = runHarnessConversation(context.Background(), adapter, func(ctx context.Context) (harness.Stream, error) {
			return adapter.Run(ctx, harness.Request{Prompt: label})
		}, conversationOptions{
			request: harness.Request{Prompt: label}, output: io.Discard, mode: RawHarnessOutput,
			role: "test", childSpans: span,
		})
		if err != nil {
			t.Fatalf("run %s harness conversation: %v", label, err)
		}
		if err := span.finish(context.Background(), operationErr); !errors.Is(err, operationErr) {
			t.Fatalf("finish %s span error = %v, want %v", label, err, operationErr)
		}
		return span
	}

	failedReviewer := errors.New("review turn failed")
	runRole(runrecord.Reviewing, "reviewing", []harness.Event{
		{Type: harness.EventSubagentStarted, SubagentID: "standards", SubagentLabel: "Standards review"},
		{Type: harness.EventSubagentStarted, SubagentID: "spec", SubagentLabel: "Spec review"},
		{Type: harness.EventSubagentFinished, SubagentID: "spec"},
	}, failedReviewer)
	runRole(runrecord.Implementing, "implementing", []harness.Event{
		{Type: harness.EventSubagentStarted, SubagentID: "helper", SubagentLabel: "Implement helper"},
		{Type: harness.EventSubagentFinished, SubagentID: "helper"},
	}, nil)

	var codexOutput bytes.Buffer
	codexSpan, err := startActivitySpan(state, &codexOutput, ProgressHarnessOutput, nil, 1, runrecord.Implementing, "Codex implementer")
	if err != nil {
		t.Fatalf("start fake Codex span: %v", err)
	}
	codex := &scriptedConversationAdapter{runs: [][]harness.Event{{{Type: harness.EventAssistantText, Text: "Codex worked."}}}}
	_, err = runHarnessConversation(context.Background(), codex, func(ctx context.Context) (harness.Stream, error) {
		return codex.Run(ctx, harness.Request{Prompt: "Codex implementer"})
	}, conversationOptions{
		request: harness.Request{Prompt: "Codex implementer"},
		output:  newLiveHarnessOutput(&codexOutput, ProgressHarnessOutput, "implement", codexSpan.view),
		mode:    ProgressHarnessOutput, role: "implement", childSpans: codexSpan,
	})
	if err != nil {
		t.Fatalf("run fake Codex conversation: %v", err)
	}
	if err := codexSpan.finish(context.Background(), nil); err != nil {
		t.Fatalf("finish fake Codex span: %v", err)
	}
	if strings.Contains(codexOutput.String(), "    ") || strings.Contains(codexOutput.String(), "sub-agent") {
		t.Fatalf("fake Codex output = %q, want no child lines", codexOutput.String())
	}

	activities, err := runrecord.ReadActivities(recorder.dir, nil)
	if err != nil {
		t.Fatalf("read activities.jsonl: %v", err)
	}
	starts := make(map[string]runrecord.ActivitySpanStart)
	ends := make(map[string]runrecord.ActivitySpanEnd)
	for _, event := range activities {
		if event.Start != nil {
			starts[event.Start.SpanID] = *event.Start
		}
		if event.End != nil {
			ends[event.End.SpanID] = *event.End
		}
	}
	childrenByParent := make(map[string][]runrecord.ActivitySpanStart)
	for _, event := range activities {
		if event.Start != nil && event.Start.ParentSpanID != "" {
			childrenByParent[event.Start.ParentSpanID] = append(childrenByParent[event.Start.ParentSpanID], *event.Start)
		}
	}
	for _, start := range starts {
		if start.Label == "reviewing" {
			children := childrenByParent[start.SpanID]
			if len(children) != 2 || children[0].Label != "Standards review" || children[1].Label != "Spec review" {
				t.Fatalf("reviewer children = %#v, want both children in start order", children)
			}
			if ends[children[0].SpanID].Outcome != string(activitySpanFailed) || ends[children[1].SpanID].Outcome != string(activitySpanCompleted) {
				t.Fatalf("reviewer child outcomes = %#v and %#v, want parent failure and completed sibling", ends[children[0].SpanID], ends[children[1].SpanID])
			}
		}
		if start.Label == "implementing" {
			children := childrenByParent[start.SpanID]
			if len(children) != 1 || children[0].Label != "Implement helper" {
				t.Fatalf("implementer children = %#v, want helper child", children)
			}
		}
		if start.Label == "Codex implementer" && len(childrenByParent[start.SpanID]) != 0 {
			t.Fatalf("Codex Activity children = %#v, want no child spans", childrenByParent[start.SpanID])
		}
	}
}
