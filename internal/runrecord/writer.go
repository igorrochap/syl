package runrecord

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/verdict"
)

// Spec supplies the metadata used to create a Run record.
type Spec struct {
	Kind               Kind
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

// SummaryInput contains the values that are formatted into summary.txt.
type SummaryInput struct {
	Iterations   int
	Final        verdict.Verdict
	Nits         []verdict.Finding
	DiffStat     string
	WorktreePath string
}

type sessionKey struct {
	iteration int
	role      string
	sessionID string
}

// Writer writes files owned by one Run record.
type Writer struct {
	directory   string
	sessions    []string
	sessionKeys map[sessionKey]struct{}
}

// CreateWriter creates one timestamped Run directory beneath originRoot.
func CreateWriter(originRoot string, spec Spec, now time.Time) (*Writer, error) {
	name, err := DirectoryName(now, spec.Kind, spec.TicketRef)
	if err != nil {
		return nil, err
	}
	return CreateNamedWriter(originRoot, name)
}

// CreateNamedWriter creates a writer for an existing Run's directory name.
func CreateNamedWriter(originRoot, directoryName string) (*Writer, error) {
	directory := RunDirectory(originRoot, directoryName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create run artifacts %s: %w", directory, err)
	}
	return NewWriter(directory), nil
}

// NewWriter constructs a writer for an already-created Run directory.
func NewWriter(directory string) *Writer {
	return &Writer{directory: directory, sessionKeys: make(map[sessionKey]struct{})}
}

// Directory returns the Run directory owned by this writer.
func (writer *Writer) Directory() string { return writer.directory }

// WriteMetadata writes metadata.txt with the stable field and context ordering.
func (writer *Writer) WriteMetadata(spec Spec) error {
	var metadata string
	switch spec.Kind {
	case Implement:
		metadata = fmt.Sprintf(
			"Branch: %s\nBranch point: %s\nWork root: %s\nImplementer harness: %s\nReviewer harness: %s\n",
			spec.Branch, spec.BranchPoint, spec.WorkRoot, spec.ImplementerHarness, spec.ReviewerHarness,
		)
		metadata = appendRoleContext(metadata, "Implementer", spec.ImplementContext)
		metadata = appendRoleContext(metadata, "Reviewer", spec.ReviewContext)
	case Review:
		metadata = fmt.Sprintf(
			"Ticket: %s\nBranch point: %s\nWork root: %s\nReviewer harness: %s\n",
			strings.TrimSpace(spec.TicketRef), spec.BranchPoint, spec.WorkRoot, spec.ReviewerHarness,
		)
		metadata = appendRoleContext(metadata, "Reviewer", spec.ReviewContext)
	default:
		return fmt.Errorf("unsupported Run kind %q", spec.Kind)
	}
	return writer.writeFile(metadataFile, []byte(metadata))
}

// WriteState atomically replaces run-state.json.
func (writer *Writer) WriteState(state State) error {
	return Write(Path(writer.directory), state)
}

// HandoffPath returns the path for one implementer handoff document.
func (writer *Writer) HandoffPath(iteration int) string {
	return filepath.Join(writer.directory, ArtifactName(ImplementHandoff, iteration))
}

// WriteArtifact writes one named-by-kind Run artifact.
func (writer *Writer) WriteArtifact(kind ArtifactKind, iteration int, contents []byte) error {
	name := ArtifactName(kind, iteration)
	if name == "" {
		return fmt.Errorf("invalid Run artifact kind %d at iteration %d", kind, iteration)
	}
	return writer.writeFile(name, contents)
}

// WriteImplementTurn writes the feed and transcript for an implementer turn.
func (writer *Writer) WriteImplementTurn(iteration int, feed, transcript string) error {
	if err := writer.WriteArtifact(ImplementFeed, iteration, []byte(feed)); err != nil {
		return err
	}
	return writer.WriteArtifact(ImplementTranscript, iteration, []byte(transcript))
}

// WriteReviewDiff writes a review diff and returns its path.
func (writer *Writer) WriteReviewDiff(iteration int, diff string) (string, error) {
	if err := writer.WriteArtifact(ReviewDiff, iteration, []byte(diff)); err != nil {
		return "", fmt.Errorf("write pre-computed diff: %w", err)
	}
	return filepath.Join(writer.directory, ArtifactName(ReviewDiff, iteration)), nil
}

// WriteReviewOutput writes the feed and transcript for a reviewer turn.
func (writer *Writer) WriteReviewOutput(iteration int, feed, transcript string) error {
	if err := writer.WriteArtifact(ReviewFeed, iteration, []byte(feed)); err != nil {
		return err
	}
	return writer.WriteArtifact(ReviewTranscript, iteration, []byte(transcript))
}

// WriteVerdict writes one structured verdict file.
func (writer *Writer) WriteVerdict(iteration int, reviewVerdict verdict.Verdict) error {
	return writer.WriteArtifact(VerdictFile, iteration, []byte(FormatVerdict(reviewVerdict)))
}

// RecordSessions adds valid session identifiers and rewrites sessions.txt.
func (writer *Writer) RecordSessions(iteration int, role string, sessionIDs []string) error {
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		key := sessionKey{iteration: iteration, role: role, sessionID: sessionID}
		if _, exists := writer.sessionKeys[key]; exists {
			continue
		}
		writer.sessionKeys[key] = struct{}{}
		writer.sessions = append(writer.sessions, fmt.Sprintf("iteration %d %s: %s", iteration, role, sessionID))
	}
	return writer.WriteSessions()
}

