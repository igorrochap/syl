package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/ui"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

var ErrReviewNeedsRevision = errors.New("review verdict is revise")

type UnparseableVerdictError struct {
	Execution ReviewExecution
	Cause     error
}

func (e *UnparseableVerdictError) Error() string {
	return fmt.Sprintf("reviewer produced no parseable verdict after one re-ask: %v", e.Cause)
}

func (e *UnparseableVerdictError) Unwrap() error { return e.Cause }

type ReviewExecution struct {
	Verdict    verdict.Verdict
	Transcript string
	Feed       string
	SessionIDs []string
}

type harnessTranscript struct {
	Transcript string
	SessionIDs []string
}

type HarnessOutputMode uint8

const (
	ParsedHarnessOutput HarnessOutputMode = iota
	RawHarnessOutput
	QuietHarnessOutput
)

type ReviewOptions struct {
	OriginRoot           string
	WorkRoot             string
	ProjectConfig        config.Config
	IssueTracker         tracker.Tracker
	Ticket               *tracker.Ticket
	TicketRef            string
	Context              string
	Adapter              harness.Adapter
	Input                io.Reader
	Output               io.Writer
	Raw                  bool
	Verbose              bool
	TranscriptUsage      bool
	Notifier             Notifier
	Git                  GitRunner
	IdentificationBanner func() error
}

type reviewPreparation struct {
	branchPoint string
	diffPath    string
	recorder    RunRecorder
}

type standaloneReviewRun struct {
	review    ReviewExecution
	startedAt time.Time
	endedAt   time.Time
}

func RunReview(ctx context.Context, options ReviewOptions) error {
	if options.Raw && options.Verbose {
		return errors.New("review: --raw and --verbose are mutually exclusive")
	}
	if options.Adapter == nil {
		return fmt.Errorf("review harness %q is not configured", options.ProjectConfig.Roles.Review.Harness)
	}
	options.Output = ensureLineTrackingWriter(options.Output)
	preparation, err := prepareReviewWithContext(ctx, options.OriginRoot, options.TicketRef, options.Context, options.Git)
	if err != nil {
		return err
	}
	if options.IdentificationBanner != nil {
		if err := options.IdentificationBanner(); err != nil {
			return err
		}
	}
	run, err := runStandaloneReview(ctx, options, preparation)
	if err != nil {
		return handleStandaloneReviewError(options, preparation, run, err)
	}
	return completeStandaloneReview(ctx, options, preparation, run)
}

func completeStandaloneReview(ctx context.Context, options ReviewOptions, preparation reviewPreparation, run standaloneReviewRun) error {
	recordStandaloneReviewUsage(
		options,
		preparation.recorder,
		run.review,
		run.startedAt,
		run.endedAt,
	)
	if artifactErr := recordStandaloneReviewArtifacts(preparation.recorder, run.review); artifactErr != nil {
		return fmt.Errorf("review: save review run artifacts: %w", artifactErr)
	}
	if err := recordStandaloneReviewVerdict(ctx, options, preparation, run.review.Verdict); err != nil {
		return err
	}
	reviewVerdict := run.review.Verdict
	if !options.Raw {
		renderer := ui.New(options.Output, ui.DetectCaps(options.Output))
		if err := renderer.ReviewVerdict(ui.ReviewVerdict{
			Status: string(reviewVerdict.Status), Summary: reviewVerdict.Summary, Findings: toUIFindings(reviewVerdict.Findings),
		}); err != nil {
			return fmt.Errorf("write verdict: %w", err)
		}
	}
	if reviewVerdict.Status == verdict.Revise {
		return ErrReviewNeedsRevision
	}
	return nil
}

