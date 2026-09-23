package orchestration

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/tracker"
)

func TestRunImplementCreatesAndRemovesLiveRunMarker(t *testing.T) {
	root := t.TempDir()
	sylHome := t.TempDir()
	var duringRun []runmarker.Pointer
	observeMarker := func() {
		pointers, err := runmarker.List(sylHome)
		if err != nil {
			t.Fatalf("List() during implement: %v", err)
		}
		duringRun = append(duringRun, pointers...)
	}
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "implemented"},
		}},
		runHooks: []func(harness.Request){func(harness.Request) { observeMarker() }},
	}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: conversationTestVerdict},
	}}}

	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root,
		WorkRoot:   root,
		SylHome:    sylHome,
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: branchSetupTracker{},
		Ticket:       tracker.Ticket{Number: 181},
		Implementer:  implementer,
		Reviewer:     reviewer,
		Git:          &implementRunGit{},
		OriginGit:    &implementRunGit{},
		Input:        strings.NewReader(""),
		Output:       io.Discard,
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v", err)
	}
	if len(duringRun) != 1 {
		t.Fatalf("markers during implement = %#v, want exactly one", duringRun)
	}
	if duringRun[0].ProjectPath != resolvedPath(t, root) {
		t.Fatalf("marker project path = %q, want %q", duringRun[0].ProjectPath, resolvedPath(t, root))
	}
	if duringRun[0].RunDir == "" || filepath.Base(duringRun[0].RunDir) == "" {
		t.Fatalf("marker run directory = %q, want a Run directory", duringRun[0].RunDir)
	}
	if duringRun[0].TicketRef != "#181" {
		t.Fatalf("marker ticket reference = %q, want #181", duringRun[0].TicketRef)
	}
	afterRun, err := runmarker.List(sylHome)
	if err != nil {
		t.Fatalf("List() after implement: %v", err)
	}
	if len(afterRun) != 0 {
		t.Fatalf("markers after implement = %#v, want none", afterRun)
	}
}

func TestRunReviewCreatesAndRemovesLiveRunMarker(t *testing.T) {
	root := t.TempDir()
	sylHome := t.TempDir()
	git := &reviewDiffGit{responses: map[string]reviewDiffResponse{
		"rev-parse HEAD":                          {output: "branch-point\n"},
		"diff branch-point":                       {output: "diff --git a/change.txt b/change.txt\n+reviewed\n"},
		"ls-files --others --exclude-standard -z": {},
	}}
	var duringRun []runmarker.Pointer
	adapter := &capturingReviewAdapter{runHook: func() {
		pointers, err := runmarker.List(sylHome)
		if err != nil {
			t.Fatalf("List() during review: %v", err)
		}
		duringRun = pointers
	}}

	err := RunReview(context.Background(), ReviewOptions{
		OriginRoot: root,
		WorkRoot:   root,
		SylHome:    sylHome,
		Git:        git,
		Input:      strings.NewReader(""),
		Output:     io.Discard,
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{Review: config.RoleConfig{Harness: config.HarnessClaude}},
		},
		Adapter: adapter,
	})
	if err != nil {
		t.Fatalf("RunReview() error = %v", err)
	}
	if len(duringRun) != 1 || duringRun[0].TicketRef != "" {
		t.Fatalf("markers during standalone review = %#v, want one unticketed marker", duringRun)
	}
	afterRun, err := runmarker.List(sylHome)
	if err != nil {
		t.Fatalf("List() after review: %v", err)
	}
	if len(afterRun) != 0 {
		t.Fatalf("markers after review = %#v, want none", afterRun)
	}
}

func TestRunContinuesWhenLiveRunMarkerWriteFails(t *testing.T) {
	root := t.TempDir()
	sylHome := t.TempDir()
	activePath := filepath.Join(sylHome, "active")
	if err := os.WriteFile(activePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root,
		WorkRoot:   root,
		SylHome:    sylHome,
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: branchSetupTracker{},
		Ticket:       tracker.Ticket{Number: 181},
		Implementer:  &capturingImplementAdapter{response: "implemented"},
		Reviewer:     &capturingImplementAdapter{response: conversationTestVerdict},
		Git:          &implementRunGit{},
		OriginGit:    &implementRunGit{},
		Input:        strings.NewReader(""),
		Output:       &output,
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v, want normal completion", err)
	}
	if !strings.Contains(output.String(), "warning: create live-run marker") {
		t.Fatalf("output = %q, want marker-write warning", output.String())
	}
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve path %q: %v", path, err)
	}
	return filepath.Clean(resolved)
}
