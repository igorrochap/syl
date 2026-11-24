package orchestration

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/igorrochap/syl/internal/runrecord"
)

func TestRoleActivitySpanRecordsFailedAndCancelledOutcomes(t *testing.T) {
	for _, test := range []struct {
		name    string
		roleErr error
		want    activitySpanOutcome
	}{
		{name: "failed", roleErr: errors.New("harness failed"), want: activitySpanFailed},
		{name: "cancelled", roleErr: context.Canceled, want: activitySpanCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMemoryRunRecorder()
			state := newRunStateTracker(recorder, newRunState(RunSpec{Kind: runrecord.Implement}))
			span, err := startRoleActivitySpan(
				state, io.Discard, RawHarnessOutput, nil, 1, runrecord.Implementing, "1/1 — implementing",
			)
			if err != nil {
				t.Fatalf("startRoleActivitySpan() error = %v", err)
			}
			if err := span.finish(context.Background(), test.roleErr); !errors.Is(err, test.roleErr) {
				t.Fatalf("finish() error = %v, want original Role error %v", err, test.roleErr)
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
