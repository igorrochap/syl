package orchestration

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/runstate"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

// RunRecorder records the domain events that make up a syl run.
type RunRecorder interface {
	ImplementHandoffPath(iteration int) string
	RecordState(state runstate.State)
	RecordUsage(entry usage.Entry) error
	RecordImplementTurn(iteration int, feed, transcript string) error
	RecordReviewDiff(iteration int, diff string) (string, error)
	RecordReviewOutput(iteration int, review ReviewExecution) error
	RecordVerdict(iteration int, reviewVerdict verdict.Verdict) error
	RecordSessions(iteration int, role string, sessionIDs []string) error
	WriteSummary(summary implementSummary) error
	WriteSessions() error
}

// RunSpec describes the artifacts and lifecycle metadata for a Run.
type RunSpec struct {
	Kind               runstate.Kind
	TicketRef          string
	MaxIterations      int
	Branch             string
	BranchPoint        string
	WorkRoot           string
	ImplementerHarness string
	ReviewerHarness    string
	ImplementContext   string
	ReviewContext      string
}

func recorderArtifactDir(recorder RunRecorder) string {
	return filepath.Dir(recorder.ImplementHandoffPath(1))
}

type artifactKind uint8

const (
	metadataArtifact artifactKind = iota
	implementFeedArtifact
	implementTranscriptArtifact
	implementHandoffArtifact
	reviewDiffArtifact
	reviewFeedArtifact
	reviewTranscriptArtifact
	verdictArtifact
	summaryArtifact
	sessionsArtifact
	usageArtifact
)

type diskRunRecorder struct {
	dir           string
	sessions      []string
	sessionKeys   map[sessionKey]struct{}
	usage         usage.Artifact
	runState      runstate.State
	warningOutput io.Writer
	marker        *sylhome.LiveRun
}

