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

func TestRunImplementFailsWhenStalledRecoveryResumeStallsAgain(t *testing.T) {
	options, git := failedImplementOptions(t)
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Partial implementation."},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Still working."},
		}},
		runWaitErrors: []error{&harness.SessionStalledError{
			SessionID:    "implement-session",
			Limit:        harness.NoProgressLimit,
			StateSummary: "first stalled state",
		}},
		resumeWaitErrors: []error{&harness.SessionStalledError{
			SessionID:    "implement-session",
			Limit:        harness.NoProgressLimit,
			StateSummary: "second stalled state summary",
		}},
	}
	options.Implementer = implementer

	err := RunImplement(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "no-progress limit") ||
		!strings.Contains(err.Error(), "second stalled state summary") {
		t.Fatalf("RunImplement() error = %v, want second no-progress limit and session summary", err)
	}
	if len(implementer.resumeCalls) != 1 {
		t.Fatalf("implementer resume calls = %d, want exactly one stalled-session recovery", len(implementer.resumeCalls))
	}
	if implementer.runCalls != 1 || implementer.resumeCalls[0].sessionID != "implement-session" {
		t.Fatalf("implementer calls = (%d runs, %#v resumes), want one run and a same-session resume", implementer.runCalls, implementer.resumeCalls)
	}
	if !strings.Contains(implementer.resumeCalls[0].prompt, "Continue the requested implementation") {
		t.Fatalf("recovery prompt = %q, want implementation continuation", implementer.resumeCalls[0].prompt)
	}
	assertFailedImplementState(t, options, git, runrecord.Failed)
}

func TestRunImplementRecoversStalledImplementerOnce(t *testing.T) {
	root := t.TempDir()
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Partial implementation."},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-session"},
			{Type: harness.EventAssistantText, Text: "Implementation completed."},
		}},
		runWaitErrors: []error{&harness.SessionStalledError{
			SessionID:    "implement-session",
			Limit:        harness.NoProgressLimit,
			StateSummary: "implementation resumed after no progress",
			Reports: []harness.SubagentReport{{
				Path: "/claude/session/subagents/agent-implementation.jsonl",
				Text: "The helper found the missing validation case.",
			}, {
				Path: "/claude/session/subagents/agent-tests.jsonl",
				Text: "The helper added a regression test.",
			}},
		}},
	}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: conversationTestVerdict},
	}}}

	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root,
		WorkRoot:   root,
		OpenRun:    NewDiskRunOpener(root, sylhome.Dir{}, io.Discard),
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessClaude},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: branchSetupTracker{},
		Ticket:       tracker.Ticket{Number: 247},
		Implementer:  implementer,
		Reviewer:     reviewer,
		Git:          &implementRunGit{},
		OriginGit:    &implementRunGit{},
		Input:        strings.NewReader(""),
		Output:       io.Discard,
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v, want successful recovery and review", err)
	}
	if implementer.runCalls != 1 || len(implementer.resumeCalls) != 1 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want one run and one recovery resume", implementer.runCalls, len(implementer.resumeCalls))
	}
	if implementer.resumeCalls[0].sessionID != "implement-session" {
		t.Fatalf("recovery session = %q, want implement-session", implementer.resumeCalls[0].sessionID)
	}
	for _, want := range []string{
		"no-progress limit",
		"Continue the requested implementation",
		"End your turn normally",
		"The helper found the missing validation case.",
		"The helper added a regression test.",
	} {
		if !strings.Contains(implementer.resumeCalls[0].prompt, want) {
			t.Errorf("implementer recovery prompt = %q, want %q", implementer.resumeCalls[0].prompt, want)
		}
	}
	if reviewer.runCalls != 1 {
		t.Fatalf("reviewer run calls = %d, want review after recovered implementation", reviewer.runCalls)
	}
	state := mustReadRunState(t, onlyRunStatePath(t, root))
	if state.Status != runrecord.Approved || state.Iteration != 1 {
		t.Fatalf("final Run state = %#v, want approved in iteration 1", state)
	}
	sessionsPath := filepath.Join(filepath.Dir(onlyRunStatePath(t, root)), artifactFilename(sessionsArtifact, 0))
	sessions, err := os.ReadFile(sessionsPath)
	if err != nil {
		t.Fatalf("read recorded sessions: %v", err)
	}
	if !strings.Contains(string(sessions), "iteration 1 implement: implement-session") ||
		strings.Contains(string(sessions), "iteration 2 implement:") {
		t.Fatalf("recorded sessions = %q, want recovered implement turn in iteration 1", sessions)
	}
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
