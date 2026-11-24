// Package orchestration coordinates Syl runs across harnesses and run records.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/ui"
)

type activitySpanOutcome string

const (
	activitySpanCompleted activitySpanOutcome = "completed"
	activitySpanFailed    activitySpanOutcome = "failed"
	activitySpanCancelled activitySpanOutcome = "cancelled"
)

type roleActivitySpan struct {
	start runrecord.ActivitySpanStart
	view  *ui.ActivitySpanView
	clock ui.Clock
	state *runStateTracker
	ended bool
}

func startRoleActivitySpan(
	state *runStateTracker,
	output io.Writer,
	mode HarnessOutputMode,
	clock ui.Clock,
	iteration int,
	activity runrecord.Activity,
	label string,
) (*roleActivitySpan, error) {
	startedAt := spanNow(clock)
	start, err := state.startActivitySpan(iteration, activity, label, startedAt)
	if err != nil {
		return nil, err
	}
	span := &roleActivitySpan{start: start, clock: clock, state: state}
	if mode == RawHarnessOutput {
		return span, nil
	}
	span.view = ui.NewActivitySpanView(output, ui.DetectCaps(output), clock, ui.DefaultActivityTickerInterval)
	if err := span.view.Start(label, startedAt); err != nil {
		_ = state.endActivitySpan(start.SpanID, activitySpanFailed, spanNow(clock))
		return nil, err
	}
	return span, nil
}

func (span *roleActivitySpan) finish(ctx context.Context, roleErr error) error {
	if span == nil || span.ended {
		return roleErr
	}
	span.ended = true
	endedAt := spanNow(span.clock)
	outcome := activitySpanCompleted
	if roleErr != nil {
		outcome = activitySpanFailed
		if errors.Is(roleErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			outcome = activitySpanCancelled
		}
	}
	stateErr := span.state.endActivitySpan(span.start.SpanID, outcome, endedAt)
	var viewErr error
	if span.view != nil {
		viewErr = span.view.End(endedAt, string(outcome))
	}
	if stateErr != nil {
		stateErr = fmt.Errorf("close Activity span %q: %w", span.start.SpanID, stateErr)
	}
	if viewErr != nil {
		viewErr = fmt.Errorf("render Activity span %q: %w", span.start.SpanID, viewErr)
	}
	return errors.Join(roleErr, stateErr, viewErr)
}

func spanNow(clock ui.Clock) time.Time {
	return ui.ClockNow(clock)
}
