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

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/tracker"
	"github.com/igorrochap/rig/internal/verdict"
)

const reviewPrompt = `/code-review

Review the current working-tree diff for this project%s. Do not modify files. End the review with the mandatory verdict block from the code-review skill.

` + questionProtocolInstruction

var ErrReviewNeedsRevision = errors.New("review verdict is revise")

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

type HarnessOutputMode bool

const (
	ParsedHarnessOutput HarnessOutputMode = false
	RawHarnessOutput    HarnessOutputMode = true
)

type ReviewOptions struct {
	ProjectRoot   string
	ProjectConfig config.Config
	IssueTracker  tracker.Tracker
	Ticket        *tracker.Ticket
	TicketRef     string
	Adapter       harness.Adapter
	Input         io.Reader
	Output        io.Writer
	Raw           bool
	Notifier      Notifier
	Git           GitRunner
}

func RunReview(ctx context.Context, options ReviewOptions) error {
	if options.Adapter == nil {
		return fmt.Errorf("review harness %q is not configured", options.ProjectConfig.Roles.Review.Harness)
	}
	prompt := currentReviewPrompt()
	if options.Ticket != nil {
		prompt = composeReviewPrompt(options.TicketRef, *options.Ticket)
	}
	notifier := options.Notifier
	if !options.ProjectConfig.Notifications.Enabled {
		notifier = nil
	}
	questions := NewQuestionHandler(options.Input, options.Output, options.TicketRef, notifier)
	reviewVerdict, err := runReview(ctx, options.Adapter, harness.Request{
		Model: options.ProjectConfig.Roles.Review.Model, Effort: options.ProjectConfig.Roles.Review.Effort, Prompt: prompt,
	}, options.Output, options.Raw, questions)
	if err != nil {
		return err
	}
	if options.ProjectConfig.Tracker.Reviews == config.TrackerGitHub {
		if options.IssueTracker == nil || options.Ticket == nil {
			return errors.New("github review logging requires an issue reference (#N)")
		}
		if err := options.IssueTracker.AddComment(ctx, options.Ticket.Number, formatGitHubReviewComment(reviewVerdict)); err != nil {
			return fmt.Errorf("post review to GitHub issue #%d: %w", options.Ticket.Number, err)
		}
	} else {
		ref := reviewRef(ctx, options.Git)
		if _, err := writeLocalReviewLog(options.ProjectRoot, options.TicketRef, ref, time.Now().UTC(), reviewVerdict); err != nil {
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

func composeReviewPrompt(ticketRef string, ticket tracker.Ticket) string {
	return composeReviewPromptWithRef(ticketRef, ticket, "")
}

func composeReviewPromptAgainstRef(ticketRef string, ticket tracker.Ticket, reviewedRef string) string {
	return composeReviewPromptWithRef(ticketRef, ticket, reviewedRef)
}

func composeReviewPromptWithRef(ticketRef string, ticket tracker.Ticket, reviewedRef string) string {
	scope := ""
	if reviewedRef != "" {
		scope = fmt.Sprintf(" against the recorded branch point %s by examining `git diff %s`", reviewedRef, reviewedRef)
	}
	prompt := fmt.Sprintf(reviewPrompt, scope)
	return fmt.Sprintf("%s\n\nreview the current diff against this ticket (%s).\n\nTicket title: %s\n\nTicket body:\n%s", prompt, ticketRef, ticket.Title, ticket.Body)
}

func currentReviewPrompt() string {
	return fmt.Sprintf(reviewPrompt, "")
}

func runReview(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, raw bool, questions *QuestionHandler) (verdict.Verdict, error) {
	mode := ParsedHarnessOutput
	if raw {
		mode = RawHarnessOutput
	}
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

func runReviewExecution(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler, turnOutput reviewTurnOutput) (ReviewExecution, error) {
	var feed bytes.Buffer
	feedOutput := io.MultiWriter(output, &feed)
	first, err := runHarnessConversation(ctx, adapter, func(runContext context.Context) (harness.Stream, error) {
		return adapter.Run(runContext, request)
	}, conversationOptions{
		output:    feedOutput,
		mode:      mode,
		questions: questions,
	})
	if err != nil {
		return ReviewExecution{}, fmt.Errorf("run review harness: %w", err)
	}
	if turnOutput != nil {
		if err := turnOutput.EndTurn(); err != nil {
			return ReviewExecution{}, fmt.Errorf("flush review output: %w", err)
		}
	}
	reviewVerdict, parseErr := verdict.Parse(first.Transcript)
	if parseErr == nil {
		return ReviewExecution{Verdict: reviewVerdict, Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}, nil
	}
	if len(first.SessionIDs) == 0 {
		return ReviewExecution{Verdict: syntheticUnparseableVerdict(), Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}, nil
	}

	retry, err := retryReviewDetails(ctx, adapter, first.SessionIDs[len(first.SessionIDs)-1], feedOutput, mode, questions)
	if err != nil {
		return ReviewExecution{}, err
	}
	if turnOutput != nil {
		if err := turnOutput.EndTurn(); err != nil {
			return ReviewExecution{}, fmt.Errorf("flush review output: %w", err)
		}
	}
	transcript := appendReviewTranscript(first.Transcript, retry.Transcript)
	sessions := append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...)
	reviewVerdict, parseErr = verdict.Parse(transcript)
	if parseErr == nil {
		return ReviewExecution{Verdict: reviewVerdict, Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}, nil
	}

	return ReviewExecution{Verdict: syntheticUnparseableVerdict(), Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}, nil
}

func retryReviewDetails(ctx context.Context, adapter harness.Adapter, sessionID string, output io.Writer, mode HarnessOutputMode, questions *QuestionHandler) (harnessTranscript, error) {
	transcript, err := runHarnessConversation(ctx, adapter, func(resumeContext context.Context) (harness.Stream, error) {
		return adapter.Resume(resumeContext, sessionID, "emit the verdict block")
	}, conversationOptions{
		output:    output,
		mode:      mode,
		questions: questions,
		sessionID: sessionID,
	})
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

func syntheticUnparseableVerdict() verdict.Verdict {
	return verdict.Verdict{
		Status:  verdict.Revise,
		Summary: "Reviewer verdict was unparseable after one re-ask",
		Findings: []verdict.Finding{{
			Kind:     verdict.Blocking,
			Location: "reviewer",
			Issue:    "verdict was unparseable",
		}},
	}
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

func reviewRef(ctx context.Context, git GitRunner) string {
	if git == nil {
		return "working-tree"
	}
	output, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "working-tree"
	}
	if ref := strings.TrimSpace(output); ref != "" {
		return ref
	}
	return "working-tree"
}

func writeLocalReviewLog(projectRoot, ticketRef, reviewedRef string, timestamp time.Time, reviewVerdict verdict.Verdict) (string, error) {
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
	contents := fmt.Sprintf("# Review: %s\n\n%sReviewed ref: %s\nTimestamp: %s\n\n## Verdict\n\n%s", title, ticketLine, reviewedRef, timestamp.Format(time.RFC3339Nano), formatVerdict(reviewVerdict))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return "", fmt.Errorf("write local review log: %w", err)
	}
	return path, nil
}
