package tracker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestNewGitLabRejectsNilRunner(t *testing.T) {
	if _, err := NewGitLab(nil); err == nil || !strings.Contains(err.Error(), "glab runner is not configured") {
		t.Fatalf("NewGitLab(nil) error = %v, want nil-runner guidance", err)
	}
}

func TestGitLabResolveMapsIssueAndAcceptsReferences(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo","color":"#0E8A16"},{"name":"doing","color":"#5319E7"}]`,
		},
		"issue view 7 --output json": {
			output: `{"id":9007,"iid":7,"title":"Ship it","description":"Details","state":"opened","labels":["todo","backend"]}`,
		},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	for _, reference := range []string{"7", "#7", "https://gitlab.com/group/subgroup/project/-/issues/7"} {
		t.Run(reference, func(t *testing.T) {
			ticket, err := gitLab.Resolve(context.Background(), reference)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", reference, err)
			}
			if ticket.Number != 7 || ticket.Title != "Ship it" || ticket.Body != "Details" || ticket.State != "opened" {
				t.Fatalf("Resolve(%q) = %#v, want mapped issue fields", reference, ticket)
			}
			if ticket.Status != "todo" || len(ticket.Labels) != 2 || ticket.Labels[0] != "todo" || ticket.Labels[1] != "backend" {
				t.Fatalf("Resolve(%q) labels = %#v, want todo status and string labels", reference, ticket)
			}
		})
	}
}

func TestGitLabReusesExistingLabelColors(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo","color":"#123456"},{"name":"doing","color":"#654321"}]`,
		},
		"issue view 7 --output json": {
			output: `{"iid":7,"labels":["todo"]}`,
		},
		"api --method PUT projects/:fullpath/issues/7 --raw-field labels=doing": {},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	if err := gitLab.UpdateStatus(context.Background(), 7, "doing"); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if runner.hasCall("label create --name todo --color #0E8A16 --description Ready to be worked") || runner.hasCall("label create --name doing --color #5319E7 --description In progress") {
		t.Fatalf("UpdateStatus() calls = %#v, want existing label colors preserved", runner.calls)
	}
}

func TestGitLabListIncludesOpenedAndClosedIssues(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue list --all --output json --per-page 100": {
			output: `[{"iid":2,"title":"Open","description":"One","state":"opened","labels":["doing"]},{"iid":3,"title":"Closed","description":"Two","state":"closed","labels":[]}]`,
		},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	tickets, err := gitLab.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tickets) != 2 || tickets[0].Number != 2 || tickets[0].State != "opened" || tickets[1].Number != 3 || tickets[1].State != "closed" {
		t.Fatalf("List() = %#v, want opened and closed issues", tickets)
	}
}

func TestGitLabUpdateStatusSwapsLabelsWithoutClosingIssue(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"}]`,
		},
		"label create --name doing --color #5319E7 --description In progress": {},
		"issue view 7 --output json": {
			output: `{"iid":7,"labels":["todo","bug"]}`,
		},
		"api --method PUT projects/:fullpath/issues/7 --raw-field labels=bug,doing": {},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	if err := gitLab.UpdateStatus(context.Background(), 7, "doing"); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if !runner.hasCall("api --method PUT projects/:fullpath/issues/7 --raw-field labels=bug,doing") || runner.hasIssueStateChangeCall() {
		t.Fatalf("UpdateStatus() calls = %#v, want complete label update and no close", runner.calls)
	}
	if runner.hasForbiddenStatusUpdateCall() {
		t.Fatalf("UpdateStatus() calls = %#v, want no add/remove label parameters", runner.calls)
	}
}

func TestGitLabUpdateStatusKeepsNonStatusLabelsWhenReturningToTodo(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue view 7 --output json": {
			output: `{"iid":7,"labels":["doing","priority::high"]}`,
		},
		"api --method PUT projects/:fullpath/issues/7 --raw-field labels=priority::high,todo": {},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	if err := gitLab.UpdateStatus(context.Background(), 7, "todo"); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if !runner.hasCall("api --method PUT projects/:fullpath/issues/7 --raw-field labels=priority::high,todo") || runner.hasIssueStateChangeCall() || runner.hasForbiddenStatusUpdateCall() {
		t.Fatalf("UpdateStatus() calls = %#v, want priority label and todo only", runner.calls)
	}
}

