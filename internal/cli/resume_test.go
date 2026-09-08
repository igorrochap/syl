package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
)

func TestResolveResumeTargetSelectsNewestRunAndHighestNumericIteration(t *testing.T) {
	root := t.TempDir()
	workRoot := t.TempDir()
	writeResumeConfig(t, root)

	selectedRun := writeResumeRun(t, root, "20260908T120000.000000000Z-42", fmt.Sprintf(
		"Work root: %s\nImplementer harness: claude\nReviewer harness: claude\n", workRoot,
	), "iteration 10 implement: session-ten\niteration 2 implement: session-two\n", true, true)
	writeResumeRun(t, root, "20260908T130000.000000000Z-43", fmt.Sprintf(
		"Work root: %s\nReviewer harness: claude\n", workRoot,
	), "iteration 1 review: review-only\n", true, false)
	writeResumeRun(t, root, "20260908T110000.000000000Z-41", fmt.Sprintf(
		"Work root: %s\nImplementer harness: codex\n", workRoot,
	), "iteration 3 implement: old-session\n", true, false)

	target, err := resolveResumeTarget(root, "implement")
	if err != nil {
		t.Fatalf("resolveResumeTarget() error = %v", err)
	}
	if target.runDir != selectedRun {
		t.Fatalf("run directory = %q, want %q", target.runDir, selectedRun)
	}
	if target.iteration != 10 || target.sessionID != "session-ten" {
		t.Fatalf("target = %#v, want iteration 10 and session-ten", target)
	}

	reviewTarget, err := resolveResumeTarget(root, "review")
	if err != nil {
		t.Fatalf("resolveResumeTarget() review error = %v", err)
	}
	if reviewTarget.iteration != 1 || reviewTarget.sessionID != "review-only" {
		t.Fatalf("review target = %#v, want iteration 1 and review-only", reviewTarget)
	}
}

func TestResolveResumeTargetMatchesStandaloneReviewSession(t *testing.T) {
	root := t.TempDir()
	workRoot := t.TempDir()
	writeResumeConfig(t, root)
	runDir := writeResumeRun(t, root, "20260908T140000.000000000Z-review", fmt.Sprintf(
		"Work root: %s\nReviewer harness: claude\n", workRoot,
	), "iteration 0 review: standalone-review\n", true, true)

	target, err := resolveResumeTarget(root, "review")
	if err != nil {
		t.Fatalf("resolveResumeTarget() error = %v", err)
	}
	if target.runDir != runDir || target.iteration != 0 || target.sessionID != "standalone-review" {
		t.Fatalf("target = %#v, want standalone review target", target)
	}
}

func TestResumeHandsOffRecordedHarnessAtRecordedRootWithCurrentMCP(t *testing.T) {
	root := t.TempDir()
	workRoot := t.TempDir()
	writeResumeConfig(t, root)
	runName := "20260908T150000.000000000Z-42"
	writeResumeRun(t, root, runName, fmt.Sprintf(
		"Work root: %s\nImplementer harness: claude\nReviewer harness: claude\n", workRoot,
	), "iteration 1 implement: recorded-session\n", true, true)

	adapter := &recordingResumeHarness{}
	var harnessRoots []string
	app := New(root, root, Dependencies{
		Harnesses: func(gotRoot string) map[string]harness.Adapter {
			harnessRoots = append(harnessRoots, gotRoot)
			return map[string]harness.Adapter{"claude": adapter}
		},
	})
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"resume", "implement"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume code = %d, stderr = %q", code, stderr.String())
	}
	if len(adapter.calls) != 1 {
		t.Fatalf("AttachSession calls = %d, want 1", len(adapter.calls))
	}
	call := adapter.calls[0]
	if call.sessionID != "recorded-session" {
		t.Fatalf("session id = %q, want recorded-session", call.sessionID)
	}
	wantRequest := harness.Request{MCP: false}
	if call.request != wantRequest {
		t.Fatalf("request = %#v, want %#v", call.request, wantRequest)
	}
	if len(harnessRoots) != 1 || harnessRoots[0] != workRoot {
		t.Fatalf("harness roots = %v, want [%s]", harnessRoots, workRoot)
	}
	for _, expected := range []string{
		runName,
		"role:",
		"implement",
		"iteration:",
		"1",
		"harness:",
		"claude",
		"session:",
		"recorded-session",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), expected)
		}
	}
	if strings.Contains(stdout.String(), "did not complete") {
		t.Fatalf("stdout = %q, want no incomplete-run warning", stdout.String())
	}
}

func TestResumeWarnsBeforeHandingOffIncompleteRun(t *testing.T) {
	root := t.TempDir()
	workRoot := t.TempDir()
	writeResumeConfig(t, root)
	writeResumeRun(t, root, "20260908T160000.000000000Z-42", fmt.Sprintf(
		"Work root: %s\nReviewer harness: claude\n", workRoot,
	), "iteration 0 review: review-session\n", true, false)
	adapter := &recordingResumeHarness{}
	app := newResumeApp(root, adapter)
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"resume", "review"}, &stdout, &stderr); code != 0 {
		t.Fatalf("resume code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "did not complete") || !strings.Contains(stdout.String(), "still executing") {
		t.Fatalf("stdout = %q, want incomplete-run warning", stdout.String())
	}
}

