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
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/ui"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

type implementSetup struct {
	git          GitRunner
	branch       string
	branchPoint  string
	baseBranch   string
	worktreePath string
}

type implementSummary struct {
	iterations         int
	final              verdict.Verdict
	nits               []verdict.Finding
	diffStat           string
	worktreePath       string
	exhaustionReason   string
	nitBudgetOverrides []runrecord.NitBudgetOverride
	pullRequestURL     string
	publishNotice      string
	publication        runrecord.Publication
}

type ImplementOptions struct {
	OriginRoot           string
	WorkRoot             string
	OpenRun              func(RunSpec) (RunRecorder, error)
	ProjectConfig        config.Config
	IssueTracker         tracker.Tracker
	Ticket               tracker.Ticket
	Implementer          harness.Adapter
	Reviewer             harness.Adapter
	Git                  GitRunner
	OriginGit            GitRunner
	Commitgen            CommitgenRunner
	Notifier             Notifier
	Input                io.Reader
	Output               io.Writer
	Context              string
	ReviewContext        string
	Verbose              bool
	PublishOnApprove     bool
	GateRunner           GateRunner
	ProvisionedWorktree  *Worktree
	IdentificationBanner func(artifactDir string) error
}

type implementRunState struct {
	setup     implementSetup
	notifier  Notifier
	questions *QuestionHandler
	recorder  RunRecorder
	runState  *runStateTracker
}

func RunImplement(ctx context.Context, options ImplementOptions) (returnErr error) {
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
		return finishFailedImplementRun(ctx, options, run, err)
	}
	loopStarted = true
	defer func() {
		if returnErr != nil {
			returnErr = finishFailedImplementRun(ctx, options, run, returnErr)
		}
	}()
	return executeImplementRun(ctx, options, run)
}

func finishFailedImplementRun(ctx context.Context, options ImplementOptions, run implementRunState, err error) error {
	if run.runState == nil {
		return err
	}
	run.runState.finishForError(ctx, err)
	shouldReport := run.runState.state.Status == runrecord.Failed || run.runState.state.Status == runrecord.Cancelled
	if !shouldReport {
		return err
	}

	// Cancellation ends the Run, but the read-only check still needs time to run.
	checkContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	status, checkErr := run.setup.git.Run(checkContext, "status", "--porcelain", "--untracked-files=all")
	if checkErr != nil {
		return fmt.Errorf("%w\n\nCould not check the work root for uncommitted changes (%s)", err, options.WorkRoot)
	}
	if strings.TrimSpace(status) == "" {
		return err
	}
	return fmt.Errorf("%w\n\n%s", err, formatUncommittedWork(options.WorkRoot, status))
}

const uncommittedWorkPathLimit = 20

func formatUncommittedWork(workRoot, status string) string {
	paths := strings.Split(strings.TrimRight(status, "\n"), "\n")
	listed := len(paths)
	if listed > uncommittedWorkPathLimit {
		listed = uncommittedWorkPathLimit
	}
	var report strings.Builder
	fmt.Fprintf(&report, "Uncommitted changes are kept in the work root: %s\n", workRoot)
	for _, path := range paths[:listed] {
		fmt.Fprintf(&report, "  %s\n", path)
	}
	if remaining := len(paths) - listed; remaining > 0 {
		fmt.Fprintf(&report, "  ... and %d more paths\n", remaining)
	}
	return report.String()
}

func executeImplementRun(ctx context.Context, options ImplementOptions, run implementRunState) error {
	iterations, final, nits, err := runImplementIterations(ctx, implementIterationsParams{
		git:                  run.setup.git,
		workRoot:             options.WorkRoot,
		implementer:          options.Implementer,
		reviewer:             options.Reviewer,
		projectConfig:        options.ProjectConfig,
		ticket:               options.Ticket,
		branchPoint:          run.setup.branchPoint,
		worktreeArtifactRoot: run.setup.worktreePath,
		recorder:             run.recorder,
		questions:            run.questions,
		runState:             run.runState,
		output:               options.Output,
		additionalContext:    options.Context,
		reviewContext:        options.ReviewContext,
		verbose:              options.Verbose,
		allowEmptyDiff:       options.PublishOnApprove,
		gateRunner:           options.GateRunner,
	})
	if err != nil {
		return err
	}
	if run.runState.state.ExhaustionReason == "" && final.Status == verdict.Revise &&
		iterations >= options.ProjectConfig.Loop.MaxIterations {
		run.runState.setExhaustionReason(fmt.Sprintf("iteration limit (%d)", options.ProjectConfig.Loop.MaxIterations))
	}

	diffStat, err := run.setup.git.Run(ctx, "diff", "--stat", run.setup.branchPoint)
	if err != nil {
		diffStat = fmt.Sprintf("unavailable: %v", err)
	}
	summary := implementSummary{
		iterations:         iterations,
		final:              final,
		nits:               nits,
		diffStat:           diffStat,
		worktreePath:       run.setup.worktreePath,
		exhaustionReason:   run.runState.state.ExhaustionReason,
		nitBudgetOverrides: run.runState.state.NitBudgetOverrides,
	}
	publicationErr := publishApprovedSummary(ctx, options, run.setup, &summary)
	if err := completeImplementRun(ctx, options, run, summary); err != nil {
		return err
	}
	return publicationErr
}

