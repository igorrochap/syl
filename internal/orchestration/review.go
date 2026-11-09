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
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
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
	OpenRun              func(RunSpec) (RunRecorder, error)
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
	runState    *runStateTracker
}

type standaloneReviewRun struct {
	review    ReviewExecution
	startedAt time.Time
	endedAt   time.Time
}

// RunReview runs one standalone reviewer Role against the current changes.
func RunReview(ctx context.Context, options ReviewOptions) (returnErr error) {
	if options.Raw && options.Verbose {
		return errors.New("review: --raw and --verbose are mutually exclusive")
	}
	if options.Adapter == nil {
		return fmt.Errorf("review harness %q is not configured", options.ProjectConfig.Roles.Review.Harness)
	}
	options.Output = ensureLineTrackingWriter(options.Output)
	preparation, err := runReviewPreparation(ctx, options)
	if err != nil {
		if preparation.runState != nil {
			preparation.runState.finishForError(ctx, err)
		}
		return err
	}
	defer func() {
		if returnErr != nil {
			preparation.runState.finishForError(ctx, returnErr)
		}
	}()
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

func runReviewPreparation(
	ctx context.Context,
	options ReviewOptions,
) (reviewPreparation, error) {
	return prepareReviewWithOpener(ctx, options, options.OpenRun)
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
		preparation.runState.finish(runrecord.Exhausted)
		return ErrReviewNeedsRevision
	}
	preparation.runState.finish(runrecord.Approved)
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
	execution := run.review
	var unparseable *UnparseableVerdictError
	if errors.As(err, &unparseable) {
		execution = unparseable.Execution
	}
	recordStandaloneReviewUsage(
		options,
		preparation.recorder,
		execution,
		run.startedAt,
		run.endedAt,
	)
	if artifactErr := recordFailedReviewTurn(preparation.recorder, 0, execution, err); artifactErr != nil {
		return fmt.Errorf("%w; save review run artifacts: %v", err, artifactErr)
	}
	if errors.As(err, &unparseable) {
		return reviewTranscriptSavedError(err, recorderArtifactDir(preparation.recorder))
	}
	return err
}

func runStandaloneReview(ctx context.Context, options ReviewOptions, preparation reviewPreparation) (standaloneReviewRun, error) {
	prompt := composeReviewPrompt(options.TicketRef, options.Ticket, preparation.branchPoint, preparation.diffPath, options.Context)
	notifier := options.Notifier
	if !options.ProjectConfig.Notifications.Enabled {
		notifier = nil
	}
	notifier = withNotificationContext(notifier, options.OriginRoot, options.Git)
	questions := NewQuestionHandler(options.Input, options.Output, options.TicketRef, notifier, preparation.runState)
	preparation.runState.setIteration(1)
	preparation.runState.setActivity(runrecord.Reviewing)
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
		Completion: harness.CompletionReviewVerdict,
		Model:      options.ProjectConfig.Roles.Review.Model, Effort: options.ProjectConfig.Roles.Review.Effort,
		Prompt: prompt, MCP: options.ProjectConfig.Roles.Review.MCP,
		SandboxMode: options.ProjectConfig.Roles.Review.SandboxMode,
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
	return prepareReviewWithOpener(ctx, ReviewOptions{
		OriginRoot: originRoot,
		WorkRoot:   originRoot,
		OpenRun:    NewDiskRunOpener(originRoot, sylhome.Dir{}, nil),
		TicketRef:  ticketRef,
		Git:        git,
	}, NewDiskRunOpener(originRoot, sylhome.Dir{}, nil))
}

