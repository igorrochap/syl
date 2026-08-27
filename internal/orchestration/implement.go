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
	Verbose              bool
	ProvisionedWorktree  *Worktree
	IdentificationBanner func(artifactDir string) error
}

func RunImplement(ctx context.Context, options ImplementOptions) (returnErr error) {
	projectConfig := options.ProjectConfig
	issueTracker := options.IssueTracker
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
	if options.Git == nil {
		return errors.New("implement: git runner is not configured")
	}
	if options.Implementer == nil {
		return fmt.Errorf("implement harness %q is not configured", projectConfig.Roles.Implement.Harness)
	}
	if options.Reviewer == nil {
		return fmt.Errorf("review harness %q is not configured", projectConfig.Roles.Review.Harness)
	}
	var (
		setup implementSetup
		err   error
	)
	if options.ProvisionedWorktree != nil {
		setup, err = prepareProvisionedImplement(ctx, options.Git, issueTracker, ticket, *options.ProvisionedWorktree)
	} else {
		setup, err = prepareImplementWithGit(ctx, options.Git, originGit, issueTracker, ticket)
	}
	if err != nil {
		return err
	}
	notifier := options.Notifier
	if !projectConfig.Notifications.Enabled {
		notifier = nil
	}
	notifier = withNotificationContext(notifier, options.OriginRoot, setup.git)
	questions := NewQuestionHandler(options.Input, options.Output, "#"+strconv.Itoa(ticket.Number), notifier)
	recorder, err := newImplementRunRecorder(
		options.OriginRoot,
		ticket.Number,
		setup.branch,
		setup.branchPoint,
	)
	if err != nil {
		return err
	}
	if options.IdentificationBanner != nil {
		if err := options.IdentificationBanner(recorder.Dir()); err != nil {
			return err
		}
	}
	loopStarted = true
	iterations, final, nits, err := runImplementIterations(ctx, implementIterationsParams{
		git:            setup.git,
		workRoot:       options.WorkRoot,
		implementer:    options.Implementer,
		reviewer:       options.Reviewer,
		projectConfig:  projectConfig,
		ticket:         ticket,
		branchPoint:    setup.branchPoint,
		reviewDiffRoot: setup.worktreePath,
		recorder:       recorder,
		questions:      questions,
		output:         options.Output,
		verbose:        options.Verbose,
	})
	if err != nil {
		return err
	}

	diffStat, err := setup.git.Run(ctx, "diff", "--stat", setup.branchPoint)
	if err != nil {
		diffStat = fmt.Sprintf("unavailable: %v", err)
	}
	summary := implementSummary{
		iterations:   iterations,
		final:        final,
		nits:         nits,
		diffStat:     diffStat,
		worktreePath: setup.worktreePath,
	}
	if err := recorder.WriteSummary(summary); err != nil {
		return err
	}
	if _, err := io.WriteString(options.Output, formatImplementSummary(summary)); err != nil {
		return fmt.Errorf("write implement summary: %w", err)
	}
	if err := recorder.WriteSessions(); err != nil {
		return err
	}
	if notifier != nil {
		_ = notifier.Notify(ctx, fmt.Sprintf("implement #%d finished: %s", ticket.Number, final.Status))
	}
	if final.Status == verdict.Revise {
		return fmt.Errorf("implement loop reached max iterations (%d) with revise verdict", projectConfig.Loop.MaxIterations)
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
	git            GitRunner
	workRoot       string
	implementer    harness.Adapter
	reviewer       harness.Adapter
	projectConfig  config.Config
	ticket         tracker.Ticket
	branchPoint    string
	reviewDiffRoot string
	recorder       RunRecorder
	questions      *QuestionHandler
	output         io.Writer
	verbose        bool
}

func runImplementIterations(ctx context.Context, params implementIterationsParams) (int, verdict.Verdict, []verdict.Finding, error) {
	var blocking []verdict.Finding
	var nits []verdict.Finding
	var final verdict.Verdict
	var previousReviewerSession string
	iterations := 0
	for iteration := 1; iteration <= params.projectConfig.Loop.MaxIterations; iteration++ {
		iterations = iteration
		activity := "implementing"
		if iteration > 1 {
			activity = fmt.Sprintf("revising %d blocking finding(s)", len(blocking))
		}
		if _, err := fmt.Fprintf(params.output, "iteration %d/%d — %s\n", iteration, params.projectConfig.Loop.MaxIterations, activity); err != nil {
			return 0, verdict.Verdict{}, nil, fmt.Errorf("write implement progress: %w", err)
		}

		mode := QuietHarnessOutput
		if params.verbose {
			mode = ParsedHarnessOutput
		}
		implementRequest := harness.Request{
			Model:  params.projectConfig.Roles.Implement.Model,
			Effort: params.projectConfig.Roles.Implement.Effort,
			Prompt: composeImplementPrompt(params.ticket, blocking, iteration),
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
			return 0, verdict.Verdict{}, nil, err
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
			return 0, verdict.Verdict{}, nil, err
		}
		params.recorder.RecordSessions(iteration, "implement", implementResult.SessionIDs)
		if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		diff, err := computeReviewDiff(ctx, params.git, params.branchPoint)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		diffPath, err := params.recorder.RecordReviewDiff(iteration, diff)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if params.reviewDiffRoot != "" {
			diffPath, err = recordWorktreeReviewDiff(params.reviewDiffRoot, params.recorder.Dir(), iteration, diff)
			if err != nil {
				return 0, verdict.Verdict{}, nil, err
			}
		}

		reviewRequest := harness.Request{
			Model:  params.projectConfig.Roles.Review.Model,
			Effort: params.projectConfig.Roles.Review.Effort,
			Prompt: composeReviewPrompt("#"+strconv.Itoa(params.ticket.Number), &params.ticket, params.branchPoint, diffPath),
			MCP:    params.projectConfig.Roles.Review.MCP,
		}
		if _, err := fmt.Fprintf(params.output, "iteration %d/%d — reviewing\n", iteration, params.projectConfig.Loop.MaxIterations); err != nil {
			return 0, verdict.Verdict{}, nil, fmt.Errorf("write review progress: %w", err)
		}
		reviewOptions := reviewResumeOptions{
			sessionID: previousReviewerSession,
			request:   reviewRequest,
			output:    newRoleLabelWriter(params.output, "review", ansiColorReview),
			mode:      mode,
			questions: params.questions,
		}
		if previousReviewerSession != "" {
			reviewOptions.resumePrompt = composeReviewResumePrompt(diffPath, blocking)
		}
		reviewStartedAt := time.Now().UTC()
		reviewResult, err := runReviewExecutionWithResumeFallback(ctx, params.reviewer, reviewOptions)
		reviewEndedAt := time.Now().UTC()
		if err != nil {
			var unparseable *UnparseableVerdictError
			if errors.As(err, &unparseable) {
				recordReviewUsage(reviewUsageParams{
					recorder:  params.recorder,
					iteration: iteration,
					role:      params.projectConfig.Roles.Review,
					execution: unparseable.Execution,
					workRoot:  params.workRoot,
					startedAt: reviewStartedAt,
					endedAt:   reviewEndedAt,
				})
				if artifactErr := params.recorder.RecordReviewOutput(iteration, unparseable.Execution); artifactErr != nil {
					return 0, verdict.Verdict{}, nil, artifactErr
				}
				return 0, verdict.Verdict{}, nil, reviewTranscriptSavedError(err, params.recorder.Dir())
			}
			return 0, verdict.Verdict{}, nil, err
		}
		recordReviewUsage(reviewUsageParams{
			recorder:  params.recorder,
			iteration: iteration,
			role:      params.projectConfig.Roles.Review,
			execution: reviewResult,
			workRoot:  params.workRoot,
			startedAt: reviewStartedAt,
			endedAt:   reviewEndedAt,
		})
		if err := params.recorder.RecordReviewOutput(iteration, reviewResult); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if err := params.recorder.RecordVerdict(iteration, reviewResult.Verdict); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		params.recorder.RecordSessions(iteration, "review", reviewResult.SessionIDs)
		final = reviewResult.Verdict
		if _, err := io.WriteString(params.output, formatVerdict(final)); err != nil {
			return 0, verdict.Verdict{}, nil, fmt.Errorf("write review verdict: %w", err)
		}
		nits = append(nits, nitFindings(final)...)
		if final.Status == verdict.Approve {
			break
		}
		blocking = blockingFindings(final)
		previousReviewerSession = lastUsableSessionID(reviewResult.SessionIDs)
	}
	return iterations, final, nits, nil
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
