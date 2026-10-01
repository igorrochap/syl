package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/tracker"
)

func TestRunImplementFailureReportsUncommittedWork(t *testing.T) {
	options, git := failedImplementOptions(t)
	git.status = " M modified.go\nA  staged.go\n?? new file.go\nR  old.go -> renamed.go\n"
	cause := errors.New("harness stopped unexpectedly")
	changedPath := filepath.Join(options.WorkRoot, "modified.go")
	options.Implementer = &failureReportAdapter{
		errorHarnessAdapter: errorHarnessAdapter{err: cause},
		runHook: func() {
			if err := os.WriteFile(changedPath, []byte("implementation in progress\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}

	err := RunImplement(context.Background(), options)
	if !errors.Is(err, cause) {
		t.Fatalf("RunImplement() error = %v, want original harness error", err)
	}
	want := "run implement harness: harness stopped unexpectedly\n\n" +
		"Uncommitted changes are kept in the work root: " + options.WorkRoot + "\n" +
		"   M modified.go\n  A  staged.go\n  ?? new file.go\n  R  old.go -> renamed.go\n"
	if err.Error() != want {
		t.Fatalf("error output = %q, want %q", err.Error(), want)
	}
	assertFailedImplementState(t, options, git, runrecord.Failed)
	contents, readErr := os.ReadFile(changedPath)
	if readErr != nil || string(contents) != "implementation in progress\n" {
		t.Fatalf("work root contents = %q, error = %v, want preserved implementation", contents, readErr)
	}
}

func TestRunImplementCancellationReportsUncommittedWork(t *testing.T) {
	options, git := failedImplementOptions(t)
	git.status = " M cancelled.go\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options.Implementer = &failureReportAdapter{
		errorHarnessAdapter: errorHarnessAdapter{err: context.Canceled},
		runHook:             cancel,
	}

	err := RunImplement(ctx, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunImplement() error = %v, want cancellation", err)
	}
	want := "run implement harness: context canceled\n\n" +
		"Uncommitted changes are kept in the work root: " + options.WorkRoot + "\n   M cancelled.go\n"
	if err.Error() != want {
		t.Fatalf("error output = %q, want %q", err.Error(), want)
	}
	assertFailedImplementState(t, options, git, runrecord.Cancelled)
}

func TestRunImplementFailureWithCleanWorkRootLeavesErrorUnchanged(t *testing.T) {
	options, git := failedImplementOptions(t)
	err := RunImplement(context.Background(), options)
	if err == nil || err.Error() != "run implement harness: harness failed" {
		t.Fatalf("RunImplement() error = %v, want only original error", err)
	}
	assertFailedImplementState(t, options, git, runrecord.Failed)
}

func TestRunImplementFailureReportsWorktreeChanges(t *testing.T) {
	options, git := failedImplementOptions(t)
	options.WorkRoot = t.TempDir()
	options.ProvisionedWorktree = &Worktree{Path: options.WorkRoot, Branch: "feat/worktree"}
	git.status = "?? worktree-only.go\n"
	originGit := &failureReportGit{prepared: true, status: " M origin-only.go\n"}
	options.OriginGit = originGit

	err := RunImplement(context.Background(), options)
	want := "run implement harness: harness failed\n\n" +
		"Uncommitted changes are kept in the work root: " + options.WorkRoot + "\n  ?? worktree-only.go\n"
	if err == nil || err.Error() != want {
		t.Fatalf("error output = %v, want %q", err, want)
	}
	if len(originGit.afterFailure) != 0 {
		t.Fatalf("origin git commands = %v, want none", originGit.afterFailure)
	}
	assertFailedImplementState(t, options, git, runrecord.Failed)
}

func TestRunImplementFailureBoundsReportedPaths(t *testing.T) {
	options, git := failedImplementOptions(t)
	var status, listed strings.Builder
	for index := 1; index <= 23; index++ {
		fmt.Fprintf(&status, "?? file-%02d.go\n", index)
		if index <= 20 {
			fmt.Fprintf(&listed, "  ?? file-%02d.go\n", index)
		}
	}
	git.status = status.String()
	err := RunImplement(context.Background(), options)
	want := "run implement harness: harness failed\n\n" +
		"Uncommitted changes are kept in the work root: " + options.WorkRoot + "\n" +
		listed.String() + "  ... and 3 more paths\n"
	if err == nil || err.Error() != want {
		t.Fatalf("error output = %v, want %q", err, want)
	}
	assertFailedImplementState(t, options, git, runrecord.Failed)
}

func TestRunImplementStatusCheckFailurePreservesOriginalError(t *testing.T) {
	options, git := failedImplementOptions(t)
	git.statusErr = errors.New("git is unavailable\nverbose diagnostics")
	cause := errors.New("original harness error")
	options.Implementer = &errorHarnessAdapter{err: cause}
	err := RunImplement(context.Background(), options)
	if !errors.Is(err, cause) {
		t.Fatalf("RunImplement() error = %v, want original harness error", err)
	}
	want := "run implement harness: original harness error\n\n" +
		"Could not check the work root for uncommitted changes (" + options.WorkRoot + ")"
	if err.Error() != want {
		t.Fatalf("error output = %q, want %q", err.Error(), want)
	}
	assertFailedImplementState(t, options, git, runrecord.Failed)
}

func failedImplementOptions(t *testing.T) (ImplementOptions, *failureReportGit) {
	t.Helper()
	root := t.TempDir()
	git := &failureReportGit{}
	return ImplementOptions{
		OriginRoot: root, WorkRoot: root,
		OpenRun: NewDiskRunOpener(root, sylhome.Dir{}, io.Discard),
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: branchSetupTracker{}, Ticket: tracker.Ticket{Number: 213},
		Implementer: &errorHarnessAdapter{err: errors.New("harness failed")},
		Reviewer:    &capturingReviewAdapter{},
		Git:         git, OriginGit: &branchSetupGit{},
		Input: strings.NewReader(""), Output: io.Discard,
	}, git
}

func assertFailedImplementState(t *testing.T, options ImplementOptions, git *failureReportGit, want runrecord.Status) {
	t.Helper()
	state := mustReadRunState(t, onlyRunStatePath(t, options.OriginRoot))
	if state.Status != want || state.EndedAt == nil {
		t.Fatalf("persisted Run state = %+v, want %s with an end time", state, want)
	}
	wantCommands := []string{"status --porcelain --untracked-files=all"}
	if !reflect.DeepEqual(git.afterFailure, wantCommands) {
		t.Fatalf("git commands after failure = %v, want %v", git.afterFailure, wantCommands)
	}
}

type failureReportGit struct {
	status       string
	statusErr    error
	prepared     bool
	afterFailure []string
}

func (g *failureReportGit) Run(ctx context.Context, args ...string) (string, error) {
	command := strings.Join(args, " ")
	if !g.prepared {
		if command == "rev-parse HEAD" {
			g.prepared = true
			return "abc123", nil
		}
		return "", nil
	}
	g.afterFailure = append(g.afterFailure, command)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return g.status, g.statusErr
}

type failureReportAdapter struct {
	errorHarnessAdapter
	runHook func()
}

func (a *failureReportAdapter) Run(context.Context, harness.Request) (harness.Stream, error) {
	a.runHook()
	return nil, a.err
}
