package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/tracker"
)

func publishApprovedImplement(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) runrecord.Publication {
	if _, err := setup.git.Run(ctx, "add", "-A"); err != nil {
		return failedPublication(runrecord.PublicationStepCommit, fmt.Errorf("stage approved implementation: %w", err))
	}
	changedPaths, err := setup.git.Run(ctx, "diff", "--cached", "--name-only", setup.branchPoint)
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, fmt.Errorf("check approved implementation diff: %w", err))
	}
	if strings.TrimSpace(changedPaths) == "" {
		return runrecord.Publication{Status: runrecord.PublicationSkipped}
	}
	message, err := implementationCommitMessage(options, setup.branch)
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, err)
	}
	if _, err := setup.git.Run(ctx, "commit", "-m", message); err != nil {
		return failedPublication(runrecord.PublicationStepCommit, fmt.Errorf("commit approved implementation: %w", err))
	}
	if _, err := setup.git.Run(ctx, "push", "-u", "origin", setup.branch); err != nil {
		return failedPublication(runrecord.PublicationStepPush, fmt.Errorf("push implementation branch %q: %w", setup.branch, err))
	}
	publisher, ok := options.IssueTracker.(tracker.PullRequestCreator)
	if !ok {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create pull request: Tracker cannot create pull requests"))
	}
	pullRequestURL, err := publisher.CreatePullRequest(ctx, tracker.PullRequest{
		Title: options.Ticket.Title,
		Body:  pullRequestBody(options.Ticket.Number, summary),
		Head:  setup.branch,
		Base:  setup.baseBranch,
	})
	if err != nil {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create draft pull request: %w", err))
	}
	pullRequestURL = strings.TrimSpace(pullRequestURL)
	if pullRequestURL == "" {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create draft pull request: tracker returned an empty URL"))
	}
	return runrecord.Publication{Status: runrecord.PublicationPublished, URL: pullRequestURL}
}

func failedPublication(step runrecord.PublicationStep, err error) runrecord.Publication {
	return runrecord.Publication{Status: runrecord.PublicationFailed, Step: step, Error: err.Error()}
}

func implementationCommitMessage(options ImplementOptions, branch string) (string, error) {
	branchType, _, ok := strings.Cut(branch, "/")
	if !ok || branchType == "" {
		return "", fmt.Errorf("create implementation commit: branch %q has no conventional type", branch)
	}
	return fmt.Sprintf("%s: %s (#%d)", branchType, options.Ticket.Title, options.Ticket.Number), nil
}

func publicationCommands(
	step runrecord.PublicationStep,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) []string {
	commands := []string{"git push -u origin " + shellArgument(setup.branch), pullRequestCommand(options, setup, summary)}
	if step == runrecord.PublicationStepCommit {
		message, err := implementationCommitMessage(options, setup.branch)
		if err != nil {
			message = fmt.Sprintf("implement: %s (#%d)", options.Ticket.Title, options.Ticket.Number)
		}
		commands = append([]string{"git add -A", "git commit -m " + shellQuote(message)}, commands...)
	}
	if step == runrecord.PublicationStepPullRequest {
		return commands[len(commands)-1:]
	}
	if step == runrecord.PublicationStepPush {
		return commands
	}
	if step == runrecord.PublicationStepCommit {
		return commands
	}
	return nil
}

func pullRequestCommand(options ImplementOptions, setup implementSetup, summary implementSummary) string {
	command := "gh pr create --draft --title " + shellQuote(options.Ticket.Title) +
		" --body " + shellQuote(pullRequestBody(options.Ticket.Number, summary)) +
		" --head " + shellArgument(setup.branch)
	if setup.baseBranch != "" {
		command += " --base " + shellArgument(setup.baseBranch)
	}
	return command
}

func formatPublicationFailure(publication runrecord.Publication, commands []string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "publication failed during %s: %s\nManual commands to finish publication:\n", publication.Step, publication.Error)
	for _, command := range commands {
		fmt.Fprintf(&report, "  %s\n", command)
	}
	return strings.TrimRight(report.String(), "\n")
}

func shellArgument(value string) string {
	if value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-") == "" {
		return value
	}
	return shellQuote(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func pullRequestBody(ticketNumber int, summary implementSummary) string {
	var body strings.Builder
	fmt.Fprintf(&body, "Closes #%d\n\n%s", ticketNumber, summary.final.Summary)
	for _, finding := range summary.nits {
		fmt.Fprintf(&body, "\n\n- [ ] %s — %s", finding.Location, finding.Issue)
	}
	return body.String()
}