func publishApprovedSummary(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary *implementSummary,
) error {
	if !options.PublishOnApprove {
		summary.publication.Status = runrecord.PublicationNotRequested
		return nil
	}
	if summary.final.Status != verdict.Approve || summary.exhaustionReason != "" {
		return nil
	}

	var notices []string
	summary.publication, notices = publishApprovedImplement(ctx, options, setup, *summary)
	summary.publishNotice = strings.Join(notices, "\n")
	switch summary.publication.Status {
	case runrecord.PublicationPublished:
		summary.pullRequestURL = summary.publication.URL
	case runrecord.PublicationSkipped:
		summary.publishNotice = "No changes to publish; skipped commit, push, and pull request."
	case runrecord.PublicationFailed:
		commands := publicationCommands(summary.publication.Step, options, setup, *summary)
		failureNotice := formatPublicationFailure(summary.publication, commands)
		if summary.publishNotice != "" {
			summary.publishNotice += "\n"
		}
		summary.publishNotice += failureNotice
		return errors.New(failureNotice)
	}
	return nil
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
		setup, err = prepareImplementWithPullRequestBase(
			ctx, options.Git, originGit, options.IssueTracker, options.Ticket, options.PublishOnApprove,
		)
	}
	if err != nil {
		return implementRunState{}, err
	}
	return initializeImplementRun(options, setup)
}

func validateImplementOptions(options ImplementOptions) error {
	if options.OpenRun == nil {
		return errors.New("implement: Run opener is not configured")
	}
	if options.Git == nil {
		return errors.New("implement: git runner is not configured")
	}
	if options.Implementer == nil {
		return fmt.Errorf("implement harness %q is not configured", options.ProjectConfig.Roles.Implement.Harness)
	}
	if options.Reviewer == nil {
		return fmt.Errorf("review harness %q is not configured", options.ProjectConfig.Roles.Review.Harness)
	}
	if options.PublishOnApprove {
		if err := tracker.RequirePullRequestCreator(options.IssueTracker); err != nil {
			return err
		}
	}
	return nil
}

func initializeImplementRun(options ImplementOptions, setup implementSetup) (implementRunState, error) {
	spec := RunSpec{
		Kind:               runrecord.Implement,
		TicketRef:          "#" + strconv.Itoa(options.Ticket.Number),
		MaxIterations:      options.ProjectConfig.Loop.MaxIterations,
		Branch:             setup.branch,
		BranchPoint:        setup.branchPoint,
		WorkRoot:           options.WorkRoot,
		ImplementerHarness: string(options.ProjectConfig.Roles.Implement.Harness),
		ReviewerHarness:    string(options.ProjectConfig.Roles.Review.Harness),
		ImplementContext:   options.Context,
		ReviewContext:      options.ReviewContext,
		PublishOnApprove:   options.PublishOnApprove,
	}
	recorder, err := options.OpenRun(spec)
	if err != nil {
		return implementRunState{}, err
	}
	if recorder == nil {
		return implementRunState{}, errors.New("implement: Run opener returned a nil recorder")
	}
	runState := newRunStateTracker(recorder, newRunState(spec))
	notifier := options.Notifier
	if !options.ProjectConfig.Notifications.Enabled {
		notifier = nil
	}
	notifier = withNotificationContext(notifier, options.OriginRoot, setup.git)
	questions := NewQuestionHandler(options.Input, options.Output, "#"+strconv.Itoa(options.Ticket.Number), notifier, runState)
	run := implementRunState{setup: setup, notifier: notifier, questions: questions, recorder: recorder, runState: runState}
	if options.IdentificationBanner != nil {
		artifactDir := filepath.Dir(recorder.ImplementHandoffPath(1))
		if err := options.IdentificationBanner(artifactDir); err != nil {
			return run, err
		}
	}
	return run, nil
}

