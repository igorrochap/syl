package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/igorrochap/syl/internal/runrecord"
)

type runStateTracker struct {
	recorder         RunRecorder
	state            runrecord.State
	previousActivity runrecord.Activity
	openSpans        []runrecord.ActivitySpanStart
	nextSpanID       int
	finalized        bool
}

func newRunState(spec RunSpec) runrecord.State {
	iteration := 0
	if spec.Kind == runrecord.Review {
		iteration = 1
	}
	return runrecord.New(spec.Kind, spec.TicketRef, iteration, spec.MaxIterations, time.Now().UTC())
}

func newRunStateTracker(recorder RunRecorder, state runrecord.State) *runStateTracker {
	tracker := &runStateTracker{recorder: recorder, state: state}
	tracker.save()
	return tracker
}

func (r *runStateTracker) setIteration(iteration int) {
	if r.finalized || r.state.Iteration == iteration {
		return
	}
	r.state.Iteration = iteration
	r.save()
}

func (r *runStateTracker) startActivitySpan(
	iteration int,
	activity runrecord.Activity,
	label string,
	startedAt time.Time,
) (runrecord.ActivitySpanStart, error) {
	parentSpanID := ""
	if len(r.openSpans) > 0 {
		parentSpanID = r.openSpans[len(r.openSpans)-1].SpanID
	}
	start, err := r.recordActivitySpanStart(parentSpanID, iteration, activity, label, startedAt)
	if err != nil {
		return runrecord.ActivitySpanStart{}, fmt.Errorf("record Activity span start: %w", err)
	}
	r.openSpans = append(r.openSpans, start)
	if iteration > 0 {
		r.state.Iteration = iteration
	}
	r.state.Activity = activity
	r.state.Question = ""
	r.save()
	return start, nil
}

func (r *runStateTracker) recordActivitySpanStart(
	parentSpanID string,
	iteration int,
	activity runrecord.Activity,
	label string,
	startedAt time.Time,
) (runrecord.ActivitySpanStart, error) {
	if r.finalized {
		return runrecord.ActivitySpanStart{}, errors.New("start Activity span after Run finished")
	}
	r.nextSpanID++
	iterationValue := ""
	if iteration > 0 {
		iterationValue = strconv.Itoa(iteration)
	}
	start := runrecord.ActivitySpanStart{
		SpanID: fmt.Sprintf("span-%06d", r.nextSpanID), ParentSpanID: parentSpanID,
		Activity: activity, Iteration: iterationValue, Label: label, StartedAt: startedAt,
	}
	if err := r.recorder.RecordActivityStart(start); err != nil {
		return runrecord.ActivitySpanStart{}, err
	}
	return start, nil
}

func (r *runStateTracker) startChildActivitySpan(
	parentSpanID string,
	iteration int,
	activity runrecord.Activity,
	label string,
	startedAt time.Time,
) (runrecord.ActivitySpanStart, error) {
	if r.finalized {
		return runrecord.ActivitySpanStart{}, errors.New("start child Activity span after Run finished")
	}
	parentIsOpen := false
	for _, open := range r.openSpans {
		if open.SpanID == parentSpanID {
			parentIsOpen = true
			break
		}
	}
	if !parentIsOpen {
		return runrecord.ActivitySpanStart{}, fmt.Errorf("start child Activity span under closed parent %q", parentSpanID)
	}
	start, err := r.recordActivitySpanStart(parentSpanID, iteration, activity, label, startedAt)
	if err != nil {
		return runrecord.ActivitySpanStart{}, fmt.Errorf("record child Activity span start: %w", err)
	}
	return start, nil
}

func (r *runStateTracker) endChildActivitySpan(spanID string, outcome activitySpanOutcome, endedAt time.Time, answer string) error {
	if r.finalized {
		return errors.New("end child Activity span after Run finished")
	}
	if err := r.recorder.RecordActivityEnd(runrecord.ActivitySpanEnd{
		SpanID: spanID, EndedAt: endedAt, Outcome: string(outcome), Answer: answer,
	}); err != nil {
		return fmt.Errorf("record child Activity span end: %w", err)
	}
	return nil
}

func (r *runStateTracker) endActivitySpan(spanID string, outcome activitySpanOutcome, endedAt time.Time) error {
	if len(r.openSpans) == 0 {
		return errors.New("end Activity span: no span is open")
	}
	last := r.openSpans[len(r.openSpans)-1]
	if last.SpanID != spanID {
		return fmt.Errorf("end Activity span %q: innermost open span is %q", spanID, last.SpanID)
	}
	writeErr := r.recorder.RecordActivityEnd(runrecord.ActivitySpanEnd{
		SpanID: spanID, EndedAt: endedAt, Outcome: string(outcome),
	})
	r.openSpans = r.openSpans[:len(r.openSpans)-1]
	r.state.Activity = ""
	if openCount := len(r.openSpans); openCount > 0 {
		r.state.Activity = r.openSpans[openCount-1].Activity
	}
	r.state.Question = ""
	r.save()
	if writeErr != nil {
		return fmt.Errorf("record Activity span end: %w", writeErr)
	}
	return nil
}

func (r *runStateTracker) setExhaustionReason(reason string) {
	if r.finalized || r.state.ExhaustionReason == reason {
		return
	}
	r.state.ExhaustionReason = reason
	r.save()
}

func (r *runStateTracker) recordNitBudgetOverride(iteration, count, budget int) {
	if r.finalized {
		return
	}
	r.state.NitBudgetOverrides = append(r.state.NitBudgetOverrides, runrecord.NitBudgetOverride{Iteration: iteration, NitCount: count, Budget: budget})
	r.save()
}

func (r *runStateTracker) questionAsked(question string) {
	if r.finalized {
		return
	}
	r.previousActivity = r.state.Activity
	r.state.Activity = runrecord.AwaitingAnswer
	r.state.Question = question
	r.save()
}

func (r *runStateTracker) questionAnswered() {
	if r.finalized {
		return
	}
	if len(r.openSpans) > 0 {
		r.state.Activity = r.openSpans[len(r.openSpans)-1].Activity
	} else {
		r.state.Activity = r.previousActivity
	}
	r.state.Question = ""
	r.save()
}

func (r *runStateTracker) finish(status runrecord.Status) {
	if r.finalized {
		return
	}
	ended := time.Now().UTC()
	r.state.Status = status
	r.state.Activity = ""
	r.state.Question = ""
	r.state.EndedAt = &ended
	r.save()
	r.finalized = true
}

func (r *runStateTracker) finishForError(ctx context.Context, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		r.finish(runrecord.Cancelled)
		return
	}
	r.finish(runrecord.Failed)
}

func (r *runStateTracker) save() {
	if r.finalized {
		return
	}
	r.state.UpdatedAt = time.Now().UTC()
	r.recorder.RecordState(r.state)
}

type questionStateObserver interface {
	questionAsked(question string)
	questionAnswered()
}

var _ questionStateObserver = (*runStateTracker)(nil)
