package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/ui"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

type implementSetup struct {
	git          GitRunner
	branch       string
	branchPoint  string
	worktreePath string
}

type implementSummary struct {
	iterations   int
	final        verdict.Verdict
	nits         []verdict.Finding
	diffStat     string
	worktreePath string
}

type ImplementOptions struct {
	OriginRoot           string
	WorkRoot             string
	ProjectConfig        config.Config
	IssueTracker         tracker.Tracker
	Ticket               tracker.Ticket
	Implementer          harness.Adapter
	Reviewer             harness.Adapter
	Git                  GitRunner
	OriginGit            GitRunner
	Notifier             Notifier
	Input                io.Reader
	Output               io.Writer
	Context              string
	ReviewContext        string
	Verbose              bool
	ProvisionedWorktree  *Worktree
	IdentificationBanner func(artifactDir string) error
}

type implementRunState struct {
	setup     implementSetup
	notifier  Notifier
	questions *QuestionHandler
	recorder  *diskRunRecorder
}

func RunImplement(ctx context.Context, options ImplementOptions) (returnErr error) {
	projectConfig := options.ProjectConfig
	ticket := options.Ticket
	originGit := options.OriginGit
	if originGit == nil {
		originGit = options.Git
	}
	loopStarted := false
	if options.ProvisionedWorktree != nil && originGit != nil {
		worktree := *options.ProvisionedWorktree
		defer func() {
			if loopStarted {
				return
			}
			if err := RemoveWorktree(ctx, originGit, worktree); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove worktree after setup failure: %w", err))
			}
		}()
	}
	run, err := prepareImplementRun(ctx, options, originGit)
	if err != nil {
		return err
	}
	loopStarted = true
	iterations, final, nits, err := runImplementIterations(ctx, implementIterationsParams{
		git:                  run.setup.git,
		workRoot:             options.WorkRoot,
		implementer:          options.Implementer,
		reviewer:             options.Reviewer,
		projectConfig:        projectConfig,
		ticket:               ticket,
		branchPoint:          run.setup.branchPoint,
		worktreeArtifactRoot: run.setup.worktreePath,
		recorder:             run.recorder,
		questions:            run.questions,
		output:               options.Output,
		additionalContext:    options.Context,
		reviewContext:        options.ReviewContext,
		verbose:              options.Verbose,
	})
	if err != nil {
		return err
	}

	diffStat, err := run.setup.git.Run(ctx, "diff", "--stat", run.setup.branchPoint)
	if err != nil {
		diffStat = fmt.Sprintf("unavailable: %v", err)
	}
	summary := implementSummary{
		iterations:   iterations,
		final:        final,
		nits:         nits,
		diffStat:     diffStat,
		worktreePath: run.setup.worktreePath,
	}
	return completeImplementRun(ctx, options, run, summary)
}

func prepareImplementRun(ctx context.Context, options ImplementOptions, originGit GitRunner) (implementRunState, error) {
	if err := validateImplementOptions(options); err != nil {
		return implementRunState{}, err
	}
	var (
		setup implementSetup
		err   error
	)
	if options.ProvisionedWorktree != nil {
		setup, err = prepareProvisionedImplement(ctx, options.Git, options.IssueTracker, options.Ticket, *options.ProvisionedWorktree)
	} else {
		setup, err = prepareImplementWithGit(ctx, options.Git, originGit, options.IssueTracker, options.Ticket)
	}
	if err != nil {
		return implementRunState{}, err
	}
	return initializeImplementRun(options, setup)
}

func validateImplementOptions(options ImplementOptions) error {
	if options.Git == nil {
		return errors.New("implement: git runner is not configured")
	}
	if options.Implementer == nil {
		return fmt.Errorf("implement harness %q is not configured", options.ProjectConfig.Roles.Implement.Harness)
	}
	if options.Reviewer == nil {
		return fmt.Errorf("review harness %q is not configured", options.ProjectConfig.Roles.Review.Harness)
	}
	return nil
}