func TestResumeRejectsUnknownRoleAndListsValidRoles(t *testing.T) {
	root := t.TempDir()
	writeResumeConfig(t, root)
	adapter := &recordingResumeHarness{}
	app := newResumeApp(root, adapter)

	t.Run("missing Role", func(t *testing.T) {
		var stdout, stderr strings.Builder
		code := app.Run(context.Background(), []string{"resume"}, &stdout, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "valid Roles: implement or review") {
			t.Fatalf("resume code = %d, stderr = %q, want required Role error", code, stderr.String())
		}
	})

	for _, role := range []string{"plan", "nonsense"} {
		t.Run(role, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := app.Run(context.Background(), []string{"resume", role}, &stdout, &stderr)
			if code == 0 {
				t.Fatal("resume code = 0, want rejection")
			}
			if !strings.Contains(stderr.String(), "valid Roles: implement or review") {
				t.Fatalf("stderr = %q, want valid Role list", stderr.String())
			}
		})
	}
	if len(adapter.calls) != 0 {
		t.Fatalf("AttachSession calls = %d, want none", len(adapter.calls))
	}
}

func TestResumeReportsDistinctSelectionAndArtifactErrors(t *testing.T) {
	tests := []struct {
		name        string
		makeProject func(t *testing.T, root string)
		want        string
	}{
		{
			name: "runs directory missing",
			makeProject: func(t *testing.T, root string) {
				writeResumeConfig(t, root)
			},
			want: "no run directories found",
		},
		{
			name: "no requested session",
			makeProject: func(t *testing.T, root string) {
				writeResumeConfig(t, root)
				writeResumeRun(t, root, "20260908T170000.000000000Z-42", "", "not a session line\n", true, false)
			},
			want: "no run has a recorded implement session",
		},
		{
			name: "missing work root metadata",
			makeProject: func(t *testing.T, root string) {
				writeResumeConfig(t, root)
				writeResumeRun(t, root, "20260908T180000.000000000Z-42", "Implementer harness: claude\n", "iteration 1 implement: session\n", true, false)
			},
			want: "runs recorded before this feature cannot be resumed",
		},
		{
			name: "missing recorded work root",
			makeProject: func(t *testing.T, root string) {
				writeResumeConfig(t, root)
				missing := filepath.Join(root, "removed-work-root")
				writeResumeRun(t, root, "20260908T190000.000000000Z-42", fmt.Sprintf(
					"Work root: %s\nImplementer harness: claude\n", missing,
				), "iteration 1 implement: session\n", true, false)
			},
			want: "does not exist",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.makeProject(t, root)
			adapter := &recordingResumeHarness{}
			app := newResumeApp(root, adapter)
			var stdout, stderr strings.Builder

			code := app.Run(context.Background(), []string{"resume", "implement"}, &stdout, &stderr)
			if code == 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("resume code = %d, stderr = %q, want %q", code, stderr.String(), test.want)
			}
			if len(adapter.calls) != 0 {
				t.Fatalf("AttachSession calls = %d, want none", len(adapter.calls))
			}
		})
	}
}

func TestResumeTreatsAbsentAndEmptySessionsAsNoSession(t *testing.T) {
	for _, sessionsPresent := range []bool{false, true} {
		name := "absent"
		if sessionsPresent {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeResumeConfig(t, root)
			writeResumeRun(t, root, "20260908T200000.000000000Z-42", "", "", sessionsPresent, false)
			adapter := &recordingResumeHarness{}
			app := newResumeApp(root, adapter)
			var stdout, stderr strings.Builder

			code := app.Run(context.Background(), []string{"resume", "review"}, &stdout, &stderr)
			if code == 0 || !strings.Contains(stderr.String(), "no run has a recorded review session") {
				t.Fatalf("resume code = %d, stderr = %q, want no-session error", code, stderr.String())
			}
		})
	}
}

type resumeAttachCall struct {
	sessionID string
	request   harness.Request
}

type recordingResumeHarness struct {
	calls []resumeAttachCall
}

func (h *recordingResumeHarness) Run(context.Context, harness.Request) (harness.Stream, error) {
	return emptyHarnessStream{}, nil
}

func (h *recordingResumeHarness) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return emptyHarnessStream{}, nil
}

func (h *recordingResumeHarness) Attach(context.Context, harness.Request) error { return nil }

func (h *recordingResumeHarness) AttachSession(_ context.Context, sessionID string, request harness.Request) error {
	h.calls = append(h.calls, resumeAttachCall{sessionID: sessionID, request: request})
	return nil
}

func newResumeApp(root string, adapter harness.Adapter) *App {
	return New(root, root, Dependencies{
		Harnesses: func(string) map[string]harness.Adapter {
			return map[string]harness.Adapter{"claude": adapter}
		},
	})
}

func writeResumeConfig(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := `[tracker]
issues = "local"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-planner"
effort = "high"

[roles.implement]
harness = "codex"
model = "gpt-current"
effort = "high"
mcp = false

[roles.review]
harness = "claude"
model = "claude-reviewer"
effort = "medium"
mcp = true
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeResumeRun(t *testing.T, root, name, metadata, sessions string, writeSessions, writeSummary bool) string {
	t.Helper()
	runDir := filepath.Join(root, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if metadata != "" {
		if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte(metadata), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if writeSessions {
		if err := os.WriteFile(filepath.Join(runDir, "sessions.txt"), []byte(sessions), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if writeSummary {
		if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte("complete\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return runDir
}
