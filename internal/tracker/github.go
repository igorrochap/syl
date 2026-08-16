package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	githubIssueFields = "number,title,body,state,labels"
	githubTodoLabel   = "todo"
	githubDoingLabel  = "doing"
	githubTodoColor   = "0E8A16"
	githubDoingColor  = "5319E7"
)

type githubErrorKind uint8

const (
	githubErrorUnknown githubErrorKind = iota
	githubErrorNotInstalled
	githubErrorUnauthenticated
	githubErrorNoRemote
	githubErrorIssueNotFound
)

type githubErrorClassifier struct {
	kind      githubErrorKind
	fragments []string
}

var githubErrorClassifiers = []githubErrorClassifier{
	{kind: githubErrorUnauthenticated, fragments: []string{"not logged", "authentication", "auth login"}},
	{kind: githubErrorNoRemote, fragments: []string{
		"not a git repository",
		"no git repository",
		"no git remote",
		"unable to determine current repository",
		"could not determine base repository",
	}},
	{kind: githubErrorIssueNotFound, fragments: []string{
		"could not resolve to an issue",
		"issue not found",
		"no such issue",
	}},
}

var issueNumberPattern = regexp.MustCompile(`(?:/issues/|#)([0-9]+)`)

// GHRunner is the external boundary used by the GitHub tracker. Production
// code supplies a runner backed by the gh executable; tests can supply a
// scripted runner without making network calls.
type GHRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// GitHub implements Tracker using the GitHub CLI.
type GitHub struct {
	runner GHRunner

	labelsMu    sync.Mutex
	labelsReady bool
}

var _ Tracker = (*GitHub)(nil)

// NewGitHub creates a GitHub tracker backed by runner.
func NewGitHub(runner GHRunner) (*GitHub, error) {
	if runner == nil {
		return nil, errors.New("github tracker: gh runner is not configured")
	}
	return &GitHub{runner: runner}, nil
}

// Resolve returns the GitHub issue referenced by #N.
func (g *GitHub) Resolve(ctx context.Context, reference string) (Ticket, error) {
	number, err := parseReference(reference)
	if err != nil {
		return Ticket{}, err
	}
	if err := g.ensureLabels(ctx); err != nil {
		return Ticket{}, err
	}

	output, err := g.run(ctx, "view issue", "issue", "view", strconv.Itoa(number), "--json", githubIssueFields)
	if err != nil {
		return Ticket{}, issueLookupError(number, err)
	}
	issue, err := decodeIssue(output)
	if err != nil {
		return Ticket{}, fmt.Errorf("decode GitHub issue #%d: %w", number, err)
	}
	return issue.ticket(), nil
}

// List returns all GitHub issues visible to the current repository.
func (g *GitHub) List(ctx context.Context) ([]Ticket, error) {
	if err := g.ensureLabels(ctx); err != nil {
		return nil, err
	}

	output, err := g.run(ctx, "list issues", "issue", "list", "--state", "all", "--limit", "100", "--json", githubIssueFields)
	if err != nil {
		return nil, err
	}
	var issues []githubIssue
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return nil, fmt.Errorf("decode GitHub issue list: %w", err)
	}
	tickets := make([]Ticket, 0, len(issues))
	for _, issue := range issues {
		tickets = append(tickets, issue.ticket())
	}
	return tickets, nil
}

// UpdateStatus changes the project's todo/doing labels without closing the
// issue. Closing remains a human decision after a review is approved.
func (g *GitHub) UpdateStatus(ctx context.Context, number int, status string) error {
	status = strings.TrimSpace(status)
	if status != githubTodoLabel && status != githubDoingLabel {
		return fmt.Errorf("invalid GitHub ticket status %q; want todo or doing", status)
	}
	if err := g.ensureLabels(ctx); err != nil {
		return err
	}

	other := githubTodoLabel
	if status == githubTodoLabel {
		other = githubDoingLabel
	}
	_, err := g.run(ctx, "update issue status", "issue", "edit", strconv.Itoa(number), "--add-label", status, "--remove-label", other)
	return err
}

// AddComment adds a comment to a GitHub issue.
func (g *GitHub) AddComment(ctx context.Context, number int, note string) error {
	if strings.TrimSpace(note) == "" {
		return errors.New("cannot add an empty GitHub issue comment")
	}
	if err := g.ensureLabels(ctx); err != nil {
		return err
	}
	_, err := g.run(ctx, "comment on issue", "issue", "comment", strconv.Itoa(number), "--body", note)
	return err
}