func completeImplementRun(ctx context.Context, options ImplementOptions, run implementRunState, summary implementSummary) error {
	if err := run.recorder.WriteSummary(summary); err != nil {
		return err
	}
	renderer := ui.New(options.Output, ui.DetectCaps(options.Output))
	publicationStatus := ""
	if options.PublishOnApprove {
		publicationStatus = string(summary.publication.Status)
	}
	if err := renderer.RunSummary(ui.RunSummary{
		Iterations: summary.iterations, FinalVerdict: string(summary.final.Status), Summary: summary.final.Summary,
		NitFindings: toUIFindings(summary.nits), WorktreePath: summary.worktreePath, DiffStat: summary.diffStat,
		PullRequestURL: summary.pullRequestURL, PublishNotice: summary.publishNotice,
		PublicationStatus: publicationStatus, NitBudgetOverrides: nitBudgetOverrideStrings(summary.nitBudgetOverrides),
	}); err != nil {
		return fmt.Errorf("write implement summary: %w", err)
	}
	if err := run.recorder.WriteSessions(); err != nil {
		return err
	}
	if run.notifier != nil {
		outcome := string(summary.final.Status)
		if summary.exhaustionReason != "" {
			outcome = string(runrecord.Exhausted)
		}
		message := fmt.Sprintf("implement #%d finished: %s", options.Ticket.Number, outcome)
		if summary.pullRequestURL != "" {
			message += " — " + summary.pullRequestURL
		}
		_ = run.notifier.Notify(ctx, message)
	}
	if exhausted, err := finishExhaustedImplementRun(options, run, summary); exhausted {
		return err
	}
	run.runState.finish(runrecord.Approved)
	return nil
}

func finishExhaustedImplementRun(options ImplementOptions, run implementRunState, summary implementSummary) (bool, error) {
	reason := summary.exhaustionReason
	if reason == "" && summary.final.Status == verdict.Revise {
		reason = fmt.Sprintf("iteration limit (%d)", options.ProjectConfig.Loop.MaxIterations)
		run.runState.setExhaustionReason(reason)
	}
	if reason == "" {
		return false, nil
	}
	run.runState.finish(runrecord.Exhausted)
	if strings.HasPrefix(reason, "iteration limit") {
		return true, fmt.Errorf("implement loop reached max iterations (%d) with revise verdict", options.ProjectConfig.Loop.MaxIterations)
	}
	return true, fmt.Errorf("implement loop reached %s", reason)
}

func prepareImplement(ctx context.Context, git GitRunner, issueTracker tracker.Tracker, ticket tracker.Ticket) (implementSetup, error) {
	return prepareImplementWithGit(ctx, git, git, issueTracker, ticket)
}

func prepareImplementWithGitConfigured(
	ctx context.Context,
	workGit GitRunner,
	originGit GitRunner,
	issueTracker tracker.Tracker,
	ticket tracker.Ticket,
	captureBase bool,
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
	baseBranch := ""
	if captureBase {
		baseBranch = currentBranch(ctx, workGit)
	}
	branch := resolveBranchName(ticket)
	if _, err := originGit.Run(ctx, "switch", "-c", branch); err != nil {
		return implementSetup{}, fmt.Errorf("create implementation branch %q: %w", branch, err)
	}
	if err := issueTracker.UpdateStatus(ctx, ticket.Number, "doing"); err != nil {
		return implementSetup{}, fmt.Errorf("mark ticket #%d as doing: %w", ticket.Number, err)
	}
	return implementSetup{git: workGit, branch: branch, branchPoint: branchPoint, baseBranch: baseBranch}, nil
}

func prepareImplementWithGit(
	ctx context.Context,
	workGit GitRunner,
	originGit GitRunner,
	issueTracker tracker.Tracker,
	ticket tracker.Ticket,
) (implementSetup, error) {
	return prepareImplementWithGitConfigured(ctx, workGit, originGit, issueTracker, ticket, false)
}

func prepareImplementWithPullRequestBase(
	ctx context.Context,
	workGit GitRunner,
	originGit GitRunner,
	issueTracker tracker.Tracker,
	ticket tracker.Ticket,
	captureBase bool,
) (implementSetup, error) {
	return prepareImplementWithGitConfigured(ctx, workGit, originGit, issueTracker, ticket, captureBase)
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
	return implementSetup{
		git: workGit, branch: branch, branchPoint: branchPoint,
		baseBranch: worktree.Base, worktreePath: worktree.Path,
	}, nil
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
	runState             *runStateTracker
	output               io.Writer
	additionalContext    string
	reviewContext        string
	verbose              bool
	allowEmptyDiff       bool
	gateRunner           GateRunner
}

type implementReviewParams struct {
	iteration               int
	findings                []verdict.Finding
	previousReviewerSession string
	diffPath                string
	protectedPaths          []string
	gateResultPath          string
	implementerFeedPaths    []string
	mode                    HarnessOutputMode
}

type implementTurnParams struct {
	iteration           int
	findings            []verdict.Finding
	previousSession     string
	handoffPath         string
	evidenceDirectory   string
	rolloverSeedPath    string
	gateAttemptFeedback *failedGateAttemptFeedback
	mode                HarnessOutputMode
}

