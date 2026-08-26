package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
)

// WorktreeOptions describes the repository and ticket for a new implementation
// worktree. Provisioning does not update the ticket or start a harness; it only
// prepares the checkout for a later implement run.
type WorktreeOptions struct {
	OriginRoot     string
	ProjectConfig  config.Config
	Ticket         tracker.Ticket
	Base           string
	Git            GitRunner
	GitForWorktree func(root string) GitRunner
	OriginGit      GitRunner
}

// Worktree is the checkout prepared for an implementation run.
type Worktree struct {
	Path   string
	Branch string
}

// ProvisionWorktree creates a clean worktree for the ticket. The worktree and
// branch remain owned by the caller once this function succeeds; before then,
// every failure rolls both back and returns the failure that caused it.
func ProvisionWorktree(ctx context.Context, options WorktreeOptions) (Worktree, error) {
	if options.OriginRoot == "" {
		return Worktree{}, errors.New("provision worktree: origin root is required")
	}
	if options.Git == nil {
		return Worktree{}, errors.New("provision worktree: git runner is not configured")
	}
	originGit := options.OriginGit
	if originGit == nil {
		originGit = options.Git
	}

	originRoot, err := filepath.Abs(options.OriginRoot)
	if err != nil {
		return Worktree{}, fmt.Errorf("resolve origin root: %w", err)
	}
	worktreeRoot, err := resolveWorktreeRoot(options.ProjectConfig.Worktree.Root)
	if err != nil {
		return Worktree{}, err
	}
	branch := resolveBranchName(options.Ticket)
	repoName := filepath.Base(filepath.Clean(originRoot))
	worktreePath := filepath.Join(worktreeRoot, repoName, worktreeBranchSlug(branch))

	if err := refuseWorktreeCollision(ctx, originGit, worktreePath, branch); err != nil {
		return Worktree{}, err
	}

	parent, err := prepareWorktreeParent(worktreeRoot, repoName)
	if err != nil {
		return Worktree{}, err
	}

	branchAttempted := false
	cleanup := func() {
		if branchAttempted {
			rollbackWorktree(ctx, originGit, worktreePath, branch)
		}
		parent.cleanup()
	}
	fail := func(cause error) (Worktree, error) {
		cleanup()
		return Worktree{}, cause
	}

	args := []string{"worktree", "add", "-b", branch, worktreePath}
	if base := strings.TrimSpace(options.Base); base != "" {
		args = append(args, base)
	}
	branchAttempted = true
	if _, err := originGit.Run(ctx, args...); err != nil {
		return fail(fmt.Errorf("create worktree %q: %w", worktreePath, err))
	}

	copied, err := copyAgentPaths(originRoot, worktreePath, options.ProjectConfig.Worktree.Copy)
	if err != nil {
		return fail(err)
	}
	if len(copied) > 0 {
		worktreeGit := options.Git
		if options.GitForWorktree != nil {
			worktreeGit = options.GitForWorktree(worktreePath)
		}
		if worktreeGit == nil {
			return fail(errors.New("check copied agent paths: worktree git runner is not configured"))
		}
		untracked, err := untrackedPaths(ctx, worktreeGit)
		if err != nil {
			return fail(err)
		}
		if len(untracked) > 0 {
			return fail(fmt.Errorf("copied agent paths are untracked and unignored: %s; add these paths to .gitignore before retrying", strings.Join(untracked, ", ")))
		}
	}

	return Worktree{Path: worktreePath, Branch: branch}, nil
}

func resolveWorktreeRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = config.DefaultWorktreeRoot
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve worktree root %q: %w", root, err)
		}
		if root == "~" {
			root = home
		} else {
			root = filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(root, "~/")))
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve worktree root: %w", err)
	}
	return filepath.Clean(root), nil
}

func worktreeBranchSlug(branch string) string {
	return strings.ReplaceAll(branch, "/", "-")
}

type worktreeParent struct {
	root        string
	rootCreated bool
	repo        string
	repoCreated bool
}

func prepareWorktreeParent(root, repoName string) (worktreeParent, error) {
	parent := worktreeParent{root: root, repo: filepath.Join(root, repoName)}
	rootInfo, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		parent.rootCreated = true
	case err != nil:
		return worktreeParent{}, fmt.Errorf("inspect worktree root %s: %w", root, err)
	case !rootInfo.IsDir():
		return worktreeParent{}, fmt.Errorf("worktree root %s is not a directory", root)
	}

	repoInfo, err := os.Stat(parent.repo)
	switch {
	case errors.Is(err, os.ErrNotExist):
		parent.repoCreated = true
	case err != nil:
		return worktreeParent{}, fmt.Errorf("inspect worktree repository directory %s: %w", parent.repo, err)
	case !repoInfo.IsDir():
		return worktreeParent{}, fmt.Errorf("worktree repository path %s is not a directory", parent.repo)
	}

	if err := os.MkdirAll(parent.repo, 0o755); err != nil {
		return worktreeParent{}, fmt.Errorf("create worktree parent %s: %w", parent.repo, err)
	}
	return parent, nil
}

func (p worktreeParent) cleanup() {
	if p.repoCreated {
		removeIfEmpty(p.repo)
	}
	if p.rootCreated {
		removeIfEmpty(p.root)
	}
}