func prepareReviewWithOpener(
	ctx context.Context,
	options ReviewOptions,
	opener func(RunSpec) (RunRecorder, error),
) (reviewPreparation, error) {
	if options.Git == nil {
		return reviewPreparation{}, errors.New("review: git runner is not configured")
	}
	if opener == nil {
		return reviewPreparation{}, errors.New("review: Run opener is not configured")
	}
	branchPoint, err := options.Git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return reviewPreparation{}, fmt.Errorf("review: record branch point: %w", err)
	}
	branchPoint = strings.TrimSpace(branchPoint)
	if branchPoint == "" {
		return reviewPreparation{}, errors.New("review: record branch point: git returned an empty ref")
	}
	diff, err := computeReviewDiff(ctx, options.Git, branchPoint)
	if err != nil {
		return reviewPreparation{}, fmt.Errorf("review: %w", err)
	}
	recorder, err := opener(RunSpec{
		Kind:            runrecord.Review,
		TicketRef:       options.TicketRef,
		MaxIterations:   1,
		BranchPoint:     branchPoint,
		WorkRoot:        options.WorkRoot,
		ReviewerHarness: string(options.ProjectConfig.Roles.Review.Harness),
		ReviewContext:   options.Context,
	})
	if err != nil {
		return reviewPreparation{}, err
	}
	if recorder == nil {
		return reviewPreparation{}, errors.New("review: Run opener returned a nil recorder")
	}
	runState := newRunStateTracker(recorder, newRunState(RunSpec{
		Kind:          runrecord.Review,
		TicketRef:     options.TicketRef,
		MaxIterations: 1,
	}))
	diffPath, err := recorder.RecordReviewDiff(0, diff)
	if err != nil {
		return reviewPreparation{branchPoint: branchPoint, recorder: recorder, runState: runState}, fmt.Errorf("review: %w", err)
	}
	return reviewPreparation{branchPoint: branchPoint, diffPath: diffPath, recorder: recorder, runState: runState}, nil
}

func runReview(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	return RunReviewExecutionWithProgress(ctx, adapter, request, output, mode, questions)
}

func recordStandaloneReviewArtifacts(recorder RunRecorder, review ReviewExecution) error {
	if err := recorder.RecordReviewOutput(0, review); err != nil {
		return err
	}
	return recorder.RecordSessions(0, "review", review.SessionIDs)
}

func RunReviewExecution(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	return runReviewExecution(ctx, adapter, request, output, mode, questions, nil)
}

func RunReviewExecutionWithProgress(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	visibleOutput := newLiveHarnessOutput(output, mode, "review")
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
	freshRequest     harness.Request
	output           io.Writer
	mode             HarnessOutputMode
	questions        *QuestionHandler
	turnOutput       reviewTurnOutput
	initialSessionID string
	disableRecovery  bool
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
		request:      request,
		freshRequest: request,
		output:       output,
		mode:         mode,
		questions:    questions,
		turnOutput:   turnOutput,
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

	visibleOutput := newLiveHarnessOutput(options.output, options.mode, "review")
	progress := newReviewProgressWriter(visibleOutput)
	resumeRequest := options.request
	resumeRequest.Prompt = options.resumePrompt
	review, err := runReviewExecutionFrom(ctx, adapter, reviewExecutionOptions{
		request:          resumeRequest,
		freshRequest:     options.request,
		output:           progress,
		mode:             options.mode,
		questions:        options.questions,
		turnOutput:       progress,
		initialSessionID: sessionID,
		start: func(runContext context.Context) (harness.Stream, error) {
			stream, resumeErr := adapter.Resume(runContext, sessionID, resumeRequest)
			if resumeErr != nil {
				return nil, &reviewResumeStartError{cause: resumeErr}
			}
			return stream, nil
		},
	})
	if err == nil {
		return review, nil
	}

	var resumeErr *reviewResumeError
	if !errors.As(err, &resumeErr) {
		return review, err
	}
	if err := progress.EndTurn(); err != nil {
		return resumeErr.execution, fmt.Errorf("flush failed resumed review output: %w", err)
	}
	fresh, freshErr := runReviewExecutionFrom(ctx, adapter, reviewExecutionOptions{
		request:      options.request,
		freshRequest: options.request,
		output:       progress,
		mode:         options.mode,
		questions:    options.questions,
		turnOutput:   progress,
		start: func(runContext context.Context) (harness.Stream, error) {
			return adapter.Run(runContext, options.request)
		},
	})
	if freshErr != nil {
		return appendReviewExecutions(resumeErr.execution, fresh), freshErr
	}
	return fresh, nil
}

type reviewResumeError struct {
	cause     error
	execution ReviewExecution
}

func (e *reviewResumeError) Error() string {
	return fmt.Sprintf("resume reviewer session: %v", e.cause)
}

func (e *reviewResumeError) Unwrap() error { return e.cause }

type reviewResumeStartError struct {
	cause error
}

func (e *reviewResumeStartError) Error() string { return e.cause.Error() }

func (e *reviewResumeStartError) Unwrap() error { return e.cause }

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
		return handleReviewConversationError(ctx, adapter, options, first, &feed, artifactOutput, err)
	}
	if err := endReviewTurn(options.turnOutput); err != nil {
		return ReviewExecution{Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}, err
	}
	return finishReviewExecution(ctx, adapter, options, first, &feed, artifactOutput)
}