func initializeImplementRun(options ImplementOptions, setup implementSetup) (implementRunState, error) {
	notifier := options.Notifier
	if !options.ProjectConfig.Notifications.Enabled {
		notifier = nil
	}
	notifier = withNotificationContext(notifier, options.OriginRoot, setup.git)
	questions := NewQuestionHandler(options.Input, options.Output, "#"+strconv.Itoa(options.Ticket.Number), notifier)
	recorder, err := newImplementRunRecorder(
		options.OriginRoot,
		options.WorkRoot,
		options.Ticket.Number,
		setup.branch,
		setup.branchPoint,
		string(options.ProjectConfig.Roles.Implement.Harness),
		string(options.ProjectConfig.Roles.Review.Harness),
		options.Context,
		options.ReviewContext,
	)
	if err != nil {
		return implementRunState{}, err
	}
	if options.IdentificationBanner != nil {
		if err := options.IdentificationBanner(recorder.Dir()); err != nil {
			return implementRunState{}, err
		}
	}
	return implementRunState{setup: setup, notifier: notifier, questions: questions, recorder: recorder}, nil
}

func completeImplementRun(ctx context.Context, options ImplementOptions, run implementRunState, summary implementSummary) error {
	if err := run.recorder.WriteSummary(summary); err != nil {
		return err
	}
	renderer := ui.New(options.Output, ui.DetectCaps(options.Output))
	if err := renderer.RunSummary(ui.RunSummary{
		Iterations: summary.iterations, FinalVerdict: string(summary.final.Status), Summary: summary.final.Summary,
		NitFindings: toUIFindings(summary.nits), WorktreePath: summary.worktreePath, DiffStat: summary.diffStat,
	}); err != nil {
		return fmt.Errorf("write implement summary: %w", err)
	}
	if err := run.recorder.WriteSessions(); err != nil {
		return err
	}
	if run.notifier != nil {
		_ = run.notifier.Notify(ctx, fmt.Sprintf("implement #%d finished: %s", options.Ticket.Number, summary.final.Status))
	}
	if summary.final.Status == verdict.Revise {
		return fmt.Errorf("implement loop reached max iterations (%d) with revise verdict", options.ProjectConfig.Loop.MaxIterations)
	}
	return nil
}

func prepareImplement(ctx context.Context, git GitRunner, issueTracker tracker.Tracker, ticket tracker.Ticket) (implementSetup, error) {
	return prepareImplementWithGit(ctx, git, git, issueTracker, ticket)
}

func prepareImplementWithGit(
	ctx context.Context,
	workGit GitRunner,
	originGit GitRunner,
	issueTracker tracker.Tracker,
	ticket tracker.Ticket,
) (implementSetup, error) {
	status, err := workGit.Run(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return implementSetup{}, fmt.Errorf("check working tree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return implementSetup{}, errors.New("working tree is dirty; commit or stash changes first")
	}

	branchPoint, err := workGit.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return implementSetup{}, fmt.Errorf("record branch point: %w", err)
	}
	branchPoint = strings.TrimSpace(branchPoint)
	if branchPoint == "" {
		return implementSetup{}, errors.New("record branch point: git returned an empty ref")
	}
	branch := resolveBranchName(ticket)
	if _, err := originGit.Run(ctx, "switch", "-c", branch); err != nil {
		return implementSetup{}, fmt.Errorf("create implementation branch %q: %w", branch, err)
	}
	if err := issueTracker.UpdateStatus(ctx, ticket.Number, "doing"); err != nil {
		return implementSetup{}, fmt.Errorf("mark ticket #%d as doing: %w", ticket.Number, err)
	}
	return implementSetup{git: workGit, branch: branch, branchPoint: branchPoint}, nil
}

func prepareProvisionedImplement(
	ctx context.Context,
	workGit GitRunner,
	issueTracker tracker.Tracker,
	ticket tracker.Ticket,
	worktree Worktree,
) (implementSetup, error) {
	if strings.TrimSpace(worktree.Path) == "" {
		return implementSetup{}, errors.New("implement worktree path is required")
	}
	branchPoint, err := workGit.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return implementSetup{}, fmt.Errorf("record worktree branch point: %w", err)
	}
	branchPoint = strings.TrimSpace(branchPoint)
	if branchPoint == "" {
		return implementSetup{}, errors.New("record worktree branch point: git returned an empty ref")
	}
	branch := strings.TrimSpace(worktree.Branch)
	if branch == "" {
		return implementSetup{}, errors.New("implement worktree branch is required")
	}
	if err := issueTracker.UpdateStatus(ctx, ticket.Number, "doing"); err != nil {
		return implementSetup{}, fmt.Errorf("mark ticket #%d as doing: %w", ticket.Number, err)
	}
	return implementSetup{git: workGit, branch: branch, branchPoint: branchPoint, worktreePath: worktree.Path}, nil
}

