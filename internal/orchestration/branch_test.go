package orchestration

import (
	"testing"

	"github.com/igorrochap/syl/internal/tracker"
)

func TestBranchName(t *testing.T) {
	tests := []struct {
		name   string
		ticket tracker.Ticket
		want   string
	}{
		{
			name:   "label match",
			ticket: tracker.Ticket{Title: "Repair broken cache", Labels: []string{"fix"}},
			want:   "fix/repair-broken-cache",
		},
		{
			name:   "title prefix",
			ticket: tracker.Ticket{Title: "refactor: extract recorder"},
			want:   "refactor/extract-recorder",
		},
		{
			name:   "implement syl prefix",
			ticket: tracker.Ticket{Title: "Implement syl: do thing"},
			want:   "feat/do-thing",
		},
		{
			name:   "default type",
			ticket: tracker.Ticket{Title: "Add account settings"},
			want:   "feat/add-account-settings",
		},
		{
			name:   "stop words and five word cap",
			ticket: tracker.Ticket{Title: "The quick and brown fox jumps over the lazy dog"},
			want:   "feat/quick-brown-fox-jumps-over",
		},
		{
			name:   "ticket fallback",
			ticket: tracker.Ticket{Number: 42, Title: "---"},
			want:   "feat/ticket-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := branchName(tt.ticket); got != tt.want {
				t.Fatalf("branchName() = %q, want %q", got, tt.want)
			}
		})
	}
}