func recordStandaloneReviewVerdict(ctx context.Context, options ReviewOptions, preparation reviewPreparation, reviewVerdict verdict.Verdict) error {
	if options.ProjectConfig.Tracker.Reviews.IsRemote() {
		if options.IssueTracker == nil || options.Ticket == nil {
			return errors.New("remote review logging requires an issue reference (N or #N)")
		}
		if err := options.IssueTracker.AddComment(ctx, options.Ticket.Number, formatRemoteReviewComment(reviewVerdict)); err != nil {
			return fmt.Errorf("post review to remote issue #%d: %w", options.Ticket.Number, err)
		}
	} else {
		if _, err := writeLocalReviewLog(options.OriginRoot, options.TicketRef, preparation.branchPoint, time.Now().UTC(), reviewVerdict); err != nil {
			return err
		}
	}
	return nil
}

func handleStandaloneReviewError(options ReviewOptions, preparation reviewPreparation, run standaloneReviewRun, err error) error {
	var unparseable *UnparseableVerdictError
	if !errors.As(err, &unparseable) {
		return err
	}
	recordStandaloneReviewUsage(
		options,
		preparation.recorder,
		unparseable.Execution,
		run.startedAt,
		run.endedAt,
	)
	if artifactErr := recordStandaloneReviewArtifacts(preparation.recorder, unparseable.Execution); artifactErr != nil {
		return fmt.Errorf("%w; save review run artifacts: %v", err, artifactErr)
	}
	return reviewTranscriptSavedError(err, preparation.recorder.Dir())
}

func runStandaloneReview(ctx context.Context, options ReviewOptions, preparation reviewPreparation) (standaloneReviewRun, error) {
	prompt := composeReviewPrompt(options.TicketRef, options.Ticket, preparation.branchPoint, preparation.diffPath, options.Context)
	notifier := options.Notifier
	if !options.ProjectConfig.Notifications.Enabled {
		notifier = nil
	}
	notifier = withNotificationContext(notifier, options.OriginRoot, options.Git)
	questions := NewQuestionHandler(options.Input, options.Output, options.TicketRef, notifier)
	mode := QuietHarnessOutput
	if options.Verbose {
		mode = ParsedHarnessOutput
	}
	if options.Raw {
		mode = RawHarnessOutput
	}
	if !options.Raw {
		if err := writeRoleSection(options.Output, "Reviewer"); err != nil {
			return standaloneReviewRun{}, fmt.Errorf("write review role: %w", err)
		}
	}
	startedAt := time.Now().UTC()
	review, err := runReview(ctx, options.Adapter, harness.Request{
		Model: options.ProjectConfig.Roles.Review.Model, Effort: options.ProjectConfig.Roles.Review.Effort,
		Prompt: prompt, MCP: options.ProjectConfig.Roles.Review.MCP,
	}, options.Output, mode, questions)
	endedAt := time.Now().UTC()
	return standaloneReviewRun{review: review, startedAt: startedAt, endedAt: endedAt}, err
}

type reviewUsageParams struct {
	recorder   RunRecorder
	iteration  int
	role       config.RoleConfig
	execution  ReviewExecution
	workRoot   string
	startedAt  time.Time
	endedAt    time.Time
	transcript bool
}

func recordStandaloneReviewUsage(
	options ReviewOptions,
	recorder RunRecorder,
	execution ReviewExecution,
	startedAt time.Time,
	endedAt time.Time,
) {
	recordReviewUsage(reviewUsageParams{
		recorder:   recorder,
		iteration:  0,
		role:       options.ProjectConfig.Roles.Review,
		execution:  execution,
		workRoot:   options.WorkRoot,
		startedAt:  startedAt,
		endedAt:    endedAt,
		transcript: options.TranscriptUsage,
	})
}

func recordReviewUsage(params reviewUsageParams) {
	invocation := usage.Invocation{
		Iteration:  params.iteration,
		Role:       "review",
		Harness:    string(params.role.Harness),
		Model:      params.role.Model,
		SessionIDs: params.execution.SessionIDs,
		StartedAt:  params.startedAt,
		EndedAt:    params.endedAt,
	}
	if params.transcript {
		recordRoleUsage(
			params.recorder,
			usage.CollectTranscriptInvocation(invocation, params.workRoot, ""),
		)
		return
	}
	recordRoleUsage(params.recorder, usage.CollectInvocation(invocation, params.workRoot, ""))
}

