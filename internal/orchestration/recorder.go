package orchestration

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

// RunRecorder records the domain events that make up a syl Run.
type RunRecorder interface {
	ImplementHandoffPath(iteration int) string
	RecordState(state runrecord.State)
	RecordUsage(entry usage.Entry) error
	RecordImplementTurn(iteration int, feed, transcript string) error
	RecordReviewDiff(iteration int, diff string) (string, error)
	RecordReviewOutput(iteration int, review ReviewExecution) error
	RecordVerdict(iteration int, reviewVerdict verdict.Verdict) error
	RecordSessions(iteration int, role string, sessionIDs []string) error
	WriteSummary(summary implementSummary) error
	WriteSessions() error
}

// RunSpec describes the metadata and lifecycle settings for a Run.
type RunSpec = runrecord.Spec

func recorderArtifactDir(recorder RunRecorder) string {
	return filepath.Dir(recorder.ImplementHandoffPath(1))
}

type diskRunRecorder struct {
	dir           string
	writer        *runrecord.Writer
	usage         usage.Artifact
	runState      runrecord.State
	warningOutput io.Writer
	marker        *sylhome.LiveRun
}

var _ RunRecorder = (*diskRunRecorder)(nil)

// NewDiskRunOpener returns the production Run opener for one origin root.
func NewDiskRunOpener(originRoot string, sylHome sylhome.Dir, warningOutput io.Writer) func(RunSpec) (RunRecorder, error) {
	return func(spec RunSpec) (RunRecorder, error) {
		return openDiskRun(originRoot, sylHome, warningOutput, spec)
	}
}

func resolveRunWorkRoot(workRoot string) (string, error) {
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return "", fmt.Errorf("resolve run work root: %w", err)
	}
	return filepath.Clean(root), nil
}

func openDiskRun(originRoot string, sylHome sylhome.Dir, warningOutput io.Writer, spec RunSpec) (*diskRunRecorder, error) {
	workRoot, err := resolveRunWorkRoot(spec.WorkRoot)
	if err != nil {
		return nil, err
	}
	spec.WorkRoot = workRoot
	writer, err := runrecord.CreateWriter(originRoot, spec, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	recorder := &diskRunRecorder{
		dir:           writer.Directory(),
		writer:        writer,
		usage:         usage.NewArtifact(),
		warningOutput: warningOutput,
		runState:      newRunState(spec),
	}
	recorder.RecordState(recorder.runState)
	if err := writer.WriteMetadata(spec); err != nil {
		return nil, recorder.failOpen(err)
	}
	if err := recorder.writeUsage(); err != nil {
		return nil, recorder.failOpen(err)
	}
	recorder.createLiveRunMarker(sylHome, originRoot)
	return recorder, nil
}

func (recorder *diskRunRecorder) failOpen(err error) error {
	failed := recorder.runState
	failed.Status = runrecord.Failed
	failed.Activity = ""
	failed.Question = ""
	ended := time.Now().UTC()
	failed.EndedAt = &ended
	recorder.RecordState(failed)
	return err
}

func (recorder *diskRunRecorder) createLiveRunMarker(sylHome sylhome.Dir, projectRoot string) {
	if sylHome.String() == "" {
		return
	}
	marker, err := sylHome.MarkLive(sylhome.LiveRun{
		ProjectPath: projectRoot,
		RunDir:      recorder.dir,
		TicketRef:   recorder.runState.TicketRef,
		Host:        recorder.runState.Hostname,
		PID:         recorder.runState.PID,
	})
	if err != nil {
		recorder.warn("create live-run marker", err)
		return
	}
	recorder.marker = &marker
}

func (recorder *diskRunRecorder) RecordState(state runrecord.State) {
	recorder.runState = state
	if err := recorder.writer.WriteState(state); err != nil {
		recorder.warn("write run state", err)
	}
	if state.Status != runrecord.Running {
		recorder.removeLiveRunMarker()
	}
}

func (recorder *diskRunRecorder) removeLiveRunMarker() {
	if recorder.marker == nil {
		return
	}
	if err := recorder.marker.Unmark(); err != nil {
		recorder.warn("remove live-run marker", err)
		return
	}
	recorder.marker = nil
}

func (recorder *diskRunRecorder) warn(action string, err error) {
	if recorder.warningOutput == nil {
		return
	}
	_, _ = fmt.Fprintf(recorder.warningOutput, "syl: warning: %s: %v\n", action, err)
}

func (recorder *diskRunRecorder) ImplementHandoffPath(iteration int) string {
	return recorder.writer.HandoffPath(iteration)
}

func (recorder *diskRunRecorder) RecordImplementTurn(iteration int, feed, transcript string) error {
	return recorder.writer.WriteImplementTurn(iteration, feed, transcript)
}

func (recorder *diskRunRecorder) RecordReviewDiff(iteration int, diff string) (string, error) {
	return recorder.writer.WriteReviewDiff(iteration, diff)
}

func (recorder *diskRunRecorder) RecordReviewOutput(iteration int, review ReviewExecution) error {
	return recorder.writer.WriteReviewOutput(iteration, review.Feed, review.Transcript)
}

func (recorder *diskRunRecorder) RecordVerdict(iteration int, reviewVerdict verdict.Verdict) error {
	return recorder.writer.WriteVerdict(iteration, reviewVerdict)
}

func (recorder *diskRunRecorder) RecordSessions(iteration int, role string, sessionIDs []string) error {
	return recorder.writer.RecordSessions(iteration, role, sessionIDs)
}

func (recorder *diskRunRecorder) WriteSummary(summary implementSummary) error {
	return recorder.writer.WriteSummary(runrecord.SummaryInput{
		Iterations: summary.iterations, Final: summary.final, Nits: summary.nits,
		DiffStat: summary.diffStat, WorktreePath: summary.worktreePath,
	})
}

func (recorder *diskRunRecorder) WriteSessions() error {
	return recorder.writer.WriteSessions()
}

func (recorder *diskRunRecorder) RecordUsage(entry usage.Entry) error {
	if recorder.usage.SchemaVersion == 0 {
		recorder.usage = usage.NewArtifact()
	}
	recorder.usage.Upsert(entry)
	return recorder.writeUsage()
}

func (recorder *diskRunRecorder) writeUsage() error {
	contents, err := usage.MarshalArtifact(recorder.usage)
	if err != nil {
		return err
	}
	return recorder.writer.WriteUsage(contents)
}