func runImplementIterations(ctx context.Context, params implementIterationsParams) (int, verdict.Verdict, []verdict.Finding, error) {
	params.runState = ensureImplementRunState(params)
	params.output = ensureLineTrackingWriter(params.output)
	var findings []verdict.Finding
	var final verdict.Verdict
	var previousImplementerSession string
	var rolloverSeedPath string
	var previousReviewerSession string
	branchGate := branchGateSource{}
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
		gateCycle, err := runImplementerGateCycle(
			ctx, params, iteration, handoffPath,
			findings, previousImplementerSession, rolloverSeedPath, mode, &branchGate,
		)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		previousImplementerSession = gateCycle.previousSession
		rolloverSeedPath = gateCycle.rolloverSeedPath
		if gateCycle.exhausted {
			return iterations, verdict.Verdict{}, nil, nil
		}
		diffPath, protectedPaths, err := prepareIterationReviewDiff(ctx, params, iteration)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}

		reviewResult, err := runImplementReview(ctx, params, implementReviewParams{
			iteration:               iteration,
			findings:                findings,
			previousReviewerSession: previousReviewerSession,
			diffPath:                diffPath,
			protectedPaths:          protectedPaths,
			gateResultPath:          gateCycle.gateResultPath,
			implementerFeedPaths:    gateCycle.implementerFeedPaths,
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
		if processApproval(params, iteration, final) {
			break
		}
		findings = final.Findings
		previousReviewerSession = lastUsableSessionID(reviewResult.SessionIDs)
	}
	return iterations, final, nitFindings(final), nil
}

func processApproval(params implementIterationsParams, iteration int, final verdict.Verdict) bool {
	if final.Status != verdict.Approve {
		return false
	}
	count := len(nitFindings(final))
	budget := params.projectConfig.Loop.NitBudget
	if count <= budget {
		return true
	}
	params.runState.recordNitBudgetOverride(iteration, count, budget)
	if iteration == params.projectConfig.Loop.MaxIterations {
		params.runState.setExhaustionReason(fmt.Sprintf("Nit budget exceeded (%d nits, budget %d) at iteration limit (%d)", count, budget, iteration))
	}
	return false
}

type branchGateSource struct {
	script   branchGateScript
	resolved bool
}

type implementGateCycleResult struct {
	previousSession      string
	rolloverSeedPath     string
	gateResultPath       string
	implementerFeedPaths []string
	exhausted            bool
}

type implementGateTurnResult struct {
	previousSession       string
	rolloverSeedPath      string
	gateResultPath        string
	implementerFeedPath   string
	failedAttemptFeedback failedGateAttemptFeedback
	gatePresent           bool
	green                 bool
}

func runImplementerGateCycle(
	ctx context.Context,
	params implementIterationsParams,
	iteration int,
	firstHandoffPath string,
	findings []verdict.Finding,
	previousSession string,
	rolloverSeedPath string,
	mode HarnessOutputMode,
	branchGate *branchGateSource,
) (implementGateCycleResult, error) {
	result := implementGateCycleResult{
		previousSession: previousSession, rolloverSeedPath: rolloverSeedPath,
	}
	gateAttempts := 0
	var previousGateAttemptFeedback *failedGateAttemptFeedback
	for implementTurn := 1; ; implementTurn++ {
		handoffPath, err := implementerTurnHandoffPath(params, iteration, implementTurn, firstHandoffPath)
		if err != nil {
			return implementGateCycleResult{}, err
		}
		turnResult, err := runImplementerGateTurn(
			ctx, params, iteration, handoffPath, findings, result, previousGateAttemptFeedback, gateAttempts+1, mode, branchGate,
		)
		if err != nil {
			return implementGateCycleResult{}, err
		}
		result.previousSession = turnResult.previousSession
		result.rolloverSeedPath = turnResult.rolloverSeedPath
		result.gateResultPath = turnResult.gateResultPath
		if turnResult.implementerFeedPath != "" {
			result.implementerFeedPaths = append(result.implementerFeedPaths, turnResult.implementerFeedPath)
		}
		if !turnResult.gatePresent || turnResult.green {
			return result, nil
		}
		gateAttempts++
		if gateAttempts >= params.projectConfig.Loop.MaxGateAttempts {
			params.runState.setExhaustionReason(fmt.Sprintf("Gate attempt limit (%d) in iteration %d", gateAttempts, iteration))
			result.exhausted = true
			return result, nil
		}
		previousGateAttemptFeedback = &turnResult.failedAttemptFeedback
	}
}