type implementIterationsParams struct {
	git                  GitRunner
	workRoot             string
	implementer          harness.Adapter
	reviewer             harness.Adapter
	projectConfig        config.Config
	ticket               tracker.Ticket
	branchPoint          string
	worktreeArtifactRoot string
	recorder             RunRecorder
	questions            *QuestionHandler
	output               io.Writer
	additionalContext    string
	reviewContext        string
	verbose              bool
}

type implementReviewParams struct {
	iteration               int
	blocking                []verdict.Finding
	previousReviewerSession string
	diffPath                string
	mode                    HarnessOutputMode
}

type implementTurnParams struct {
	iteration        int
	blocking         []verdict.Finding
	previousSession  string
	handoffPath      string
	rolloverSeedPath string
	mode             HarnessOutputMode
}

func runImplementIterations(ctx context.Context, params implementIterationsParams) (int, verdict.Verdict, []verdict.Finding, error) {
	params.output = ensureLineTrackingWriter(params.output)
	var blocking []verdict.Finding
	var final verdict.Verdict
	var previousImplementerSession string
	var rolloverSeedPath string
	var previousReviewerSession string
	iterations := 0
	for iteration := 1; iteration <= params.projectConfig.Loop.MaxIterations; iteration++ {
		iterations = iteration
		mode := QuietHarnessOutput
		if params.verbose {
			mode = ParsedHarnessOutput
		}
		handoffPath, err := prepareIterationHandoffPath(params, iteration)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		implementResult, err := runImplementTurn(ctx, params, implementTurnParams{
			iteration:        iteration,
			blocking:         blocking,
			previousSession:  previousImplementerSession,
			handoffPath:      handoffPath,
			rolloverSeedPath: rolloverSeedPath,
			mode:             mode,
		})
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		rolloverSeedPath, previousImplementerSession, err = nextImplementerTurn(
			handoffPath,
			implementResult.SessionIDs,
		)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		diffPath, err := prepareIterationReviewDiff(ctx, params, iteration)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}

		reviewResult, err := runImplementReview(ctx, params, implementReviewParams{
			iteration:               iteration,
			blocking:                blocking,
			previousReviewerSession: previousReviewerSession,
			diffPath:                diffPath,
			mode:                    mode,
		})
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		final = reviewResult.Verdict
		renderer := ui.New(params.output, ui.DetectCaps(params.output))
		if err := renderer.ReviewVerdict(ui.ReviewVerdict{
			Status: string(final.Status), Summary: final.Summary, Findings: toUIFindings(final.Findings),
		}); err != nil {
			return 0, verdict.Verdict{}, nil, fmt.Errorf("write review verdict: %w", err)
		}
		if final.Status == verdict.Approve {
			break
		}
		blocking = blockingFindings(final)
		previousReviewerSession = lastUsableSessionID(reviewResult.SessionIDs)
	}
	return iterations, final, nitFindings(final), nil
}

