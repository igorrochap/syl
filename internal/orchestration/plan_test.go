package orchestration

import (
	"bytes"
	"strings"
	"testing"

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