func handleReviewConversationError(
	ctx context.Context,
	adapter harness.Adapter,
	options reviewExecutionOptions,
	transcript harnessTranscript,
	feed *bytes.Buffer,
	artifact io.Writer,
	conversationErr error,
) (ReviewExecution, error) {
	execution := ReviewExecution{
		Transcript: transcript.Transcript,
		Feed:       feed.String(),
		SessionIDs: transcript.SessionIDs,
	}
	var resumeStartErr *reviewResumeStartError
	if errors.As(conversationErr, &resumeStartErr) {
		return execution, &reviewResumeError{cause: resumeStartErr.cause, execution: execution}
	}
	var stalled *harness.SessionStalledError
	if errors.As(conversationErr, &stalled) {
		if options.disableRecovery {
			return execution, fmt.Errorf("run review harness: %w", conversationErr)
		}
		execution.SessionIDs = appendSessionID(execution.SessionIDs, stalled.SessionID)
		if turnErr := endReviewTurn(options.turnOutput); turnErr != nil {
			return execution, errors.Join(fmt.Errorf("run review harness: %w", conversationErr), turnErr)
		}
		return recoverStalledReview(ctx, adapter, options, execution, stalled, feed, artifact)
	}
	reviewErr := fmt.Errorf("run review harness: %w", conversationErr)
	if options.initialSessionID == "" {
		return execution, reviewErr
	}
	return execution, &reviewResumeError{cause: reviewErr, execution: execution}
}

func finishReviewExecution(
	ctx context.Context,
	adapter harness.Adapter,
	options reviewExecutionOptions,
	first harnessTranscript,
	feed *bytes.Buffer,
	artifact io.Writer,
) (ReviewExecution, error) {
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
		artifact:  artifact,
		mode:      options.mode,
		questions: options.questions,
		role:      "review",
		sessionID: first.SessionIDs[len(first.SessionIDs)-1],
	}
	retryOptions.request.Prompt = "emit the verdict block"
	retry, err := retryReviewDetails(ctx, adapter, retryOptions)
	if err != nil {
		execution := ReviewExecution{
			Transcript: appendReviewTranscript(first.Transcript, retry.Transcript),
			Feed:       feed.String(),
			SessionIDs: append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...),
		}
		var stalled *harness.SessionStalledError
		if !errors.As(err, &stalled) || options.disableRecovery {
			return execution, err
		}
		execution.SessionIDs = appendSessionID(execution.SessionIDs, stalled.SessionID)
		if turnErr := endReviewTurn(options.turnOutput); turnErr != nil {
			return execution, errors.Join(err, turnErr)
		}
		return recoverStalledReview(ctx, adapter, options, execution, stalled, feed, artifact)
	}
	if err := endReviewTurn(options.turnOutput); err != nil {
		return ReviewExecution{
			Transcript: appendReviewTranscript(first.Transcript, retry.Transcript),
			Feed:       feed.String(),
			SessionIDs: append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...),
		}, err
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

func recoverStalledReview(
	ctx context.Context,
	adapter harness.Adapter,
	options reviewExecutionOptions,
	first ReviewExecution,
	stalled *harness.SessionStalledError,
	feed *bytes.Buffer,
	artifact io.Writer,
) (ReviewExecution, error) {
	sessionID, ok := normalizeSessionID(stalled.SessionID)
	if !ok {
		sessionID = lastUsableSessionID(first.SessionIDs)
	}
	if sessionID == "" {
		return first, fmt.Errorf("review: stalled session has no session id: %w", stalled)
	}
	request := options.request
	request.Prompt = stalledReviewRecoveryPrompt(stalled.Reports, stalled.Limit)
	retryOptions := conversationOptions{
		request:   request,
		output:    options.output,
		artifact:  artifact,
		mode:      options.mode,
		questions: options.questions,
		role:      "review",
		sessionID: sessionID,
	}
	retry, retryErr := retryReviewDetails(ctx, adapter, retryOptions)
	turnErr := endReviewTurn(options.turnOutput)
	if retryErr != nil {
		combined := appendStalledReviewTurn(first, retry, feed.String())
		if turnErr != nil {
			return combined, errors.Join(retryErr, turnErr)
		}
		var resumeStartErr *reviewResumeStartError
		if errors.As(retryErr, &resumeStartErr) {
			return rerunStalledReviewInFreshSession(ctx, adapter, options, first)
		}
		return combined, retryErr
	}
	if turnErr != nil {
		return appendStalledReviewTurn(first, retry, feed.String()), turnErr
	}

	transcript := appendReviewTranscript(first.Transcript, retry.Transcript)
	sessions := append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...)
	reviewVerdict, parseErr := verdict.Parse(transcript)
	if parseErr != nil {
		execution := ReviewExecution{Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}
		return execution, &UnparseableVerdictError{Execution: execution, Cause: parseErr}
	}
	return ReviewExecution{Verdict: reviewVerdict, Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}, nil
}