type sessionKey struct {
	iteration int
	role      string
	sessionID string
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

// Context blocks use two-space indentation so their lines remain separate from
// the unindented metadata keys. The usage metadata reader relies on that boundary.
func appendRoleContext(metadata, role, context string) string {
	context = strings.TrimSpace(context)
	if context == "" {
		return metadata
	}

	var builder strings.Builder
	builder.WriteString(metadata)
	builder.WriteString(role)
	builder.WriteString(" context:\n")
	for _, line := range strings.Split(context, "\n") {
		builder.WriteString("  ")
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func openDiskRun(originRoot string, sylHome sylhome.Dir, warningOutput io.Writer, spec RunSpec) (*diskRunRecorder, error) {
	workRoot, err := resolveRunWorkRoot(spec.WorkRoot)
	if err != nil {
		return nil, err
	}
	suffix, metadata, runType, err := runArtifacts(spec, workRoot)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(
		originRoot,
		".syl",
		"runs",
		time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+suffix,
	)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s run artifacts %s: %w", runType, dir, err)
	}
	recorder := &diskRunRecorder{
		dir:           dir,
		sessionKeys:   make(map[sessionKey]struct{}),
		usage:         usage.NewArtifact(),
		warningOutput: warningOutput,
		runState:      newRunState(spec),
	}
	recorder.RecordState(recorder.runState)
	if err := writeArtifact(filepath.Join(dir, artifactFilename(metadataArtifact, 0)), metadata); err != nil {
		return nil, recorder.failOpen(err)
	}
	if err := recorder.writeUsage(); err != nil {
		return nil, recorder.failOpen(err)
	}
	recorder.createLiveRunMarker(sylHome, originRoot)
	return recorder, nil
}

func runArtifacts(spec RunSpec, workRoot string) (string, string, string, error) {
	trimmedTicketRef := strings.TrimSpace(spec.TicketRef)
	suffix := strings.TrimPrefix(trimmedTicketRef, "#")
	if suffix == "" {
		suffix = string(spec.Kind)
	}
	switch spec.Kind {
	case runstate.Implement:
		metadata := fmt.Sprintf(
			"Branch: %s\nBranch point: %s\nWork root: %s\nImplementer harness: %s\nReviewer harness: %s\n",
			spec.Branch, spec.BranchPoint, workRoot, spec.ImplementerHarness, spec.ReviewerHarness,
		)
		metadata = appendRoleContext(metadata, "Implementer", spec.ImplementContext)
		metadata = appendRoleContext(metadata, "Reviewer", spec.ReviewContext)
		return suffix, metadata, "implement", nil
	case runstate.Review:
		number, err := strconv.Atoi(suffix)
		if err != nil || number <= 0 {
			suffix = "review"
		}
		metadata := fmt.Sprintf(
			"Ticket: %s\nBranch point: %s\nWork root: %s\nReviewer harness: %s\n",
			trimmedTicketRef, spec.BranchPoint, workRoot, spec.ReviewerHarness,
		)
		metadata = appendRoleContext(metadata, "Reviewer", spec.ReviewContext)
		return suffix, metadata, "review", nil
	default:
		return "", "", "", fmt.Errorf("unsupported Run kind %q", spec.Kind)
	}
}

func (r *diskRunRecorder) failOpen(err error) error {
	failed := r.runState
	failed.Status = runstate.Failed
	failed.Activity = ""
	failed.Question = ""
	ended := time.Now().UTC()
	failed.EndedAt = &ended
	r.RecordState(failed)
	return err
}

func (r *diskRunRecorder) createLiveRunMarker(sylHome sylhome.Dir, projectRoot string) {
	if sylHome.String() == "" {
		return
	}
	marker, err := sylHome.MarkLive(sylhome.LiveRun{
		ProjectPath: projectRoot,
		RunDir:      r.dir,
		TicketRef:   r.runState.TicketRef,
		Host:        r.runState.Hostname,
		PID:         r.runState.PID,
	})
	if err != nil {
		r.warn("create live-run marker", err)
		return
	}
	r.marker = &marker
}

func (r *diskRunRecorder) RecordState(state runstate.State) {
	r.runState = state
	if err := runstate.Write(runstate.Path(r.dir), state); err != nil {
		r.warn("write run state", err)
	}
	if state.Status != runstate.Running {
		r.removeLiveRunMarker()
	}
}

func (r *diskRunRecorder) removeLiveRunMarker() {
	if r.marker == nil {
		return
	}
	if err := r.marker.Unmark(); err != nil {
		r.warn("remove live-run marker", err)
		return
	}
	r.marker = nil
}

func (r *diskRunRecorder) warn(action string, err error) {
	if r.warningOutput == nil {
		return
	}
	_, _ = fmt.Fprintf(r.warningOutput, "syl: warning: %s: %v\n", action, err)
}

func (r *diskRunRecorder) ImplementHandoffPath(iteration int) string {
	return filepath.Join(r.dir, artifactFilename(implementHandoffArtifact, iteration))
}

func (r *diskRunRecorder) RecordImplementTurn(iteration int, feed, transcript string) error {
	if err := r.write(implementFeedArtifact, iteration, feed); err != nil {
		return err
	}
	return r.write(implementTranscriptArtifact, iteration, transcript)
}

func (r *diskRunRecorder) RecordReviewDiff(iteration int, diff string) (string, error) {
	path := filepath.Join(r.dir, artifactFilename(reviewDiffArtifact, iteration))
	if err := writeArtifact(path, diff); err != nil {
		return "", fmt.Errorf("write pre-computed diff: %w", err)
	}
	return path, nil
}

func (r *diskRunRecorder) RecordReviewOutput(iteration int, review ReviewExecution) error {
	if err := r.write(reviewFeedArtifact, iteration, review.Feed); err != nil {
		return err
	}
	return r.write(reviewTranscriptArtifact, iteration, review.Transcript)
}

func (r *diskRunRecorder) RecordVerdict(
	iteration int,
	reviewVerdict verdict.Verdict,
) error {
	return r.write(verdictArtifact, iteration, formatVerdict(reviewVerdict))
}

func (r *diskRunRecorder) RecordSessions(iteration int, role string, sessionIDs []string) error {
	if r.sessionKeys == nil {
		r.sessionKeys = make(map[sessionKey]struct{})
	}
	recordSessions(&r.sessions, r.sessionKeys, iteration, role, sessionIDs)
	return r.WriteSessions()
}

func (r *diskRunRecorder) WriteSummary(summary implementSummary) error {
	return r.write(summaryArtifact, 0, formatImplementSummary(summary))
}

func (r *diskRunRecorder) WriteSessions() error {
	sessions := append([]string(nil), r.sessions...)
	sort.Strings(sessions)
	return r.write(sessionsArtifact, 0, strings.Join(sessions, "\n")+"\n")
}

func (r *diskRunRecorder) RecordUsage(entry usage.Entry) error {
	if r.usage.SchemaVersion == 0 {
		r.usage = usage.NewArtifact()
	}
	r.usage.Upsert(entry)
	return r.writeUsage()
}

func (r *diskRunRecorder) writeUsage() error {
	return usage.WriteArtifact(filepath.Join(r.dir, artifactFilename(usageArtifact, 0)), r.usage)
}

func (r *diskRunRecorder) write(kind artifactKind, iteration int, contents string) error {
	return writeArtifact(filepath.Join(r.dir, artifactFilename(kind, iteration)), contents)
}

func recordSessions(
	sessions *[]string,
	sessionKeys map[sessionKey]struct{},
	iteration int,
	role string,
	sessionIDs []string,
) {
	for _, recordedSessionID := range sessionIDs {
		sessionID, ok := normalizeSessionID(recordedSessionID)
		if !ok {
			continue
		}
		key := sessionKey{iteration: iteration, role: role, sessionID: sessionID}
		if _, exists := sessionKeys[key]; exists {
			continue
		}
		sessionKeys[key] = struct{}{}
		*sessions = append(*sessions, fmt.Sprintf("iteration %d %s: %s", iteration, role, sessionID))
	}
}

func artifactFilename(kind artifactKind, iteration int) string {
	// Standalone reviews use iteration zero to preserve their unprefixed names.
	if iteration == 0 {
		return standaloneArtifactFilename(kind)
	}
	return iterationArtifactFilename(kind, iteration)
}

func standaloneArtifactFilename(kind artifactKind) string {
	switch kind {
	case metadataArtifact:
		return "metadata.txt"
	case reviewDiffArtifact:
		return "review.diff"
	case reviewFeedArtifact:
		return "review.feed"
	case reviewTranscriptArtifact:
		return "review.transcript"
	case verdictArtifact:
		return "verdict.txt"
	case summaryArtifact:
		return "summary.txt"
	case sessionsArtifact:
		return "sessions.txt"
	case usageArtifact:
		return "usage.json"
	}
	return ""
}

func iterationArtifactFilename(kind artifactKind, iteration int) string {
	switch kind {
	case implementFeedArtifact:
		return fmt.Sprintf("iteration-%02d-implement.feed", iteration)
	case implementTranscriptArtifact:
		return fmt.Sprintf("iteration-%02d-implement.transcript", iteration)
	case implementHandoffArtifact:
		return fmt.Sprintf("handoff-%02d.md", iteration)
	case reviewDiffArtifact:
		return fmt.Sprintf("iteration-%02d-review.diff", iteration)
	case reviewFeedArtifact:
		return fmt.Sprintf("iteration-%02d-review.feed", iteration)
	case reviewTranscriptArtifact:
		return fmt.Sprintf("iteration-%02d-review.transcript", iteration)
	case verdictArtifact:
		return fmt.Sprintf("iteration-%02d-verdict.txt", iteration)
	}
	return ""
}

func writeArtifact(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}
