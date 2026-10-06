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

type capturingPlanAdapter struct {
	scriptedConversationAdapter
	request harness.Request
}

func (a *capturingPlanAdapter) Attach(_ context.Context, request harness.Request) error {
	a.request = request
	return nil
}
