package orchestration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
)

func TestProvisionWorktreeCreatesCentralCheckoutAndCopiesOnlyMissingAgentPaths(t *testing.T) {
	origin := newWorktreeRepository(t)
	writeWorktreeFile(t, filepath.Join(origin, ".gitignore"), ".agents/skills/generated/\n.claude\nCLAUDE.md\n")
	writeWorktreeFile(t, filepath.Join(origin, ".agents", "skills", "committed", "SKILL.md"), "committed skill\n")
	worktreeGit(t, origin, "add", ".gitignore", ".agents/skills/committed/SKILL.md")
	worktreeGit(t, origin, "commit", "-m", "commit agent paths")
	writeWorktreeFile(t, filepath.Join(origin, ".agents", "skills", "committed", "SKILL.md"), "origin change\n")
	writeWorktreeFile(t, filepath.Join(origin, ".agents", "skills", "generated", "SKILL.md"), "generated skill\n")
	if err := os.Symlink(".agents", filepath.Join(origin, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(origin, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	branch := "fix/provisioned-worktree"
	worktreePath := filepath.Join(worktreeRoot, filepath.Base(origin), worktreeBranchSlug(branch))
	provisioned, err := ProvisionWorktree(context.Background(), WorktreeOptions{
		OriginRoot:    origin,
		ProjectConfig: config.Config{Worktree: config.WorktreeConfig{Root: worktreeRoot}},
		Ticket:        tracker.Ticket{Number: 81, Title: "Provision worktree", Body: "Branch: " + branch},
		Git:           gitadapter.ExecGitRunner{Dir: worktreePath},
		OriginGit:     gitadapter.ExecGitRunner{Dir: origin},
	})
	if err != nil {
		t.Fatalf("ProvisionWorktree() error = %v", err)
	}

	wantPath := worktreePath
	if provisioned.Path != wantPath {
		t.Fatalf("worktree path = %q, want %q", provisioned.Path, wantPath)
	}
	if provisioned.Branch != branch {
		t.Fatalf("worktree branch = %q, want %q", provisioned.Branch, branch)
	}
	if got := worktreeGit(t, provisioned.Path, "branch", "--show-current"); got != branch {
		t.Fatalf("checked-out branch = %q, want %q", got, branch)
	}
	if !strings.Contains(worktreeGit(t, origin, "worktree", "list", "--porcelain"), wantPath) {
		t.Fatalf("worktree list does not contain %q", wantPath)
	}

	if got := readWorktreeFile(t, filepath.Join(provisioned.Path, ".agents", "skills", "committed", "SKILL.md")); got != "committed skill\n" {
		t.Fatalf("tracked skill = %q, want checkout content untouched by origin changes", got)
	}
	if got := readWorktreeFile(t, filepath.Join(provisioned.Path, ".agents", "skills", "generated", "SKILL.md")); got != "generated skill\n" {
		t.Fatalf("missing skill = %q, want copied content", got)
	}
	assertWorktreeSymlink(t, filepath.Join(provisioned.Path, ".claude"), ".agents")
	assertWorktreeSymlink(t, filepath.Join(provisioned.Path, "CLAUDE.md"), "AGENTS.md")
	if got := worktreeGit(t, provisioned.Path, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("new worktree status = %q, want copied ignored paths to stay out of the diff", got)
	}
}

func TestProvisionWorktreeUsesExplicitBaseRef(t *testing.T) {
	origin := newWorktreeRepository(t)
	base := worktreeGit(t, origin, "rev-parse", "HEAD")
	writeWorktreeFile(t, filepath.Join(origin, "tracked.txt"), "second commit\n")
	worktreeGit(t, origin, "add", "tracked.txt")
	worktreeGit(t, origin, "commit", "-m", "second commit")

	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	worktreePath := filepath.Join(worktreeRoot, filepath.Base(origin), worktreeBranchSlug(resolveBranchName(tracker.Ticket{Number: 82, Title: "Use a base ref"})))
	provisioned, err := ProvisionWorktree(context.Background(), WorktreeOptions{
		OriginRoot:    origin,
		ProjectConfig: config.Config{Worktree: config.WorktreeConfig{Root: worktreeRoot}},
		Ticket:        tracker.Ticket{Number: 82, Title: "Use a base ref"},
		Base:          base,
		Git:           gitadapter.ExecGitRunner{Dir: worktreePath},
		OriginGit:     gitadapter.ExecGitRunner{Dir: origin},
	})
	if err != nil {
		t.Fatalf("ProvisionWorktree() error = %v", err)
	}
	if got := worktreeGit(t, provisioned.Path, "rev-parse", "HEAD"); got != base {
		t.Fatalf("worktree HEAD = %q, want explicit base %q", got, base)
	}
	if got := readWorktreeFile(t, filepath.Join(provisioned.Path, "tracked.txt")); got != "committed\n" {
		t.Fatalf("base checkout file = %q, want first commit content", got)
	}
}

func TestProvisionWorktreeRollsBackWorktreeAndBranchWhenCopiedPathsPolluteDiff(t *testing.T) {
	origin := newWorktreeRepository(t)
	writeWorktreeFile(t, filepath.Join(origin, ".agents", "skills", "missing", "SKILL.md"), "not ignored\n")
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ticket := tracker.Ticket{Number: 83, Title: "Rollback copied paths", Body: "Branch: fix/rollback-copied-paths"}
	worktreePath := filepath.Join(worktreeRoot, filepath.Base(origin), worktreeBranchSlug(resolveBranchName(ticket)))
	originGit := gitadapter.ExecGitRunner{Dir: origin}

	_, err := ProvisionWorktree(context.Background(), WorktreeOptions{
		OriginRoot:    origin,
		ProjectConfig: config.Config{Worktree: config.WorktreeConfig{Root: worktreeRoot}},
		Ticket:        ticket,
		Git:           gitadapter.ExecGitRunner{Dir: worktreePath},
		OriginGit:     originGit,
	})
	if err == nil {
		t.Fatal("ProvisionWorktree() error = nil, want unignored-copy refusal")
	}
	for _, expected := range []string{".agents/skills/missing/SKILL.md", "add these paths to .gitignore"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("ProvisionWorktree() error = %q, want %q", err, expected)
		}
	}

	if _, statErr := os.Lstat(worktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("worktree path stat error = %v, want removed path", statErr)
	}
	if got := worktreeGit(t, origin, "branch", "--list", "--format=%(refname:short)", "fix/rollback-copied-paths"); got != "" {
		t.Fatalf("rolled-back branch = %q, want absent", got)
	}
	if strings.Contains(worktreeGit(t, origin, "worktree", "list", "--porcelain"), worktreePath) {
		t.Fatalf("rolled-back worktree %q remains registered", worktreePath)
	}
	if _, statErr := os.Stat(filepath.Join(worktreeRoot, filepath.Base(origin))); !os.IsNotExist(statErr) {
		t.Fatalf("worktree parent stat error = %v, want empty parents removed", statErr)
	}
}

func TestProvisionWorktreeRefusesExistingWorktreeAndBranch(t *testing.T) {
	origin := newWorktreeRepository(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ticket := tracker.Ticket{Number: 84, Title: "Collision", Body: "Branch: fix/collision"}
	options := WorktreeOptions{
		OriginRoot:    origin,
		ProjectConfig: config.Config{Worktree: config.WorktreeConfig{Root: worktreeRoot}},
		Ticket:        ticket,
		Git:           gitadapter.ExecGitRunner{Dir: filepath.Join(worktreeRoot, filepath.Base(origin), worktreeBranchSlug(resolveBranchName(ticket)))},
		OriginGit:     gitadapter.ExecGitRunner{Dir: origin},
	}
	first, err := ProvisionWorktree(context.Background(), options)
	if err != nil {
		t.Fatalf("first ProvisionWorktree() error = %v", err)
	}

	_, err = ProvisionWorktree(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "git worktree remove --force") || !strings.Contains(err.Error(), first.Path) {
		t.Fatalf("second ProvisionWorktree() error = %v, want worktree cleanup command", err)
	}
	if _, statErr := os.Stat(first.Path); statErr != nil {
		t.Fatalf("existing worktree stat error = %v, want it preserved", statErr)
	}

	branchTicket := tracker.Ticket{Number: 85, Title: "Branch collision", Body: "Branch: fix/branch-collision"}
	worktreeGit(t, origin, "branch", "fix/branch-collision")
	_, err = ProvisionWorktree(context.Background(), WorktreeOptions{
		OriginRoot:    origin,
		ProjectConfig: config.Config{Worktree: config.WorktreeConfig{Root: worktreeRoot}},
		Ticket:        branchTicket,
		Git:           gitadapter.ExecGitRunner{Dir: filepath.Join(worktreeRoot, filepath.Base(origin), worktreeBranchSlug(resolveBranchName(branchTicket)))},
		OriginGit:     gitadapter.ExecGitRunner{Dir: origin},
	})
	if err == nil || !strings.Contains(err.Error(), "git branch -D fix/branch-collision") {
		t.Fatalf("branch collision error = %v, want branch cleanup command", err)
	}
}

func newWorktreeRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	worktreeGit(t, root, "init", "-b", "main")
	worktreeGit(t, root, "config", "user.email", "syl-tests@example.com")
	worktreeGit(t, root, "config", "user.name", "syl tests")
	writeWorktreeFile(t, filepath.Join(root, "tracked.txt"), "committed\n")
	writeWorktreeFile(t, filepath.Join(root, "AGENTS.md"), "project instructions\n")
	worktreeGit(t, root, "add", ".")
	worktreeGit(t, root, "commit", "-m", "initial commit")
	return root
}

func writeWorktreeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readWorktreeFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func worktreeGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", root, strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertWorktreeSymlink(t *testing.T, path, wantTarget string) {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	if target != wantTarget {
		t.Fatalf("symlink %s = %q, want %q", path, target, wantTarget)
	}
}
