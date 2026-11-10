package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

func publishApprovedImplement(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) (runrecord.Publication, []string) {
	var notices []string
	hasChanges, err := stageApprovedImplementation(ctx, setup)
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, err), notices
	}
	if !hasChanges {
		return runrecord.Publication{Status: runrecord.PublicationSkipped}, notices
	}
	message, err := implementationCommitMessage(options, setup.branch)
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, err), notices
	}
	notice, err := commitApprovedImplementation(ctx, options, setup, message)
	if notice != "" {
		notices = append(notices, notice)
	}
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, fmt.Errorf("commit approved implementation: %w", err)), notices
	}
	if _, err := setup.git.Run(ctx, "push", "-u", "origin", setup.branch); err != nil {
		return failedPublication(runrecord.PublicationStepPush, fmt.Errorf("push implementation branch %q: %w", setup.branch, err)), notices
	}
	pullRequest, notice := commitgenPullRequest(ctx, options, setup, summary)
	if notice != "" {
		notices = append(notices, notice)
	}
	return createDraftPullRequest(ctx, options, pullRequest, notices)
}

func stageApprovedImplementation(ctx context.Context, setup implementSetup) (bool, error) {
	if _, err := setup.git.Run(ctx, "add", "-A"); err != nil {
		return false, fmt.Errorf("stage approved implementation: %w", err)
	}
	changedPaths, err := setup.git.Run(ctx, "diff", "--cached", "--name-only", setup.branchPoint)
	if err != nil {
		return false, fmt.Errorf("check approved implementation diff: %w", err)
	}
	return strings.TrimSpace(changedPaths) != "", nil
}

func createDraftPullRequest(
	ctx context.Context,
	options ImplementOptions,
	request tracker.PullRequest,
	notices []string,
) (runrecord.Publication, []string) {
	publisher, ok := options.IssueTracker.(tracker.PullRequestCreator)
	if !ok {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create pull request: Tracker cannot create pull requests")), notices
	}
	pullRequestURL, err := publisher.CreatePullRequest(ctx, request)
	if err != nil {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create draft pull request: %w", err)), notices
	}
	pullRequestURL = strings.TrimSpace(pullRequestURL)
	if pullRequestURL == "" {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create draft pull request: tracker returned an empty URL")), notices
	}
	return runrecord.Publication{Status: runrecord.PublicationPublished, URL: pullRequestURL}, notices
}

func commitApprovedImplementation(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	fallbackMessage string,
) (string, error) {
	if options.Commitgen == nil || !options.Commitgen.Available() {
		if _, commitErr := setup.git.Run(ctx, "commit", "-m", fallbackMessage); commitErr != nil {
			return "commitgen unavailable; used fallback commit message", commitErr
		}
		return "commitgen unavailable; used fallback commit message", nil
	}

	snapshot, err := snapshotStagedPublication(ctx, setup.git)
	if err != nil {
		return "", err
	}

	_, commandErr := options.Commitgen.Run(ctx, options.WorkRoot,
		"--agent", "--context", fmt.Sprintf("Ticket #%d: %s", options.Ticket.Number, options.Ticket.Title),
	)
	completeCommit, headMoved, verifyErr := commitgenCreatedOneCompleteCommit(ctx, setup.git, snapshot.head, snapshot.tree)
	if commandErr == nil && verifyErr == nil && completeCommit {
		return "", nil
	}

	reason := commitgenFallbackNotice(commandErr, verifyErr, headMoved)
	return fallbackPublicationCommit(ctx, setup, snapshot.head, snapshot.tree, fallbackMessage, reason)
}

type stagedPublicationSnapshot struct {
	head string
	tree string
}

func snapshotStagedPublication(ctx context.Context, git GitRunner) (stagedPublicationSnapshot, error) {
	head, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return stagedPublicationSnapshot{}, fmt.Errorf("record HEAD before commitgen: %w", err)
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return stagedPublicationSnapshot{}, errors.New("record HEAD before commitgen: git returned an empty ref")
	}
	tree, err := git.Run(ctx, "write-tree")
	if err != nil {
		return stagedPublicationSnapshot{}, fmt.Errorf("record staged implementation tree before commitgen: %w", err)
	}
	tree = strings.TrimSpace(tree)
	if tree == "" {
		return stagedPublicationSnapshot{}, errors.New("record staged implementation tree before commitgen: git returned an empty tree")
	}
	return stagedPublicationSnapshot{head: head, tree: tree}, nil
}

func commitgenFallbackNotice(commandErr, verifyErr error, headMoved bool) string {
	switch {
	case commandErr != nil:
		return "commitgen failed; used fallback commit message"
	case verifyErr != nil:
		return "commitgen commit could not be verified; used fallback commit message"
	case headMoved:
		return "commitgen did not create one complete commit; used fallback commit message"
	default:
		return "commitgen left HEAD unchanged; used fallback commit message"
	}
}

