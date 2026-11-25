package orchestration

import (
	"bytes"
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
	want := "run implement harness: harness stopped unexpectedly\n\n" + noTranscriptFailureDetails(t, options) + "\n\n" +
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
	var output bytes.Buffer
	options.Output = &output
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
	if !strings.Contains(output.String(), "cancelled (0.0s)") {
		t.Fatalf("terminal output = %q, want a cancelled Activity span with duration", output.String())
	}
	for _, detail := range []string{"Failure transcript (", "Session transcript:", "Run directory:"} {
		if strings.Contains(err.Error(), detail) || strings.Contains(output.String(), detail) {
			t.Fatalf("cancellation output includes failure detail %q: error=%q output=%q", detail, err, output.String())
		}
	}
	assertFailedImplementState(t, options, git, runrecord.Cancelled)
	activities, err := runrecord.ReadActivities(onlyRunDirectory(t, options.OriginRoot), nil)
	if err != nil {
		t.Fatalf("read activities.jsonl: %v", err)
	}
	if !activityLogHasOutcome(activities, activitySpanCancelled) {
		t.Fatalf("Activity log = %#v, want a cancelled span outcome", activities)
	}
}

func TestRunImplementFailureReportsVerboseTranscriptTailAndPaths(t *testing.T) {
	options, _ := failedImplementOptions(t)
	var output bytes.Buffer
	options.Output = &output
	const transcriptPath = "/sessions/failed-role.jsonl"
	events := []harness.Event{{Type: harness.EventSession, SessionID: "failed-role"}}
	for line := 1; line <= 25; line++ {
		if line%2 == 0 {
			events = append(events, harness.Event{Type: harness.EventToolUse, ToolName: "Read", ArgumentGist: fmt.Sprintf("file-%02d.go", line)})
			continue
		}
		events = append(events, harness.Event{Type: harness.EventAssistantText, Text: fmt.Sprintf("failed prose %02d\n", line)})
	}
	events = append(events, harness.Event{Type: harness.EventResult, Text: "fake harness exploded", IsError: true})
	options.Implementer = &scriptedConversationAdapter{
		runs:            [][]harness.Event{events},
		transcriptPaths: map[string]string{"failed-role": transcriptPath},
	}

	err := RunImplement(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "harness returned an error: fake harness exploded") {
		t.Fatalf("RunImplement() error = %v, want the harness error", err)
	}
	if !strings.Contains(err.Error(), "run implement harness: harness returned an error: fake harness exploded") {
		t.Fatalf("RunImplement() error = %v, want the current Run failure message", err)
	}
	if !strings.Contains(err.Error(), "Session transcript: "+transcriptPath) {
		t.Fatalf("RunImplement() error = %v, want the Session transcript path", err)
	}
	runDirectory := onlyRunDirectory(t, options.OriginRoot)
	if !strings.Contains(err.Error(), "Run directory: "+runDirectory) {
		t.Fatalf("RunImplement() error = %v, want the Run directory", err)
	}
	if !strings.Contains(output.String(), "failed (0.0s)") {
		t.Fatalf("terminal output = %q, want a failed Role span with duration", output.String())
	}
	activities, readActivitiesErr := runrecord.ReadActivities(runDirectory, nil)
	if readActivitiesErr != nil {
		t.Fatalf("read activities.jsonl: %v", readActivitiesErr)
	}
	if !activityLogHasOutcome(activities, activitySpanFailed) {
		t.Fatalf("Activity log = %#v, want a failed span outcome", activities)
	}
	marker := "Failure transcript (last 20 lines):\n"
	start := strings.Index(err.Error(), marker)
	if start < 0 {
		t.Fatalf("RunImplement() error = %v, want a transcript tail", err)
	}
	tailStart := start + len(marker)
	pathStart := strings.Index(err.Error()[tailStart:], "\nSession transcript:")
	if pathStart < 0 {
		t.Fatalf("RunImplement() error = %v, want the tail to end before the Session transcript path", err)
	}
	tail := strings.TrimSuffix(err.Error()[tailStart:tailStart+pathStart], "\n")
	lines := strings.Split(tail, "\n")
	if len(lines) > 20 {
		t.Fatalf("failure tail has %d lines, want at most 20: %q", len(lines), tail)
	}
	if strings.Contains(tail, "failed prose 01") || strings.Contains(tail, "Earlier span") {
		t.Fatalf("failure tail contains old transcript lines: %q", tail)
	}
	if !strings.Contains(tail, "failed prose 25") || !strings.Contains(tail, "tool: Read — file-24.go") {
		t.Fatalf("failure tail = %q, want the newest prose and rendered tool lines", tail)
	}
}