func prepareReview(ctx context.Context, originRoot, ticketRef string, git GitRunner) (reviewPreparation, error) {
	return prepareReviewWithContext(ctx, originRoot, ticketRef, "", git)
}

func prepareReviewWithContext(ctx context.Context, originRoot, ticketRef, reviewContext string, git GitRunner) (reviewPreparation, error) {
	if git == nil {
		return reviewPreparation{}, errors.New("review: git runner is not configured")
	}
	branchPoint, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return reviewPreparation{}, fmt.Errorf("review: record branch point: %w", err)
	}
	branchPoint = strings.TrimSpace(branchPoint)
	if branchPoint == "" {
		return reviewPreparation{}, errors.New("review: record branch point: git returned an empty ref")
	}
	diff, err := computeReviewDiff(ctx, git, branchPoint)
	if err != nil {
		return reviewPreparation{}, fmt.Errorf("review: %w", err)
	}
	recorder, err := newReviewRunRecorder(originRoot, ticketRef, branchPoint, reviewContext)
	if err != nil {
		return reviewPreparation{}, err
	}
	diffPath, err := recorder.RecordReviewDiff(0, diff)
	if err != nil {
		return reviewPreparation{}, fmt.Errorf("review: %w", err)
	}
	return reviewPreparation{branchPoint: branchPoint, diffPath: diffPath, recorder: recorder}, nil
}

func runReview(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	return RunReviewExecutionWithProgress(ctx, adapter, request, output, mode, questions)
}

func recordStandaloneReviewArtifacts(recorder RunRecorder, review ReviewExecution) error {
	if err := recorder.RecordReviewOutput(0, review); err != nil {
		return err
	}
	recorder.RecordSessions(0, "review", review.SessionIDs)
	return recorder.WriteSessions()
}

func RunReviewExecution(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	return runReviewExecution(ctx, adapter, request, output, mode, questions, nil)
}

func RunReviewExecutionWithProgress(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	visibleOutput := newLiveHarnessOutput(output, mode)
	if mode == RawHarnessOutput {
		return runReviewExecution(ctx, adapter, request, visibleOutput, mode, questions, nil)
	}
	progress := newReviewProgressWriter(visibleOutput)
	return runReviewExecution(ctx, adapter, request, progress, mode, questions, progress)
}

type reviewTurnOutput interface {
	EndTurn() error
}

type reviewExecutionOptions struct {
	request          harness.Request
	output           io.Writer
	mode             HarnessOutputMode
	questions        *QuestionHandler
	turnOutput       reviewTurnOutput
	initialSessionID string
	start            harnessStreamStarter
}

type reviewResumeOptions struct {
	sessionID    string
	request      harness.Request
	resumePrompt string
	output       io.Writer
	mode         HarnessOutputMode
	questions    *QuestionHandler
}

func runReviewExecution(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler, turnOutput reviewTurnOutput) (ReviewExecution, error) {
	return runReviewExecutionFrom(ctx, adapter, reviewExecutionOptions{
		request:    request,
		output:     output,
		mode:       mode,
		questions:  questions,
		turnOutput: turnOutput,
		start: func(runContext context.Context) (harness.Stream, error) {
			return adapter.Run(runContext, request)
		},
	})
}