func TestGitLabUpdateStatusHandlesEmptyAndExistingTargetLabels(t *testing.T) {
	tests := []struct {
		name        string
		issueLabels string
		wantUpdate  string
	}{
		{name: "empty labels", issueLabels: `[]`, wantUpdate: "doing"},
		{name: "already doing", issueLabels: `["doing"]`, wantUpdate: "doing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
				"label list --output json --per-page 100": {
					output: `[{"name":"todo"},{"name":"doing"}]`,
				},
				"issue view 7 --output json": {
					output: `{"iid":7,"labels":` + test.issueLabels + `}`,
				},
				"api --method PUT projects/:fullpath/issues/7 --raw-field labels=" + test.wantUpdate: {},
			}}
			gitLab, err := NewGitLab(runner)
			if err != nil {
				t.Fatalf("NewGitLab() error = %v", err)
			}

			if err := gitLab.UpdateStatus(context.Background(), 7, "doing"); err != nil {
				t.Fatalf("UpdateStatus() error = %v", err)
			}
			if !runner.hasCall("api --method PUT projects/:fullpath/issues/7 --raw-field labels="+test.wantUpdate) || runner.hasForbiddenStatusUpdateCall() {
				t.Fatalf("UpdateStatus() calls = %#v, want labels=%s without add/remove parameters", runner.calls, test.wantUpdate)
			}
		})
	}
}

func TestGitLabUpdateStatusRejectsCommaInExistingLabelWithoutUpdating(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue view 7 --output json": {
			output: `{"iid":7,"labels":["needs,triage"]}`,
		},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	err = gitLab.UpdateStatus(context.Background(), 7, "doing")
	if err == nil || !strings.Contains(err.Error(), "needs,triage") {
		t.Fatalf("UpdateStatus() error = %v, want comma-containing label", err)
	}
	if runner.hasStatusUpdateCall() {
		t.Fatalf("UpdateStatus() calls = %#v, want no update request", runner.calls)
	}
}

func TestGitLabUpdateStatusReturnsIssueNotFoundFromRead(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue view 7 --output json": {
			err:    errors.New("issue lookup failed"),
			output: "issue not found",
		},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	err = gitLab.UpdateStatus(context.Background(), 7, "doing")
	if err == nil || !strings.Contains(err.Error(), "GitLab issue #7 not found") {
		t.Fatalf("UpdateStatus() error = %v, want issue-not-found error", err)
	}
	if runner.hasStatusUpdateCall() {
		t.Fatalf("UpdateStatus() calls = %#v, want no update request", runner.calls)
	}
}

func TestGitLabUpdateStatusClassifiesUpdateAuthenticationError(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue view 7 --output json": {
			output: `{"iid":7,"labels":["todo"]}`,
		},
		"api --method PUT projects/:fullpath/issues/7 --raw-field labels=doing": {
			err: errors.New("not authenticated"),
		},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	err = gitLab.UpdateStatus(context.Background(), 7, "doing")
	if err == nil || !strings.Contains(err.Error(), "glab is not authenticated") {
		t.Fatalf("UpdateStatus() error = %v, want authentication guidance", err)
	}
}

func TestGitLabUpdateStatusRejectsUnknownStatusWithoutRunningCommand(t *testing.T) {
	runner := &scriptedGitLabRunner{}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	err = gitLab.UpdateStatus(context.Background(), 7, "done")
	if err == nil || !strings.Contains(err.Error(), "todo or doing") {
		t.Fatalf("UpdateStatus() error = %v, want accepted statuses", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("UpdateStatus() calls = %#v, want no command", runner.calls)
	}
}

func TestGitLabBootstrapsMissingLabelsOnlyOnce(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"}]`,
		},
		"label create --name doing --color #5319E7 --description In progress": {},
		"issue view 7 --output json": {
			output: `{"iid":7,"labels":["todo"]}`,
		},
		"issue view 8 --output json": {
			output: `{"iid":8,"labels":["todo"]}`,
		},
		"api --method PUT projects/:fullpath/issues/7 --raw-field labels=doing": {},
		"api --method PUT projects/:fullpath/issues/8 --raw-field labels=doing": {},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	for _, number := range []int{7, 8} {
		if err := gitLab.UpdateStatus(context.Background(), number, "doing"); err != nil {
			t.Fatalf("UpdateStatus(%d) error = %v", number, err)
		}
	}
	if got := runner.count("label list --output json --per-page 100"); got != 1 {
		t.Fatalf("label list count = %d, want exactly once", got)
	}
	if got := runner.count("label create --name doing --color #5319E7 --description In progress"); got != 1 {
		t.Fatalf("doing label create count = %d, want exactly once", got)
	}
}

func TestGitLabAddCommentSendsNoteAndRejectsEmptyNote(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue note 7 --message verdict": {},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	if err := gitLab.AddComment(context.Background(), 7, "verdict"); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	if !runner.hasCall("issue note 7 --message verdict") {
		t.Fatalf("AddComment() calls = %#v, want issue note", runner.calls)
	}

	emptyRunner := &scriptedGitLabRunner{}
	emptyGitLab, err := NewGitLab(emptyRunner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}
	if err := emptyGitLab.AddComment(context.Background(), 7, " \n\t "); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty AddComment() error = %v, want empty-note error", err)
	}
	if len(emptyRunner.calls) != 0 {
		t.Fatalf("empty AddComment() calls = %#v, want no command", emptyRunner.calls)
	}
}

