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
		{
			name: "fenced repeated verdict uses last block",
			text: "```text\n" +
				"VERDICT: approve\n" +
				"SUMMARY: Earlier answer\n" +
				"FINDINGS:\n" +
				"```\n\n" +
				"```text\n" +
				"VERDICT: revise\n" +
				"SUMMARY: Seven findings remain\n" +
				"FINDINGS:\n" +
				"- [blocking] one.go:1 — first finding\n" +
				"- [blocking] two.go:2 — second finding\n" +
				"- [blocking] three.go:3 — third finding\n" +
				"- [blocking] four.go:4 — fourth finding\n" +
				"- [nit] five.go:5 — fifth finding\n" +
				"- [nit] six.go:6 — sixth finding\n" +
				"- [nit] seven.go:7 — seventh finding\n" +
				"```\n",
			want: Verdict{
				Status:  Revise,
				Summary: "Seven findings remain",
				Findings: []Finding{
					{Kind: Blocking, Location: "one.go:1", Issue: "first finding"},
					{Kind: Blocking, Location: "two.go:2", Issue: "second finding"},
					{Kind: Blocking, Location: "three.go:3", Issue: "third finding"},
					{Kind: Blocking, Location: "four.go:4", Issue: "fourth finding"},
					{Kind: Nit, Location: "five.go:5", Issue: "fifth finding"},
					{Kind: Nit, Location: "six.go:6", Issue: "sixth finding"},
					{Kind: Nit, Location: "seven.go:7", Issue: "seventh finding"},
				},
			},
		},
		{
			name: "en dash and hyphen separators preserve issue text",
			text: "VERDICT: revise\nSUMMARY: Alternate separators\nFINDINGS:\n" +
				"- [blocking] en-dash.go:1 – use the supported separator\n" +
				"- [nit] hyphen.go:2 - preserve this - hyphen in the issue\n",
			want: Verdict{
				Status:  Revise,
				Summary: "Alternate separators",
				Findings: []Finding{
					{Kind: Blocking, Location: "en-dash.go:1", Issue: "use the supported separator"},
					{Kind: Nit, Location: "hyphen.go:2", Issue: "preserve this - hyphen in the issue"},
				},
			},
		},
		{
			name: "trailing prose ends findings list",
			text: "VERDICT: revise\nSUMMARY: Findings are followed by explanation\nFINDINGS:\n" +
				"- [blocking] change.go:1 — fix the bug\n\n" +
				"The reviewer added context after the list.\n",
			want: Verdict{
				Status:   Revise,
				Summary:  "Findings are followed by explanation",
				Findings: []Finding{{Kind: Blocking, Location: "change.go:1", Issue: "fix the bug"}},
			},
		},
		{
			name:    "malformed finding remains an error after valid findings",
			text:    "VERDICT: revise\nSUMMARY: One malformed finding\nFINDINGS:\n- [nit] change.go:1 — valid\n- [blocking] malformed finding\ntrailing prose\n",
			wantErr: true,
		},
		{
			name:    "indented malformed finding remains an error",
			text:    "VERDICT: revise\nSUMMARY: One indented malformed finding\nFINDINGS:\n  - [blocking] malformed finding\n",
			wantErr: true,
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