func runReviewExecutionWithResumeFallback(ctx context.Context, adapter harness.Adapter, options reviewResumeOptions) (ReviewExecution, error) {
	sessionID, hasSession := normalizeSessionID(options.sessionID)
	if !hasSession {
		return RunReviewExecutionWithProgress(ctx, adapter, options.request, options.output, options.mode, options.questions)
	}

	visibleOutput := newLiveHarnessOutput(options.output, options.mode)
	progress := newReviewProgressWriter(visibleOutput)
	resumeRequest := options.request
	resumeRequest.Prompt = options.resumePrompt
	review, err := runReviewExecutionFrom(ctx, adapter, reviewExecutionOptions{
		request:          resumeRequest,
		output:           progress,
		mode:             options.mode,
		questions:        options.questions,
		turnOutput:       progress,
		initialSessionID: sessionID,
		start: func(runContext context.Context) (harness.Stream, error) {
			stream, resumeErr := adapter.Resume(runContext, sessionID, resumeRequest)
			if resumeErr != nil {
				return nil, &reviewResumeError{cause: resumeErr}
			}
			return stream, nil
		},
	})
	if err == nil {
		return review, nil
	}

	var resumeErr *reviewResumeError
	if !errors.As(err, &resumeErr) {
		return ReviewExecution{}, err
	}
	if err := progress.EndTurn(); err != nil {
		return ReviewExecution{}, fmt.Errorf("flush failed resumed review output: %w", err)
	}
	return runReviewExecutionFrom(ctx, adapter, reviewExecutionOptions{
		request:    options.request,
		output:     progress,
		mode:       options.mode,
		questions:  options.questions,
		turnOutput: progress,
		start: func(runContext context.Context) (harness.Stream, error) {
			return adapter.Run(runContext, options.request)
		},
	})
}

type reviewResumeError struct {
	cause error
}

func (e *reviewResumeError) Error() string {
	return fmt.Sprintf("resume reviewer session: %v", e.cause)
}

func (e *reviewResumeError) Unwrap() error { return e.cause }

func runReviewExecutionFrom(ctx context.Context, adapter harness.Adapter, options reviewExecutionOptions) (ReviewExecution, error) {
	var feed bytes.Buffer
	artifactOutput := newPlainHarnessOutput(&feed, options.mode)
	first, err := runHarnessConversation(ctx, adapter, options.start, conversationOptions{
		request:   options.request,
		output:    options.output,
		artifact:  artifactOutput,
		mode:      options.mode,
		questions: options.questions,
		role:      "review",
		sessionID: options.initialSessionID,
	})
	if err != nil {
		reviewErr := fmt.Errorf("run review harness: %w", err)
		if options.initialSessionID != "" {
			return ReviewExecution{}, &reviewResumeError{cause: reviewErr}
		}
		return ReviewExecution{}, reviewErr
	}
	if err := endReviewTurn(options.turnOutput); err != nil {
		return ReviewExecution{}, err
	}
	reviewVerdict, parseErr := verdict.Parse(first.Transcript)
	if parseErr == nil {
		return ReviewExecution{Verdict: reviewVerdict, Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}, nil
	}
	if len(first.SessionIDs) == 0 {
		execution := ReviewExecution{Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}
		return execution, &UnparseableVerdictError{Execution: execution, Cause: parseErr}
	}

	retryOptions := conversationOptions{
		request:   options.request,
		output:    options.output,
		artifact:  artifactOutput,
		mode:      options.mode,
		questions: options.questions,
		role:      "review",
		sessionID: first.SessionIDs[len(first.SessionIDs)-1],
	}
	retryOptions.request.Prompt = "emit the verdict block"
	retry, err := retryReviewDetails(ctx, adapter, retryOptions)
	if err != nil {
		return ReviewExecution{}, err
	}
	if err := endReviewTurn(options.turnOutput); err != nil {
		return ReviewExecution{}, err
	}
	transcript := appendReviewTranscript(first.Transcript, retry.Transcript)
	sessions := append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...)
	reviewVerdict, parseErr = verdict.Parse(transcript)
	if parseErr == nil {
		return ReviewExecution{Verdict: reviewVerdict, Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}, nil
	}

	execution := ReviewExecution{Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}
	return execution, &UnparseableVerdictError{Execution: execution, Cause: parseErr}
}

