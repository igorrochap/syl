package orchestration

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/runstate"
	"github.com/igorrochap/syl/internal/tracker"
)

func TestRunImplementRecordsRunStateLifecycle(t *testing.T) {
	root := t.TempDir()
	var observations []runstate.State
	readState := func() runstate.State {
		t.Helper()
		path := onlyRunStatePath(t, root)
		state, err := runstate.Read(path)
		if err != nil {
			t.Fatalf("read run state: %v", err)
		}
		return state
	}
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "implemented"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "revised"},
		}},
		runHooks:    []func(harness.Request){func(harness.Request) { observations = append(observations, readState()) }},
		resumeHooks: []func(harness.Request){func(harness.Request) { observations = append(observations, readState()) }},
	}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] file.go:1 — fix it\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
		runHooks:    []func(harness.Request){func(harness.Request) { observations = append(observations, readState()) }},
		resumeHooks: []func(harness.Request){func(harness.Request) { observations = append(observations, readState()) }},
	}
	var output strings.Builder
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root,
		WorkRoot:   root,
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 2},
		},
		IssueTracker: branchSetupTracker{},
		Ticket:       tracker.Ticket{Number: 180, Title: "Run state"},
		Implementer:  implementer,
		Reviewer:     reviewer,
		Git:          &implementRunGit{},
		OriginGit:    &implementRunGit{},
		Input:        strings.NewReader(""),
		Output:       &output,
		IdentificationBanner: func(string) error {
			state := readState()
			if state.Status != runstate.Running || state.Activity != runstate.Preparing {
				t.Fatalf("initial state = %#v, want running/preparing", state)
			}
			if state.PID <= 0 || state.Hostname == "" || state.StartedAt.IsZero() {
				t.Fatalf("initial state = %#v, want process and start metadata", state)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v", err)
	}
	if len(observations) != 4 {
		t.Fatalf("observations = %d, want implement/review/implement/review stages", len(observations))
	}
	if observations[0].Activity != runstate.Implementing || observations[0].Iteration != 1 || observations[0].MaxIterations != 2 {
		t.Fatalf("first implement state = %#v", observations[0])
	}
	if observations[1].Activity != runstate.Reviewing || observations[1].Iteration != 1 {
		t.Fatalf("first review state = %#v", observations[1])
	}
	if observations[2].Activity != runstate.Implementing || observations[2].Iteration != 2 {
		t.Fatalf("second implement state = %#v", observations[2])
	}
	if observations[3].Activity != runstate.Reviewing || observations[3].Iteration != 2 {
		t.Fatalf("second review state = %#v", observations[3])
	}
	final := readState()
	if final.Status != runstate.Approved || final.EndedAt == nil || final.Activity != "" || final.Question != "" {
		t.Fatalf("final state = %#v, want approved with ended time and no live activity", final)
	}
	if final.Kind != runstate.Implement || final.TicketRef != "#180" {
		t.Fatalf("final identity = %#v", final)
	}
}

func TestRunImplementRecordsQuestionPauseAndResume(t *testing.T) {
	root := t.TempDir()
	statePath := func() string { return onlyRunStatePath(t, root) }
	var questionState runstate.State
	var resumedState runstate.State
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "QUESTION:\nWhich option?\nEND QUESTION"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: "implemented after answer"},
		}},
		resumeHooks: []func(harness.Request){func(harness.Request) {
			resumedState = mustReadRunState(t, statePath())
		}},
	}
	input := &stateObservingReader{
		reader: strings.NewReader("Use option A\n"),
		onRead: func() { questionState = mustReadRunState(t, statePath()) },
	}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
	}}}
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root, WorkRoot: root,
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: branchSetupTracker{}, Ticket: tracker.Ticket{Number: 180},
		Implementer: implementer, Reviewer: reviewer, Git: &implementRunGit{}, OriginGit: &implementRunGit{},
		Input: input, Output: io.Discard,
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v", err)
	}
	if questionState.Activity != runstate.AwaitingAnswer || questionState.Question != "Which option?" {
		t.Fatalf("question state = %#v, want awaiting answer", questionState)
	}
	if resumedState.Activity != runstate.Implementing || resumedState.Question != "" {
		t.Fatalf("resumed state = %#v, want implementing without question", resumedState)
	}
}

func TestRunReviewRecordsStandaloneRunState(t *testing.T) {
	root := t.TempDir()
	var reviewingState runstate.State
	git := &reviewDiffGit{responses: map[string]reviewDiffResponse{
		"rev-parse HEAD":                          {output: "branch-point\n"},
		"diff branch-point":                       {output: "diff --git a/change.txt b/change.txt\n+reviewed\n"},
		"ls-files --others --exclude-standard -z": {},
	}}
	err := RunReview(context.Background(), ReviewOptions{
		OriginRoot: root, WorkRoot: root, Git: git, Input: strings.NewReader(""), Output: io.Discard,
		ProjectConfig: config.Config{Roles: config.RolesConfig{Review: config.RoleConfig{Harness: config.HarnessClaude}}},
		Adapter: &capturingReviewAdapter{runHook: func() {
			reviewingState = mustReadRunState(t, onlyRunStatePath(t, root))
		}},
	})
	if err != nil {
		t.Fatalf("RunReview() error = %v", err)
	}
	if reviewingState.Status != runstate.Running || reviewingState.Activity != runstate.Reviewing || reviewingState.Iteration != 1 || reviewingState.MaxIterations != 1 {
		t.Fatalf("active standalone review state = %#v, want running/reviewing at iteration 1/1", reviewingState)
	}
	state := mustReadRunState(t, onlyRunStatePath(t, root))
	if state.Kind != runstate.Review || state.Iteration != 1 || state.MaxIterations != 1 || state.Status != runstate.Approved || state.EndedAt == nil {
		t.Fatalf("standalone review state = %#v", state)
	}
}

