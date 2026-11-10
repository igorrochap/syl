package tracker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestGitHubResolveReturnsIssueDetailsAndInitializesMissingLabelsOnce(t *testing.T) {
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"label list --limit 100 --json name": {
			output: `[{"name":"todo"}]`,
		},
		"label create doing --color 5319E7 --description In progress": {},
		"issue view 7 --json number,title,body,state,labels": {
			output: `{"number":7,"title":"Ship it","body":"Details","state":"OPEN","labels":[{"name":"todo"}]}`,
		},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	ticket, err := githubTracker.Resolve(context.Background(), "#7")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if ticket.Number != 7 || ticket.Title != "Ship it" || ticket.Body != "Details" || ticket.Status != "todo" {
		t.Fatalf("Resolve() = %#v, want issue details and todo status", ticket)
	}
	if ticket.State != "OPEN" || len(ticket.Labels) != 1 || ticket.Labels[0] != "todo" {
		t.Fatalf("Resolve() metadata = state %q labels %#v, want OPEN and todo", ticket.State, ticket.Labels)
	}

	if _, err := githubTracker.Resolve(context.Background(), "#7"); err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if got := runner.count("label create doing --color 5319E7 --description In progress"); got != 1 {
		t.Fatalf("doing label create count = %d, want exactly once", got)
	}
}

func TestGitHubUpdateStatusSwapsProjectLabelsWithoutClosingIssue(t *testing.T) {
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"label list --limit 100 --json name": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue edit 7 --add-label doing --remove-label todo": {},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	if err := githubTracker.UpdateStatus(context.Background(), 7, "doing"); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if runner.hasCall("issue close 7") || runner.hasCall("issue edit 7 --state closed") {
		t.Fatalf("status update calls = %#v, want no close operation", runner.calls)
	}
	if !runner.hasCall("issue edit 7 --add-label doing --remove-label todo") {
		t.Fatalf("status update calls = %#v, want todo to doing label swap", runner.calls)
	}
}

func TestGitHubCreateAppliesTodoLabelAndReturnsCreatedIssue(t *testing.T) {
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"label list --limit 100 --json name": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue create --title New ticket --body Body --label todo": {
			output: "https://github.com/example/project/issues/12\n",
		},
		"issue view 12 --json number,title,body,state,labels": {
			output: `{"number":12,"title":"New ticket","body":"Body","state":"OPEN","labels":[{"name":"todo"}]}`,
		},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	ticket, err := githubTracker.Create(context.Background(), "New ticket", "Body")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if ticket.Number != 12 || ticket.Status != "todo" {
		t.Fatalf("Create() = %#v, want issue 12 with todo status", ticket)
	}
	if !runner.hasCall("issue create --title New ticket --body Body --label todo") {
		t.Fatalf("create calls = %#v, want todo label", runner.calls)
	}
}

func TestGitHubCreatePullRequestOpensDraftWithHeadAndOptionalBase(t *testing.T) {
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"pr list --head feat/ship-it --state open --json url --limit 1": {
			output: "[]",
		},
		"pr create --draft --title Ship it --body Closes #7 --head feat/ship-it --base main": {
			output: "https://github.com/example/project/pull/23\n",
		},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	url, err := githubTracker.CreatePullRequest(context.Background(), PullRequest{
		Title: "Ship it", Body: "Closes #7", Head: "feat/ship-it", Base: "main",
	})
	if err != nil {
		t.Fatalf("CreatePullRequest() error = %v", err)
	}
	if url != "https://github.com/example/project/pull/23" {
		t.Fatalf("CreatePullRequest() URL = %q, want the created pull request URL", url)
	}
	if !runner.hasCall("pr create --draft --title Ship it --body Closes #7 --head feat/ship-it --base main") {
		t.Fatalf("CreatePullRequest() calls = %#v, want draft PR arguments", runner.calls)
	}
}

func TestGitHubCreatePullRequestReturnsExistingPullRequest(t *testing.T) {
	const existingURL = "https://github.com/example/project/pull/23"
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"pr list --head feat/ship-it --state open --json url --limit 1": {
			output: `[{"url":"` + existingURL + `"}]`,
		},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	url, err := githubTracker.CreatePullRequest(context.Background(), PullRequest{
		Title: "Ship it", Body: "Closes #7", Head: "feat/ship-it", Base: "main",
	})
	if err != nil || url != existingURL {
		t.Fatalf("CreatePullRequest() = (%q, %v), want existing URL and no error", url, err)
	}
	if runner.hasCall("pr create --draft --title Ship it --body Closes #7 --head feat/ship-it --base main") {
		t.Fatalf("gh calls = %#v, want existing pull request to skip creation", runner.calls)
	}
}

