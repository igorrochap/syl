package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const (
	gitlabTodoLabel  = "todo"
	gitlabDoingLabel = "doing"
	gitlabTodoColor  = "0E8A16"
	gitlabDoingColor = "5319E7"
)

type gitlabErrorKind uint8

const (
	gitlabErrorUnknown gitlabErrorKind = iota
	gitlabErrorNotInstalled
	gitlabErrorUnauthenticated
	gitlabErrorNoProject
	gitlabErrorIssueNotFound
)

type gitlabErrorClassifier struct {
	kind      gitlabErrorKind
	fragments []string
}

var gitlabErrorClassifiers = []gitlabErrorClassifier{
	{kind: gitlabErrorUnauthenticated, fragments: []string{
		"not logged", "not authenticated", "authentication required", "auth login",
	}},
	{kind: gitlabErrorNoProject, fragments: []string{
		"no gitlab project", "no project found", "project was not found",
		"could not find a gitlab project", "could not determine project",
		"unable to determine project", "not a gitlab project", "not a git repository",
		"no git repository",
	}},
	{kind: gitlabErrorIssueNotFound, fragments: []string{
		"issue not found", "no such issue", "issue does not exist",
	}},
}

// GLabRunner is the external boundary used by the GitLab tracker. Production
// code supplies a runner backed by the glab executable; tests can supply a
// scripted runner without making network calls.
type GLabRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// GitLab implements Tracker using the GitLab CLI.
type GitLab struct {
	runner GLabRunner

	labelsMu    sync.Mutex
	labelsReady bool
}

var _ Tracker = (*GitLab)(nil)

// NewGitLab creates a GitLab tracker backed by runner.
func NewGitLab(runner GLabRunner) (*GitLab, error) {
	if runner == nil {
		return nil, errors.New("gitlab tracker: glab runner is not configured")
	}
	return &GitLab{runner: runner}, nil
}

// Resolve returns the GitLab issue referenced by N, #N, or an issue URL.
func (g *GitLab) Resolve(ctx context.Context, reference string) (Ticket, error) {
	number, err := parseReference(reference)
	if err != nil {
		return Ticket{}, err
	}
	if err := g.ensureLabels(ctx); err != nil {
		return Ticket{}, err
	}

	output, err := g.run(ctx, "view issue", "issue", "view", strconv.Itoa(number), "--output", "json")
	if err != nil {
		return Ticket{}, gitlabIssueLookupError(number, err)
	}
	issue, err := decodeGitLabIssue(output)
	if err != nil {
		return Ticket{}, fmt.Errorf("decode GitLab issue #%d: %w", number, err)
	}
	return issue.ticket(), nil
}

// List returns all GitLab issues visible to the current project.
func (g *GitLab) List(ctx context.Context) ([]Ticket, error) {
	if err := g.ensureLabels(ctx); err != nil {
		return nil, err
	}

	output, err := g.run(ctx, "list issues", "issue", "list", "--all", "--output", "json", "--per-page", "100")
	if err != nil {
		return nil, err
	}
	var issues []gitlabIssue
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return nil, fmt.Errorf("decode GitLab issue list: %w", err)
	}
	tickets := make([]Ticket, 0, len(issues))
	for _, issue := range issues {
		tickets = append(tickets, issue.ticket())
	}
	return tickets, nil
}

// UpdateStatus changes the project's todo/doing labels without closing the
// issue. Closing remains a human decision after a review is approved.
func (g *GitLab) UpdateStatus(ctx context.Context, number int, status string) error {
	status = strings.TrimSpace(status)
	if status != gitlabTodoLabel && status != gitlabDoingLabel {
		return fmt.Errorf("invalid GitLab ticket status %q; want todo or doing", status)
	}
	if err := g.ensureLabels(ctx); err != nil {
		return err
	}

	other := gitlabTodoLabel
	if status == gitlabTodoLabel {
		other = gitlabDoingLabel
	}
	_, err := g.run(ctx, "update issue status", "issue", "update", strconv.Itoa(number), "--label", status, "--unlabel", other)
	return err
}

// AddComment adds a note to a GitLab issue.
func (g *GitLab) AddComment(ctx context.Context, number int, note string) error {
	if strings.TrimSpace(note) == "" {
		return errors.New("cannot add an empty GitLab issue note")
	}
	if err := g.ensureLabels(ctx); err != nil {
		return err
	}
	_, err := g.run(ctx, "add note to issue", "issue", "note", strconv.Itoa(number), "--message", note)
	return err
}

// Create creates a GitLab issue with the todo label.
func (g *GitLab) Create(ctx context.Context, title, body string) (Ticket, error) {
	if strings.TrimSpace(title) == "" {
		return Ticket{}, errors.New("cannot create a GitLab issue without a title")
	}
	if err := g.ensureLabels(ctx); err != nil {
		return Ticket{}, err
	}

	output, err := g.run(ctx, "create issue", "issue", "create", "--title", strings.TrimSpace(title), "--description", body, "--label", gitlabTodoLabel, "--yes", "--no-editor")
	if err != nil {
		return Ticket{}, err
	}
	number, err := createdGitLabIssueNumber(output)
	if err != nil {
		return Ticket{}, err
	}
	return g.Resolve(ctx, "#"+strconv.Itoa(number))
}