func TestRunImplementRecordsExhaustedAndCancelledStates(t *testing.T) {
	tests := []struct {
		name       string
		ctx        func() context.Context
		reviewer   harness.Adapter
		wantStatus runstate.Status
		wantError  string
	}{
		{
			name: "exhausted",
			ctx:  context.Background,
			reviewer: &scriptedConversationAdapter{runs: [][]harness.Event{{
				{Type: harness.EventSession, SessionID: "review-1"},
				{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: More work\nFINDINGS:\n"},
			}}},
			wantStatus: runstate.Exhausted,
			wantError:  "max iterations",
		},
		{
			name:       "cancelled",
			ctx:        context.Background,
			reviewer:   &errorHarnessAdapter{err: context.Canceled},
			wantStatus: runstate.Cancelled,
			wantError:  "run review harness",
		},
		{
			name:       "failed",
			ctx:        context.Background,
			reviewer:   &errorHarnessAdapter{err: errors.New("harness failed")},
			wantStatus: runstate.Failed,
			wantError:  "run review harness",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sylHome := t.TempDir()
			err := RunImplement(test.ctx(), ImplementOptions{
				OriginRoot: root, WorkRoot: root, Git: &implementRunGit{}, OriginGit: &implementRunGit{},
				SylHome:      sylHome,
				IssueTracker: branchSetupTracker{}, Ticket: tracker.Ticket{Number: 180},
				ProjectConfig: config.Config{
					Roles: config.RolesConfig{
						Implement: config.RoleConfig{Harness: config.HarnessCodex},
						Review:    config.RoleConfig{Harness: config.HarnessClaude},
					},
					Loop: config.LoopConfig{MaxIterations: 1},
				},
				Implementer: &capturingImplementAdapter{response: "implemented"}, Reviewer: test.reviewer,
				Input: strings.NewReader(""), Output: io.Discard,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("RunImplement() error = %v, want %q", err, test.wantError)
			}
			state := mustReadRunState(t, onlyRunStatePath(t, root))
			if state.Status != test.wantStatus || state.EndedAt == nil {
				t.Fatalf("state = %#v, want %s with ended time", state, test.wantStatus)
			}
			markers, markerErr := runmarker.List(sylHome)
			if markerErr != nil {
				t.Fatalf("List() after %s Run: %v", test.name, markerErr)
			}
			if len(markers) != 0 {
				t.Fatalf("markers after %s Run = %#v, want none", test.name, markers)
			}
		})
	}
}

func TestRunContinuesWhenRunStateWritesFail(t *testing.T) {
	root := t.TempDir()
	var output strings.Builder
	var runDir string
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root, WorkRoot: root, Git: &implementRunGit{}, OriginGit: &implementRunGit{},
		IssueTracker: branchSetupTracker{}, Ticket: tracker.Ticket{Number: 180},
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		Implementer: &capturingImplementAdapter{response: "implemented"},
		Reviewer:    &capturingImplementAdapter{response: conversationTestVerdict},
		Input:       strings.NewReader(""), Output: &output,
		IdentificationBanner: func(artifactDir string) error {
			runDir = artifactDir
			if err := os.Remove(runstate.Path(runDir)); err != nil {
				t.Fatalf("remove run state: %v", err)
			}
			return os.Mkdir(runstate.Path(runDir), 0o755)
		},
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v, want normal completion", err)
	}
	if !strings.Contains(output.String(), "warning: write run state") {
		t.Fatalf("output = %q, want run-state warning", output.String())
	}
}

func onlyRunStatePath(t *testing.T, root string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".syl", "runs", "*", "run-state.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("run state paths = %v, error = %v", matches, err)
	}
	return matches[0]
}

func mustReadRunState(t *testing.T, path string) runstate.State {
	t.Helper()
	state, err := runstate.Read(path)
	if err != nil {
		t.Fatalf("read run state: %v", err)
	}
	return state
}

type stateObservingReader struct {
	reader io.Reader
	onRead func()
	seen   bool
}

func (r *stateObservingReader) Read(p []byte) (int, error) {
	if !r.seen {
		r.seen = true
		r.onRead()
	}
	return r.reader.Read(p)
}

type errorHarnessAdapter struct {
	err error
}

func (a *errorHarnessAdapter) Run(context.Context, harness.Request) (harness.Stream, error) {
	return nil, a.err
}

func (*errorHarnessAdapter) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return nil, errors.New("unexpected resume")
}

func (*errorHarnessAdapter) Attach(context.Context, harness.Request) error { return nil }

func (*errorHarnessAdapter) AttachSession(context.Context, string, harness.Request) error { return nil }

var _ harness.Adapter = (*errorHarnessAdapter)(nil)

var _ io.Reader = (*stateObservingReader)(nil)
