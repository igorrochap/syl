package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/tracker"
)

func TestBranchName(t *testing.T) {
	tests := []struct {
		name   string
		ticket tracker.Ticket
		want   string
	}{
		{
			name:   "label match",
			ticket: tracker.Ticket{Title: "Repair broken cache", Labels: []string{"fix"}},
			want:   "fix/repair-broken-cache",
		},
		{
			name:   "title prefix",
			ticket: tracker.Ticket{Title: "refactor: extract recorder"},
			want:   "refactor/extract-recorder",
		},
		{
			name:   "implement syl prefix",
			ticket: tracker.Ticket{Title: "Implement syl: do thing"},
			want:   "feat/do-thing",
		},
		{
			name:   "default type",
			ticket: tracker.Ticket{Title: "Add account settings"},
			want:   "feat/add-account-settings",
		},
		{
			name:   "stop words and five word cap",
			ticket: tracker.Ticket{Title: "The quick and brown fox jumps over the lazy dog"},
			want:   "feat/quick-brown-fox-jumps-over",
		},
		{
			name:   "ticket fallback",
			ticket: tracker.Ticket{Number: 42, Title: "---"},
			want:   "feat/ticket-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := branchName(tt.ticket); got != tt.want {
				t.Fatalf("branchName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveBranchName(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "valid suggestion",
			body: "Investigate the cache failure.\n\nBranch: fix/valid-name\n",
			want: "fix/valid-name",
		},
		{
			name: "exact length limit",
			body: "Branch: fix/" + strings.Repeat("a", 56) + "\n",
			want: "fix/" + strings.Repeat("a", 56),
		},
		{
			name: "invalid type",
			body: "Branch: feature/valid-name\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "underscore",
			body: "Branch: fix/unsafe_name\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "uppercase",
			body: "Branch: fix/Uppercase\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "leading hyphen",
			body: "Branch: fix/-leading\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "trailing hyphen",
			body: "Branch: fix/trailing-\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "double hyphen",
			body: "Branch: fix/double--hyphen\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "over length limit",
			body: "Branch: fix/" + strings.Repeat("a", 57) + "\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "invalid first suggestion",
			body: "Branch: feature/invalid-first\nBranch: fix/valid-second\n",
			want: "feat/repair-broken-cache",
		},
		{
			name: "missing suggestion",
			body: "Investigate the cache failure.\n",
			want: "feat/repair-broken-cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ticket := tracker.Ticket{Title: "Repair broken cache", Body: tt.body}
			if got := resolveBranchName(ticket); got != tt.want {
				t.Fatalf("resolveBranchName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrepareImplementUsesResolvedBranchName(t *testing.T) {
	git := &branchSetupGit{}
	ticket := tracker.Ticket{
		Number: 42,
		Title:  "Repair broken cache",
		Body:   "Branch: fix/cache-race\n",
	}

	setup, err := prepareImplement(context.Background(), git, branchSetupTracker{}, ticket)
	if err != nil {
		t.Fatalf("prepareImplement() error = %v", err)
	}
	if setup.branch != "fix/cache-race" {
		t.Fatalf("prepareImplement() branch = %q, want %q", setup.branch, "fix/cache-race")
	}
	if got := git.branchCommand; got != "switch -c fix/cache-race" {
		t.Fatalf("git branch command = %q, want %q", got, "switch -c fix/cache-race")
	}
}

type branchSetupGit struct {
	branchCommand string
}

func (g *branchSetupGit) Run(_ context.Context, args ...string) (string, error) {
	command := strings.Join(args, " ")
	switch command {
	case "status --porcelain --untracked-files=all":
		return "", nil
	case "rev-parse HEAD":
		return "abc123", nil
	case "switch -c fix/cache-race":
		g.branchCommand = command
		return "", nil
	default:
		return "", nil
	}
}

type branchSetupTracker struct{}

func (branchSetupTracker) Resolve(context.Context, string) (tracker.Ticket, error) {
	return tracker.Ticket{}, nil
}

func (branchSetupTracker) List(context.Context) ([]tracker.Ticket, error) {
	return nil, nil
}

func (branchSetupTracker) UpdateStatus(context.Context, int, string) error {
	return nil
}

func (branchSetupTracker) AddComment(context.Context, int, string) error {
	return nil
}

func (branchSetupTracker) Create(context.Context, string, string) (tracker.Ticket, error) {
	return tracker.Ticket{}, nil
}
