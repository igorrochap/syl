package orchestration

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/tracker"
)

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

func TestReadPublicationEvidenceDistinguishesMissingFileFromReadFailure(t *testing.T) {
	t.Run("missing evidence is absent", func(t *testing.T) {
		evidence, err := readPublicationEvidence(t.TempDir())
		if err != nil || evidence.recorded || evidence.summary != "" || len(evidence.images) != 0 {
			t.Fatalf("readPublicationEvidence() = (%#v, %v), want absent without error", evidence, err)
		}
	})

	t.Run("read failure is returned", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "file-not-directory")
		if err := os.WriteFile(directory, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		evidence, err := readPublicationEvidence(directory)
		if err == nil || evidence.recorded || evidence.summary != "" || len(evidence.images) != 0 || !strings.Contains(err.Error(), "read evidence.md") {
			t.Fatalf("readPublicationEvidence() = (%#v, %v), want a read error", evidence, err)
		}
		body := appendVerificationSection("", publicationVerification{evidenceUnavailable: true})
		if !strings.Contains(body, "Evidence: unavailable") || strings.Contains(body, "Evidence: none") ||
			strings.Contains(body, directory) {
			t.Fatalf("verification body = %q, want a safe unavailable message instead of missing Evidence or a local path", body)
		}
	})
}