func endReviewTurn(turnOutput reviewTurnOutput) error {
	if turnOutput == nil {
		return nil
	}
	if err := turnOutput.EndTurn(); err != nil {
		return fmt.Errorf("flush review output: %w", err)
	}
	return nil
}

func lastUsableSessionID(sessionIDs []string) string {
	for index := len(sessionIDs) - 1; index >= 0; index-- {
		if sessionID, ok := normalizeSessionID(sessionIDs[index]); ok {
			return sessionID
		}
	}
	return ""
}

func retryReviewDetails(ctx context.Context, adapter harness.Adapter, options conversationOptions) (harnessTranscript, error) {
	transcript, err := runHarnessConversation(ctx, adapter, func(resumeContext context.Context) (harness.Stream, error) {
		return adapter.Resume(resumeContext, options.sessionID, options.request)
	}, options)
	if err != nil {
		return harnessTranscript{}, fmt.Errorf("re-ask reviewer for verdict: %w", err)
	}
	return transcript, nil
}

func appendReviewTranscript(first, retry string) string {
	// Keep both turns so the parser's last-block rule can select the retry's
	// corrected verdict while retaining the complete review transcript.
	if first != "" && retry != "" && !strings.HasSuffix(first, "\n") {
		first += "\n"
	}
	return first + retry
}

func writeRawEvent(output io.Writer, event harness.Event) error {
	if event.Raw == "" {
		return nil
	}
	if _, err := io.WriteString(output, event.Raw); err != nil {
		return fmt.Errorf("write raw harness output: %w", err)
	}
	return nil
}

func writeParsedEvent(output io.Writer, event harness.Event) error {
	if renderer, ok := output.(harnessEventRenderer); ok {
		return renderWithHarnessEventRenderer(renderer, event)
	}
	return writeLegacyParsedEvent(output, event)
}

func renderWithHarnessEventRenderer(renderer harnessEventRenderer, event harness.Event) error {
	switch event.Type {
	case harness.EventAssistantText, harness.EventResult:
		return renderer.Assistant(event.Text)
	case harness.EventToolUse:
		return renderer.Tool(event.ToolName, event.ArgumentGist)
	default:
		return nil
	}
}

func writeLegacyParsedEvent(output io.Writer, event harness.Event) error {
	switch event.Type {
	case harness.EventAssistantText:
		if _, err := io.WriteString(output, event.Text); err != nil {
			return fmt.Errorf("write assistant output: %w", err)
		}
	case harness.EventToolUse:
		if err := writeLineBreakIfNeeded(output); err != nil {
			return fmt.Errorf("separate tool output: %w", err)
		}
		if _, err := fmt.Fprintf(output, "tool: %s", event.ToolName); err != nil {
			return fmt.Errorf("write tool output: %w", err)
		}
		if event.ArgumentGist != "" {
			if _, err := fmt.Fprintf(output, " — %s", event.ArgumentGist); err != nil {
				return fmt.Errorf("write tool output: %w", err)
			}
		}
		if _, err := io.WriteString(output, "\n"); err != nil {
			return fmt.Errorf("write tool output: %w", err)
		}
	}
	return nil
}

func outputAtLineStart(output io.Writer) bool {
	if lineWriter, ok := output.(interface{ AtLineStart() bool }); ok {
		return lineWriter.AtLineStart()
	}
	if byteWriter, ok := output.(interface{ Bytes() []byte }); ok {
		return bytesAtLineStart(byteWriter.Bytes())
	}
	if stringWriter, ok := output.(interface{ String() string }); ok {
		return bytesAtLineStart([]byte(stringWriter.String()))
	}
	return true
}

func writeLineBreakIfNeeded(output io.Writer) error {
	if outputAtLineStart(output) {
		return nil
	}
	_, err := io.WriteString(output, "\n")
	return err
}

func bytesAtLineStart(value []byte) bool {
	if len(value) == 0 {
		return true
	}
	lastByte := value[len(value)-1]
	return lastByte == '\n' || lastByte == '\r'
}