func rerunStalledReviewInFreshSession(
	ctx context.Context,
	adapter harness.Adapter,
	options reviewExecutionOptions,
	first ReviewExecution,
) (ReviewExecution, error) {
	request := options.freshRequest
	if strings.TrimSpace(request.Prompt) == "" {
		request = options.request
	}
	fresh, err := runReviewExecutionFrom(ctx, adapter, reviewExecutionOptions{
		request:         request,
		freshRequest:    request,
		output:          options.output,
		mode:            options.mode,
		questions:       options.questions,
		turnOutput:      options.turnOutput,
		disableRecovery: true,
		start: func(runContext context.Context) (harness.Stream, error) {
			return adapter.Run(runContext, request)
		},
	})
	combined := appendReviewExecutions(first, fresh)
	combined.Verdict = fresh.Verdict
	return combined, err
}

func appendStalledReviewTurn(first ReviewExecution, retry harnessTranscript, feed string) ReviewExecution {
	return ReviewExecution{
		Transcript: appendReviewTranscript(first.Transcript, retry.Transcript),
		Feed:       feed,
		SessionIDs: append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...),
	}
}

func stalledReviewRecoveryPrompt(reports []harness.SubagentReport, limit harness.SessionStallLimit) string {
	var prompt strings.Builder
	if limit == harness.NoProgressLimit {
		prompt.WriteString("The reviewer session exceeded its no-progress limit. ")
	} else {
		prompt.WriteString("The reviewer session stalled after its subagents finished. ")
	}
	prompt.WriteString("Continue from the existing session transcript, merge the finished subagent reports when available, and end with the mandatory verdict block from the code-review skill.\n")
	if len(reports) == 0 {
		return prompt.String()
	}
	useReportPaths := false
	var totalTextBytes int
	for _, report := range reports {
		totalTextBytes += len(report.Text)
		if report.TooLarge || report.Text == "" {
			useReportPaths = true
		}
	}
	if totalTextBytes > harness.StalledSessionReportTextLimit {
		useReportPaths = true
	}
	if useReportPaths {
		prompt.WriteString("Read and merge the finished subagent reports at these paths:\n")
		for _, report := range reports {
			prompt.WriteString("- ")
			prompt.WriteString(report.Path)
			prompt.WriteByte('\n')
		}
		return prompt.String()
	}

	prompt.WriteString("Finished subagent reports:\n")
	for index, report := range reports {
		fmt.Fprintf(&prompt, "\n--- Report %d (%s) ---\n%s\n", index+1, report.Path, report.Text)
	}
	prompt.WriteString("Merge the reports into the review.")
	return prompt.String()
}

func appendReviewExecutions(first, next ReviewExecution) ReviewExecution {
	return ReviewExecution{
		Transcript: appendReviewTranscript(first.Transcript, next.Transcript),
		Feed:       appendReviewTranscript(first.Feed, next.Feed),
		SessionIDs: append(append([]string(nil), first.SessionIDs...), next.SessionIDs...),
	}
}

func recordFailedReviewTurn(recorder RunRecorder, iteration int, execution ReviewExecution, failure error) error {
	if err := recorder.RecordReviewOutput(iteration, execution); err != nil {
		return err
	}
	if err := recorder.RecordFailure(iteration, "review", failure.Error()); err != nil {
		return err
	}
	return recorder.RecordSessions(iteration, "review", execution.SessionIDs)
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
		stream, resumeErr := adapter.Resume(resumeContext, options.sessionID, options.request)
		if resumeErr != nil {
			return nil, &reviewResumeStartError{cause: resumeErr}
		}
		return stream, nil
	}, options)
	if err != nil {
		return transcript, fmt.Errorf("re-ask reviewer for verdict: %w", err)
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
	return runrecord.FormatVerdict(reviewVerdict)
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
