package orchestration

import (
	"context"
	"errors"
	"time"

	"github.com/igorrochap/syl/internal/runstate"
)

type runStateTracker struct {
	recorder         RunRecorder
	state            runstate.State
	previousActivity runstate.Activity
	finalized        bool
}

func newRunState(spec RunSpec) runstate.State {
	iteration := 0
	if spec.Kind == runstate.Review {
		iteration = 1
	}
	return runstate.New(spec.Kind, spec.TicketRef, iteration, spec.MaxIterations, time.Now().UTC())
}

func newRunStateTracker(recorder RunRecorder, state runstate.State) *runStateTracker {
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

func (r *runStateTracker) setActivity(activity runstate.Activity) {
	if r.finalized || r.state.Activity == activity {
		return
	}
	r.state.Activity = activity
	r.state.Question = ""
	r.save()
}

func (r *runStateTracker) startActivity(iteration int, activity runstate.Activity) {
	if r.finalized {
		return
	}
	changed := r.state.Iteration != iteration || r.state.Activity != activity || r.state.Question != ""
	if !changed {
		return
	}
	r.state.Iteration = iteration
	r.state.Activity = activity
	r.state.Question = ""
	r.save()
}

func (r *runStateTracker) questionAsked(question string) {
	if r.finalized {
		return
	}
	r.previousActivity = r.state.Activity
	r.state.Activity = runstate.AwaitingAnswer
	r.state.Question = question
	r.save()
}

func (r *runStateTracker) questionAnswered() {
	if r.finalized {
		return
	}
	r.state.Activity = r.previousActivity
	r.state.Question = ""
	r.save()
}

func (r *runStateTracker) finish(status runstate.Status) {
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
		r.finish(runstate.Cancelled)
		return
	}
	r.finish(runstate.Failed)
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