func runImplementReview(ctx context.Context, params implementIterationsParams, reviewParams implementReviewParams) (ReviewExecution, error) {
	reviewRequest := harness.Request{
		Model:  params.projectConfig.Roles.Review.Model,
		Effort: params.projectConfig.Roles.Review.Effort,
		Prompt: composeReviewPrompt("#"+strconv.Itoa(params.ticket.Number), &params.ticket, params.branchPoint, reviewParams.diffPath, params.reviewContext),
		MCP:    params.projectConfig.Roles.Review.MCP,
	}
	renderer := ui.New(params.output, ui.DetectCaps(params.output))
	if err := writeRoleSection(params.output, "Reviewer"); err != nil {
		return ReviewExecution{}, fmt.Errorf("write review role: %w", err)
	}
	if err := renderer.Step(ui.Step{Label: fmt.Sprintf("iteration %d/%d — reviewing", reviewParams.iteration, params.projectConfig.Loop.MaxIterations)}); err != nil {
		return ReviewExecution{}, fmt.Errorf("write review progress: %w", err)
	}
	reviewOptions := reviewResumeOptions{
		sessionID: reviewParams.previousReviewerSession,
		request:   reviewRequest,
		output:    params.output,
		mode:      reviewParams.mode,
		questions: params.questions,
	}
	if reviewParams.previousReviewerSession != "" {
		reviewOptions.resumePrompt = composeReviewResumePrompt(reviewParams.diffPath, reviewParams.blocking, params.reviewContext)
	}
	reviewStartedAt := time.Now().UTC()
	reviewResult, err := runReviewExecutionWithResumeFallback(ctx, params.reviewer, reviewOptions)
	reviewEndedAt := time.Now().UTC()
	if err != nil {
		var unparseable *UnparseableVerdictError
		if errors.As(err, &unparseable) {
			recordReviewUsage(reviewUsageParams{
				recorder:  params.recorder,
				iteration: reviewParams.iteration,
				role:      params.projectConfig.Roles.Review,
				execution: unparseable.Execution,
				workRoot:  params.workRoot,
				startedAt: reviewStartedAt,
				endedAt:   reviewEndedAt,
			})
			if artifactErr := params.recorder.RecordReviewOutput(reviewParams.iteration, unparseable.Execution); artifactErr != nil {
				return ReviewExecution{}, artifactErr
			}
			return ReviewExecution{}, reviewTranscriptSavedError(err, params.recorder.Dir())
		}
		return ReviewExecution{}, err
	}
	recordReviewUsage(reviewUsageParams{
		recorder:  params.recorder,
		iteration: reviewParams.iteration,
		role:      params.projectConfig.Roles.Review,
		execution: reviewResult,
		workRoot:  params.workRoot,
		startedAt: reviewStartedAt,
		endedAt:   reviewEndedAt,
	})
	if err := params.recorder.RecordReviewOutput(reviewParams.iteration, reviewResult); err != nil {
		return ReviewExecution{}, err
	}
	if err := params.recorder.RecordVerdict(reviewParams.iteration, reviewResult.Verdict); err != nil {
		return ReviewExecution{}, err
	}
	if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
		return ReviewExecution{}, err
	}
	return recordReviewSessions(params.recorder, reviewParams.iteration, reviewResult)
}

func recordReviewSessions(recorder RunRecorder, iteration int, review ReviewExecution) (ReviewExecution, error) {
	if err := recorder.RecordSessions(iteration, "review", review.SessionIDs); err != nil {
		return ReviewExecution{}, fmt.Errorf("record review sessions: %w", err)
	}
	return review, nil
}

func prepareIterationReviewDiff(ctx context.Context, params implementIterationsParams, iteration int) (string, error) {
	diff, err := computeReviewDiff(ctx, params.git, params.branchPoint)
	if err != nil {
		return "", err
	}
	diffPath, err := params.recorder.RecordReviewDiff(iteration, diff)
	if err != nil {
		return "", err
	}
	if params.worktreeArtifactRoot == "" {
		return diffPath, nil
	}
	return recordWorktreeReviewDiff(params.worktreeArtifactRoot, params.recorder.Dir(), iteration, diff)
}

func prepareIterationHandoffPath(params implementIterationsParams, iteration int) (string, error) {
	handoffPath := params.recorder.ImplementHandoffPath(iteration)
	if params.worktreeArtifactRoot == "" {
		return handoffPath, nil
	}
	path, err := worktreeRunArtifactPath(params.worktreeArtifactRoot, params.recorder.Dir(), filepath.Base(handoffPath))
	if err != nil {
		return "", fmt.Errorf("prepare worktree handoff path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create worktree handoff directory: %w", err)
	}
	return path, nil
}

