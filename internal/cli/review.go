package cli

import (
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

Review the current working-tree diff for this project. Do not modify files. End the review with the mandatory verdict block from the code-review skill.`

var errReviewNeedsRevision = errors.New("review verdict is revise")

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

			prompt := reviewPrompt
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
	return fmt.Sprintf("%s\n\nreview the current diff against this ticket (%s).\n\nTicket title: %s\n\nTicket body:\n%s", reviewPrompt, ticketRef, ticket.Title, ticket.Body)
}

func runReview(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, raw bool) (verdict.Verdict, error) {
	stream, err := adapter.Run(ctx, request)
	if err != nil {
		return verdict.Verdict{}, fmt.Errorf("run review harness: %w", err)
	}
	transcript, sessionID, err := consumeHarnessStream(stream, output, raw)
	if err != nil {
		return verdict.Verdict{}, err
	}
	reviewVerdict, parseErr := verdict.Parse(transcript)
	if parseErr == nil {
		return reviewVerdict, nil
	}
	if sessionID == "" {
		return syntheticUnparseableVerdict(), nil
	}

	retryTranscript, err := retryReview(ctx, adapter, sessionID, output, raw)
	if err != nil {
		return verdict.Verdict{}, err
	}
	reviewVerdict, parseErr = verdict.Parse(appendReviewTranscript(transcript, retryTranscript))
	if parseErr == nil {
		return reviewVerdict, nil
	}

	return syntheticUnparseableVerdict(), nil
}

func retryReview(ctx context.Context, adapter harness.Adapter, sessionID string, output io.Writer, raw bool) (string, error) {
	retry, err := adapter.Resume(ctx, sessionID, "emit the verdict block")
	if err != nil {
		return "", fmt.Errorf("re-ask reviewer for verdict: %w", err)
	}
	transcript, _, err := consumeHarnessStream(retry, output, raw)
	if err != nil {
		return "", err
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

func consumeHarnessStream(stream harness.Stream, output io.Writer, raw bool) (string, string, error) {
	var transcript strings.Builder
	var sessionID string
	harnessFailed := false
	var harnessError string
	for event := range stream.Events() {
		if raw {
			if err := writeRawEvent(output, event); err != nil {
				return "", "", err
			}
		} else {
			if err := writeParsedEvent(output, event); err != nil {
				return "", "", err
			}
		}

		if event.SessionID != "" {
			sessionID = event.SessionID
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
		return "", "", fmt.Errorf("read review harness: %w", err)
	}
	if harnessFailed {
		if harnessError == "" {
			harnessError = "unknown harness error"
		}
		return "", "", fmt.Errorf("review harness returned an error: %s", harnessError)
	}
	return transcript.String(), sessionID, nil
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
