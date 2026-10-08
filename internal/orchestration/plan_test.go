package orchestration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
)

func TestWriteCreatedTicketsDoesNotSuggestKnownBlockedTicket(t *testing.T) {
	var output bytes.Buffer
	created := []tracker.Ticket{{Number: 22, Body: "## Blocked by\n\n- #21"}}

	if err := writeCreatedTickets(&output, created); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Created: #22") {
		t.Fatalf("output = %q, want created ticket report", output.String())
	}
	if strings.Contains(output.String(), "Next: syl implement") {
		t.Fatalf("output = %q, want no next command for known blocked tickets", output.String())
	}
}

func TestNextCreatedTicketFallsBackOnlyToUnknownBlockerStatus(t *testing.T) {
	created := []tracker.Ticket{
		{Number: 22, Body: "No dependency metadata."},
		{Number: 23, Body: "## Blocked by\n\n- #21"},
	}

	next, ok := nextCreatedTicket(created)
	if !ok || next != 22 {
		t.Fatalf("nextCreatedTicket() = (%d, %t), want (22, true)", next, ok)
	}
}

func TestWriteCreatedTicketsPropagatesRendererErrors(t *testing.T) {
	tests := []struct {
		name    string
		created []tracker.Ticket
		failAt  int
	}{
		{name: "empty report", failAt: 1},
		{
			name:    "next ticket",
			created: []tracker.Ticket{{Number: 22, Body: "**Blocked by:** None"}},
			failAt:  2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &planFailAtWriter{failAt: test.failAt}
			if err := writeCreatedTickets(writer, test.created); err == nil {
				t.Fatalf("writeCreatedTickets() error = nil, want write failure at call %d", test.failAt)
			}
		})
	}
}

type planFailAtWriter struct {
	failAt int
	writes int
	output bytes.Buffer
}

func (w *planFailAtWriter) Write(value []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("write failed")
	}
	return w.output.Write(value)
}

func TestRunPlanPassesRoleSandboxMode(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, ".agents", "skills", "to-tickets")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := &capturingPlanAdapter{}
	err := RunPlan(context.Background(), PlanOptions{
		WorkRoot: root, Topic: "sandbox mode", IssueTracker: branchSetupTracker{}, Adapter: adapter,
		Role: config.RoleConfig{Model: "gpt-5.6-luna", Effort: config.EffortHigh, SandboxMode: config.SandboxModeReadOnly},
	})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.request.SandboxMode != config.SandboxModeReadOnly {
		t.Fatalf("plan sandbox = %q, want read-only", adapter.request.SandboxMode)
	}
}

func TestRunPlanAllowsMissingRemoteAroundSession(t *testing.T) {
	tests := []struct {
		name      string
		snapshots []planSnapshot
		want      []string
	}{
		{
			name: "remote appears and reports all tickets",
			snapshots: []planSnapshot{
				{err: tracker.ErrNoRemote},
				{tickets: []tracker.Ticket{
					{Number: 2, Body: "**Blocked by:** None"},
					{Number: 1, Body: "**Blocked by:** None"},
				}},
			},
			want: []string{"Created: #1, #2", "Next: syl implement 1"},
		},
		{
			name: "remote remains missing",
			snapshots: []planSnapshot{
				{err: tracker.ErrNoRemote},
				{err: tracker.ErrNoRemote},
			},
			want: []string{"No tickets created (no origin configured)."},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := planSkillsRoot(t)
			var output bytes.Buffer
			adapter := &capturingPlanAdapter{}
			err := RunPlan(context.Background(), PlanOptions{
				WorkRoot: root, Topic: "add offline mode", TrackerName: config.TrackerGitHub,
				IssueTracker: &scriptedPlanTracker{snapshots: test.snapshots}, Adapter: adapter,
				Output: &output, NoRemote: true,
			})
			if err != nil {
				t.Fatalf("RunPlan() error = %v", err)
			}
			if adapter.request.Prompt != composePlanPrompt(PlanOptions{Topic: "add offline mode", TrackerName: config.TrackerGitHub}) {
				t.Fatalf("planner prompt = %q, want existing prompt unchanged", adapter.request.Prompt)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("output = %q, want %q", output.String(), want)
				}
			}
		})
	}
}