func runImplementerGateTurn(
	ctx context.Context,
	params implementIterationsParams,
	iteration int,
	handoffPath string,
	findings []verdict.Finding,
	cycle implementGateCycleResult,
	previousGateAttemptFeedback *failedGateAttemptFeedback,
	attemptNumber int,
	mode HarnessOutputMode,
	branchGate *branchGateSource,
) (implementGateTurnResult, error) {
	turnFindings := findings
	if previousGateAttemptFeedback != nil {
		turnFindings = nil
	}
	implementResult, err := runImplementTurn(ctx, params, implementTurnParams{
		iteration: iteration, findings: turnFindings, previousSession: cycle.previousSession,
		handoffPath: handoffPath, evidenceDirectory: params.recorder.EvidenceDirectory(iteration),
		rolloverSeedPath:    cycle.rolloverSeedPath,
		gateAttemptFeedback: previousGateAttemptFeedback, mode: mode,
	})
	if err != nil {
		return implementGateTurnResult{}, err
	}
	params.runState.setActivity(runrecord.Verifying)
	rolloverSeedPath, previousSession, err := nextImplementerTurn(handoffPath, implementResult.SessionIDs)
	if err != nil {
		return implementGateTurnResult{}, err
	}
	if err := resolveBranchGate(ctx, params, branchGate); err != nil {
		return implementGateTurnResult{}, err
	}
	result := implementGateTurnResult{rolloverSeedPath: rolloverSeedPath, previousSession: previousSession}
	if iteration > 1 {
		result.implementerFeedPath, err = recordImplementReviewFeed(params, iteration, attemptNumber, implementResult.Feed)
		if err != nil {
			return implementGateTurnResult{}, err
		}
	}
	if !branchGate.script.loaded {
		result.gateResultPath, err = params.recorder.RecordNoGate(iteration)
		if err != nil {
			return implementGateTurnResult{}, fmt.Errorf("record missing branch-point Gate: %w", err)
		}
		return result, nil
	}
	result.failedAttemptFeedback, result.gateResultPath, result.green, err = recordGateAttempt(
		ctx, params, iteration, attemptNumber, gateScriptPath(branchGate.script),
	)
	if err != nil {
		return implementGateTurnResult{}, err
	}
	result.gatePresent = true
	return result, nil
}

func implementerTurnHandoffPath(
	params implementIterationsParams,
	iteration, turn int,
	firstHandoffPath string,
) (string, error) {
	if turn == 1 {
		return firstHandoffPath, nil
	}
	return prepareGateAttemptHandoffPath(params, iteration, turn)
}

func resolveBranchGate(ctx context.Context, params implementIterationsParams, branchGate *branchGateSource) error {
	if branchGate.resolved {
		return nil
	}
	script, err := extractBranchGateScript(ctx, params.git, params.recorder, params.branchPoint)
	if err != nil {
		return err
	}
	branchGate.script = script
	branchGate.resolved = true
	return nil
}

func ensureImplementRunState(params implementIterationsParams) *runStateTracker {
	if params.runState != nil {
		return params.runState
	}
	return newRunStateTracker(params.recorder, newRunState(RunSpec{
		Kind:          runrecord.Implement,
		TicketRef:     "#" + strconv.Itoa(params.ticket.Number),
		MaxIterations: params.projectConfig.Loop.MaxIterations,
	}))
}

