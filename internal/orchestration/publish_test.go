package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		contents, recorded, err := readPublicationEvidence(t.TempDir())
		if err != nil || recorded || contents != "" {
			t.Fatalf("readPublicationEvidence() = (%q, %t, %v), want absent without error", contents, recorded, err)
		}
	})

	t.Run("read failure is returned", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "file-not-directory")
		if err := os.WriteFile(directory, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		contents, recorded, err := readPublicationEvidence(directory)
		if err == nil || recorded || contents != "" || !strings.Contains(err.Error(), "read evidence.md") {
			t.Fatalf("readPublicationEvidence() = (%q, %t, %v), want a read error", contents, recorded, err)
		}
		body := appendVerificationSection("", publicationVerification{evidenceUnavailable: true})
		if !strings.Contains(body, "Evidence: unavailable") || strings.Contains(body, "Evidence: none") ||
			strings.Contains(body, directory) {
			t.Fatalf("verification body = %q, want a safe unavailable message instead of missing Evidence or a local path", body)
		}
	})
}

func TestPublicationVerificationSummarizesImagesOnlyEvidenceAsText(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "evidence.md"), []byte("![runtime screenshot](screen.png)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, recorded, err := readPublicationEvidence(directory)
	if err != nil || !recorded {
		t.Fatalf("readPublicationEvidence() = (%q, %t, %v), want recorded Evidence", contents, recorded, err)
	}
	body := appendVerificationSection("", publicationVerification{evidence: contents, evidenceRecorded: recorded})
	if !strings.Contains(body, "Evidence summary:\n\n[Image omitted: runtime screenshot]") {
		t.Fatalf("verification body = %q, want text summary of the omitted image", body)
	}
	if strings.Contains(body, "![") || strings.Contains(body, "screen.png") {
		t.Fatalf("verification body = %q, want no embedded image or image path", body)
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
	if !strings.Contains(summary, "Exercised: the CLI output. The second output line was also checked.") {
		t.Fatalf("Evidence summary = %q, want the continuation line", summary)
	}
	if got := strings.Count(summary, "- Outcome: pass"); got != 2 {
		t.Fatalf("Evidence summary = %q, want two separate exercises, found %d outcomes", summary, got)
	}
}

func TestSummarizeEvidenceStartsExerciseOnHeadingWithoutExercisedField(t *testing.T) {
	contents := "# Evidence\n\n## Exercise: CLI\n" +
		"- What was exercised: the command output.\n" +
		"- Outcome: pass\n\n" +
		"## Exercise: API\n" +
		"- How it was exercised: made an HTTP request.\n" +
		"- Outcome: pass\n"
	exercises := strings.Split(summarizeEvidenceText(contents), "\n")
	if len(exercises) != 2 || !strings.Contains(exercises[0], "Exercised: the command output.") ||
		!strings.Contains(exercises[1], "How: made an HTTP request.") ||
		strings.Contains(exercises[1], "command output") {
		t.Fatalf("Evidence exercises = %q, want the API heading to start an independent exercise", exercises)
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