// WriteSessions rewrites sessions.txt in stable line order.
func (writer *Writer) WriteSessions() error {
	sessions := append([]string(nil), writer.sessions...)
	sort.Strings(sessions)
	return writer.writeFile(sessionsFile, []byte(strings.Join(sessions, "\n")+"\n"))
}

// WriteSummary formats and writes summary.txt.
func (writer *Writer) WriteSummary(summary SummaryInput) error {
	return writer.writeFile(summaryFile, []byte(FormatSummary(summary)))
}

// WriteUsage writes serialized usage.json bytes supplied by the usage module.
func (writer *Writer) WriteUsage(contents []byte) error {
	return writer.writeFile(usageFile, contents)
}

var standaloneArtifactNames = map[ArtifactKind]string{
	ReviewDiff:       "review.diff",
	ReviewFeed:       "review.feed",
	ReviewTranscript: "review.transcript",
	VerdictFile:      "verdict.txt",
	SummaryFile:      summaryFile,
	SessionsFile:     sessionsFile,
	UsageFile:        usageFile,
	MetadataFile:     metadataFile,
}

var iterationArtifactFormats = map[ArtifactKind]string{
	ImplementFeed:       "iteration-%02d-implement.feed",
	ImplementTranscript: "iteration-%02d-implement.transcript",
	ImplementHandoff:    "handoff-%02d.md",
	ReviewDiff:          "iteration-%02d-review.diff",
	ReviewFeed:          "iteration-%02d-review.feed",
	ReviewTranscript:    "iteration-%02d-review.transcript",
	VerdictFile:         "iteration-%02d-verdict.txt",
}

// ArtifactName returns the stable name for one artifact kind and iteration.
func ArtifactName(kind ArtifactKind, iteration int) string {
	if iteration == 0 {
		return standaloneArtifactNames[kind]
	}
	format, ok := iterationArtifactFormats[kind]
	if !ok {
		return ""
	}
	return fmt.Sprintf(format, iteration)
}

// FormatVerdict returns the stable text representation stored in verdict files.
func FormatVerdict(reviewVerdict verdict.Verdict) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "VERDICT: %s\nSUMMARY: %s\nFINDINGS:\n", reviewVerdict.Status, reviewVerdict.Summary)
	if len(reviewVerdict.Findings) == 0 {
		builder.WriteString("- (none)\n")
		return builder.String()
	}
	for _, finding := range reviewVerdict.Findings {
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
	}
	return builder.String()
}

// FormatSummary returns the stable text representation stored in summary.txt.
func FormatSummary(summary SummaryInput) string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"Iterations: %d\nFinal verdict: %s\nSummary: %s\nNit findings:\n",
		summary.Iterations, summary.Final.Status, summary.Final.Summary,
	)
	if len(summary.Nits) == 0 {
		builder.WriteString("- (none)\n")
	} else {
		for _, finding := range summary.Nits {
			fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
		}
	}
	if summary.WorktreePath != "" {
		fmt.Fprintf(&builder,
			"Worktree: %s\nRemove worktree: git worktree remove --force %s\n",
			summary.WorktreePath, summary.WorktreePath,
		)
	}
	fmt.Fprintf(&builder, "Diff stat:\n%s\n", strings.TrimRight(summary.DiffStat, " \t\r\n"))
	return builder.String()
}

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

func (writer *Writer) writeFile(name string, contents []byte) error {
	path := filepath.Join(writer.directory, name)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}
