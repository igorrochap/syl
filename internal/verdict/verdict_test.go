package verdict

import "testing"

func TestParseFixtures(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    Verdict
		wantErr bool
	}{
		{
			name: "approve",
			text: "Review notes\n\n" +
				"VERDICT: approve\n" +
				"SUMMARY: Everything required is complete\n" +
				"FINDINGS:\n",
			want: Verdict{Status: Approve, Summary: "Everything required is complete"},
		},
		{
			name: "revise",
			text: "VERDICT: revise\n" +
				"SUMMARY: One change remains\n" +
				"FINDINGS:\n" +
				"- [blocking] internal/cli/review.go:42 — handle a missing session\n" +
				"- [nit] README.md:7 — clarify the `--raw` example\n",
			want: Verdict{
				Status:  Revise,
				Summary: "One change remains",
				Findings: []Finding{
					{Kind: Blocking, Location: "internal/cli/review.go:42", Issue: "handle a missing session"},
					{Kind: Nit, Location: "README.md:7", Issue: "clarify the `--raw` example"},
				},
			},
		},
		{
			name: "empty findings",
			text: "VERDICT: revise\nSUMMARY: No structured findings were emitted\nFINDINGS:\n",
			want: Verdict{Status: Revise, Summary: "No structured findings were emitted"},
		},
		{
			name:    "missing block",
			text:    "The reviewer forgot the contract.",
			wantErr: true,
		},
		{
			name:    "garbled block",
			text:    "VERDICT: maybe\nSUMMARY: uncertain\nFINDINGS:\n",
			wantErr: true,
		},
		{
			name: "multiple blocks last wins",
			text: "VERDICT: approve\nSUMMARY: Earlier answer\nFINDINGS:\n\n" +
				"More discussion\n\n" +
				"VERDICT: revise\nSUMMARY: Later answer\nFINDINGS:\n" +
				"- [blocking] cmd/rig/main.go:9 — later block wins: ✓ and 日本語\n",
			want: Verdict{
				Status:  Revise,
				Summary: "Later answer",
				Findings: []Finding{
					{Kind: Blocking, Location: "cmd/rig/main.go:9", Issue: "later block wins: ✓ and 日本語"},
				},
			},
		},
		{
			name: "unusual finding characters",
			text: "VERDICT: revise\nSUMMARY: Keep the user's punctuation intact — please\nFINDINGS:\n" +
				"- [blocking] path with spaces/file.go:10 — quote \"x\", emoji 🚧, and an em dash — all matter\n",
			want: Verdict{
				Status:  Revise,
				Summary: "Keep the user's punctuation intact — please",
				Findings: []Finding{
					{Kind: Blocking, Location: "path with spaces/file.go:10", Issue: "quote \"x\", emoji 🚧, and an em dash — all matter"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() error = %v, wantErr %t", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Status != tt.want.Status || got.Summary != tt.want.Summary {
				t.Fatalf("Parse() = %#v, want %#v", got, tt.want)
			}
			if len(got.Findings) != len(tt.want.Findings) {
				t.Fatalf("finding count = %d, want %d: %#v", len(got.Findings), len(tt.want.Findings), got.Findings)
			}
			for i := range got.Findings {
				if got.Findings[i] != tt.want.Findings[i] {
					t.Errorf("finding[%d] = %#v, want %#v", i, got.Findings[i], tt.want.Findings[i])
				}
			}
		})
	}
}
