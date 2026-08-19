package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

const reviewPrompt = `/code-review

Review the pre-computed diff at %s against the recorded branch point %s. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

` + questionProtocolInstruction

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
	ProjectRoot          string
	ProjectConfig        config.Config
	IssueTracker         tracker.Tracker
	Ticket               *tracker.Ticket
	TicketRef            string
	Adapter              harness.Adapter
	Input                io.Reader
	Output               io.Writer
	Raw                  bool
	Verbose              bool
	Notifier             Notifier
	Git                  GitRunner
	IdentificationBanner func() error
}

type reviewPreparation struct {
	branchPoint string
	diffPath    string
	recorder    RunRecorder
}

func RunReview(ctx context.Context, options ReviewOptions) error {
	if options.Raw && options.Verbose {
		return errors.New("review: --raw and --verbose are mutually exclusive")
	}
	if options.Adapter == nil {
		return fmt.Errorf("review harness %q is not configured", options.ProjectConfig.Roles.Review.Harness)
	}
	preparation, err := prepareReview(ctx, options.ProjectRoot, options.TicketRef, options.Git)
	if err != nil {
		return err
	}
	if options.IdentificationBanner != nil {
		if err := options.IdentificationBanner(); err != nil {
			return err
		}
	}
	prompt := composeReviewPrompt(options.TicketRef, options.Ticket, preparation.branchPoint, preparation.diffPath)
	notifier := options.Notifier
	if !options.ProjectConfig.Notifications.Enabled {
		notifier = nil
	}
	questions := NewQuestionHandler(options.Input, options.Output, options.TicketRef, notifier)
	mode := QuietHarnessOutput
	if options.Verbose {
		mode = ParsedHarnessOutput
	}
	if options.Raw {
		mode = RawHarnessOutput
	}
	reviewVerdict, err := runReview(ctx, options.Adapter, harness.Request{
		Model: options.ProjectConfig.Roles.Review.Model, Effort: options.ProjectConfig.Roles.Review.Effort,
		Prompt: prompt, MCP: options.ProjectConfig.Roles.Review.MCP,
	}, options.Output, mode, questions)
	if err != nil {
		var unparseable *UnparseableVerdictError
		if errors.As(err, &unparseable) {
			if artifactErr := preparation.recorder.RecordReviewOutput(0, unparseable.Execution); artifactErr != nil {
				return fmt.Errorf("%w; save review run artifacts: %v", err, artifactErr)
			}
			return reviewTranscriptSavedError(err, preparation.recorder.Dir())
		}
		return err
	}
	if options.ProjectConfig.Tracker.Reviews == config.TrackerGitHub {
		if options.IssueTracker == nil || options.Ticket == nil {
			return errors.New("github review logging requires an issue reference (N or #N)")
		}
		if err := options.IssueTracker.AddComment(ctx, options.Ticket.Number, formatGitHubReviewComment(reviewVerdict)); err != nil {
			return fmt.Errorf("post review to GitHub issue #%d: %w", options.Ticket.Number, err)
		}
	} else {
		if _, err := writeLocalReviewLog(options.ProjectRoot, options.TicketRef, preparation.branchPoint, time.Now().UTC(), reviewVerdict); err != nil {
			return err
		}
	}
	if !options.Raw {
		if _, err := io.WriteString(options.Output, formatVerdict(reviewVerdict)); err != nil {
			return fmt.Errorf("write verdict: %w", err)
		}
	}
	if reviewVerdict.Status == verdict.Revise {
		return ErrReviewNeedsRevision
	}
	return nil
}

func composeReviewPrompt(ticketRef string, ticket *tracker.Ticket, branchPoint, diffPath string) string {
	prompt := fmt.Sprintf(reviewPrompt, diffPath, branchPoint)
	if ticket == nil {
		return prompt
	}
	return fmt.Sprintf("%s\n\nReview the current diff against this ticket (%s).\n\nTicket title: %s\n\nTicket body:\n%s", prompt, ticketRef, ticket.Title, ticket.Body)
}

func prepareReview(ctx context.Context, projectRoot, ticketRef string, git GitRunner) (reviewPreparation, error) {
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
	recorder, err := newReviewRunRecorder(projectRoot, ticketRef, branchPoint)
	if err != nil {
		return reviewPreparation{}, err
	}
	diffPath, err := recorder.RecordReviewDiff(0, diff)
	if err != nil {
		return reviewPreparation{}, fmt.Errorf("review: %w", err)
	}
	return reviewPreparation{branchPoint: branchPoint, diffPath: diffPath, recorder: recorder}, nil
}

func runReview(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (verdict.Verdict, error) {
	review, err := RunReviewExecution(ctx, adapter, request, output, mode, questions)
	if err != nil {
		return verdict.Verdict{}, err
	}
	return review.Verdict, nil
}

func RunReviewExecution(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	return runReviewExecution(ctx, adapter, request, output, mode, questions, nil)
}

func RunReviewExecutionWithProgress(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (ReviewExecution, error) {
	progress := newReviewProgressWriter(output)
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

	progress := newReviewProgressWriter(options.output)
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
	first, err := runHarnessConversation(ctx, adapter, options.start, conversationOptions{
		request:   options.request,
		output:    options.output,
		artifact:  &feed,
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
	if options.turnOutput != nil {
		if err := options.turnOutput.EndTurn(); err != nil {
			return ReviewExecution{}, fmt.Errorf("flush review output: %w", err)
		}
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
		artifact:  &feed,
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
	if options.turnOutput != nil {
		if err := options.turnOutput.EndTurn(); err != nil {
			return ReviewExecution{}, fmt.Errorf("flush review output: %w", err)
		}
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
	switch event.Type {
	case harness.EventAssistantText:
		if _, err := io.WriteString(output, event.Text); err != nil {
			return fmt.Errorf("write assistant output: %w", err)
		}
	case harness.EventToolUse:
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

func formatVerdict(reviewVerdict verdict.Verdict) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "VERDICT: %s\nSUMMARY: %s\nFINDINGS:\n", reviewVerdict.Status, reviewVerdict.Summary)
	for _, finding := range reviewVerdict.Findings {
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
	}
	return builder.String()
}

func formatGitHubReviewComment(reviewVerdict verdict.Verdict) string {
	return fmt.Sprintf("## Review verdict\n\n```text\n%s```\n", formatVerdict(reviewVerdict))
}

func writeLocalReviewLog(projectRoot, ticketRef, branchPoint string, timestamp time.Time, reviewVerdict verdict.Verdict) (string, error) {
	projectName := filepath.Base(filepath.Clean(projectRoot))
	if projectName == "." || projectName == string(filepath.Separator) || projectName == "" {
		projectName = "project"
	}
	reviewsDir := filepath.Join(projectRoot, ".scratch", projectName, "reviews")
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
	diff, err := git.Run(ctx, "diff", branchPoint)
	if err != nil {
		return "", fmt.Errorf("compute review diff against %s: %w", branchPoint, err)
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("pre-computed diff against %s is empty", branchPoint)
	}
	return diff, nil
}

func reviewTranscriptSavedError(err error, dir string) error {
	return fmt.Errorf("%w; full review transcript saved in run artifacts directory %s", err, dir)
}
