package orchestration

import "testing"

func TestEnsureClosingReferenceMatchesExactTicketNumber(t *testing.T) {
	for _, test := range []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "larger issue number does not match",
			description: "Related: Closes #123",
			want:        "Related: Closes #123\n\nCloses #12",
		},
		{
			name:        "larger issue number is not cleaned up",
			description: "Closes #120",
			want:        "Closes #120\n\nCloses #12",
		},
		{
			name:        "exact issue number is retained once",
			description: "Closes #120, and Closes #12, then Closes #12",
			want:        "Closes #120, and Closes #12, then",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ensureClosingReference(test.description, 12); got != test.want {
				t.Fatalf("ensureClosingReference(%q, 12) = %q, want %q", test.description, got, test.want)
			}
		})
	}
}
