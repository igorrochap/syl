package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

// RunRecorder records the domain events that make up a syl run.
type RunRecorder interface {
	Dir() string
	RecordImplementTurn(iteration int, feed, transcript string) error
	RecordReviewDiff(iteration int, diff string) (string, error)
	RecordReviewOutput(iteration int, review ReviewExecution) error
	RecordVerdict(iteration int, reviewVerdict verdict.Verdict) error
	RecordSessions(iteration int, role string, sessionIDs []string)
	WriteSummary(iterations int, final verdict.Verdict, nits []verdict.Finding, diffStat string) error
	WriteSessions() error
}

// UsageRecorder is implemented by disk-backed run recorders. Keeping this
// optional preserves the small in-memory recorder seam used by orchestration
// tests and by callers that do not persist usage artifacts.
type UsageRecorder interface {
	RecordUsage(entry usage.Entry) error
}

type artifactKind uint8

const (
	metadataArtifact artifactKind = iota
	implementFeedArtifact
	implementTranscriptArtifact
	reviewDiffArtifact
	reviewFeedArtifact
	reviewTranscriptArtifact
	verdictArtifact
	summaryArtifact
	sessionsArtifact
	usageArtifact
)

type diskRunRecorder struct {
	dir         string
	sessions    []string
	sessionKeys map[sessionKey]struct{}
	usage       usage.Artifact
}

type sessionKey struct {
	iteration int
	role      string
	sessionID string
}

var _ RunRecorder = (*diskRunRecorder)(nil)

func newImplementRunRecorder(
	projectRoot string,
	issueNumber int,
	branch string,
	branchPoint string,
) (*diskRunRecorder, error) {
	metadata := fmt.Sprintf("Branch: %s\nBranch point: %s\n", branch, branchPoint)
	return newDiskRunRecorder(
		projectRoot,
		strconv.Itoa(issueNumber),
		metadata,
		"implement",
	)
}

func newReviewRunRecorder(
	projectRoot string,
	ticketRef string,
	branchPoint string,
) (*diskRunRecorder, error) {
	suffix := "review"
	trimmedTicketRef := strings.TrimSpace(ticketRef)
	if number, err := strconv.Atoi(strings.TrimPrefix(trimmedTicketRef, "#")); err == nil && number > 0 {
		suffix = strconv.Itoa(number)
	}
	metadata := fmt.Sprintf("Ticket: %s\nBranch point: %s\n", trimmedTicketRef, branchPoint)
	return newDiskRunRecorder(projectRoot, suffix, metadata, "review")
}

func newDiskRunRecorder(
	projectRoot string,
	suffix string,
	metadata string,
	runType string,
) (*diskRunRecorder, error) {
	dir := filepath.Join(
		projectRoot,
		".syl",
		"runs",
		time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+suffix,
	)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s run artifacts: %w", runType, err)
	}
	if err := writeArtifact(filepath.Join(dir, artifactFilename(metadataArtifact, 0)), metadata); err != nil {
		return nil, err
	}
	recorder := &diskRunRecorder{
		dir:         dir,
		sessionKeys: make(map[sessionKey]struct{}),
		usage:       usage.NewArtifact(),
	}
	if err := recorder.writeUsage(); err != nil {
		return nil, err
	}
	return recorder, nil
}

func (r *diskRunRecorder) Dir() string {
	return r.dir
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

func (r *diskRunRecorder) RecordSessions(iteration int, role string, sessionIDs []string) {
	if r.sessionKeys == nil {
		r.sessionKeys = make(map[sessionKey]struct{})
	}
	recordSessions(&r.sessions, r.sessionKeys, iteration, role, sessionIDs)
}

func (r *diskRunRecorder) WriteSummary(
	iterations int,
	final verdict.Verdict,
	nits []verdict.Finding,
	diffStat string,
) error {
	return r.write(summaryArtifact, 0, formatImplementSummary(iterations, final, nits, diffStat))
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
	}

	switch kind {
	case implementFeedArtifact:
		return fmt.Sprintf("iteration-%02d-implement.feed", iteration)
	case implementTranscriptArtifact:
		return fmt.Sprintf("iteration-%02d-implement.transcript", iteration)
	case reviewDiffArtifact:
		return fmt.Sprintf("iteration-%02d-review.diff", iteration)
	case reviewFeedArtifact:
		return fmt.Sprintf("iteration-%02d-review.feed", iteration)
	case reviewTranscriptArtifact:
		return fmt.Sprintf("iteration-%02d-review.transcript", iteration)
	case verdictArtifact:
		return fmt.Sprintf("iteration-%02d-verdict.txt", iteration)
	default:
		return ""
	}
}

func writeArtifact(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}
