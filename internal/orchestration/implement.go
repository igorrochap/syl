package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

var branchTypePattern = regexp.MustCompile(`^(feat|fix|refactor|chore|docs|test|perf|build|ci)$`)

type implementSetup struct {
	git         GitRunner
	branch      string
	branchPoint string
}

type ImplementOptions struct {
	ProjectRoot          string
	ProjectConfig        config.Config
	IssueTracker         tracker.Tracker
	Ticket               tracker.Ticket
	Implementer          harness.Adapter
	Reviewer             harness.Adapter
	Git                  GitRunner
	Notifier             Notifier
	Input                io.Reader
	Output               io.Writer
	Verbose              bool
	IdentificationBanner func(artifactDir string) error
}

func RunImplement(ctx context.Context, options ImplementOptions) error {
	projectConfig := options.ProjectConfig
	issueTracker := options.IssueTracker
	ticket := options.Ticket
	if options.Git == nil {
		return errors.New("implement: git runner is not configured")
	}
	if options.Implementer == nil {
		return fmt.Errorf("implement harness %q is not configured", projectConfig.Roles.Implement.Harness)
	}
	if options.Reviewer == nil {
		return fmt.Errorf("review harness %q is not configured", projectConfig.Roles.Review.Harness)
	}
	setup, err := prepareImplement(ctx, options.Git, issueTracker, ticket)
	if err != nil {
		return err
	}
	notifier := options.Notifier
	if !projectConfig.Notifications.Enabled {
		notifier = nil
	}
	questions := NewQuestionHandler(options.Input, options.Output, "#"+strconv.Itoa(ticket.Number), notifier)
	recorder, err := newImplementRunRecorder(
		options.ProjectRoot,
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
	iterations, final, nits, err := runImplementIterations(ctx, implementIterationsParams{
		git:           setup.git,
		implementer:   options.Implementer,
		reviewer:      options.Reviewer,
		projectConfig: projectConfig,
		ticket:        ticket,
		branchPoint:   setup.branchPoint,
		recorder:      recorder,
		questions:     questions,
		output:        options.Output,
		verbose:       options.Verbose,
	})
	if err != nil {
		return err
	}

	diffStat, err := setup.git.Run(ctx, "diff", "--stat", setup.branchPoint)
	if err != nil {
		diffStat = fmt.Sprintf("unavailable: %v", err)
	}
	if err := recorder.WriteSummary(iterations, final, nits, diffStat); err != nil {
		return err
	}
	summary := formatImplementSummary(iterations, final, nits, diffStat)
	if _, err := io.WriteString(options.Output, summary); err != nil {
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
	status, err := git.Run(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return implementSetup{}, fmt.Errorf("check working tree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return implementSetup{}, errors.New("working tree is dirty; commit or stash changes first")
	}

	branchPoint, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return implementSetup{}, fmt.Errorf("record branch point: %w", err)
	}
	branchPoint = strings.TrimSpace(branchPoint)
	if branchPoint == "" {
		return implementSetup{}, errors.New("record branch point: git returned an empty ref")
	}
	branch := branchName(ticket)
	if _, err := git.Run(ctx, "switch", "-c", branch); err != nil {
		return implementSetup{}, fmt.Errorf("create implementation branch %q: %w", branch, err)
	}
	if err := issueTracker.UpdateStatus(ctx, ticket.Number, "doing"); err != nil {
		return implementSetup{}, fmt.Errorf("mark ticket #%d as doing: %w", ticket.Number, err)
	}
	return implementSetup{git: git, branch: branch, branchPoint: branchPoint}, nil
}

type implementIterationsParams struct {
	git           GitRunner
	implementer   harness.Adapter
	reviewer      harness.Adapter
	projectConfig config.Config
	ticket        tracker.Ticket
	branchPoint   string
	recorder      RunRecorder
	questions     *QuestionHandler
	output        io.Writer
	verbose       bool
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
		implementResult, err := runImplementRole(
			ctx,
			params.implementer,
			implementRequest,
			newRoleLabelWriter(params.output, "implement", ansiColorImplement),
			mode,
			params.questions,
		)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
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
		reviewResult, err := runReviewExecutionWithResumeFallback(ctx, params.reviewer, reviewOptions)
		if err != nil {
			var unparseable *UnparseableVerdictError
			if errors.As(err, &unparseable) {
				if artifactErr := params.recorder.RecordReviewOutput(iteration, unparseable.Execution); artifactErr != nil {
					return 0, verdict.Verdict{}, nil, artifactErr
				}
				return 0, verdict.Verdict{}, nil, reviewTranscriptSavedError(err, params.recorder.Dir())
			}
			return 0, verdict.Verdict{}, nil, err
		}
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

func branchName(ticket tracker.Ticket) string {
	title := strings.TrimSpace(ticket.Title)
	branchType := ""
	contextTitle := title
	for _, label := range ticket.Labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if branchTypePattern.MatchString(label) {
			branchType = label
			break
		}
	}
	if before, after, ok := strings.Cut(title, ":"); ok {
		candidate := strings.ToLower(strings.TrimSpace(before))
		if branchType == "" && branchTypePattern.MatchString(candidate) {
			branchType = candidate
		}
		if branchType != "" || strings.Contains(candidate, "implement") || strings.Contains(candidate, "syl") {
			contextTitle = strings.TrimSpace(after)
		}
	}
	if branchType == "" {
		branchType = "feat"
	}
	context := slugContext(contextTitle)
	if context == "" {
		context = "ticket-" + strconv.Itoa(ticket.Number)
	}
	return branchType + "/" + context
}

func slugContext(value string) string {
	words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	stopWords := map[string]bool{"a": true, "an": true, "and": true, "for": true, "in": true, "of": true, "on": true, "the": true, "to": true, "with": true}
	selected := make([]string, 0, 5)
	for _, word := range words {
		if stopWords[word] {
			continue
		}
		selected = append(selected, word)
		if len(selected) == 5 {
			break
		}
	}
	return strings.Join(selected, "-")
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

func formatImplementSummary(iterations int, final verdict.Verdict, nits []verdict.Finding, diffStat string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Iterations: %d\nFinal verdict: %s\nSummary: %s\nNit findings:\n", iterations, final.Status, final.Summary)
	if len(nits) == 0 {
		builder.WriteString("- (none)\n")
	} else {
		for _, finding := range nits {
			fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
		}
	}
	fmt.Fprintf(&builder, "Diff stat:\n%s\n", strings.TrimSpace(diffStat))
	return builder.String()
}
