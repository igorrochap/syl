package runrecord

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/verdict"
)

func TestParseArtifactName(t *testing.T) {
	tests := []struct {
		name string
		want Artifact
		ok   bool
	}{
		{"review.diff", Artifact{Kind: ReviewDiff, Name: "review.diff", Standalone: true, Iteration: 1, Role: "review", Extension: "diff"}, true},
		{"review.feed", Artifact{Kind: ReviewFeed, Name: "review.feed", Standalone: true, Iteration: 1, Role: "review", Extension: "feed"}, true},
		{"review.transcript", Artifact{Kind: ReviewTranscript, Name: "review.transcript", Standalone: true, Iteration: 1, Role: "review", Extension: "transcript"}, true},
		{"verdict.txt", Artifact{Kind: VerdictFile, Name: "verdict.txt", Standalone: true, Iteration: 1}, true},
		{"summary.txt", Artifact{Kind: SummaryFile, Name: "summary.txt", Standalone: true}, true},
		{"sessions.txt", Artifact{Kind: SessionsFile, Name: "sessions.txt", Standalone: true}, true},
		{"usage.json", Artifact{Kind: UsageFile, Name: "usage.json", Standalone: true}, true},
		{"handoff-03.md", Artifact{Kind: ImplementHandoff, Name: "handoff-03.md", Iteration: 3, Role: "implement", Extension: "md"}, true},
		{"iteration-02-implement.feed", Artifact{Kind: ImplementFeed, Name: "iteration-02-implement.feed", Iteration: 2, Role: "implement", Extension: "feed"}, true},
		{"iteration-02-implement.transcript", Artifact{Kind: ImplementTranscript, Name: "iteration-02-implement.transcript", Iteration: 2, Role: "implement", Extension: "transcript"}, true},
		{"iteration-10-review.diff", Artifact{Kind: ReviewDiff, Name: "iteration-10-review.diff", Iteration: 10, Role: "review", Extension: "diff"}, true},
		{"iteration-01-review.feed", Artifact{Kind: ReviewFeed, Name: "iteration-01-review.feed", Iteration: 1, Role: "review", Extension: "feed"}, true},
		{"iteration-01-review.transcript", Artifact{Kind: ReviewTranscript, Name: "iteration-01-review.transcript", Iteration: 1, Role: "review", Extension: "transcript"}, true},
		{"iteration-01-verdict.txt", Artifact{Kind: VerdictFile, Name: "iteration-01-verdict.txt", Iteration: 1, Role: "verdict", Extension: "txt"}, true},
		{"metadata.txt", Artifact{}, false},
		{"handoff-00.md", Artifact{}, false},
		{"handoff-x.md", Artifact{}, false},
		{"handoff-01.txt", Artifact{}, false},
		{"iteration-", Artifact{}, false},
		{"iteration-01", Artifact{}, false},
		{"iteration--review.diff", Artifact{}, false},
		{"iteration-x-review.diff", Artifact{}, false},
		{"iteration-00-review.diff", Artifact{}, false},
		{"iteration-01-review", Artifact{}, false},
		{"iteration-01-.diff", Artifact{}, false},
		{"iteration-01-review.md", Artifact{}, false},
		{"iteration-01-implement.diff", Artifact{}, false},
		{"unrelated.txt", Artifact{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseArtifactName(test.name)
			if ok != test.ok || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseArtifactName(%q) = (%#v, %t), want (%#v, %t)", test.name, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestArtifactNameMatchesParsedName(t *testing.T) {
	tests := []struct {
		kind      ArtifactKind
		iteration int
		want      string
	}{
		{ReviewDiff, 0, "review.diff"},
		{ReviewFeed, 0, "review.feed"},
		{ReviewTranscript, 0, "review.transcript"},
		{VerdictFile, 0, "verdict.txt"},
		{SummaryFile, 0, "summary.txt"},
		{SessionsFile, 0, "sessions.txt"},
		{UsageFile, 0, "usage.json"},
		{MetadataFile, 0, "metadata.txt"},
		{ImplementFeed, 0, ""},
		{ImplementTranscript, 0, ""},
		{ImplementHandoff, 0, ""},
		{ImplementFeed, 2, "iteration-02-implement.feed"},
		{ImplementTranscript, 2, "iteration-02-implement.transcript"},
		{ImplementHandoff, 2, "handoff-02.md"},
		{ReviewDiff, 12, "iteration-12-review.diff"},
		{ReviewFeed, 1, "iteration-01-review.feed"},
		{ReviewTranscript, 1, "iteration-01-review.transcript"},
		{VerdictFile, 1, "iteration-01-verdict.txt"},
		{SummaryFile, 1, ""},
		{SessionsFile, 1, ""},
		{UsageFile, 1, ""},
		{MetadataFile, 1, ""},
		{ArtifactKind(0), 1, ""},
	}
	for _, test := range tests {
		if got := ArtifactName(test.kind, test.iteration); got != test.want {
			t.Errorf("ArtifactName(%d, %d) = %q, want %q", test.kind, test.iteration, got, test.want)
		}
		if test.iteration == 0 || test.want == "" {
			continue
		}
		parsed, ok := ParseArtifactName(test.want)
		if !ok || parsed.Kind != test.kind || parsed.Iteration != test.iteration {
			t.Errorf("ParseArtifactName(%q) = (%#v, %t), want kind %d iteration %d", test.want, parsed, ok, test.kind, test.iteration)
		}
	}
}

func TestArtifactLabel(t *testing.T) {
	tests := map[string]string{
		"iteration-01-review.diff": "diff",
		"handoff-01.md":            "md",
		"summary.txt":              "summary.txt",
		"unknown.bin":              "unknown.bin",
	}
	for name, want := range tests {
		if got := ArtifactLabel(name); got != want {
			t.Errorf("ArtifactLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseIterationNumber(t *testing.T) {
	tests := map[string]int{
		"iteration-03-review.diff": 3,
		"iteration-12-verdict.txt": 12,
		"iteration-00-review.diff": 0,
		"iteration-x-review.diff":  0,
		"iteration-01":             0,
		"iteration--a":             0,
		"handoff-01.md":            0,
	}
	for name, want := range tests {
		if got := ParseIterationNumber(name); got != want {
			t.Errorf("ParseIterationNumber(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestParseMetadata(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     Metadata
	}{
		{
			name: "implement run with both contexts",
			contents: "Branch: feat/x\nBranch point: abc\nWork root: /work\nImplementer harness: codex\nReviewer harness: claude\n" +
				"Implementer context:\n  line one\n  line two\nReviewer context:\n  check it\n",
			want: Metadata{
				Kind: Implement, Branch: "feat/x", BranchPoint: "abc", WorkRoot: "/work",
				ImplementerHarness: "codex", ReviewerHarness: "claude",
				ImplementContext: "line one\nline two", ReviewContext: "check it",
			},
		},
		{
			name:     "review run is inferred from ticket",
			contents: "Ticket: #12\nBranch point: abc\nReviewer harness: claude\n",
			want:     Metadata{Kind: Review, TicketRef: "#12", BranchPoint: "abc", ReviewerHarness: "claude"},
		},
		{
			name:     "review run is inferred from reviewer harness alone",
			contents: "Reviewer harness: claude\n",
			want:     Metadata{Kind: Review, ReviewerHarness: "claude"},
		},
		{
			name:     "branch wins over an earlier ticket",
			contents: "Ticket: #1\nBranch: feat/y\n",
			want:     Metadata{Kind: Implement, TicketRef: "#1", Branch: "feat/y"},
		},
		{
			name:     "keys are case insensitive and values trimmed",
			contents: "BRANCH:   feat/z  \n",
			want:     Metadata{Kind: Implement, Branch: "feat/z"},
		},
		{
			name:     "indented lines without a context header are ignored",
			contents: "Branch: b\n  stray\n\tstray\nnot a field\nunknown: value\n",
			want:     Metadata{Kind: Implement, Branch: "b"},
		},
		{
			name:     "context block ends at the next field",
			contents: "Reviewer context:\n  first\nBranch: b\n  ignored\n",
			want:     Metadata{Kind: Implement, Branch: "b", ReviewContext: "first"},
		},
		{name: "empty", contents: "", want: Metadata{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ParseMetadata([]byte(test.contents)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseMetadata() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestHarnessFor(t *testing.T) {
	metadata := Metadata{ImplementerHarness: "codex", ReviewerHarness: "claude"}
	if got := metadata.HarnessFor(Review); got != "claude" {
		t.Errorf("HarnessFor(Review) = %q, want claude", got)
	}
	if got := metadata.HarnessFor(Implement); got != "codex" {
		t.Errorf("HarnessFor(Implement) = %q, want codex", got)
	}
}

func TestParseSessionsIgnoresMalformedLines(t *testing.T) {
	contents := strings.Join([]string{
		"iteration 1 implement: abc",
		"Iteration 2 review:  def ",
		"no separator",
		"iteration 1: abc",
		"other 1 implement: abc",
		"iteration x implement: abc",
		"iteration -1 implement: abc",
		"iteration 1 implement:   ",
	}, "\n")
	want := []Session{
		{Iteration: 1, Role: "implement", SessionID: "abc"},
		{Iteration: 2, Role: "review", SessionID: "def"},
	}
	if got := ParseSessions([]byte(contents)); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSessions() = %#v, want %#v", got, want)
	}
}

func TestParseSummary(t *testing.T) {
	contents := "Iterations: 3\r\nFinal verdict: approve\r\nSummary: All good\r\nNit findings:\r\n- (none)\r\nDiff stat:\r\n a.go | 2 +-\r\n b.go | 1 +\r\n"
	got := ParseSummary([]byte(contents))
	if got.Iterations != 3 || got.FinalVerdict != "approve" || got.Text != "All good" ||
		got.DiffStat != "a.go | 2 +-\n b.go | 1 +" {
		t.Fatalf("ParseSummary() = %#v", got)
	}
	if got.Contents != contents {
		t.Fatalf("ParseSummary().Contents = %q, want the original text", got.Contents)
	}
}

func TestParseSummaryKeepsFirstOccurrenceAndRejectsBadIterations(t *testing.T) {
	first := ParseSummary([]byte("Iterations: 0\nIterations: 2\nIterations: 5\nFinal verdict: a\nFinal verdict: b\nSummary: x\nSummary: y\n"))
	if first.Iterations != 2 || first.FinalVerdict != "a" || first.Text != "x" {
		t.Fatalf("ParseSummary() = %#v, want iterations 2, verdict a, text x", first)
	}
	invalid := ParseSummary([]byte("Iterations: many\nIterations: -1\n"))
	if invalid.Iterations != 0 {
		t.Fatalf("ParseSummary() iterations = %d, want 0", invalid.Iterations)
	}
	unknown := ParseSummary([]byte("nothing here"))
	if unknown.FinalVerdict != "" || unknown.Text != "" || unknown.DiffStat != "" {
		t.Fatalf("ParseSummary() = %#v, want empty fields", unknown)
	}
}

func TestParseVerdictLabel(t *testing.T) {
	if got := ParseVerdictLabel([]byte("SUMMARY: x\nVERDICT:  revise \nVERDICT: approve\n")); got != "revise" {
		t.Errorf("ParseVerdictLabel() = %q, want revise", got)
	}
	if got := ParseVerdictLabel([]byte("SUMMARY: x\n")); got != "" {
		t.Errorf("ParseVerdictLabel() = %q, want empty", got)
	}
}

func TestFormatVerdictAndSummary(t *testing.T) {
	finding := verdict.Finding{Kind: verdict.Blocking, Location: "a.go:1", Issue: "fix"}
	withFindings := FormatVerdict(verdict.Verdict{Status: verdict.Revise, Summary: "s", Findings: []verdict.Finding{finding}})
	if want := "VERDICT: revise\nSUMMARY: s\nFINDINGS:\n- [blocking] a.go:1 — fix\n"; withFindings != want {
		t.Errorf("FormatVerdict() = %q, want %q", withFindings, want)
	}
	none := FormatVerdict(verdict.Verdict{Status: verdict.Approve, Summary: "s"})
	if want := "VERDICT: approve\nSUMMARY: s\nFINDINGS:\n- (none)\n"; none != want {
		t.Errorf("FormatVerdict() = %q, want %q", none, want)
	}

	input := SummaryInput{
		Iterations: 2, Final: verdict.Verdict{Status: verdict.Approve, Summary: "ok"},
		Nits: []verdict.Finding{finding}, WorktreePath: "/wt", DiffStat: " a.go | 1 +\n\n",
	}
	want := "Iterations: 2\nFinal verdict: approve\nSummary: ok\nNit findings:\n- [blocking] a.go:1 — fix\n" +
		"Worktree: /wt\nRemove worktree: git worktree remove --force /wt\nDiff stat:\n a.go | 1 +\n"
	if got := FormatSummary(input); got != want {
		t.Errorf("FormatSummary() = %q, want %q", got, want)
	}
	input.Nits, input.WorktreePath = nil, ""
	want = "Iterations: 2\nFinal verdict: approve\nSummary: ok\nNit findings:\n- (none)\nDiff stat:\n a.go | 1 +\n"
	if got := FormatSummary(input); got != want {
		t.Errorf("FormatSummary() = %q, want %q", got, want)
	}
}

func TestDirectoryNameAndParsing(t *testing.T) {
	now := time.Date(2026, time.September, 29, 20, 0, 0, 5, time.UTC)
	tests := []struct {
		kind      Kind
		ticketRef string
		want      string
		wantErr   bool
	}{
		{Implement, "#204", "20260929T200000.000000005Z-204", false},
		{Implement, "", "20260929T200000.000000005Z-implement", false},
		{Implement, "abc", "20260929T200000.000000005Z-abc", false},
		{Review, "#12", "20260929T200000.000000005Z-12", false},
		{Review, "abc", "20260929T200000.000000005Z-review", false},
		{Review, "", "20260929T200000.000000005Z-review", false},
		{Kind("other"), "#1", "", true},
	}
	for _, test := range tests {
		got, err := DirectoryName(now, test.kind, test.ticketRef)
		if (err != nil) != test.wantErr || got != test.want {
			t.Errorf("DirectoryName(%q, %q) = (%q, %v), want (%q, err %t)", test.kind, test.ticketRef, got, err, test.want, test.wantErr)
		}
	}
	name := "20260929T200000.000000005Z-204"
	if got := DirectoryTimestamp(name); !got.Equal(now) {
		t.Errorf("DirectoryTimestamp() = %v, want %v", got, now)
	}
	for _, bad := range []string{"", "-204", "nodash", "garbage-1"} {
		if got := DirectoryTimestamp(bad); !got.IsZero() {
			t.Errorf("DirectoryTimestamp(%q) = %v, want zero", bad, got)
		}
	}
	ticketTests := map[string]string{name: "204", "x-": "", "x-0": "", "x-abc": "", "nodash": "", "-5": ""}
	for input, want := range ticketTests {
		if got := LegacyTicketNumber(input); got != want {
			t.Errorf("LegacyTicketNumber(%q) = %q, want %q", input, got, want)
		}
	}
	if got := DirectorySortKey(name); got != "20260929T200000.000000005Z\x00"+name {
		t.Errorf("DirectorySortKey() = %q", got)
	}
	if got := DirectorySortKey("nodash"); got != "nodash" {
		t.Errorf("DirectorySortKey(nodash) = %q", got)
	}
	if got := RelativeDirectory("run"); got != ".syl/runs/run" {
		t.Errorf("RelativeDirectory() = %q", got)
	}
}

func TestArtifactPathHelpers(t *testing.T) {
	absolute := map[string]bool{"/etc/passwd": true, "\\share": true, "C:\\x": true, "c:x": true, "a/b": false, "a": false, "": false}
	for path, want := range absolute {
		if got := isAbsoluteArtifactPath(path); got != want {
			t.Errorf("isAbsoluteArtifactPath(%q) = %t, want %t", path, got, want)
		}
	}
	parents := map[string]bool{"..": true, "a/../b": true, "a\\..\\b": true, "a/..b": false, "a/b": false, "": false}
	for path, want := range parents {
		if got := hasParentPathComponent(path); got != want {
			t.Errorf("hasParentPathComponent(%q) = %t, want %t", path, got, want)
		}
	}
	within := []struct {
		root, path string
		want       bool
	}{
		{"/r", "/r/a", true}, {"/r", "/r", true}, {"/r", "/x", false}, {"/r", "/r/../x", false}, {"/r", "rel", false},
	}
	for _, test := range within {
		if got := pathWithin(test.root, test.path); got != test.want {
			t.Errorf("pathWithin(%q, %q) = %t, want %t", test.root, test.path, got, test.want)
		}
	}
}