func (g *GitLab) ensureLabels(ctx context.Context) error {
	g.labelsMu.Lock()
	defer g.labelsMu.Unlock()
	if g.labelsReady {
		return nil
	}

	output, err := g.run(ctx, "list project labels", "label", "list", "--output", "json", "--per-page", "100")
	if err != nil {
		return err
	}
	var labels []gitlabLabel
	if err := json.Unmarshal([]byte(output), &labels); err != nil {
		return fmt.Errorf("decode GitLab labels: %w", err)
	}
	present := make(map[string]gitlabLabel, len(labels))
	for _, label := range labels {
		present[label.Name] = label
	}
	for _, label := range []struct {
		name        string
		color       string
		description string
	}{
		{name: gitlabTodoLabel, color: gitlabTodoColor, description: "Ready to be worked"},
		{name: gitlabDoingLabel, color: gitlabDoingColor, description: "In progress"},
	} {
		if _, ok := present[label.name]; ok {
			// Keep an existing project label and its configured color.
			continue
		}
		if _, err := g.run(ctx, "create project label", "label", "create", "--name", label.name, "--color", label.color, "--description", label.description); err != nil {
			return err
		}
	}
	g.labelsReady = true
	return nil
}

func (g *GitLab) run(ctx context.Context, operation string, args ...string) (string, error) {
	output, err := g.runner.Run(ctx, args...)
	if err == nil {
		return output, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}

	kind, details := classifyGitLabError(err, output)
	switch kind {
	case gitlabErrorNotInstalled:
		return "", fmt.Errorf("glab is not installed; install GitLab CLI and try again: %s", details)
	case gitlabErrorUnauthenticated:
		return "", fmt.Errorf("glab is not authenticated; run `glab auth login` and try again: %s", details)
	case gitlabErrorNoProject:
		return "", fmt.Errorf("no GitLab project was found for the current directory; run syl from a GitLab project: %s", details)
	default:
		return "", fmt.Errorf("glab %s: %s", operation, details)
	}
}

func classifyGitLabError(err error, output string) (gitlabErrorKind, string) {
	details := strings.TrimSpace(strings.TrimSpace(output) + " " + strings.TrimSpace(errorText(err)))
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(strings.ToLower(details), "executable file not found") {
		return gitlabErrorNotInstalled, details
	}

	lowerDetails := strings.ToLower(details)
	for _, classifier := range gitlabErrorClassifiers {
		for _, fragment := range classifier.fragments {
			if strings.Contains(lowerDetails, fragment) {
				return classifier.kind, details
			}
		}
	}
	issueMentioned := strings.Contains(lowerDetails, "issue")
	issueNotFoundMessage := strings.Contains(lowerDetails, "not found") || strings.Contains(lowerDetails, "does not exist") || strings.Contains(lowerDetails, "could not find")
	if issueMentioned && issueNotFoundMessage {
		return gitlabErrorIssueNotFound, details
	}
	return gitlabErrorUnknown, details
}

type gitlabIssue struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
}

type gitlabLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (i gitlabIssue) ticket() Ticket {
	status := ""
	for _, label := range i.Labels {
		switch label {
		case gitlabDoingLabel:
			status = gitlabDoingLabel
		case gitlabTodoLabel:
			if status == "" {
				status = gitlabTodoLabel
			}
		}
	}
	return Ticket{
		Number: i.IID,
		Title:  i.Title,
		Body:   i.Description,
		Status: status,
		State:  i.State,
		Labels: i.Labels,
	}
}

func decodeGitLabIssue(output string) (gitlabIssue, error) {
	var issue gitlabIssue
	if err := json.Unmarshal([]byte(output), &issue); err != nil {
		return gitlabIssue{}, err
	}
	return issue, nil
}

func gitlabIssueLookupError(number int, err error) error {
	if kind, _ := classifyGitLabError(err, ""); kind == gitlabErrorIssueNotFound {
		return fmt.Errorf("GitLab issue #%d not found; check the issue number and project: %w", number, err)
	}
	return err
}

func createdGitLabIssueNumber(output string) (int, error) {
	value := strings.TrimSpace(output)
	match := issueNumberPattern.FindStringSubmatch(value)
	if match == nil {
		if number, err := strconv.Atoi(value); err == nil && number > 0 {
			return number, nil
		}
		return 0, fmt.Errorf("create GitLab issue returned no issue number: %q", value)
	}
	number, err := strconv.Atoi(match[1])
	if err != nil || number < 1 {
		return 0, fmt.Errorf("create GitLab issue returned invalid issue number: %q", value)
	}
	return number, nil
}