func commitgenCreatedOneCompleteCommit(
	ctx context.Context,
	git GitRunner,
	headBefore string,
	stagedTree string,
) (bool, bool, error) {
	headAfter, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return false, false, err
	}
	headAfter = strings.TrimSpace(headAfter)
	if headAfter == headBefore {
		return false, false, nil
	}
	parents, err := git.Run(ctx, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return false, true, fmt.Errorf("inspect commit parents: %w", err)
	}
	tree, err := git.Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return false, true, err
	}
	parentFields := strings.Fields(parents)
	completeCommit := len(parentFields) == 2 && parentFields[0] == headAfter &&
		parentFields[1] == headBefore && strings.TrimSpace(tree) == stagedTree
	return completeCommit, true, nil
}

func fallbackPublicationCommit(
	ctx context.Context,
	setup implementSetup,
	headBefore string,
	stagedTree string,
	fallbackMessage string,
	reason string,
) (string, error) {
	if _, err := setup.git.Run(ctx, "reset", "--soft", headBefore); err != nil {
		return reason, fmt.Errorf("restore HEAD after commitgen failure: %w", err)
	}
	if _, err := setup.git.Run(ctx, "read-tree", stagedTree); err != nil {
		return reason, fmt.Errorf("restore staged implementation after commitgen failure: %w", err)
	}
	if _, err := setup.git.Run(ctx, "commit", "-m", fallbackMessage); err != nil {
		return reason, err
	}
	return reason, nil
}

func commitgenPullRequest(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) (tracker.PullRequest, string) {
	fallback := tracker.PullRequest{
		Title: options.Ticket.Title,
		Body:  pullRequestBody(options.Ticket.Number, summary),
		Head:  setup.branch,
		Base:  setup.baseBranch,
	}
	if options.Commitgen == nil || !options.Commitgen.Available() {
		return fallback, "commitgen unavailable; used fallback pull request text"
	}

	base := strings.TrimSpace(setup.baseBranch)
	var err error
	if base == "" {
		resolver, ok := options.IssueTracker.(tracker.PullRequestBaseResolver)
		if !ok {
			return fallback, "forge default branch unavailable; used fallback pull request text"
		}
		base, err = resolver.DefaultPullRequestBase(ctx)
		if err != nil || strings.TrimSpace(base) == "" {
			return fallback, "forge default branch unavailable; used fallback pull request text"
		}
		base = strings.TrimSpace(base)
	}

	output, err := options.Commitgen.Run(ctx, options.WorkRoot, "pr", "--agent", "--base", base, "--head", setup.branch)
	if err != nil {
		return pullRequestWithBase(fallback, base), "commitgen pr failed; used fallback pull request text"
	}
	title, description, ok := parseCommitgenPullRequest(output)
	if !ok {
		return pullRequestWithBase(fallback, base), "commitgen pr printed no usable title; used fallback pull request text"
	}

	description = ensureClosingReference(description, options.Ticket.Number)
	return tracker.PullRequest{
		Title: title,
		Body:  appendNitChecklist(description, summary.nits),
		Head:  setup.branch,
		Base:  base,
	}, ""
}

func pullRequestWithBase(request tracker.PullRequest, base string) tracker.PullRequest {
	request.Base = base
	return request
}

func parseCommitgenPullRequest(output string) (string, string, bool) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for index, line := range lines {
		title := strings.TrimSpace(line)
		if title == "" {
			continue
		}
		for _, prefix := range []string{"Title:", "Titulo:", "Título:"} {
			if strings.HasPrefix(title, prefix) {
				title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
				break
			}
		}
		if title == "" {
			return "", "", false
		}
		return title, strings.TrimSpace(strings.Join(lines[index+1:], "\n")), true
	}
	return "", "", false
}

func ensureClosingReference(description string, ticketNumber int) string {
	reference := fmt.Sprintf("Closes #%d", ticketNumber)
	first := closingReferenceIndex(description, reference, 0)
	if first < 0 {
		if description == "" {
			return reference
		}
		return description + "\n\n" + reference
	}
	suffix := description[first+len(reference):]
	var clean strings.Builder
	clean.WriteString(description[:first+len(reference)])
	position := 0
	for {
		duplicate := closingReferenceIndex(suffix, reference, position)
		if duplicate < 0 {
			clean.WriteString(suffix[position:])
			return strings.TrimSpace(clean.String())
		}
		clean.WriteString(suffix[position:duplicate])
		position = duplicate + len(reference)
	}
}

func closingReferenceIndex(description, reference string, start int) int {
	for start < len(description) {
		match := strings.Index(description[start:], reference)
		if match < 0 {
			return -1
		}
		index := start + match
		after := index + len(reference)
		if after == len(description) || description[after] < '0' || description[after] > '9' {
			return index
		}
		start = after
	}
	return -1
}

func appendNitChecklist(description string, nits []verdict.Finding) string {
	var body strings.Builder
	body.WriteString(strings.TrimSpace(description))
	for _, finding := range nits {
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		fmt.Fprintf(&body, "- [ ] %s — %s", finding.Location, finding.Issue)
	}
	return body.String()
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
	body := fmt.Sprintf("Closes #%d\n\n%s", ticketNumber, summary.final.Summary)
	return appendNitChecklist(ensureClosingReference(body, ticketNumber), summary.nits)
}