func TestGitHubCreatePullRequestOmitsBaseWhenUnspecified(t *testing.T) {
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"pr list --head feat/ship-it --state open --json url --limit 1": {
			output: "[]",
		},
		"pr create --draft --title Ship it --body Closes #7 --head feat/ship-it": {
			output: "https://github.com/example/project/pull/23\n",
		},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	if _, err := githubTracker.CreatePullRequest(context.Background(), PullRequest{
		Title: "Ship it", Body: "Closes #7", Head: "feat/ship-it",
	}); err != nil {
		t.Fatalf("CreatePullRequest() error = %v", err)
	}
	if !runner.hasCall("pr create --draft --title Ship it --body Closes #7 --head feat/ship-it") {
		t.Fatalf("CreatePullRequest() calls = %#v, want no base argument", runner.calls)
	}
}

func TestGitHubDefaultPullRequestBaseReadsForgeBranch(t *testing.T) {
	const command = "repo view --json defaultBranchRef --jq .defaultBranchRef.name"
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		command: {output: " main \n"},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}
	base, err := githubTracker.DefaultPullRequestBase(context.Background())
	if err != nil || base != "main" {
		t.Fatalf("DefaultPullRequestBase() = (%q, %v), want main and no error", base, err)
	}
	if !runner.hasCall(command) {
		t.Fatalf("GitHub calls = %#v, want forge default branch lookup", runner.calls)
	}
}

func TestGitHubDefaultPullRequestBaseReportsLookupFailureAndEmptyBranch(t *testing.T) {
	for _, response := range []githubResponse{
		{err: errors.New("repository lookup failed")},
		{output: " \n"},
	} {
		runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
			"repo view --json defaultBranchRef --jq .defaultBranchRef.name": response,
		}}
		githubTracker, err := NewGitHub(runner)
		if err != nil {
			t.Fatalf("NewGitHub() error = %v", err)
		}
		if base, err := githubTracker.DefaultPullRequestBase(context.Background()); err == nil || base != "" {
			t.Fatalf("DefaultPullRequestBase() = (%q, %v), want empty base and an error", base, err)
		}
	}
}

func TestGitHubListReturnsAllIssueMetadata(t *testing.T) {
	runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
		"label list --limit 100 --json name": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue list --state all --limit 100 --json number,title,body,state,labels": {
			output: `[{"number":2,"title":"First","body":"One","state":"OPEN","labels":[{"name":"doing"}]},{"number":3,"title":"Second","body":"Two","state":"CLOSED","labels":[]}]`,
		},
	}}
	githubTracker, err := NewGitHub(runner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}

	tickets, err := githubTracker.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tickets) != 2 || tickets[0].Status != "doing" || tickets[1].State != "CLOSED" {
		t.Fatalf("List() = %#v, want issue metadata and mapped status", tickets)
	}
}

func TestGitHubErrorsAreDistinctAndActionable(t *testing.T) {
	tests := []struct {
		name       string
		runnerErr  error
		runnerText string
		want       string
		noRemote   bool
	}{
		{
			name:      "gh is not installed",
			runnerErr: exec.ErrNotFound,
			want:      "install GitHub CLI",
		},
		{
			name:       "gh is unauthenticated",
			runnerErr:  errors.New("gh auth status failed"),
			runnerText: "not logged into any GitHub hosts",
			want:       "gh auth login",
		},
		{
			name:       "repository has no remote",
			runnerErr:  errors.New("repository lookup failed"),
			runnerText: "unable to determine current repository",
			want:       "GitHub remote",
			noRemote:   true,
		},
		{
			name:       "directory is not a git repository",
			runnerErr:  errors.New("repository lookup failed"),
			runnerText: "not a git repository",
			want:       "GitHub remote",
			noRemote:   true,
		},
		{
			name:       "issue does not exist",
			runnerErr:  errors.New("issue lookup failed"),
			runnerText: "Could not resolve to an Issue with the number of 42",
			want:       "issue #42 not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &scriptedGitHubRunner{responses: map[string]githubResponse{
				"label list --limit 100 --json name": {
					output: `[{"name":"todo"},{"name":"doing"}]`,
				},
				"issue view 42 --json number,title,body,state,labels": {
					err:    tt.runnerErr,
					output: tt.runnerText,
				},
			}}
			githubTracker, err := NewGitHub(runner)
			if err != nil {
				t.Fatalf("NewGitHub() error = %v", err)
			}

			_, err = githubTracker.Resolve(context.Background(), "#42")
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), tt.runnerText) {
				t.Fatalf("Resolve() error = %v, want %q and runner output %q", err, tt.want, tt.runnerText)
			}
			if errors.Is(err, ErrNoRemote) != tt.noRemote {
				t.Fatalf("Resolve() errors.Is(ErrNoRemote) = %t, want %t", errors.Is(err, ErrNoRemote), tt.noRemote)
			}
		})
	}
}

type githubResponse struct {
	output string
	err    error
}

type scriptedGitHubRunner struct {
	responses map[string]githubResponse
	calls     []string
}

func (r *scriptedGitHubRunner) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, key)
	response, ok := r.responses[key]
	if !ok {
		return "", fmt.Errorf("unexpected gh command %q", key)
	}
	return response.output, response.err
}

func (r *scriptedGitHubRunner) count(command string) int {
	count := 0
	for _, call := range r.calls {
		if call == command {
			count++
		}
	}
	return count
}

func (r *scriptedGitHubRunner) hasCall(command string) bool {
	return r.count(command) > 0
}
