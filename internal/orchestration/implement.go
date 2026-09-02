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
		git:               run.setup.git,
		workRoot:          options.WorkRoot,
		implementer:       options.Implementer,
		reviewer:          options.Reviewer,
		projectConfig:     projectConfig,
		ticket:            ticket,
		branchPoint:       run.setup.branchPoint,
		reviewDiffRoot:    run.setup.worktreePath,
		recorder:          run.recorder,
		questions:         run.questions,
		output:            options.Output,
		additionalContext: options.Context,
		reviewContext:     options.ReviewContext,
		verbose:           options.Verbose,
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
		options.Ticket.Number,
		setup.branch,
		setup.branchPoint,
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
	if _, err := io.WriteString(options.Output, formatImplementSummary(summary)); err != nil {
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
	git               GitRunner
	workRoot          string
	implementer       harness.Adapter
	reviewer          harness.Adapter
	projectConfig     config.Config
	ticket            tracker.Ticket
	branchPoint       string
	reviewDiffRoot    string
	recorder          RunRecorder
	questions         *QuestionHandler
	output            io.Writer
	additionalContext string
	reviewContext     string
	verbose           bool
}

type implementReviewParams struct {
	iteration               int
	blocking                []verdict.Finding
	previousReviewerSession string
	diffPath                string
	mode                    HarnessOutputMode
}

func runImplementIterations(ctx context.Context, params implementIterationsParams) (int, verdict.Verdict, []verdict.Finding, error) {
	var blocking []verdict.Finding
	var final verdict.Verdict
	var previousReviewerSession string
	iterations := 0
	for iteration := 1; iteration <= params.projectConfig.Loop.MaxIterations; iteration++ {
		iterations = iteration
		mode := QuietHarnessOutput
		if params.verbose {
			mode = ParsedHarnessOutput
		}
		if err := runImplementTurn(ctx, params, iteration, blocking, mode); err != nil {
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
		if _, err := io.WriteString(params.output, formatVerdict(final)); err != nil {
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
	if _, err := fmt.Fprintf(params.output, "iteration %d/%d — reviewing\n", reviewParams.iteration, params.projectConfig.Loop.MaxIterations); err != nil {
		return ReviewExecution{}, fmt.Errorf("write review progress: %w", err)
	}
	reviewOptions := reviewResumeOptions{
		sessionID: reviewParams.previousReviewerSession,
		request:   reviewRequest,
		output:    newRoleLabelWriter(params.output, "review", ansiColorReview),
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
	params.recorder.RecordSessions(reviewParams.iteration, "review", reviewResult.SessionIDs)
	return reviewResult, nil
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
	if params.reviewDiffRoot == "" {
		return diffPath, nil
	}
	return recordWorktreeReviewDiff(params.reviewDiffRoot, params.recorder.Dir(), iteration, diff)
}

func runImplementTurn(ctx context.Context, params implementIterationsParams, iteration int, blocking []verdict.Finding, mode HarnessOutputMode) error {
	activity := "implementing"
	if iteration > 1 {
		activity = fmt.Sprintf("revising %d blocking finding(s)", len(blocking))
	}
	if _, err := fmt.Fprintf(params.output, "iteration %d/%d — %s\n", iteration, params.projectConfig.Loop.MaxIterations, activity); err != nil {
		return fmt.Errorf("write implement progress: %w", err)
	}

	implementRequest := harness.Request{
		Model:  params.projectConfig.Roles.Implement.Model,
		Effort: params.projectConfig.Roles.Implement.Effort,
		Prompt: composeImplementPrompt(params.ticket, blocking, iteration, params.additionalContext),
		MCP:    params.projectConfig.Roles.Implement.MCP,
	}
	implementStartedAt := time.Now().UTC()
	implementResult, err := runImplementRole(
		ctx,
		params.implementer,
		implementRequest,
		newRoleLabelWriter(params.output, "implement", ansiColorImplement),
		mode,
		params.questions,
	)
	implementEndedAt := time.Now().UTC()
	if err != nil {
		return err
	}
	recordRoleUsage(params.recorder, usage.CollectInvocation(usage.Invocation{
		Iteration:  iteration,
		Role:       "implement",
		Harness:    string(params.projectConfig.Roles.Implement.Harness),
		Model:      params.projectConfig.Roles.Implement.Model,
		SessionIDs: implementResult.SessionIDs,
		StartedAt:  implementStartedAt,
		EndedAt:    implementEndedAt,
	}, params.workRoot, ""))
	if err := params.recorder.RecordImplementTurn(
		iteration,
		implementResult.Feed,
		implementResult.Transcript,
	); err != nil {
		return err
	}
	params.recorder.RecordSessions(iteration, "implement", implementResult.SessionIDs)
	return ensureHeadUnchanged(ctx, params.git, params.branchPoint)
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

func runImplementRole(
	ctx context.Context,
	adapter harness.Adapter,
	request harness.Request,
	output io.Writer,
	mode HarnessOutputMode,
	questions *QuestionHandler,
) (implementExecution, error) {
	var feed bytes.Buffer
	result, err := runHarnessConversation(ctx, adapter, func(runContext context.Context) (harness.Stream, error) {
		return adapter.Run(runContext, request)
	}, conversationOptions{
		request:   request,
		output:    output,
		artifact:  &feed,
		mode:      mode,
		questions: questions,
		role:      "implement",
	})
	if err != nil {
		return implementExecution{}, fmt.Errorf("run implement harness: %w", err)
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
	fmt.Fprintf(&builder, "Diff stat:\n%s\n", strings.TrimSpace(summary.diffStat))
	return builder.String()
}

func recordWorktreeReviewDiff(worktreeRoot, runDir string, iteration int, diff string) (string, error) {
	root, err := filepath.Abs(worktreeRoot)
	if err != nil {
		return "", fmt.Errorf("resolve worktree review diff root: %w", err)
	}
	runName := filepath.Base(filepath.Clean(runDir))
	if runName == "." || runName == string(filepath.Separator) || runName == "" {
		return "", errors.New("record worktree review diff: run directory is required")
	}
	path := filepath.Join(root, ".syl", "runs", runName, artifactFilename(reviewDiffArtifact, iteration))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create worktree review diff directory: %w", err)
	}
	if err := writeArtifact(path, diff); err != nil {
		return "", fmt.Errorf("write worktree review diff: %w", err)
	}
	return path, nil
}