func removeIfEmpty(path string) {
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		return
	}
	_ = os.Remove(path)
}

type registeredWorktree struct {
	path   string
	branch string
}

func refuseWorktreeCollision(ctx context.Context, git GitRunner, path, branch string) error {
	registered, err := listRegisteredWorktrees(ctx, git)
	if err != nil {
		return err
	}
	for _, worktree := range registered {
		if filepath.Clean(worktree.path) == filepath.Clean(path) {
			return fmt.Errorf("worktree %q already exists; clear it with `git worktree remove --force %s`", path, path)
		}
		if worktree.branch == branch {
			return fmt.Errorf("branch %q is already checked out at %s; clear it with `git worktree remove --force %s`", branch, worktree.path, worktree.path)
		}
	}

	branchList, err := git.Run(ctx, "branch", "--list", "--format=%(refname:short)", branch)
	if err != nil {
		return fmt.Errorf("check whether branch %q exists: %w", branch, err)
	}
	if strings.TrimSpace(branchList) != "" {
		return fmt.Errorf("branch %q already exists; clear it with `git branch -D %s`", branch, branch)
	}

	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("worktree path %q already exists; clear it with `git worktree remove --force %s`", path, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect worktree path %s: %w", path, err)
	}
	return nil
}

func listRegisteredWorktrees(ctx context.Context, git GitRunner) ([]registeredWorktree, error) {
	output, err := git.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %w", err)
	}

	var worktrees []registeredWorktree
	var current *registeredWorktree
	flush := func() {
		if current != nil {
			worktrees = append(worktrees, *current)
			current = nil
		}
	}
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current = &registeredWorktree{path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch refs/heads/") && current != nil:
			current.branch = strings.TrimPrefix(line, "branch refs/heads/")
		case strings.TrimSpace(line) == "":
			flush()
		}
	}
	flush()
	return worktrees, nil
}

func rollbackWorktree(ctx context.Context, git GitRunner, path, branch string) {
	_ = RemoveWorktree(ctx, git, Worktree{Path: path, Branch: branch})
}

// RemoveWorktree removes a provisioned implementation worktree and its
// branch. It is intended for setup failures; callers that have started the
// implement loop should leave the worktree for the user to inspect.
func RemoveWorktree(ctx context.Context, git GitRunner, worktree Worktree) error {
	if git == nil {
		return errors.New("remove worktree: git runner is not configured")
	}
	if strings.TrimSpace(worktree.Path) == "" {
		return errors.New("remove worktree: path is required")
	}
	if strings.TrimSpace(worktree.Branch) == "" {
		return errors.New("remove worktree: branch is required")
	}
	cleanupCtx := context.WithoutCancel(ctx)
	var cleanupErrors []error
	if _, err := git.Run(cleanupCtx, "worktree", "remove", "--force", worktree.Path); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove worktree %q: %w", worktree.Path, err))
	}
	if _, err := git.Run(cleanupCtx, "worktree", "prune"); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("prune worktrees: %w", err))
	}
	if err := os.RemoveAll(worktree.Path); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove worktree path %q: %w", worktree.Path, err))
	}
	if _, err := git.Run(cleanupCtx, "branch", "-D", worktree.Branch); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("remove worktree branch %q: %w", worktree.Branch, err))
	}
	return errors.Join(cleanupErrors...)
}

func copyAgentPaths(originRoot, worktreePath string, configuredPaths []string) ([]string, error) {
	var copied []string
	paths := append([]string{".agents", ".claude", "CLAUDE.md"}, configuredPaths...)
	for _, relativePath := range paths {
		if err := copyMissingPath(
			filepath.Join(originRoot, filepath.FromSlash(relativePath)),
			filepath.Join(worktreePath, filepath.FromSlash(relativePath)),
			worktreePath,
			&copied,
		); err != nil {
			return nil, fmt.Errorf("copy agent path %s: %w", relativePath, err)
		}
	}
	sort.Strings(copied)
	return copied, nil
}

func copyMissingPath(source, destination, worktreeRoot string, copied *[]string) error {
	sourceInfo, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	destinationInfo, err := os.Lstat(destination)
	switch {
	case err == nil && !sourceInfo.IsDir():
		return nil
	case err == nil && !destinationInfo.IsDir():
		return nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}

	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, destination); err != nil {
			return err
		}
		*copied = append(*copied, relativeWorktreePath(worktreeRoot, destination))
		return nil
	}

	if sourceInfo.IsDir() {
		if err := os.MkdirAll(destination, sourceInfo.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyMissingPath(
				filepath.Join(source, entry.Name()),
				filepath.Join(destination, entry.Name()),
				worktreeRoot,
				copied,
			); err != nil {
				return err
			}
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := copyFile(source, destination, sourceInfo.Mode().Perm()); err != nil {
		return err
	}
	*copied = append(*copied, relativeWorktreePath(worktreeRoot, destination))
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func relativeWorktreePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

func untrackedPaths(ctx context.Context, git GitRunner) ([]string, error) {
	output, err := git.Run(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("check copied agent paths against .gitignore: %w", err)
	}
	output = strings.TrimSuffix(output, "\x00")
	if output == "" {
		return nil, nil
	}
	paths := strings.Split(output, "\x00")
	sort.Strings(paths)
	return paths, nil
}