// Create creates a GitHub issue with the todo label.
func (g *GitHub) Create(ctx context.Context, title, body string) (Ticket, error) {
	if strings.TrimSpace(title) == "" {
		return Ticket{}, errors.New("cannot create a GitHub issue without a title")
	}
	if err := g.ensureLabels(ctx); err != nil {
		return Ticket{}, err
	}

	output, err := g.run(ctx, "create issue", "issue", "create", "--title", strings.TrimSpace(title), "--body", body, "--label", githubTodoLabel)
	if err != nil {
		return Ticket{}, err
	}
	number, err := createdIssueNumber(output)
	if err != nil {
		return Ticket{}, err
	}
	return g.Resolve(ctx, "#"+strconv.Itoa(number))
}

func (g *GitHub) ensureLabels(ctx context.Context) error {
	g.labelsMu.Lock()
	defer g.labelsMu.Unlock()
	if g.labelsReady {
		return nil
	}

	output, err := g.run(ctx, "list project labels", "label", "list", "--limit", "100", "--json", "name")
	if err != nil {
		return err
	}
	var labels []githubLabel
	if err := json.Unmarshal([]byte(output), &labels); err != nil {
		return fmt.Errorf("decode GitHub labels: %w", err)
	}
	present := make(map[string]bool, len(labels))
	for _, label := range labels {
		present[label.Name] = true
	}
	for _, label := range []struct {
		name        string
		color       string
		description string
	}{
		{name: githubTodoLabel, color: githubTodoColor, description: "Ready to be worked"},
		{name: githubDoingLabel, color: githubDoingColor, description: "In progress"},
	} {
		if present[label.name] {
			continue
		}
		if _, err := g.run(ctx, "create project label", "label", "create", label.name, "--color", label.color, "--description", label.description); err != nil {
			return err
		}
	}
	g.labelsReady = true
	return nil
}

func (g *GitHub) run(ctx context.Context, operation string, args ...string) (string, error) {
	output, err := g.runner.Run(ctx, args...)
	if err == nil {
		return output, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}

	kind, details := classifyGitHubError(err, output)
	switch kind {
	case githubErrorNotInstalled:
		return "", fmt.Errorf("GitHub CLI (gh) is not installed; install GitHub CLI and try again: %s", details)
	case githubErrorUnauthenticated:
		return "", fmt.Errorf("GitHub CLI is not authenticated; run `gh auth login` and try again: %s", details)
	case githubErrorNoRemote:
		return "", fmt.Errorf("no GitHub remote found; run rig from a GitHub repository: %s", details)
	default:
		return "", fmt.Errorf("gh %s: %s", operation, details)
	}
}

func classifyGitHubError(err error, output string) (githubErrorKind, string) {
	details := strings.TrimSpace(strings.TrimSpace(output) + " " + strings.TrimSpace(errorText(err)))
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(strings.ToLower(details), "executable file not found") {
		return githubErrorNotInstalled, details
	}

	lowerDetails := strings.ToLower(details)
	for _, classifier := range githubErrorClassifiers {
		for _, fragment := range classifier.fragments {
			if strings.Contains(lowerDetails, fragment) {
				return classifier.kind, details
			}
		}
	}
	return githubErrorUnknown, details
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type githubIssue struct {
	Number int           `json:"number"`
	Title  string        `json:"title"`
	Body   string        `json:"body"`
	State  string        `json:"state"`
	Labels []githubLabel `json:"labels"`
}

type githubLabel struct {
	Name string `json:"name"`
}

func (i githubIssue) ticket() Ticket {
	labels := make([]string, 0, len(i.Labels))
	status := ""
	for _, label := range i.Labels {
		labels = append(labels, label.Name)
		switch label.Name {
		case githubDoingLabel:
			status = githubDoingLabel
		case githubTodoLabel:
			if status == "" {
				status = githubTodoLabel
			}
		}
	}
	return Ticket{
		Number: i.Number,
		Title:  i.Title,
		Body:   i.Body,
		Status: status,
		State:  i.State,
		Labels: labels,
	}
}

func decodeIssue(output string) (githubIssue, error) {
	var issue githubIssue
	if err := json.Unmarshal([]byte(output), &issue); err != nil {
		return githubIssue{}, err
	}
	return issue, nil
}

func issueLookupError(number int, err error) error {
	if kind, _ := classifyGitHubError(err, ""); kind == githubErrorIssueNotFound {
		return fmt.Errorf("GitHub issue #%d not found; check the issue number and repository: %w", number, err)
	}
	return err
}

func createdIssueNumber(output string) (int, error) {
	value := strings.TrimSpace(output)
	match := issueNumberPattern.FindStringSubmatch(value)
	if match == nil {
		if number, err := strconv.Atoi(value); err == nil && number > 0 {
			return number, nil
		}
		return 0, fmt.Errorf("create GitHub issue returned no issue number: %q", value)
	}
	number, err := strconv.Atoi(match[1])
	if err != nil || number < 1 {
		return 0, fmt.Errorf("create GitHub issue returned invalid issue number: %q", value)
	}
	return number, nil
}
