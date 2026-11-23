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
	PublishOnApprove   bool
}

// SummaryInput contains the values that are formatted into summary.txt.
type SummaryInput struct {
	Iterations         int
	Final              verdict.Verdict
	Nits               []verdict.Finding
	DiffStat           string
	WorktreePath       string
	ExhaustionReason   string
	NitBudgetOverrides []NitBudgetOverride
	PullRequestURL     string
	PublicationNotice  string
	Publication        Publication
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
		if spec.PublishOnApprove {
			metadata += "Publish on approve: true\n"
		}
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

// GateHandoffPath returns a distinct handoff path for a Gate-driven turn.
func (writer *Writer) GateHandoffPath(iteration, turn int) string {
	return filepath.Join(writer.directory, fmt.Sprintf("handoff-%02d-gate-%02d.md", iteration, turn))
}

// EvidenceDirectory returns the Evidence directory for one implementer iteration.
func (writer *Writer) EvidenceDirectory(iteration int) string {
	return filepath.Join(writer.directory, ArtifactName(EvidenceDirectory, iteration))
}

// GateScriptPath returns the artifact path for the branch-point Gate script.
func (writer *Writer) GateScriptPath() string {
	return filepath.Join(writer.directory, ArtifactName(GateScript, 0))
}

// WriteGateScript stores the script and preserves whether the branch-point copy is executable.
func (writer *Writer) WriteGateScript(contents []byte, mode os.FileMode) (string, error) {
	path := writer.GateScriptPath()
	if err := writer.writeFileMode(filepath.Base(path), contents, mode.Perm()); err != nil {
		return "", err
	}
	return path, nil
}

// WriteGateAttempt persists the structured result and full output of one attempt.
func (writer *Writer) WriteGateAttempt(iteration int, attempt GateAttempt) (GateAttemptPaths, error) {
	if iteration <= 0 || attempt.Number <= 0 {
		return GateAttemptPaths{}, fmt.Errorf("invalid Gate attempt %d in iteration %d", attempt.Number, iteration)
	}
	resultName := fmt.Sprintf("iteration-%02d-gate-attempt-%02d.txt", iteration, attempt.Number)
	outputName := fmt.Sprintf("iteration-%02d-gate-attempt-%02d.output.txt", iteration, attempt.Number)
	if err := writer.writeFile(outputName, []byte(attempt.Output)); err != nil {
		return GateAttemptPaths{}, err
	}
	var result strings.Builder
	outcome := "red"
	if attempt.Green {
		outcome = "green"
	}
	fmt.Fprintf(&result, "Gate attempt: %d\nOutcome: %s\nExit status: %d\n", attempt.Number, outcome, attempt.ExitStatus)
	if attempt.PerGateResultsAvailable {
		result.WriteString("Per-Gate results: available\n")
		for _, gate := range attempt.Results {
			fmt.Fprintf(&result, "  %s: %s\n", gate.Name, gate.Result)
		}
	} else {
		result.WriteString("Per-Gate results: unavailable\n")
	}
	fmt.Fprintf(&result, "Full output: %s\n", outputName)
	if err := writer.writeFile(resultName, []byte(result.String())); err != nil {
		return GateAttemptPaths{}, err
	}
	return GateAttemptPaths{
		Result: filepath.Join(writer.directory, resultName),
		Output: filepath.Join(writer.directory, outputName),
	}, nil
}

// WriteNoGate records that the branch point does not define a Gate script.
func (writer *Writer) WriteNoGate(iteration int) (string, error) {
	if iteration <= 0 {
		return "", fmt.Errorf("invalid no-Gate iteration %d", iteration)
	}
	name := fmt.Sprintf("iteration-%02d-gate.txt", iteration)
	if err := writer.writeFile(name, []byte("Gate: no gate\nBranch point has no scripts/quality.sh\n")); err != nil {
		return "", err
	}
	return filepath.Join(writer.directory, name), nil
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

// WriteImplementReviewFeed preserves one implementer turn for the reviewer.
func (writer *Writer) WriteImplementReviewFeed(iteration, turn int, feed string) (string, error) {
	if iteration < 1 || turn < 1 {
		return "", fmt.Errorf("invalid implementer turn %d/%d", iteration, turn)
	}
	name := fmt.Sprintf("iteration-%02d-implement-turn-%02d.feed", iteration, turn)
	if err := writer.writeFile(name, []byte(feed)); err != nil {
		return "", err
	}
	return filepath.Join(writer.directory, name), nil
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

// WriteFailure writes the error and diagnostics for one failed Role turn.
func (writer *Writer) WriteFailure(iteration int, role, contents string) error {
	name := FailureArtifactName(iteration, role)
	if name == "" {
		return fmt.Errorf("invalid Run failure artifact for role %q at iteration %d", role, iteration)
	}
	return writer.writeFile(name, []byte(contents))
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
	ImplementFailure: "implement.failure.txt",
	ReviewFailure:    "review.failure.txt",
	SummaryFile:      summaryFile,
	SessionsFile:     sessionsFile,
	UsageFile:        usageFile,
	MetadataFile:     metadataFile,
	GateScript:       "branch-point-quality.sh",
}

var iterationArtifactFormats = map[ArtifactKind]string{
	ImplementFeed:       "iteration-%02d-implement.feed",
	ImplementTranscript: "iteration-%02d-implement.transcript",
	ImplementHandoff:    "handoff-%02d.md",
	ReviewDiff:          "iteration-%02d-review.diff",
	ReviewFeed:          "iteration-%02d-review.feed",
	ReviewTranscript:    "iteration-%02d-review.transcript",
	VerdictFile:         "iteration-%02d-verdict.txt",
	ImplementFailure:    "iteration-%02d-implement.failure.txt",
	ReviewFailure:       "iteration-%02d-review.failure.txt",
	EvidenceDirectory:   "evidence-%d",
}

// FailureArtifactName returns the stable name for a failed Role artifact.
func FailureArtifactName(iteration int, role string) string {
	role = strings.TrimSpace(role)
	var kind ArtifactKind
	switch role {
	case "implement":
		kind = ImplementFailure
	case "review":
		kind = ReviewFailure
	default:
		return ""
	}
	return ArtifactName(kind, iteration)
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
	finalVerdict := string(summary.Final.Status)
	if finalVerdict == "" {
		finalVerdict = "none"
	}
	fmt.Fprintf(
		&builder,
		"Iterations: %d\nFinal verdict: %s\nSummary: %s\n",
		summary.Iterations, finalVerdict, summary.Final.Summary,
	)
	for _, override := range summary.NitBudgetOverrides {
		fmt.Fprintln(&builder, override)
	}
	builder.WriteString("Nit findings:\n")
	appendSummaryFindings(&builder, summary.Nits)
	appendSummaryWorktree(&builder, summary.WorktreePath)
	appendSummaryExhaustion(&builder, summary.ExhaustionReason)
	appendSummaryPublication(&builder, summary.Publication)
	appendIndentedBlock(&builder, "Publication notice", summary.PublicationNotice)
	appendSummaryPullRequest(&builder, summary)
	appendSummaryDiffStat(&builder, summary.DiffStat)
	return builder.String()
}

func appendSummaryFindings(builder *strings.Builder, findings []verdict.Finding) {
	if len(findings) == 0 {
		builder.WriteString("- (none)\n")
	} else {
		for _, finding := range findings {
			fmt.Fprintf(builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
		}
	}
}

func appendSummaryWorktree(builder *strings.Builder, worktreePath string) {
	if worktreePath == "" {
		return
	}
	fmt.Fprintf(builder,
		"Worktree: %s\nRemove worktree: git worktree remove --force %s\n",
		worktreePath, worktreePath,
	)
}

func appendSummaryExhaustion(builder *strings.Builder, reason string) {
	if reason == "" {
		return
	}
	fmt.Fprintf(builder, "Exhaustion reason: %s\n", reason)
}

func appendSummaryPublication(builder *strings.Builder, publication Publication) {
	if publication.Status == "" {
		return
	}
	fmt.Fprintf(builder, "Publication: %s\n", publication.Status)
	if publication.Step != "" {
		fmt.Fprintf(builder, "Publication step: %s\n", publication.Step)
	}
	if publication.Error == "" {
		return
	}
	appendIndentedBlock(builder, "Publication error", publication.Error)
}

func appendIndentedBlock(builder *strings.Builder, heading, contents string) {
	if contents == "" {
		return
	}
	fmt.Fprintf(builder, "%s:\n", heading)
	for _, line := range strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n") {
		fmt.Fprintf(builder, "  %s\n", line)
	}
}

func appendSummaryPullRequest(builder *strings.Builder, summary SummaryInput) {
	pullRequestURL := summary.Publication.URL
	if pullRequestURL == "" {
		pullRequestURL = summary.PullRequestURL
	}
	if pullRequestURL == "" {
		return
	}
	fmt.Fprintf(builder, "Pull request: %s\n", pullRequestURL)
}

func appendSummaryDiffStat(builder *strings.Builder, diffStat string) {
	fmt.Fprintf(builder, "Diff stat:\n%s\n", strings.TrimRight(diffStat, " \t\r\n"))
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
	return writer.writeFileMode(name, contents, 0o644)
}

func (writer *Writer) writeFileMode(name string, contents []byte, mode os.FileMode) error {
	path := filepath.Join(writer.directory, name)
	if err := os.WriteFile(path, contents, mode); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}
