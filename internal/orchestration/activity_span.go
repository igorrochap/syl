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
	activitySpanGreen     activitySpanOutcome = "green"
	activitySpanRed       activitySpanOutcome = "red"
)

type activitySpan struct {
	start runrecord.ActivitySpanStart
	view  *ui.ActivitySpanView
	clock ui.Clock
	state *runStateTracker
	ended bool
}

func startActivitySpan(
	state *runStateTracker,
	output io.Writer,
	mode HarnessOutputMode,
	clock ui.Clock,
	iteration int,
	activity runrecord.Activity,
	label string,
) (*activitySpan, error) {
	startedAt := spanNow(clock)
	start, err := state.startActivitySpan(iteration, activity, label, startedAt)
	if err != nil {
		return nil, err
	}
	span := &activitySpan{start: start, clock: clock, state: state}
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

func startUnrecordedActivitySpan(
	output io.Writer,
	mode HarnessOutputMode,
	clock ui.Clock,
	label string,
) (*activitySpan, error) {
	startedAt := spanNow(clock)
	span := &activitySpan{clock: clock}
	if mode == RawHarnessOutput {
		span.start.StartedAt = startedAt
		return span, nil
	}
	span.view = ui.NewActivitySpanView(output, ui.DetectCaps(output), clock, ui.DefaultActivityTickerInterval)
	if err := span.view.Start(label, startedAt); err != nil {
		return nil, err
	}
	span.start.StartedAt = startedAt
	return span, nil
}

func (span *activitySpan) finishUnrecorded(ctx context.Context, operationErr error) (activitySpanTiming, error) {
	endedAt := spanNow(span.clock)
	outcome := activitySpanOutcomeFor(ctx, operationErr, activitySpanCompleted)
	var viewErr error
	if span.view != nil {
		viewErr = span.view.End(endedAt, string(outcome))
	}
	return activitySpanTiming{startedAt: span.start.StartedAt, endedAt: endedAt, outcome: outcome}, errors.Join(operationErr, viewErr)
}

func (span *activitySpan) finish(ctx context.Context, operationErr error) error {
	return span.finishWithOutcome(ctx, operationErr, activitySpanCompleted)
}

func (span *activitySpan) finishWithOutcome(ctx context.Context, operationErr error, successOutcome activitySpanOutcome) error {
	if span == nil || span.ended {
		return operationErr
	}
	span.ended = true
	endedAt := spanNow(span.clock)
	outcome := activitySpanOutcomeFor(ctx, operationErr, successOutcome)
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
	return errors.Join(operationErr, stateErr, viewErr)
}

type activitySpanContext struct {
	state     *runStateTracker
	output    io.Writer
	mode      HarnessOutputMode
	clock     ui.Clock
	iteration int
}

func newActivitySpanContext(options ImplementOptions, state *runStateTracker, iteration int) activitySpanContext {
	return activitySpanContext{
		state: state, output: options.Output, mode: harnessOutputMode(options.Verbose, options.Raw),
		clock: options.Clock, iteration: iteration,
	}
}

func (spans activitySpanContext) run(
	ctx context.Context,
	activity runrecord.Activity,
	label string,
	operation func() error,
) error {
	span, err := startActivitySpan(
		spans.state, spans.output, spans.mode, spans.clock, spans.iteration, activity, label,
	)
	if err != nil {
		return err
	}
	operationErr := operation()
	return span.finish(ctx, operationErr)
}

type activitySpanTiming struct {
	startedAt time.Time
	endedAt   time.Time
	outcome   activitySpanOutcome
}

func activitySpanOutcomeFor(
	ctx context.Context,
	operationErr error,
	successOutcome activitySpanOutcome,
) activitySpanOutcome {
	if operationErr == nil {
		return successOutcome
	}
	if errors.Is(operationErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return activitySpanCancelled
	}
	return activitySpanFailed
}

func recordActivitySpan(
	state *runStateTracker,
	iteration int,
	activity runrecord.Activity,
	label string,
	timing activitySpanTiming,
) error {
	start, err := state.startActivitySpan(iteration, activity, label, timing.startedAt)
	if err != nil {
		return err
	}
	return state.endActivitySpan(start.SpanID, timing.outcome, timing.endedAt)
}

type activitySpanOutput struct {
	output io.Writer
	view   *ui.ActivitySpanView
}

func (writer activitySpanOutput) Write(contents []byte) (int, error) {
	output := writer.output
	if output == nil {
		output = io.Discard
	}
	if writer.view == nil {
		return output.Write(contents)
	}
	writer.view.Pause()
	written, err := output.Write(contents)
	if written > 0 {
		writer.view.RecordOutput(string(contents[:written]))
	}
	writer.view.Resume()
	if written < len(contents) && err == nil {
		err = io.ErrShortWrite
	}
	return written, err
}

func spanNow(clock ui.Clock) time.Time {
	return ui.ClockNow(clock)
}
