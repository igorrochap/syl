package orchestration

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/igorrochap/syl/internal/runrecord"
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
