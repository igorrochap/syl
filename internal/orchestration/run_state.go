package orchestration

import (
	"context"
	"errors"
	"time"

	"github.com/igorrochap/syl/internal/runstate"
)

type runStateTracker struct {
	persister        runStateProvider
	marker           liveRunMarkerProvider
	state            runstate.State
	previousActivity runstate.Activity
	finalized        bool
}

func newRunStateTracker(recorder RunRecorder) *runStateTracker {
	persister, ok := recorder.(runStateProvider)
	if !ok {
		return nil
	}
	marker, _ := recorder.(liveRunMarkerProvider)
	return &runStateTracker{persister: persister, marker: marker, state: persister.runStateSnapshot()}
}

func (r *runStateTracker) setIteration(iteration int) {
	if r == nil || r.finalized || r.state.Iteration == iteration {
		return
	}
	r.state.Iteration = iteration
	r.save()
}

func (r *runStateTracker) setActivity(activity runstate.Activity) {
	if r == nil || r.finalized || r.state.Activity == activity {
		return
	}
	r.state.Activity = activity
	r.state.Question = ""
	r.save()
}

func (r *runStateTracker) questionAsked(question string) {
	if r == nil || r.finalized {
		return
	}
	r.previousActivity = r.state.Activity
	r.state.Activity = runstate.AwaitingAnswer
	r.state.Question = question
	r.save()
}

func (r *runStateTracker) questionAnswered() {
	if r == nil || r.finalized {
		return
	}
	r.state.Activity = r.previousActivity
	r.state.Question = ""
	r.save()
}

func (r *runStateTracker) finish(status runstate.Status) {
	if r == nil || r.finalized {
		return
	}
	ended := time.Now().UTC()
	r.state.Status = status
	r.state.Activity = ""
	r.state.Question = ""
	r.state.EndedAt = &ended
	r.save()
	if r.marker != nil {
		r.marker.removeLiveRunMarker()
	}
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
	if r == nil || r.finalized {
		return
	}
	r.state.UpdatedAt = time.Now().UTC()
	r.persister.persistRunState(r.state)
}

type questionStateObserver interface {
	questionAsked(question string)
	questionAnswered()
}

type liveRunMarkerProvider interface {
	removeLiveRunMarker()
}

var _ questionStateObserver = (*runStateTracker)(nil)