func runImplementReview(ctx context.Context, params implementIterationsParams, reviewParams implementReviewParams) (ReviewExecution, error) {
	params.runState.setActivity(runrecord.Reviewing)
	promptContext := reviewPromptContext{
		diffPath:             reviewParams.diffPath,
		evidence:             inspectReviewEvidence(params.recorder.EvidenceDirectory(reviewParams.iteration)),
		findings:             reviewParams.findings,
		additionalContext:    params.reviewContext,
		implementerFeedPaths: reviewParams.implementerFeedPaths,
	}
	reviewPrompt := composeReviewPrompt(
		"#"+strconv.Itoa(params.ticket.Number), &params.ticket, params.branchPoint, promptContext,
	)
	if reviewParams.iteration > 1 {
		carryoverContext := promptContext
		carryoverContext.additionalContext = ""
		reviewPrompt += "\n\n" + composeReviewCarryoverPrompt(carryoverContext)
	}
	reviewPrompt = appendProtectedPaths(reviewPrompt, reviewParams.protectedPaths)
	reviewPrompt = appendGateResultPath(reviewPrompt, reviewParams.gateResultPath)
	reviewRequest := harness.Request{
		Completion:  harness.CompletionReviewVerdict,
		Model:       params.projectConfig.Roles.Review.Model,
		Effort:      params.projectConfig.Roles.Review.Effort,
		Prompt:      reviewPrompt,
		MCP:         params.projectConfig.Roles.Review.MCP,
		SandboxMode: params.projectConfig.Roles.Review.SandboxMode,
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
		resumePrompt := composeResumedReviewCarryoverPrompt(promptContext)
		resumePrompt = appendProtectedPaths(resumePrompt, reviewParams.protectedPaths)
		reviewOptions.resumePrompt = appendGateResultPath(resumePrompt, reviewParams.gateResultPath)
	}
	reviewStartedAt := time.Now().UTC()
	reviewResult, err := runReviewExecutionWithResumeFallback(ctx, params.reviewer, reviewOptions)
	reviewEndedAt := time.Now().UTC()
	if err != nil {
		return ReviewExecution{}, recordImplementReviewFailure(
			params, reviewParams, reviewResult, reviewStartedAt, reviewEndedAt, err,
		)
	}
	reviewResult.Verdict = verifyVerdictLocations(params.workRoot, reviewResult.Verdict)
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

func recordImplementReviewFailure(
	params implementIterationsParams,
	reviewParams implementReviewParams,
	execution ReviewExecution,
	startedAt time.Time,
	endedAt time.Time,
	failure error,
) error {
	var unparseable *UnparseableVerdictError
	if errors.As(failure, &unparseable) {
		execution = unparseable.Execution
	}
	recordReviewUsage(reviewUsageParams{
		recorder:  params.recorder,
		iteration: reviewParams.iteration,
		role:      params.projectConfig.Roles.Review,
		execution: execution,
		workRoot:  params.workRoot,
		startedAt: startedAt,
		endedAt:   endedAt,
	})
	if err := recordFailedReviewTurn(params.recorder, reviewParams.iteration, execution, failure); err != nil {
		return fmt.Errorf("%w; save partial review artifacts: %v", failure, err)
	}
	if errors.As(failure, &unparseable) {
		return reviewTranscriptSavedError(failure, recorderArtifactDir(params.recorder))
	}
	return failure
}

func recordReviewSessions(recorder RunRecorder, iteration int, review ReviewExecution) (ReviewExecution, error) {
	if err := recorder.RecordSessions(iteration, "review", review.SessionIDs); err != nil {
		return ReviewExecution{}, fmt.Errorf("record review sessions: %w", err)
	}
	return review, nil
}

func prepareIterationReviewDiff(ctx context.Context, params implementIterationsParams, iteration int) (string, []string, error) {
	diff, err := computeReviewDiffWithEmpty(ctx, params.git, params.branchPoint, params.allowEmptyDiff)
	if err != nil {
		return "", nil, err
	}
	diffPath, err := params.recorder.RecordReviewDiff(iteration, diff)
	if err != nil {
		return "", nil, err
	}
	paths := protectedPathsInDiff(diff, params.projectConfig.Gate.ProtectedPaths)
	if params.worktreeArtifactRoot == "" {
		return diffPath, paths, nil
	}
	diffPath, err = recordWorktreeReviewDiff(params.worktreeArtifactRoot, filepath.Dir(diffPath), iteration, diff)
	return diffPath, paths, err
}

func recordImplementReviewFeed(params implementIterationsParams, iteration, turn int, feed string) (string, error) {
	path, err := params.recorder.RecordImplementReviewFeed(iteration, turn, feed)
	if err != nil {
		return "", fmt.Errorf("record implementer feed for review: %w", err)
	}
	if params.worktreeArtifactRoot == "" {
		return path, nil
	}
	writer, err := worktreeRunWriter(params.worktreeArtifactRoot, filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("prepare worktree implementer feed: %w", err)
	}
	return writer.WriteImplementReviewFeed(iteration, turn, feed)
}

func prepareIterationHandoffPath(params implementIterationsParams, iteration int) (string, error) {
	handoffPath := params.recorder.ImplementHandoffPath(iteration)
	if params.worktreeArtifactRoot == "" {
		return handoffPath, nil
	}
	writer, err := worktreeRunWriter(params.worktreeArtifactRoot, filepath.Dir(handoffPath))
	if err != nil {
		return "", fmt.Errorf("prepare worktree handoff path: %w", err)
	}
	return writer.HandoffPath(iteration), nil
}

func prepareGateAttemptHandoffPath(params implementIterationsParams, iteration, turn int) (string, error) {
	handoffPath := params.recorder.ImplementGateHandoffPath(iteration, turn)
	if params.worktreeArtifactRoot == "" {
		return handoffPath, nil
	}
	writer, err := worktreeRunWriter(params.worktreeArtifactRoot, filepath.Dir(params.recorder.ImplementHandoffPath(iteration)))
	if err != nil {
		return "", fmt.Errorf("prepare worktree Gate attempt handoff path: %w", err)
	}
	return writer.GateHandoffPath(iteration, turn), nil
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
	params.runState.startActivity(turn.iteration, runrecord.Implementing)
	if err := presentImplementTurn(params, turn); err != nil {
		return implementExecution{}, err
	}
	implementRequest := buildImplementRequest(params, turn)
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
		recordRoleUsage(params.recorder, implementUsage(params, turn, implementResult, implementStartedAt, implementEndedAt))
		return implementExecution{}, recordFailedImplementExecution(params.recorder, turn.iteration, implementResult, err)
	}
	recordRoleUsage(params.recorder, implementUsage(params, turn, implementResult, implementStartedAt, implementEndedAt))
	if err := recordSuccessfulImplementTurn(ctx, params, turn, implementResult); err != nil {
		return implementExecution{}, err
	}
	return implementResult, nil
}

