package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/tracker"
	"github.com/igorrochap/rig/internal/verdict"
	"github.com/spf13/cobra"
)

const implementPrompt = `/implement

Implement the ticket below in the current project. Use the vendored implement skill and leave the working tree with the requested changes for review. Do not commit or push changes.

` + questionProtocolInstruction + `

Ticket: %s
Title: %s

Ticket body (including acceptance criteria):
%s`

const reviseImplementPrompt = `/fix-review

Address ONLY the reviewer's [blocking] findings listed below. Use the vendored fix-review skill. Leave the working tree with the changes for review and do not commit or push changes.

` + questionProtocolInstruction + `

Ticket: %s

Blocking findings:
%s`

var branchTypePattern = regexp.MustCompile(`^(feat|fix|refactor|chore|docs|test|perf|build|ci)$`)

type implementSetup struct {
	git         GitRunner
	branch      string
	branchPoint string
}

type runArtifacts struct {
	dir      string
	sessions []string
}

func newRunArtifacts(projectRoot string, issueNumber int, branch, branchPoint string) (runArtifacts, error) {
	dir := filepath.Join(projectRoot, ".rig", "runs", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+strconv.Itoa(issueNumber))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return runArtifacts{}, fmt.Errorf("create implement run artifacts: %w", err)
	}
	metadata := fmt.Sprintf("Branch: %s\nBranch point: %s\n", branch, branchPoint)
	if err := writeArtifact(filepath.Join(dir, "metadata.txt"), metadata); err != nil {
		return runArtifacts{}, err
	}
	return runArtifacts{dir: dir}, nil
}

func (a *App) runImplement(ctx context.Context, command *cobra.Command, projectConfig config.Config, issueTracker tracker.Tracker, ticket tracker.Ticket) error {
	setup, err := a.prepareImplement(ctx, issueTracker, ticket)
	if err != nil {
		return err
	}

	implementer, reviewer, err := a.implementationHarnesses(projectConfig)
	if err != nil {
		return err
	}
	questions := a.questionHandler(command.InOrStdin(), command.OutOrStdout(), "#"+strconv.Itoa(ticket.Number), projectConfig.Notifications.Enabled)
	artifacts, err := newRunArtifacts(a.projectRoot, ticket.Number, setup.branch, setup.branchPoint)
	if err != nil {
		return err
	}
	iterations, final, nits, err := runImplementIterations(ctx, setup.git, implementer, reviewer, projectConfig, ticket, setup.branchPoint, &artifacts, questions)
	if err != nil {
		return err
	}

	diffStat, err := setup.git.Run(ctx, "diff", "--stat", setup.branchPoint)
	if err != nil {
		diffStat = fmt.Sprintf("unavailable: %v", err)
	}
	if err := artifacts.writeSummary(iterations, final, nits, diffStat); err != nil {
		return err
	}
	summary := formatImplementSummary(iterations, final, nits, diffStat)
	if _, err := io.WriteString(command.OutOrStdout(), summary); err != nil {
		return fmt.Errorf("write implement summary: %w", err)
	}
	if err := artifacts.writeSessions(); err != nil {
		return err
	}
	if notifier := a.deps.Notifier; projectConfig.Notifications.Enabled {
		if notifier == nil {
			notifier = newPlatformNotifier()
		}
		_ = notifier.Notify(ctx, fmt.Sprintf("implement #%d finished: %s", ticket.Number, final.Status))
	}
	if final.Status == verdict.Revise {
		return fmt.Errorf("implement loop reached max iterations (%d) with revise verdict", projectConfig.Loop.MaxIterations)
	}
	return nil
}