func TestRunPlanNoRemoteOnlyToleratesRemoteTrackerErrors(t *testing.T) {
	tests := []struct {
		name         string
		trackerName  config.Tracker
		noRemote     bool
		snapshots    []planSnapshot
		want         string
		wantAttached bool
	}{
		{
			name:         "flag omitted",
			trackerName:  config.TrackerGitHub,
			snapshots:    []planSnapshot{{err: tracker.ErrNoRemote}},
			want:         "snapshot tickets before planning",
			wantAttached: false,
		},
		{
			name:         "other remote tracker error",
			trackerName:  config.TrackerGitHub,
			noRemote:     true,
			snapshots:    []planSnapshot{{err: errors.New("not authenticated")}},
			want:         "snapshot tickets before planning",
			wantAttached: false,
		},
		{
			name:         "local tracker",
			trackerName:  config.TrackerLocal,
			noRemote:     true,
			snapshots:    []planSnapshot{{err: tracker.ErrNoRemote}},
			want:         "snapshot tickets before planning",
			wantAttached: false,
		},
		{
			name:         "other after-snapshot error",
			trackerName:  config.TrackerGitLab,
			noRemote:     true,
			snapshots:    []planSnapshot{{}, {err: errors.New("not authenticated")}},
			want:         "list tickets after planning",
			wantAttached: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := planSkillsRoot(t)
			adapter := &capturingPlanAdapter{}
			err := RunPlan(context.Background(), PlanOptions{
				WorkRoot: root, Topic: "add offline mode", TrackerName: test.trackerName,
				IssueTracker: &scriptedPlanTracker{snapshots: test.snapshots}, Adapter: adapter,
				NoRemote: test.noRemote,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunPlan() error = %v, want error containing %q", err, test.want)
			}
			gotAttached := adapter.request.Prompt != ""
			if gotAttached != test.wantAttached {
				t.Fatalf("planner attached = %t, want %t", gotAttached, test.wantAttached)
			}
		})
	}
}

func TestRunPlanNoRemoteNoticePropagatesOutputFailure(t *testing.T) {
	root := planSkillsRoot(t)
	tracker := &scriptedPlanTracker{snapshots: []planSnapshot{{}, {err: tracker.ErrNoRemote}}}
	options := PlanOptions{
		WorkRoot: root, Topic: "add offline mode", TrackerName: config.TrackerGitLab,
		IssueTracker: tracker, Adapter: &capturingPlanAdapter{}, Output: &planFailAtWriter{failAt: 1}, NoRemote: true,
	}
	if err := RunPlan(context.Background(), options); err == nil || !strings.Contains(err.Error(), "write no-remote plan notice") {
		t.Fatalf("RunPlan() error = %v, want no-remote notice write error", err)
	}
}

func planSkillsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	skill := filepath.Join(root, ".agents", "skills", "to-tickets")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

type planSnapshot struct {
	tickets []tracker.Ticket
	err     error
}

type scriptedPlanTracker struct {
	branchSetupTracker
	snapshots []planSnapshot
	calls     int
}

func (t *scriptedPlanTracker) List(context.Context) ([]tracker.Ticket, error) {
	if t.calls >= len(t.snapshots) {
		return nil, errors.New("unexpected plan ticket snapshot")
	}
	result := t.snapshots[t.calls]
	t.calls++
	return result.tickets, result.err
}

type capturingPlanAdapter struct {
	scriptedConversationAdapter
	request harness.Request
}

func (a *capturingPlanAdapter) Attach(_ context.Context, request harness.Request) error {
	a.request = request
	return nil
}
