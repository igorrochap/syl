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
	start      runrecord.ActivitySpanStart
	view       *ui.ActivitySpanView
	clock      ui.Clock
	state      *runStateTracker
	iteration  int
	children   map[string]*childActivitySpan
	childOrder []string
	question   *childActivitySpan
	ended      bool
}

type childActivitySpan struct {
	start runrecord.ActivitySpanStart
	ended bool
}

type subagentEventSink interface {
	SubagentStarted(id, label string) error
	SubagentTool(id, name, gist string)
	SubagentFinished(id string) error
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
	view, err := startActivitySpanView(output, mode, clock, label, startedAt)
	if err != nil {
		_ = state.endActivitySpan(start.SpanID, activitySpanFailed, spanNow(clock))
		return nil, err
	}
	return &activitySpan{
		start: start, view: view, clock: clock, state: state, iteration: iteration,
		children: make(map[string]*childActivitySpan),
	}, nil
}

func startUnrecordedActivitySpan(
	output io.Writer,
	mode HarnessOutputMode,
	clock ui.Clock,
	label string,
) (*activitySpan, error) {
	startedAt := spanNow(clock)
	view, err := startActivitySpanView(output, mode, clock, label, startedAt)
	if err != nil {
		return nil, err
	}
	return &activitySpan{
		start: runrecord.ActivitySpanStart{StartedAt: startedAt}, view: view, clock: clock,
		children: make(map[string]*childActivitySpan),
	}, nil
}

func startActivitySpanView(
	output io.Writer,
	mode HarnessOutputMode,
	clock ui.Clock,
	label string,
	startedAt time.Time,
) (*ui.ActivitySpanView, error) {
	if mode == RawHarnessOutput {
		return nil, nil
	}
	view := ui.NewActivitySpanView(output, ui.DetectCaps(output), clock, ui.DefaultActivityTickerInterval)
	if err := view.Start(label, startedAt); err != nil {
		return nil, err
	}
	return view, nil
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
	childrenErr := span.finishOpenChildren(endedAt, outcome)
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
	return errors.Join(operationErr, childrenErr, stateErr, viewErr)
}

func (span *activitySpan) SubagentStarted(id, label string) error {
	if span == nil || span.state == nil {
		return nil
	}
	if _, exists := span.children[id]; exists {
		return nil
	}
	startedAt := spanNow(span.clock)
	child, err := span.registerChild(span.start.Activity, label, startedAt)
	if err != nil {
		return err
	}
	span.children[id] = child
	span.childOrder = append(span.childOrder, id)
	if span.view == nil {
		return nil
	}
	if err := span.view.SubagentStarted(id, label, startedAt); err != nil {
		return errors.Join(err, span.finishChild(child, activitySpanFailed, spanNow(span.clock), ""))
	}
	return nil
}

func (span *activitySpan) startAwaitingAnswer(question string) (*childActivitySpan, error) {
	if span == nil || span.state == nil {
		return nil, nil
	}
	child, err := span.registerChild(runrecord.AwaitingAnswer, question, spanNow(span.clock))
	if err != nil {
		return nil, err
	}
	span.question = child
	return child, nil
}

func (span *activitySpan) registerChild(activity runrecord.Activity, label string, startedAt time.Time) (*childActivitySpan, error) {
	start, err := span.state.startChildActivitySpan(
		span.start.SpanID, span.iteration, activity, label, startedAt,
	)
	if err != nil {
		return nil, err
	}
	return &childActivitySpan{start: start}, nil
}

func (span *activitySpan) finishAwaitingAnswer(child *childActivitySpan, answer string, outcome activitySpanOutcome) error {
	if span == nil || span.state == nil || child == nil || child.ended {
		return nil
	}
	if err := span.finishChild(child, outcome, spanNow(span.clock), answer); err != nil {
		return fmt.Errorf("close awaiting-answer span %q: %w", child.start.SpanID, err)
	}
	span.question = nil
	return nil
}

func (span *activitySpan) pauseForQuestion() error {
	if span == nil || span.view == nil {
		return nil
	}
	return span.view.PauseForQuestion()
}

func (span *activitySpan) pauseForFramedQuestion() {
	if span == nil || span.view == nil {
		return
	}
	span.view.Pause()
}

func (span *activitySpan) resumeAfterQuestion() error {
	if span == nil || span.view == nil {
		return nil
	}
	return span.view.ResumeAfterQuestion()
}

func (span *activitySpan) resumeAfterFramedQuestion() {
	if span == nil || span.view == nil {
		return
	}
	span.view.Resume()
}

func (span *activitySpan) recordOutput(value string) {
	if span == nil || span.view == nil {
		return
	}
	span.view.RecordOutput(value)
}

func (span *activitySpan) finishChild(child *childActivitySpan, outcome activitySpanOutcome, endedAt time.Time, answer string) error {
	if child == nil || child.ended {
		return nil
	}
	child.ended = true
	return span.state.endChildActivitySpan(child.start.SpanID, outcome, endedAt, answer)
}

func (span *activitySpan) SubagentTool(id, name, gist string) {
	if span == nil || span.view == nil {
		return
	}
	span.view.SubagentTool(id, name, gist)
}

func (span *activitySpan) SubagentFinished(id string) error {
	if span == nil || span.state == nil {
		return nil
	}
	child, exists := span.children[id]
	if !exists || child.ended {
		return nil
	}
	endedAt := spanNow(span.clock)
	child.ended = true
	stateErr := span.state.endChildActivitySpan(child.start.SpanID, activitySpanCompleted, endedAt, "")
	var viewErr error
	if span.view != nil {
		viewErr = span.view.SubagentFinished(id, endedAt, string(activitySpanCompleted))
	}
	return errors.Join(stateErr, viewErr)
}

func (span *activitySpan) finishOpenChildren(endedAt time.Time, outcome activitySpanOutcome) error {
	var childErrors []error
	for _, id := range span.childOrder {
		child := span.children[id]
		if child.ended {
			continue
		}
		child.ended = true
		if err := span.state.endChildActivitySpan(child.start.SpanID, outcome, endedAt, ""); err != nil {
			childErrors = append(childErrors, err)
		}
		if span.view != nil {
			if err := span.view.SubagentFinished(id, endedAt, string(outcome)); err != nil {
				childErrors = append(childErrors, err)
			}
		}
	}
	if span.question != nil && !span.question.ended {
		if err := span.finishChild(span.question, outcome, endedAt, ""); err != nil {
			childErrors = append(childErrors, err)
		}
	}
	return errors.Join(childErrors...)
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