func presentImplementTurn(params implementIterationsParams, turn implementTurnParams) error {
	activity := "implementing"
	if turn.gateAttemptFeedback != nil {
		activity = "addressing Gate failure"
	} else if turn.iteration > 1 {
		activity = fmt.Sprintf("revising %d finding(s)", len(turn.findings))
	}
	renderer := ui.New(params.output, ui.DetectCaps(params.output))
	if err := writeRoleSection(params.output, "Implementer"); err != nil {
		return fmt.Errorf("write implement role: %w", err)
	}
	if err := renderer.Step(ui.Step{Label: fmt.Sprintf("iteration %d/%d — %s", turn.iteration, params.projectConfig.Loop.MaxIterations, activity)}); err != nil {
		return fmt.Errorf("write implement progress: %w", err)
	}
	return nil
}

func buildImplementRequest(params implementIterationsParams, turn implementTurnParams) harness.Request {
	options := implementPromptOptions{
		handoffPath: turn.handoffPath, evidenceDirectory: turn.evidenceDirectory,
		rolloverSeedPath: turn.rolloverSeedPath, additionalContext: params.additionalContext,
	}
	implementPrompt := ""
	if turn.gateAttemptFeedback != nil {
		implementPrompt = composeGateAttemptPrompt(
			params.ticket, options,
			formatGateAttemptFeedback(*turn.gateAttemptFeedback),
		)
	} else {
		implementPrompt = composeImplementPrompt(
			params.ticket, turn.findings, turn.iteration, options,
		)
	}
	return harness.Request{
		Completion:  harness.CompletionTurnEnd,
		Model:       params.projectConfig.Roles.Implement.Model,
		Effort:      params.projectConfig.Roles.Implement.Effort,
		Prompt:      implementPrompt,
		MCP:         params.projectConfig.Roles.Implement.MCP,
		SandboxMode: params.projectConfig.Roles.Implement.SandboxMode,
	}
}

func implementUsage(
	params implementIterationsParams,
	turn implementTurnParams,
	execution implementExecution,
	startedAt, endedAt time.Time,
) usage.Entry {
	return usage.CollectInvocation(usage.Invocation{
		Iteration:  turn.iteration,
		Role:       "implement",
		Harness:    string(params.projectConfig.Roles.Implement.Harness),
		Model:      params.projectConfig.Roles.Implement.Model,
		SessionIDs: execution.SessionIDs,
		StartedAt:  startedAt,
		EndedAt:    endedAt,
	}, params.workRoot, "")
}

func recordSuccessfulImplementTurn(
	ctx context.Context,
	params implementIterationsParams,
	turn implementTurnParams,
	execution implementExecution,
) error {
	if err := params.recorder.RecordImplementTurn(
		turn.iteration,
		execution.Feed,
		execution.Transcript,
	); err != nil {
		return err
	}
	if err := params.recorder.RecordSessions(turn.iteration, "implement", execution.SessionIDs); err != nil {
		return fmt.Errorf("record implement sessions: %w", err)
	}
	if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
		return err
	}
	return nil
}

func recordFailedImplementExecution(
	recorder RunRecorder,
	iteration int,
	execution implementExecution,
	turnErr error,
) error {
	if err := recordFailedImplementTurn(recorder, iteration, execution, turnErr); err != nil {
		return fmt.Errorf("%w; save partial implement artifacts: %v", turnErr, err)
	}
	return turnErr
}

// recordRoleUsage persists usage as best-effort metadata. Usage collection or
// persistence failures must not fail the run.
func recordRoleUsage(recorder RunRecorder, entry usage.Entry) {
	_ = recorder.RecordUsage(entry)
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
		return result, err
	}
	fresh, freshErr := runImplementRole(ctx, adapter, request, output, mode, questions)
	if freshErr != nil {
		return appendImplementExecutions(result, fresh), freshErr
	}
	return fresh, nil
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
		execution := implementExecution{
			Feed: feed.String(), Transcript: result.Transcript, SessionIDs: result.SessionIDs,
		}
		if errors.Is(err, harness.ErrSessionStalled) {
			return recoverStalledImplementer(ctx, adapter, options, execution, err, visibleOutput)
		}
		implementErr := fmt.Errorf("run implement harness: %w", err)
		if options.initialSessionID != "" {
			return execution, &implementResumeError{cause: implementErr}
		}
		return execution, implementErr
	}
	return implementExecution{
		Feed:       feed.String(),
		Transcript: result.Transcript,
		SessionIDs: result.SessionIDs,
	}, nil
}

