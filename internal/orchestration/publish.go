package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/igorrochap/syl/internal/tracker"
)

func publishApprovedImplement(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) (string, bool, error) {
	if _, err := setup.git.Run(ctx, "add", "-A"); err != nil {
		return "", false, fmt.Errorf("stage approved implementation: %w", err)
	}
	changedPaths, err := setup.git.Run(ctx, "diff", "--cached", "--name-only", setup.branchPoint)
	if err != nil {
		return "", false, fmt.Errorf("check approved implementation diff: %w", err)
	}
	if strings.TrimSpace(changedPaths) == "" {
		return "", true, nil
	}
	branchType, _, ok := strings.Cut(setup.branch, "/")
	if !ok || branchType == "" {
		return "", false, fmt.Errorf("create implementation commit: branch %q has no conventional type", setup.branch)
	}
	message := fmt.Sprintf("%s: %s (#%d)", branchType, options.Ticket.Title, options.Ticket.Number)
	if _, err := setup.git.Run(ctx, "commit", "-m", message); err != nil {
		return "", false, fmt.Errorf("commit approved implementation: %w", err)
	}
	if _, err := setup.git.Run(ctx, "push", "-u", "origin", setup.branch); err != nil {
		return "", false, fmt.Errorf("push implementation branch %q: %w", setup.branch, err)
	}
	publisher, ok := options.IssueTracker.(tracker.PullRequestCreator)
	if !ok {
		return "", false, fmt.Errorf("create pull request: Tracker cannot create pull requests")
	}
	pullRequestURL, err := publisher.CreatePullRequest(ctx, tracker.PullRequest{
		Title: options.Ticket.Title,
		Body:  pullRequestBody(options.Ticket.Number, summary),
		Head:  setup.branch,
		Base:  setup.baseBranch,
	})
	if err != nil {
		return "", false, fmt.Errorf("create draft pull request: %w", err)
	}
	return pullRequestURL, false, nil
}

func pullRequestBody(ticketNumber int, summary implementSummary) string {
	var body strings.Builder
	fmt.Fprintf(&body, "Closes #%d\n\n%s", ticketNumber, summary.final.Summary)
	for _, finding := range summary.nits {
		fmt.Fprintf(&body, "\n\n- [ ] %s — %s", finding.Location, finding.Issue)
	}
	return body.String()
}
