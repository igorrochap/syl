package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/tracker"
	"github.com/igorrochap/rig/internal/verdict"
	"github.com/spf13/cobra"
)

const reviewPrompt = `/code-review

Review the current working-tree diff for this project%s. Do not modify files. End the review with the mandatory verdict block from the code-review skill.`

var errReviewNeedsRevision = errors.New("review verdict is revise")

type reviewExecution struct {
	Verdict    verdict.Verdict
	Transcript string
	Feed       string
	SessionIDs []string
}

type harnessTranscript struct {
	Transcript string
	SessionIDs []string
}

type harnessOutputMode bool

const (
	parsedHarnessOutput harnessOutputMode = false
	rawHarnessOutput    harnessOutputMode = true
)

func (a *App) reviewCommand() *cobra.Command {
	var raw bool
	command := &cobra.Command{
		Use:   "review [#N]",
		Short: "review the current working-tree changes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectConfig, err := config.Load(a.projectRoot)
			if err != nil {
				return err
			}
			if projectConfig.Tracker.Reviews == config.TrackerGitHub && len(args) == 0 {
				return errors.New("github review logging requires an issue reference (#N)")
			}

			var ticket *tracker.Ticket
			ticketRef := ""
			var issueTracker tracker.Tracker
			if len(args) == 1 {
				issueTracker, err = a.newIssueTracker(projectConfig)
				if err != nil {
					return err
				}
				resolved, err := issueTracker.Resolve(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				ticket = &resolved
				ticketRef = strings.TrimSpace(args[0])
			}

			harnessName := string(projectConfig.Roles.Review.Harness)
			adapter, ok := a.deps.Harnesses[harnessName]
			if !ok || adapter == nil {
				return fmt.Errorf("review harness %q is not configured", harnessName)
			}

			prompt := currentReviewPrompt()
			if ticket != nil {
				prompt = composeReviewPrompt(ticketRef, *ticket)
			}
			reviewVerdict, err := runReview(cmd.Context(), adapter, harness.Request{
				Model:  projectConfig.Roles.Review.Model,
				Effort: projectConfig.Roles.Review.Effort,
				Prompt: prompt,
			}, cmd.OutOrStdout(), raw)
			if err != nil {
				return err
			}

			if projectConfig.Tracker.Reviews == config.TrackerGitHub {
				if err := issueTracker.AddComment(cmd.Context(), ticket.Number, formatGitHubReviewComment(reviewVerdict)); err != nil {
					return fmt.Errorf("post review to GitHub issue #%d: %w", ticket.Number, err)
				}
			} else {
				ref := reviewRef(a.projectRoot)
				if _, err := writeLocalReviewLog(a.projectRoot, ticketRef, ref, time.Now().UTC(), reviewVerdict); err != nil {
					return err
				}
			}
			if !raw {
				if _, err := io.WriteString(cmd.OutOrStdout(), formatVerdict(reviewVerdict)); err != nil {
					return fmt.Errorf("write verdict: %w", err)
				}
			}
			if reviewVerdict.Status == verdict.Revise {
				return errReviewNeedsRevision
			}
			return nil
		},
	}
	command.Flags().BoolVar(&raw, "raw", false, "pass the harness output through untouched")
	return command
}

func (a *App) newIssueTracker(projectConfig config.Config) (tracker.Tracker, error) {
	if projectConfig.Tracker.Issues == config.TrackerGitHub {
		return tracker.NewGitHub(a.deps.GH)
	}
	return tracker.NewLocal(a.projectRoot, "")
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

func runReview(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, raw bool) (verdict.Verdict, error) {
	mode := parsedHarnessOutput
	if raw {
		mode = rawHarnessOutput
	}
	review, err := runReviewExecution(ctx, adapter, request, output, mode)
	if err != nil {
		return verdict.Verdict{}, err
	}
	return review.Verdict, nil
}

func runReviewExecution(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, mode harnessOutputMode) (reviewExecution, error) {
	stream, err := adapter.Run(ctx, request)
	if err != nil {
		return reviewExecution{}, fmt.Errorf("run review harness: %w", err)
	}
	var feed bytes.Buffer
	feedOutput := io.MultiWriter(output, &feed)
	first, err := consumeHarnessStreamDetails(stream, feedOutput, mode)
	if err != nil {
		return reviewExecution{}, err
	}
	reviewVerdict, parseErr := verdict.Parse(first.Transcript)
	if parseErr == nil {
		return reviewExecution{Verdict: reviewVerdict, Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}, nil
	}
	if len(first.SessionIDs) == 0 {
		return reviewExecution{Verdict: syntheticUnparseableVerdict(), Transcript: first.Transcript, Feed: feed.String(), SessionIDs: first.SessionIDs}, nil
	}

	retry, err := retryReviewDetails(ctx, adapter, first.SessionIDs[len(first.SessionIDs)-1], feedOutput, mode)
	if err != nil {
		return reviewExecution{}, err
	}
	transcript := appendReviewTranscript(first.Transcript, retry.Transcript)
	sessions := append(append([]string(nil), first.SessionIDs...), retry.SessionIDs...)
	reviewVerdict, parseErr = verdict.Parse(transcript)
	if parseErr == nil {
		return reviewExecution{Verdict: reviewVerdict, Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}, nil
	}

	return reviewExecution{Verdict: syntheticUnparseableVerdict(), Transcript: transcript, Feed: feed.String(), SessionIDs: sessions}, nil
}

func retryReviewDetails(ctx context.Context, adapter harness.Adapter, sessionID string, output io.Writer, mode harnessOutputMode) (harnessTranscript, error) {
	retry, err := adapter.Resume(ctx, sessionID, "emit the verdict block")
	if err != nil {
		return harnessTranscript{}, fmt.Errorf("re-ask reviewer for verdict: %w", err)
	}
	transcript, err := consumeHarnessStreamDetails(retry, output, mode)
	if err != nil {
		return harnessTranscript{}, err
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

func consumeHarnessStreamDetails(stream harness.Stream, output io.Writer, mode harnessOutputMode) (harnessTranscript, error) {
	var transcript strings.Builder
	var sessionIDs []string
	harnessFailed := false
	var harnessError string
	for event := range stream.Events() {
		if mode == rawHarnessOutput {
			if err := writeRawEvent(output, event); err != nil {
				return harnessTranscript{}, err
			}
		} else {
			if err := writeParsedEvent(output, event); err != nil {
				return harnessTranscript{}, err
			}
		}

		if event.SessionID != "" {
			sessionIDs = append(sessionIDs, event.SessionID)
		}
		if event.Type == harness.EventAssistantText || event.Type == harness.EventResult {
			transcript.WriteString(event.Text)
		}
		if event.Type == harness.EventResult && event.IsError {
			harnessFailed = true
			harnessError = event.Text
		}
	}
	if err := stream.Wait(); err != nil {
		return harnessTranscript{}, fmt.Errorf("read review harness: %w", err)
	}
	if harnessFailed {
		if harnessError == "" {
			harnessError = "unknown harness error"
		}
		return harnessTranscript{}, fmt.Errorf("review harness returned an error: %s", harnessError)
	}
	return harnessTranscript{Transcript: transcript.String(), SessionIDs: sessionIDs}, nil
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

func reviewRef(projectRoot string) string {
	output, err := exec.Command("git", "-C", projectRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		return "working-tree"
	}
	if ref := strings.TrimSpace(string(output)); ref != "" {
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