func recoverStalledImplementer(
	ctx context.Context,
	adapter harness.Adapter,
	options implementExecutionOptions,
	first implementExecution,
	stallErr error,
	output io.Writer,
) (implementExecution, error) {
	var stalled *harness.SessionStalledError
	if !errors.As(stallErr, &stalled) {
		return first, fmt.Errorf("run implement harness: %w", stallErr)
	}
	sessionID := stalled.SessionID
	if sessionID == "" {
		sessionID = options.initialSessionID
	}
	if sessionID == "" {
		sessionID = lastUsableSessionID(first.SessionIDs)
	}
	if sessionID == "" {
		return first, fmt.Errorf("run implement harness: stalled session has no session id: %w", stallErr)
	}

	request := options.request
	request.Prompt = "The implementer session stopped making progress. "
	if stalled.Limit != "" {
		request.Prompt = fmt.Sprintf("The implementer session hit its %s. ", stalled.Limit)
	}
	request.Prompt += "Continue the requested implementation from the existing session. End your turn normally when the work is complete."
	request.Prompt = appendImplementerReports(request.Prompt, stalled.Reports)
	var recoveryFeed bytes.Buffer
	recoveryArtifact := newPlainHarnessOutput(&recoveryFeed, options.mode)
	recovery, err := runHarnessConversation(ctx, adapter, func(runContext context.Context) (harness.Stream, error) {
		return adapter.Resume(runContext, sessionID, request)
	}, conversationOptions{
		request:   request,
		output:    output,
		artifact:  recoveryArtifact,
		mode:      options.mode,
		questions: options.questions,
		role:      "implement",
		sessionID: sessionID,
	})
	resumed := implementExecution{
		Feed: recoveryFeed.String(), Transcript: recovery.Transcript, SessionIDs: recovery.SessionIDs,
	}
	combined := appendImplementExecutions(first, resumed)
	if err != nil {
		return combined, fmt.Errorf("resume stalled implementer session: %w", err)
	}
	return combined, nil
}

func appendImplementerReports(prompt string, reports []harness.SubagentReport) string {
	if len(reports) == 0 {
		return prompt
	}

	var reportBytes int
	usePaths := false
	for _, report := range reports {
		reportBytes += len(report.Text)
		if report.TooLarge || report.Text == "" {
			usePaths = true
		}
	}
	if reportBytes > harness.StalledSessionReportTextLimit {
		usePaths = true
	}

	var recovery strings.Builder
	recovery.WriteString(prompt)
	if usePaths {
		recovery.WriteString("\nRead the finished subagent reports at these paths before continuing:\n")
		for _, report := range reports {
			fmt.Fprintf(&recovery, "- %s\n", report.Path)
		}
		return recovery.String()
	}

	recovery.WriteString("\nFinished subagent reports:\n")
	for index, report := range reports {
		fmt.Fprintf(&recovery, "\n--- Report %d (%s) ---\n%s\n", index+1, report.Path, report.Text)
	}
	recovery.WriteString("Use these reports when continuing the implementation.")
	return recovery.String()
}

func recordFailedImplementTurn(recorder RunRecorder, iteration int, execution implementExecution, failure error) error {
	if err := recorder.RecordImplementTurn(iteration, execution.Feed, execution.Transcript); err != nil {
		return err
	}
	if err := recorder.RecordFailure(iteration, "implement", failure.Error()); err != nil {
		return err
	}
	return recorder.RecordSessions(iteration, "implement", execution.SessionIDs)
}

func appendImplementExecutions(first, next implementExecution) implementExecution {
	return implementExecution{
		Feed:       appendTranscriptString(first.Feed, next.Feed),
		Transcript: appendTranscriptString(first.Transcript, next.Transcript),
		SessionIDs: append(append([]string(nil), first.SessionIDs...), next.SessionIDs...),
	}
}

func appendTranscriptString(first, next string) string {
	if first != "" && next != "" && !strings.HasSuffix(first, "\n") {
		first += "\n"
	}
	return first + next
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
	return runrecord.FormatSummary(runrecord.SummaryInput{
		Iterations: summary.iterations, Final: summary.final, Nits: summary.nits,
		DiffStat: summary.diffStat, WorktreePath: summary.worktreePath,
		ExhaustionReason: summary.exhaustionReason, NitBudgetOverrides: summary.nitBudgetOverrides, PullRequestURL: summary.pullRequestURL,
		Publication: summary.publication,
	})
}

func recordWorktreeReviewDiff(worktreeRoot, runDir string, iteration int, diff string) (string, error) {
	writer, err := worktreeRunWriter(worktreeRoot, runDir)
	if err != nil {
		return "", fmt.Errorf("record worktree review diff: %w", err)
	}
	return writer.WriteReviewDiff(iteration, diff)
}

func worktreeRunWriter(worktreeRoot, runDir string) (*runrecord.Writer, error) {
	root, err := filepath.Abs(worktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree run artifact root: %w", err)
	}
	runName := filepath.Base(filepath.Clean(runDir))
	if runName == "." || runName == string(filepath.Separator) || runName == "" {
		return nil, errors.New("worktree run artifact directory is required")
	}
	return runrecord.CreateNamedWriter(root, runName)
}

func nitBudgetOverrideStrings(overrides []runrecord.NitBudgetOverride) []fmt.Stringer {
	lines := make([]fmt.Stringer, 0, len(overrides))
	for _, override := range overrides {
		lines = append(lines, override)
	}
	return lines
}