func formatVerdict(reviewVerdict verdict.Verdict) string {
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

func formatRemoteReviewComment(reviewVerdict verdict.Verdict) string {
	return fmt.Sprintf("## Review verdict\n\n```text\n%s```\n", formatVerdict(reviewVerdict))
}

func writeLocalReviewLog(originRoot, ticketRef, branchPoint string, timestamp time.Time, reviewVerdict verdict.Verdict) (string, error) {
	projectName := filepath.Base(filepath.Clean(originRoot))
	if projectName == "." || projectName == string(filepath.Separator) || projectName == "" {
		projectName = "project"
	}
	reviewsDir := filepath.Join(originRoot, ".scratch", projectName, "reviews")
	if err := os.MkdirAll(reviewsDir, 0o755); err != nil {
		return "", fmt.Errorf("create local review log directory: %w", err)
	}
	filename := timestamp.Format("20060102T150405.000000000Z") + "-working-tree.md"
	path := filepath.Join(reviewsDir, filename)
	ticketLine := ""
	title := "working tree"
	if ticketRef != "" {
		ticketLine = fmt.Sprintf("Ticket: %s\n", ticketRef)
		title = ticketRef
	}
	contents := fmt.Sprintf("# Review: %s\n\n%sReviewed ref: %s\nTimestamp: %s\n\n## Verdict\n\n%s", title, ticketLine, branchPoint, timestamp.Format(time.RFC3339Nano), formatVerdict(reviewVerdict))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return "", fmt.Errorf("write local review log: %w", err)
	}
	return path, nil
}

func computeReviewDiff(ctx context.Context, git GitRunner, branchPoint string) (string, error) {
	trackedDiff, err := git.Run(ctx, "diff", branchPoint)
	if err != nil {
		return "", fmt.Errorf("compute review diff against %s: %w", branchPoint, err)
	}
	untrackedFiles, err := listUntrackedFiles(ctx, git)
	if err != nil {
		return "", err
	}
	untrackedDiff, err := renderUntrackedDiffs(ctx, git, untrackedFiles)
	if err != nil {
		return "", err
	}
	var diff strings.Builder
	diff.WriteString(trackedDiff)
	if trackedDiff != "" && untrackedDiff != "" && trackedDiff[len(trackedDiff)-1] != '\n' {
		diff.WriteByte('\n')
	}
	diff.WriteString(untrackedDiff)
	completeDiff := diff.String()
	if strings.TrimSpace(completeDiff) == "" {
		return "", fmt.Errorf("pre-computed diff against %s is empty", branchPoint)
	}
	return completeDiff, nil
}

func listUntrackedFiles(ctx context.Context, git GitRunner) ([]string, error) {
	output, err := git.Run(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list untracked files: %w", err)
	}
	files := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(files) == 1 && files[0] == "" {
		return nil, nil
	}
	sort.Strings(files)
	return files, nil
}

func renderUntrackedDiffs(ctx context.Context, git GitRunner, files []string) (string, error) {
	var rendered strings.Builder
	endsWithNewline := true
	for _, path := range files {
		diff, err := git.Run(ctx, "diff", "--no-index", "--", "/dev/null", path)
		if err != nil && !isDiffFoundExit(err) {
			return "", fmt.Errorf("render untracked file %q: %w", path, err)
		}
		if rendered.Len() > 0 && !endsWithNewline {
			rendered.WriteByte('\n')
		}
		rendered.WriteString(diff)
		if diff != "" {
			endsWithNewline = diff[len(diff)-1] == '\n'
		}
	}
	return rendered.String(), nil
}

func isDiffFoundExit(err error) bool {
	const gitDiffFoundExitCode = 1

	var exitError interface{ ExitCode() int }
	return errors.As(err, &exitError) && exitError.ExitCode() == gitDiffFoundExitCode
}

func reviewTranscriptSavedError(err error, dir string) error {
	return fmt.Errorf("%w; full review transcript saved in run artifacts directory %s", err, dir)
}