func TestRunImplementReviewerFailureMatchesGoldenTailWithoutEarlierSpan(t *testing.T) {
	options, _ := failedImplementOptions(t)
	options.Git = &implementRunGit{}
	options.OriginGit = &implementRunGit{}
	const earlierSpan = "Earlier implementer span transcript."
	const transcriptPath = "/sessions/reviewer-failure.jsonl"
	options.Implementer = &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "implement-session"},
		{Type: harness.EventAssistantText, Text: earlierSpan},
	}}}
	reviewerEvents := []harness.Event{{Type: harness.EventSession, SessionID: "review-failure"}}
	for line := 1; line <= 25; line++ {
		if line%2 == 0 {
			reviewerEvents = append(reviewerEvents, harness.Event{
				Type: harness.EventToolUse, ToolName: "Read", ArgumentGist: fmt.Sprintf("file-%02d.go", line),
			})
			continue
		}
		reviewerEvents = append(reviewerEvents, harness.Event{
			Type: harness.EventAssistantText, Text: fmt.Sprintf("review prose %02d\n", line),
		})
	}
	reviewerEvents = append(reviewerEvents, harness.Event{
		Type: harness.EventResult, Text: "fake reviewer exploded", IsError: true,
	})
	options.Reviewer = &scriptedConversationAdapter{
		runs:            [][]harness.Event{reviewerEvents},
		transcriptPaths: map[string]string{"review-failure": transcriptPath},
	}

	err := RunImplement(context.Background(), options)
	if err == nil {
		t.Fatal("RunImplement() error = nil, want reviewer harness failure")
	}
	runDirectory := onlyRunDirectory(t, options.OriginRoot)
	implementTranscript, readErr := os.ReadFile(filepath.Join(runDirectory, "iteration-01-implement.transcript"))
	if readErr != nil {
		t.Fatalf("read earlier implementer transcript: %v", readErr)
	}
	if !strings.Contains(string(implementTranscript), earlierSpan) {
		t.Fatalf("implementer transcript = %q, want the earlier-span marker", implementTranscript)
	}
	actual := strings.ReplaceAll(err.Error(), transcriptPath, "<session-transcript>")
	actual = strings.ReplaceAll(actual, runDirectory, "<run-directory>")
	want, readErr := os.ReadFile(filepath.Join("testdata", "reviewer-role-failure.golden"))
	if readErr != nil {
		t.Fatalf("read reviewer failure golden: %v", readErr)
	}
	if actual+"\n" != string(want) {
		t.Fatalf("reviewer failure = %q, want golden %q", actual, want)
	}
	if strings.Contains(actual, earlierSpan) {
		t.Fatalf("reviewer failure tail contains earlier Role transcript %q", earlierSpan)
	}
}

func TestRunImplementFailureWithCleanWorkRootLeavesErrorUnchanged(t *testing.T) {
	options, git := failedImplementOptions(t)
	err := RunImplement(context.Background(), options)
	want := "run implement harness: harness failed\n\n" + noTranscriptFailureDetails(t, options)
	if err == nil || err.Error() != want {
		t.Fatalf("RunImplement() error = %v, want failure details with the original error", err)
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
	var output bytes.Buffer
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
		Output:       &output,
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
	if !strings.Contains(output.String(), "recovering stalled session (0.0s)") {
		t.Fatalf("terminal output = %q, want the implementer recovery child span and duration", output.String())
	}
	runDir := filepath.Dir(onlyRunStatePath(t, root))
	activities, err := runrecord.ReadActivities(runDir, nil)
	if err != nil {
		t.Fatalf("read activities.jsonl: %v", err)
	}
	starts := make(map[string]runrecord.ActivitySpanStart)
	ends := make(map[string]runrecord.ActivitySpanEnd)
	for _, event := range activities {
		if event.Start != nil {
			starts[event.Start.SpanID] = *event.Start
		}
		if event.End != nil {
			ends[event.End.SpanID] = *event.End
		}
	}
	for _, start := range starts {
		if start.Label != "recovering stalled session" {
			continue
		}
		parent, parentExists := starts[start.ParentSpanID]
		end, endExists := ends[start.SpanID]
		if parentExists && parent.Activity == runrecord.Implementing && endExists &&
			end.Outcome == string(activitySpanCompleted) && !end.EndedAt.IsZero() {
			return
		}
		t.Fatalf("implementer recovery span = %#v end = %#v parent = %#v; want a completed child under the implementer", start, end, parent)
	}
	t.Fatal("activities.jsonl has no implementer recovery child span")
}

func TestRunImplementFailureReportsWorktreeChanges(t *testing.T) {
	options, git := failedImplementOptions(t)
	options.WorkRoot = t.TempDir()
	options.ProvisionedWorktree = &Worktree{Path: options.WorkRoot, Branch: "feat/worktree"}
	git.status = "?? worktree-only.go\n"
	originGit := &failureReportGit{prepared: true, status: " M origin-only.go\n"}
	options.OriginGit = originGit

	err := RunImplement(context.Background(), options)
	want := "run implement harness: harness failed\n\n" + noTranscriptFailureDetails(t, options) + "\n\n" +
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
	want := "run implement harness: harness failed\n\n" + noTranscriptFailureDetails(t, options) + "\n\n" +
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
	want := "run implement harness: original harness error\n\n" + noTranscriptFailureDetails(t, options) + "\n\n" +
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

func onlyRunDirectory(t *testing.T, root string) string {
	t.Helper()
	return filepath.Dir(onlyRunStatePath(t, root))
}

func noTranscriptFailureDetails(t *testing.T, options ImplementOptions) string {
	t.Helper()
	return "Failure transcript (last 20 lines):\n(no transcript lines captured)\n" +
		"Session transcript: unavailable (no session ID was captured)\n" +
		"Run directory: " + onlyRunDirectory(t, options.OriginRoot)
}

func activityLogHasOutcome(activities []runrecord.ActivityEvent, want activitySpanOutcome) bool {
	for _, event := range activities {
		if event.End != nil && event.End.Outcome == string(want) {
			return true
		}
	}
	return false
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