func handoffExists(handoffPath string) (bool, error) {
	_, err := os.Stat(handoffPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect implementer handoff %s: %w", handoffPath, err)
}

func nextImplementerTurn(handoffPath string, sessionIDs []string) (string, string, error) {
	rolloverRequired, err := handoffExists(handoffPath)
	if err != nil {
		return "", "", err
	}
	if rolloverRequired {
		return handoffPath, "", nil
	}
	return "", lastUsableSessionID(sessionIDs), nil
}

func runImplementTurn(
	ctx context.Context,
	params implementIterationsParams,
	turn implementTurnParams,
) (implementExecution, error) {
	activity := "implementing"
	if turn.iteration > 1 {
		activity = fmt.Sprintf("revising %d blocking finding(s)", len(turn.blocking))
	}
	renderer := ui.New(params.output, ui.DetectCaps(params.output))
	if err := writeRoleSection(params.output, "Implementer"); err != nil {
		return implementExecution{}, fmt.Errorf("write implement role: %w", err)
	}
	if err := renderer.Step(ui.Step{Label: fmt.Sprintf("iteration %d/%d — %s", turn.iteration, params.projectConfig.Loop.MaxIterations, activity)}); err != nil {
		return implementExecution{}, fmt.Errorf("write implement progress: %w", err)
	}

	implementRequest := harness.Request{
		Model:  params.projectConfig.Roles.Implement.Model,
		Effort: params.projectConfig.Roles.Implement.Effort,
		Prompt: composeImplementPrompt(
			params.ticket,
			turn.blocking,
			turn.iteration,
			turn.handoffPath,
			turn.rolloverSeedPath,
			params.additionalContext,
		),
		MCP: params.projectConfig.Roles.Implement.MCP,
	}
	implementStartedAt := time.Now().UTC()
	implementResult, err := runImplementRoleWithResumeFallback(
		ctx,
		params.implementer,
		implementRequest,
		turn.previousSession,
		params.output,
		turn.mode,
		params.questions,
	)
	implementEndedAt := time.Now().UTC()
	if err != nil {
		return implementExecution{}, err
	}
	recordRoleUsage(params.recorder, usage.CollectInvocation(usage.Invocation{
		Iteration:  turn.iteration,
		Role:       "implement",
		Harness:    string(params.projectConfig.Roles.Implement.Harness),
		Model:      params.projectConfig.Roles.Implement.Model,
		SessionIDs: implementResult.SessionIDs,
		StartedAt:  implementStartedAt,
		EndedAt:    implementEndedAt,
	}, params.workRoot, ""))
	if err := params.recorder.RecordImplementTurn(
		turn.iteration,
		implementResult.Feed,
		implementResult.Transcript,
	); err != nil {
		return implementExecution{}, err
	}
	if err := params.recorder.RecordSessions(turn.iteration, "implement", implementResult.SessionIDs); err != nil {
		return implementExecution{}, fmt.Errorf("record implement sessions: %w", err)
	}
	if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
		return implementExecution{}, err
	}
	return implementResult, nil
}

// recordRoleUsage persists usage as best-effort metadata. Usage collection or
// persistence failures must not fail the run.
func recordRoleUsage(recorder RunRecorder, entry usage.Entry) {
	usageRecorder, ok := recorder.(UsageRecorder)
	if !ok {
		return
	}
	_ = usageRecorder.RecordUsage(entry)
}

type implementExecution struct {
	Feed       string
	Transcript string
	SessionIDs []string
}

type implementExecutionOptions struct {
	request          harness.Request
	output           io.Writer
	mode             HarnessOutputMode
	questions        *QuestionHandler
	initialSessionID string
	start            harnessStreamStarter
}

func runImplementRole(
	ctx context.Context,
	adapter harness.Adapter,
	request harness.Request,
	output io.Writer,
	mode HarnessOutputMode,
	questions *QuestionHandler,
) (implementExecution, error) {
	return runImplementRoleFrom(ctx, adapter, implementExecutionOptions{
		request:   request,
		output:    output,
		mode:      mode,
		questions: questions,
		start: func(runContext context.Context) (harness.Stream, error) {
			return adapter.Run(runContext, request)
		},
	})
}

func runImplementRoleWithResumeFallback(
	ctx context.Context,
	adapter harness.Adapter,
	request harness.Request,
	sessionID string,
	output io.Writer,
	mode HarnessOutputMode,
	questions *QuestionHandler,
) (implementExecution, error) {
	sessionID, hasSession := normalizeSessionID(sessionID)
	if !hasSession {
		return runImplementRole(ctx, adapter, request, output, mode, questions)
	}

	result, err := runImplementRoleFrom(ctx, adapter, implementExecutionOptions{
		request:          request,
		output:           output,
		mode:             mode,
		questions:        questions,
		initialSessionID: sessionID,
		start: func(runContext context.Context) (harness.Stream, error) {
			stream, resumeErr := adapter.Resume(runContext, sessionID, request)
			if resumeErr != nil {
				return nil, &implementResumeError{cause: resumeErr}
			}
			return stream, nil
		},
	})
	if err == nil {
		return result, nil
	}

	var resumeErr *implementResumeError
	if !errors.As(err, &resumeErr) {
		return implementExecution{}, err
	}
	return runImplementRole(ctx, adapter, request, output, mode, questions)
}

type implementResumeError struct {
	cause error
}