func TestReadPublicationEvidenceSelectsOnlyPRWorthyLocalImages(t *testing.T) {
	directory := t.TempDir()
	contents := "# Evidence\n\n" +
		"![Dashboard](./screens/dashboard.png) — PR-worthy\n" +
		"![Overview](<./screens/overview wide.png>) (PR-worthy)\n" +
		"![Console](./screens/console.png)\n" +
		"![Remote](https://example.test/screen.png) PR-worthy\n" +
		"![Protocol relative](//cdn.example.test/screen.png) PR-worthy\n" +
		"![Query](./screens/dashboard.png?download=1) PR-worthy\n" +
		"```markdown\n![Example](./screens/example.png) PR-worthy\n```\n"
	if err := os.WriteFile(filepath.Join(directory, "evidence.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	writeEvidenceImage(t, filepath.Join(directory, "screens", "dashboard.png"))
	writeEvidenceImage(t, filepath.Join(directory, "screens", "overview wide.png"))

	evidence, err := readPublicationEvidence(directory)
	if err != nil || !evidence.recorded {
		t.Fatalf("readPublicationEvidence() = (%#v, %v), want recorded Evidence without error", evidence, err)
	}
	want := []tracker.PullRequestEvidenceImage{{
		Path: filepath.Join(directory, "screens", "dashboard.png"), Alt: "Dashboard",
	}, {
		Path: filepath.Join(directory, "screens", "overview wide.png"), Alt: "Overview",
	}}
	if !reflect.DeepEqual(evidence.images, want) || len(evidence.imageNotices) != 3 {
		t.Fatalf("PR-worthy images = %#v, notices = %v; want %#v and three remote path notices", evidence.images, evidence.imageNotices, want)
	}
	for _, expected := range []string{"Remote", "Protocol relative", "Query", "not a local Evidence file"} {
		if !strings.Contains(strings.Join(evidence.imageNotices, "\n"), expected) {
			t.Errorf("image notices = %v, want %q", evidence.imageNotices, expected)
		}
	}
}

func TestReadPublicationEvidenceRejectsInvalidMarkedImagePaths(t *testing.T) {
	directory := t.TempDir()
	outsideImage := filepath.Join(t.TempDir(), "outside.png")
	writeEvidenceImage(t, outsideImage)
	insideText := filepath.Join(directory, "notes.txt")
	if err := os.WriteFile(insideText, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	imageSymlink := filepath.Join(directory, "linked.png")
	if err := os.Symlink(outsideImage, imageSymlink); err != nil {
		t.Skipf("create image symlink: %v", err)
	}
	contents := "# Evidence\n\n" +
		"![Absolute](" + outsideImage + ") PR-worthy\n" +
		"![Traversal](../" + filepath.Base(outsideImage) + ") PR-worthy\n" +
		"![Text](./notes.txt) PR-worthy\n" +
		"![Symlink](./linked.png) PR-worthy\n" +
		"![Hash](./hash#image.png) PR-worthy\n" +
		"![Malformed](<./bad%zz.png>) PR-worthy\n"
	if err := os.WriteFile(filepath.Join(directory, "evidence.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	evidence, err := readPublicationEvidence(directory)
	if err != nil || !evidence.recorded || len(evidence.images) != 0 || len(evidence.imageNotices) != 6 {
		t.Fatalf("readPublicationEvidence() = (%#v, %v), want six actionable image notices and no uploads", evidence, err)
	}
	for _, expected := range []string{
		"Absolute", "must be relative", "Traversal", "escapes", "Text", "not an image",
		"Symlink", "resolves outside", "Hash", "contains #", "Malformed", "invalid URL escape",
	} {
		if !strings.Contains(strings.Join(evidence.imageNotices, "\n"), expected) {
			t.Errorf("image notices = %v, want %q", evidence.imageNotices, expected)
		}
	}
}

func TestPublicationVerificationSummarizesImagesOnlyEvidenceAsText(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "evidence.md"), []byte("![runtime screenshot](screen.png)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := readPublicationEvidence(directory)
	if err != nil || !evidence.recorded {
		t.Fatalf("readPublicationEvidence() = (%#v, %v), want recorded Evidence", evidence, err)
	}
	body := appendVerificationSection("", publicationVerification{evidence: evidence.summary, evidenceRecorded: evidence.recorded})
	if !strings.Contains(body, "Evidence summary:\n\n[Image omitted: runtime screenshot]") {
		t.Fatalf("verification body = %q, want text summary of the omitted image", body)
	}
	if strings.Contains(body, "![") || strings.Contains(body, "screen.png") {
		t.Fatalf("verification body = %q, want no embedded image or image path", body)
	}
}

func writeEvidenceImage(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationVerificationUsesOverallPassForEmptyGateResults(t *testing.T) {
	var section strings.Builder
	writeGateVerification(&section, publicationGateVerification{
		ran: true, feedback: gateAttemptFeedback{resultsAvailable: true},
	})
	if got := section.String(); !strings.Contains(got, "Gate: overall PASS (per-Gate results unavailable).") ||
		strings.Contains(got, "Gates:\n\n") {
		t.Fatalf("Gate verification = %q, want overall PASS when result list is empty", got)
	}
}

func TestSummarizeEvidenceSkipsFencedCodeAndEmptyEvidence(t *testing.T) {
	t.Run("fenced code is omitted from structured summary", func(t *testing.T) {
		contents := "# Evidence\n\n- What was exercised: the publication parser.\n" +
			"```markdown\n- Outcome: fake result from an example\n```\n" +
			"- Outcome: pass\n"
		summary := summarizeEvidenceText(contents)
		if !strings.Contains(summary, "Exercised: the publication parser.") ||
			!strings.Contains(summary, "Outcome: pass") ||
			strings.Contains(summary, "fake result") {
			t.Fatalf("Evidence summary = %q, want real fields and no fenced code", summary)
		}
	})

	t.Run("fenced code and headings produce the empty result", func(t *testing.T) {
		contents := "# Evidence\n\n## Example\n```text\nonly example content\n```\n"
		if got := summarizeEvidenceText(contents); got != "evidence.md is empty." {
			t.Fatalf("summarizeEvidenceText() = %q, want the empty Evidence result", got)
		}
	})
}

func TestSummarizeEvidenceKeepsMultilineContinuationAndSplitsExercises(t *testing.T) {
	contents := "# Evidence\n\n## Exercise: CLI\n" +
		"- What was exercised: the CLI output.\n\n" +
		"The second output line was also checked.\n" +
		"- Outcome: pass\n\n" +
		"## Exercise: API\n" +
		"- What was exercised: the HTTP response.\n" +
		"- Outcome: pass\n"
	summary := summarizeEvidenceText(contents)
	want := "- Outcome: pass\n" +
		"  - Exercised: the CLI output. The second output line was also checked.\n" +
		"- Outcome: pass\n" +
		"  - Exercised: the HTTP response."
	if summary != want {
		t.Fatalf("Evidence summary = %q, want readable field lists %q", summary, want)
	}
}

func TestSummarizeEvidenceStartsExerciseOnHeadingWithoutExercisedField(t *testing.T) {
	contents := "# Evidence\n\n## Exercise: CLI\n" +
		"- What was exercised: the command output.\n" +
		"- Outcome: pass\n\n" +
		"## Exercise: API\n" +
		"- How it was exercised: made an HTTP request.\n" +
		"- Outcome: pass\n"
	got := strings.Split(summarizeEvidenceText(contents), "\n")
	want := []string{
		"- Outcome: pass",
		"  - Exercised: the command output.",
		"- Outcome: pass",
		"  - How: made an HTTP request.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Evidence exercises = %q, want independently formatted exercise lists %q", got, want)
	}
}

func TestSummarizeEvidenceKeepsStandaloneNotExercisedReason(t *testing.T) {
	contents := "# Evidence\n\n- What was exercised: the runtime service.\n" +
		"- Outcome: not exercised\n\n" +
		"Reason: the service was unavailable in this environment.\n"
	summary := summarizeEvidenceText(contents)
	if !strings.Contains(summary, "Outcome: not exercised the service was unavailable in this environment.") {
		t.Fatalf("Evidence summary = %q, want standalone reason attached to outcome", summary)
	}
}

func TestSummarizeEvidenceRendersLocalLinksAsPlainText(t *testing.T) {
	contents := "Full output: [integration.log](integration.log).\n" +
		"Docs: [docs](https://example.com/docs).\n" +
		"See [run log](<logs/integration output.log> \"full output\"), [](<reports/latest.txt>), " +
		"[outcome](#outcome), []( #outcome), [mail](mailto:user@example.com) and [custom](task:result).\n"
	got := summarizeEvidenceText(contents)
	want := "Full output: integration.log. Docs: [docs](https://example.com/docs). " +
		"See run log, latest.txt, outcome, outcome, mail and custom."
	if got != want {
		t.Fatalf("Evidence summary = %q, want %q", got, want)
	}
}

func TestSummarizeEvidencePreservesShortcutLikeEvidenceText(t *testing.T) {
	contents := "Read the [report] now and [summary]\n" +
		"[report]: reports/local.log\n[summary]: reports/summary.log\n"
	want := "Read the [report] now and [summary]"
	if got := summarizeEvidenceText(contents); got != want {
		t.Fatalf("Evidence summary = %q, want bracketed text preserved and local definitions removed", got)
	}
}

func TestSummarizeEvidencePreservesUnreferencedBracketedDefinitions(t *testing.T) {
	contents := "[Image omitted: screenshot]: screen output omitted\n" +
		"[PASS]: passed the quality checks\n"
	want := "[Image omitted: screenshot]: screen output omitted [PASS]: passed the quality checks"
	if got := summarizeEvidenceText(contents); got != want {
		t.Fatalf("Evidence summary = %q, want bracketed text unchanged", got)
	}
}

func TestSummarizeEvidenceRemovesUnusedLocalReferenceDefinitions(t *testing.T) {
	contents := "Status: pass\n[local]: logs/run.log\n[remote]: https://example.com/run.log\n" +
		"[duplicate]: https://example.com/first.log\n[duplicate]: logs/second.log\n"
	want := "Status: pass [remote]: https://example.com/run.log [duplicate]: https://example.com/first.log"
	if got := summarizeEvidenceText(contents); got != want {
		t.Fatalf("Evidence summary = %q, want unused local definition removed and remote definition retained", got)
	}
}

func TestSummarizeEvidenceRendersLocalReferenceLinksAsPlainText(t *testing.T) {
	contents := "Use [logs][local] and [docs][remote].\n" +
		"[local]: <logs/integration.log> \"run output\"\n" +
		"[remote]: https://example.com/docs \"documentation\"\n"
	got := summarizeEvidenceText(contents)
	if !strings.Contains(got, "Use logs and [docs][remote].") ||
		strings.Contains(got, "[local]:") || !strings.Contains(got, `[remote]: https://example.com/docs "documentation"`) {
		t.Fatalf("Evidence summary = %q, want local reference as label and remote definition retained", got)
	}
}

func TestSummarizeEvidenceKeepsLinksInCodeUnchanged(t *testing.T) {
	contents := "Example: `[relative](integration.log)`\n\n" +
		"Long code: `open\n[relative](integration.log)` and literal \\[relative](integration.log).\n\n" +
		"```markdown\n[relative](integration.log)\n```\n"
	got := summarizeEvidenceText(contents)
	if !strings.Contains(got, "`[relative](integration.log)`") ||
		!strings.Contains(got, "`open [relative](integration.log)`") ||
		!strings.Contains(got, `\[relative](integration.log)`) ||
		strings.Contains(got, "```markdown") {
		t.Fatalf("Evidence summary = %q, want inline code unchanged and fenced code omitted", got)
	}
}

func TestSummarizeEvidencePreservesLegacyFencesWithoutLinks(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "tilde fences remain visible", contents: "before\n~~~\ninside\n~~~\nafter", want: "before ~~~ inside ~~~ after"},
		{name: "four-space backtick fences remain hidden", contents: "before\n    ```text\nhidden\n    ```\nafter", want: "before after"},
		{name: "shorter backtick closer still closes", contents: "````\nhidden\n```\nvisible", want: "visible"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := summarizeEvidenceText(test.contents); got != test.want {
				t.Fatalf("Evidence summary = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSummarizeEvidenceScansLinksAfterLegacyFenceClosers(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
	}{
		{
			name:     "shorter closer ends longer fence",
			contents: "````markdown\n[hidden](hidden.log)\n```\n[visible](visible.log)",
		},
		{
			name:     "closer with trailing text ends fence",
			contents: "```markdown\nhidden\n``` trailing text\n[visible](visible.log)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, want := summarizeEvidenceText(test.contents), "visible"; got != want {
				t.Fatalf("Evidence summary = %q, want link after legacy fence closer %q", got, want)
			}
		})
	}
}

func TestSummarizeEvidenceKeepsLegacyFenceSummaryWithRemoteLinks(t *testing.T) {
	contents := "before\n~~~\ninside\n~~~\n[docs](https://example.com/docs)\nafter"
	want := "before ~~~ inside ~~~ [docs](https://example.com/docs) after"
	if got := summarizeEvidenceText(contents); got != want {
		t.Fatalf("Evidence summary = %q, want remote links to preserve legacy fence summary %q", got, want)
	}
}

func TestSummarizeEvidenceRendersLocalFenceOpenerLinkAsPlainText(t *testing.T) {
	contents := "before\n~~~[log](integration.log)\ninside\n~~~\nafter"
	want := "before ~~~log inside ~~~ after"
	if got := summarizeEvidenceText(contents); got != want {
		t.Fatalf("Evidence summary = %q, want fence opener link label without local target %q", got, want)
	}
}

func TestSummarizeEvidenceRewritesLocalTildeFenceLinkWithoutDroppingBlock(t *testing.T) {
	contents := "before\n~~~\n[log](integration.log)\n~~~\nafter"
	want := "before ~~~ log ~~~ after"
	if got := summarizeEvidenceText(contents); got != want {
		t.Fatalf("Evidence summary = %q, want local link as text with tilde block retained %q", got, want)
	}
}

func TestSummarizeEvidenceRewritesLocalReferenceInsideTildeFence(t *testing.T) {
	contents := "~~~\n[log][local]\n~~~\n[local]: logs/run.log"
	if got, want := summarizeEvidenceText(contents), "~~~ log ~~~"; got != want {
		t.Fatalf("Evidence summary = %q, want local reference as text and tilde block retained %q", got, want)
	}
}

func TestAppendVerificationSectionMergesCommitgenVerification(t *testing.T) {
	commitgenBody := "Generated details.\n\n## Verification\n\nCommitgen's verification.\n\n## Notes\nKept notes."
	body := appendVerificationSection(commitgenBody, publicationVerification{
		gates:            publicationGateVerification{},
		evidenceRecorded: true, evidence: "Exercise summary.",
	})
	if strings.Count(body, "## Verification") != 1 ||
		!strings.Contains(body, "Commitgen's verification.") ||
		!strings.Contains(body, "Gates: none (no Gate ran).") ||
		!strings.Contains(body, "Exercise summary.") ||
		!strings.Contains(body, "## Notes\nKept notes.") {
		t.Fatalf("body = %q, want generated and commitgen content merged with later sections preserved", body)
	}
}

func TestSummarizeEvidenceKeepsOutcomeAndBoundsSummary(t *testing.T) {
	contents := "# Evidence\n\n## Exercise: long detail\n" +
		"- **What was exercised:** " + strings.Repeat("verbose detail ", 250) + "\n" +
		"- **Outcome:** not exercised — local runtime service is unavailable.\n"
	summary := summarizeEvidenceText(contents)
	if !strings.Contains(summary, "Outcome: not exercised — local runtime service is unavailable.") {
		t.Fatalf("Evidence summary = %q, want the not-exercised reason", summary)
	}
	if len([]rune(summary)) > maxPublicationEvidenceSummaryRunes {
		t.Fatalf("Evidence summary length = %d, want at most %d runes", len([]rune(summary)), maxPublicationEvidenceSummaryRunes)
	}
	if len(summary) >= len(contents) {
		t.Fatalf("Evidence summary length = %d, want less than source length %d", len(summary), len(contents))
	}
}