func TestGitLabCreateAppliesTodoLabelAndResolvesCreatedIssue(t *testing.T) {
	runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
		"label list --output json --per-page 100": {
			output: `[{"name":"todo"},{"name":"doing"}]`,
		},
		"issue create --title New ticket --description Body --label todo --yes --no-editor": {
			output: "https://gitlab.com/group/subgroup/project/-/issues/12\n",
		},
		"issue view 12 --output json": {
			output: `{"iid":12,"title":"New ticket","description":"Body","state":"opened","labels":["todo"]}`,
		},
	}}
	gitLab, err := NewGitLab(runner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}

	ticket, err := gitLab.Create(context.Background(), "New ticket", "Body")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if ticket.Number != 12 || ticket.Title != "New ticket" || ticket.Status != "todo" {
		t.Fatalf("Create() = %#v, want resolved todo ticket", ticket)
	}
	if !runner.hasCall("issue create --title New ticket --description Body --label todo --yes --no-editor") {
		t.Fatalf("Create() calls = %#v, want todo label and noninteractive flags", runner.calls)
	}

	emptyRunner := &scriptedGitLabRunner{}
	emptyGitLab, err := NewGitLab(emptyRunner)
	if err != nil {
		t.Fatalf("NewGitLab() error = %v", err)
	}
	if _, err := emptyGitLab.Create(context.Background(), " \t", "Body"); err == nil || !strings.Contains(err.Error(), "without a title") {
		t.Fatalf("empty Create() error = %v, want empty-title error", err)
	}
	if len(emptyRunner.calls) != 0 {
		t.Fatalf("empty Create() calls = %#v, want no command", emptyRunner.calls)
	}
}

func TestGitLabErrorsAreDistinctAndActionable(t *testing.T) {
	tests := []struct {
		name       string
		runnerErr  error
		runnerText string
		want       string
		noRemote   bool
	}{
		{name: "glab is not installed", runnerErr: exec.ErrNotFound, want: "glab is not installed"},
		{name: "glab is unauthenticated", runnerErr: errors.New("auth status failed"), runnerText: "not logged into any GitLab hosts", want: "glab auth login"},
		{name: "no GitLab project", runnerErr: errors.New("project lookup failed"), runnerText: "no GitLab project found for this directory", want: "no GitLab project", noRemote: true},
		{name: "directory is not a git repository", runnerErr: errors.New("repository lookup failed"), runnerText: "not a git repository", want: "no GitLab project", noRemote: true},
		{name: "issue does not exist", runnerErr: errors.New("issue lookup failed"), runnerText: "issue 42 not found", want: "issue #42 not found"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedGitLabRunner{responses: map[string]gitLabResponse{
				"label list --output json --per-page 100": {
					output: `[{"name":"todo"},{"name":"doing"}]`,
				},
				"issue view 42 --output json": {err: test.runnerErr, output: test.runnerText},
			}}
			gitLab, err := NewGitLab(runner)
			if err != nil {
				t.Fatalf("NewGitLab() error = %v", err)
			}

			_, err = gitLab.Resolve(context.Background(), "#42")
			if err == nil || !strings.Contains(err.Error(), test.want) || test.runnerText != "" && !strings.Contains(err.Error(), test.runnerText) {
				t.Fatalf("Resolve() error = %v, want %q and runner output %q", err, test.want, test.runnerText)
			}
			if errors.Is(err, ErrNoRemote) != test.noRemote {
				t.Fatalf("Resolve() errors.Is(ErrNoRemote) = %t, want %t", errors.Is(err, ErrNoRemote), test.noRemote)
			}
		})
	}
}

type gitLabResponse struct {
	output string
	err    error
}

type scriptedGitLabRunner struct {
	responses map[string]gitLabResponse
	calls     []string
}

func (r *scriptedGitLabRunner) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	r.calls = append(r.calls, key)
	response, ok := r.responses[key]
	if !ok {
		return "", fmt.Errorf("unexpected glab command %q", key)
	}
	return response.output, response.err
}

func (r *scriptedGitLabRunner) count(command string) int {
	count := 0
	for _, call := range r.calls {
		if call == command {
			count++
		}
	}
	return count
}

func (r *scriptedGitLabRunner) hasCall(command string) bool {
	return r.count(command) > 0
}

func (r *scriptedGitLabRunner) hasStatusUpdateCall() bool {
	for _, call := range r.calls {
		if strings.HasPrefix(call, "api --method PUT projects/:fullpath/issues/") {
			return true
		}
	}
	return false
}

func (r *scriptedGitLabRunner) hasForbiddenStatusUpdateCall() bool {
	for _, call := range r.calls {
		if strings.Contains(call, "--unlabel") || strings.Contains(call, "add_labels") || strings.Contains(call, "remove_labels") {
			return true
		}
	}
	return false
}

func (r *scriptedGitLabRunner) hasIssueStateChangeCall() bool {
	for _, call := range r.calls {
		if strings.Contains(call, "close") || strings.Contains(call, "reopen") || strings.Contains(call, "state_event") {
			return true
		}
	}
	return false
}