func (e *implementResumeError) Error() string {
	return fmt.Sprintf("resume implementer session: %v", e.cause)
}

func (e *implementResumeError) Unwrap() error { return e.cause }

func runImplementRoleFrom(
	ctx context.Context,
	adapter harness.Adapter,
	options implementExecutionOptions,
) (implementExecution, error) {
	var feed bytes.Buffer
	visibleOutput := options.output
	if options.mode != RawHarnessOutput {
		visibleOutput = newLiveHarnessOutput(options.output, options.mode, "implement")
	}
	artifactOutput := newPlainHarnessOutput(&feed, options.mode)
	result, err := runHarnessConversation(ctx, adapter, options.start, conversationOptions{
		request:   options.request,
		output:    visibleOutput,
		artifact:  artifactOutput,
		mode:      options.mode,
		questions: options.questions,
		role:      "implement",
		sessionID: options.initialSessionID,
	})
	if err != nil {
		implementErr := fmt.Errorf("run implement harness: %w", err)
		if options.initialSessionID != "" {
			return implementExecution{}, &implementResumeError{cause: implementErr}
		}
		return implementExecution{}, implementErr
	}
	return implementExecution{
		Feed:       feed.String(),
		Transcript: result.Transcript,
		SessionIDs: result.SessionIDs,
	}, nil
}

func ensureHeadUnchanged(ctx context.Context, git GitRunner, branchPoint string) error {
	head, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("verify implementation did not commit: %w", err)
	}
	if strings.TrimSpace(head) != branchPoint {
		return fmt.Errorf("implementation changed HEAD; agents must leave changes uncommitted")
	}
	return nil
}

func blockingFindings(review verdict.Verdict) []verdict.Finding {
	findings := make([]verdict.Finding, 0)
	for _, finding := range review.Findings {
		if finding.Kind == verdict.Blocking {
			findings = append(findings, finding)
		}
	}
	return findings
}

func nitFindings(review verdict.Verdict) []verdict.Finding {
	findings := make([]verdict.Finding, 0)
	for _, finding := range review.Findings {
		if finding.Kind == verdict.Nit {
			findings = append(findings, finding)
		}
	}
	return findings
}

func toUIFindings(findings []verdict.Finding) []ui.Finding {
	converted := make([]ui.Finding, 0, len(findings))
	for _, finding := range findings {
		converted = append(converted, ui.Finding{Kind: string(finding.Kind), Location: finding.Location, Issue: finding.Issue})
	}
	return converted
}

func formatImplementSummary(summary implementSummary) string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"Iterations: %d\nFinal verdict: %s\nSummary: %s\nNit findings:\n",
		summary.iterations, summary.final.Status, summary.final.Summary,
	)
	if len(summary.nits) == 0 {
		builder.WriteString("- (none)\n")
	} else {
		for _, finding := range summary.nits {
			fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
		}
	}
	if summary.worktreePath != "" {
		fmt.Fprintf(&builder,
			"Worktree: %s\nRemove worktree: git worktree remove --force %s\n",
			summary.worktreePath, summary.worktreePath,
		)
	}
	fmt.Fprintf(&builder, "Diff stat:\n%s\n", strings.TrimRight(summary.diffStat, " \t\r\n"))
	return builder.String()
}

func recordWorktreeReviewDiff(worktreeRoot, runDir string, iteration int, diff string) (string, error) {
	path, err := worktreeRunArtifactPath(worktreeRoot, runDir, artifactFilename(reviewDiffArtifact, iteration))
	if err != nil {
		return "", fmt.Errorf("record worktree review diff: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create worktree review diff directory: %w", err)
	}
	if err := writeArtifact(path, diff); err != nil {
		return "", fmt.Errorf("write worktree review diff: %w", err)
	}
	return path, nil
}

func worktreeRunArtifactPath(worktreeRoot, runDir, filename string) (string, error) {
	root, err := filepath.Abs(worktreeRoot)
	if err != nil {
		return "", fmt.Errorf("resolve worktree run artifact root: %w", err)
	}
	runName := filepath.Base(filepath.Clean(runDir))
	if runName == "." || runName == string(filepath.Separator) || runName == "" {
		return "", errors.New("worktree run artifact directory is required")
	}
	if filename == "" || filepath.Base(filename) != filename {
		return "", errors.New("worktree run artifact filename is required")
	}
	return filepath.Join(root, ".syl", "runs", runName, filename), nil
}