func (a *App) prepareImplement(ctx context.Context, issueTracker tracker.Tracker, ticket tracker.Ticket) (implementSetup, error) {
	git := a.gitRunner()
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

func (a *App) implementationHarnesses(projectConfig config.Config) (harness.Adapter, harness.Adapter, error) {
	implementer, ok := a.deps.Harnesses[string(projectConfig.Roles.Implement.Harness)]
	if !ok || implementer == nil {
		return nil, nil, fmt.Errorf("implement harness %q is not configured", projectConfig.Roles.Implement.Harness)
	}
	reviewer, ok := a.deps.Harnesses[string(projectConfig.Roles.Review.Harness)]
	if !ok || reviewer == nil {
		return nil, nil, fmt.Errorf("review harness %q is not configured", projectConfig.Roles.Review.Harness)
	}
	return implementer, reviewer, nil
}

func runImplementIterations(ctx context.Context, git GitRunner, implementer, reviewer harness.Adapter, projectConfig config.Config, ticket tracker.Ticket, branchPoint string, artifacts *runArtifacts, questions *questionHandler) (int, verdict.Verdict, []verdict.Finding, error) {
	var blocking []verdict.Finding
	var nits []verdict.Finding
	var final verdict.Verdict
	iterations := 0
	for iteration := 1; iteration <= projectConfig.Loop.MaxIterations; iteration++ {
		iterations = iteration
		implementRequest := harness.Request{
			Model:  projectConfig.Roles.Implement.Model,
			Effort: projectConfig.Roles.Implement.Effort,
			Prompt: composeImplementPrompt(ticket, blocking, iteration),
		}
		implementResult, err := runImplementRole(ctx, implementer, implementRequest, io.Discard, artifacts.dir, iteration, questions)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		artifacts.recordSessions(iteration, "implement", implementResult.SessionIDs)
		if err := ensureHeadUnchanged(ctx, git, branchPoint); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}

		reviewRequest := harness.Request{
			Model:  projectConfig.Roles.Review.Model,
			Effort: projectConfig.Roles.Review.Effort,
			Prompt: composeReviewPromptAgainstRef("#"+strconv.Itoa(ticket.Number), ticket, branchPoint),
		}
		reviewResult, err := runReviewExecution(ctx, reviewer, reviewRequest, io.Discard, parsedHarnessOutput, questions)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if err := artifacts.recordReview(iteration, reviewResult); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if err := ensureHeadUnchanged(ctx, git, branchPoint); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		artifacts.recordSessions(iteration, "review", reviewResult.SessionIDs)
		final = reviewResult.Verdict
		nits = append(nits, nitFindings(final)...)
		if final.Status == verdict.Approve {
			break
		}
		blocking = blockingFindings(final)
	}
	return iterations, final, nits, nil
}

func composeImplementPrompt(ticket tracker.Ticket, blocking []verdict.Finding, iteration int) string {
	if iteration == 1 {
		return fmt.Sprintf(implementPrompt, "#"+strconv.Itoa(ticket.Number), ticket.Title, ticket.Body)
	}
	return fmt.Sprintf(reviseImplementPrompt, "#"+strconv.Itoa(ticket.Number), formatBlockingFindings(blocking))
}

func runImplementRole(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, artifactDir string, iteration int, questions *questionHandler) (harnessTranscript, error) {
	var feed bytes.Buffer
	result, err := runHarnessConversation(ctx, adapter, func(runContext context.Context) (harness.Stream, error) {
		return adapter.Run(runContext, request)
	}, conversationOptions{
		output:    io.MultiWriter(output, &feed),
		mode:      parsedHarnessOutput,
		questions: questions,
	})
	if err != nil {
		return harnessTranscript{}, fmt.Errorf("run implement harness: %w", err)
	}
	if err := writeArtifact(filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d-implement.feed", iteration)), feed.String()); err != nil {
		return harnessTranscript{}, err
	}
	if err := writeArtifact(filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d-implement.transcript", iteration)), result.Transcript); err != nil {
		return harnessTranscript{}, err
	}
	return result, nil
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
		if branchType != "" || strings.Contains(candidate, "implement") || strings.Contains(candidate, "rig") {
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

func formatBlockingFindings(findings []verdict.Finding) string {
	if len(findings) == 0 {
		return "(none)"
	}
	var builder strings.Builder
	for _, finding := range findings {
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
	}
	return strings.TrimSuffix(builder.String(), "\n")
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

func (r *runArtifacts) recordSessions(iteration int, role string, sessionIDs []string) {
	for _, sessionID := range sessionIDs {
		if strings.TrimSpace(sessionID) == "" {
			continue
		}
		r.sessions = append(r.sessions, fmt.Sprintf("iteration %d %s: %s", iteration, role, sessionID))
	}
}

func (r *runArtifacts) recordReview(iteration int, review reviewExecution) error {
	if err := writeArtifact(filepath.Join(r.dir, fmt.Sprintf("iteration-%02d-review.feed", iteration)), review.Feed); err != nil {
		return err
	}
	if err := writeArtifact(filepath.Join(r.dir, fmt.Sprintf("iteration-%02d-review.transcript", iteration)), review.Transcript); err != nil {
		return err
	}
	return writeArtifact(filepath.Join(r.dir, fmt.Sprintf("iteration-%02d-verdict.txt", iteration)), formatVerdict(review.Verdict))
}

func (r *runArtifacts) writeSessions() error {
	sessions := append([]string(nil), r.sessions...)
	sort.Strings(sessions)
	return writeArtifact(filepath.Join(r.dir, "sessions.txt"), strings.Join(sessions, "\n")+"\n")
}

func (r *runArtifacts) writeSummary(iterations int, final verdict.Verdict, nits []verdict.Finding, diffStat string) error {
	return writeArtifact(filepath.Join(r.dir, "summary.txt"), formatImplementSummary(iterations, final, nits, diffStat))
}

func writeArtifact(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}
