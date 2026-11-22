package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestComposeImplementPromptFirstIteration(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Concentrate prompts", Body: "Keep every byte.\n\nDo not change wording."}

	got := composeImplementPrompt(ticket, nil, 1, implementPromptOptions{
		handoffPath: "/tmp/handoff-1.md", evidenceDirectory: "/tmp/evidence-1",
	})
	want := `/implement

Implement the ticket below in the current project. Use the vendored implement skill and leave the working tree with the requested changes for review. Do not commit or push changes.

If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.

At the end of this turn, self-assess your remaining context. Only if you judge that you are low on context, invoke ` + "`/handoff /tmp/handoff-1.md`" + ` to prepare the next iteration. Do not invoke it otherwise.

Before ending this implementer turn, use the vendored verify skill to exercise the change in the running software and save Evidence to /tmp/evidence-1. Do not commit Evidence; it stays with this Run.

Ticket: #42
Title: Concentrate prompts

Ticket body (including acceptance criteria):
Keep every byte.

Do not change wording.`

	assertPromptEqual(t, got, want)
}

func TestComposePlanPromptNamesGitLabTracker(t *testing.T) {
	got := composePlanPrompt(PlanOptions{Topic: "publish the issue tracker", TrackerName: config.TrackerGitLab})
	want := "/to-tickets\n\nTopic: publish the issue tracker\n\n" +
		"Use the to-tickets skill to produce tickets on the configured GitLab tracker."

	assertPromptEqual(t, got, want)
}

func TestComposePlanPromptSetsUpRemoteBeforeFirstTrackerWrite(t *testing.T) {
	tests := []struct {
		name          string
		options       PlanOptions
		wantCLI       string
		beforeTracker string
		beforeSetup   string
	}{
		{
			name: "tickets on GitHub",
			options: PlanOptions{
				Topic: "publish the issue tracker", TrackerName: config.TrackerGitHub,
				NoRemote: true, RemoteMissingAtStart: true,
			},
			wantCLI:       "gh repo create <name> --source . --remote origin --private",
			beforeTracker: "Use the to-tickets skill",
		},
		{
			name: "spec on GitHub",
			options: PlanOptions{
				Topic: "publish the issue tracker", TrackerName: config.TrackerGitHub,
				NoRemote: true, RemoteMissingAtStart: true, Spec: true,
			},
			wantCLI:       "gh repo create <name> --source . --remote origin --private",
			beforeTracker: "First use the to-spec skill",
		},
		{
			name: "grill with docs before setup",
			options: PlanOptions{
				Topic: "publish the issue tracker", TrackerName: config.TrackerGitLab,
				NoRemote: true, RemoteMissingAtStart: true, Grill: true, WithDocs: true, Spec: true,
			},
			wantCLI:       "glab repo create <name> --private --remoteName origin",
			beforeTracker: "After the grilling is complete,",
			beforeSetup:   "First use the grill-with-docs skill",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := composePlanPrompt(test.options)
			setupStart := strings.Index(got, "Before the first tracker write")
			trackerStart := strings.Index(got, test.beforeTracker)
			if setupStart == -1 || trackerStart == -1 || setupStart >= trackerStart {
				t.Fatalf("plan prompt = %q, want setup before %q", got, test.beforeTracker)
			}
			if test.beforeSetup != "" {
				beforeSetupStart := strings.Index(got, test.beforeSetup)
				if beforeSetupStart == -1 || beforeSetupStart >= setupStart {
					t.Fatalf("plan prompt = %q, want %q before setup", got, test.beforeSetup)
				}
			}
			assertContainsAll(t, got,
				test.wantCLI,
				"If this directory is not a Git repository, offer to run `git init`",
				"Ask the user to confirm each command before running it",
				"do not run the remaining setup commands",
				"skip the to-spec and to-tickets steps, and end the session",
				"Leave any local docs created by --with-docs in place",
			)
			if test.options.TrackerName == config.TrackerGitHub && strings.Contains(got, "glab repo create") {
				t.Fatalf("GitHub plan prompt = %q, want no GitLab command", got)
			}
			if test.options.TrackerName == config.TrackerGitLab && strings.Contains(got, "gh repo create") {
				t.Fatalf("GitLab plan prompt = %q, want no GitHub command", got)
			}
		})
	}
}

func TestComposePlanPromptAddsRemoteSetupOnlyForMissingRemoteNoRemoteSessions(t *testing.T) {
	for _, grill := range []bool{false, true} {
		for _, withDocs := range []bool{false, true} {
			for _, spec := range []bool{false, true} {
				options := PlanOptions{
					Topic: "publish the issue tracker", TrackerName: config.TrackerGitHub,
					Grill: grill, WithDocs: withDocs, Spec: spec,
				}
				baseline := composePlanPrompt(options)

				withNoRemoteFlag := options
				withNoRemoteFlag.NoRemote = true
				withNoRemoteButRemotePresent := composePlanPrompt(withNoRemoteFlag)
				assertPromptEqual(t, withNoRemoteButRemotePresent, baseline)

				withMissingRemoteSignal := withNoRemoteFlag
				withMissingRemoteSignal.RemoteMissingAtStart = true
				withoutNoRemoteFlag := options
				withoutNoRemoteFlag.RemoteMissingAtStart = true
				assertPromptEqual(t, composePlanPrompt(withoutNoRemoteFlag), baseline)
				if !strings.Contains(composePlanPrompt(withMissingRemoteSignal), "Before the first tracker write") {
					t.Fatalf("plan prompt with --no-remote and missing origin = %q, want setup instruction", composePlanPrompt(withMissingRemoteSignal))
				}
			}
		}
	}
}

func TestComposeImplementPromptRevisionWithBlockingFindings(t *testing.T) {
	ticket := tracker.Ticket{Number: 42}
	findings := []verdict.Finding{
		{Kind: verdict.Blocking, Location: "prompts.go:10", Issue: "first issue"},
		{Kind: verdict.Blocking, Location: "prompts_test.go:20", Issue: "second issue"},
	}

	got := composeImplementPrompt(ticket, findings, 2, implementPromptOptions{
		handoffPath: "/tmp/handoff-2.md", evidenceDirectory: "/tmp/evidence-2",
	})
	assertContainsAll(t, got,
		"Fix every [blocking] finding", "cannot decline a [blocking]", "decline a [nit] only with a reason",
		"Nits:\n(none)", "- [blocking] prompts.go:10 — first issue",
		"use the vendored verify skill", "save Evidence to /tmp/evidence-2",
	)
	if strings.Contains(got, "Address ONLY") {
		t.Fatal("obsolete blocking-only instruction")
	}

}

func TestComposeImplementPromptWhitespaceOnlyContextMatchesWithoutContext(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Concentrate prompts", Body: "Keep every byte."}
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "prompts.go:10", Issue: "first issue"}}

	for _, test := range []struct {
		name      string
		iteration int
		blocking  []verdict.Finding
	}{
		{name: "first iteration", iteration: 1},
		{name: "revision", iteration: 2, blocking: findings},
	} {
		t.Run(test.name, func(t *testing.T) {
			handoffPath := "/tmp/handoff.md"
			evidencePath := "/tmp/evidence-1"
			if test.iteration > 1 {
				evidencePath = "/tmp/evidence-2"
			}
			options := implementPromptOptions{handoffPath: handoffPath, evidenceDirectory: evidencePath}
			withoutContext := composeImplementPrompt(ticket, test.blocking, test.iteration, options)
			options.additionalContext = " \n\t "
			withWhitespaceContext := composeImplementPrompt(ticket, test.blocking, test.iteration, options)
			assertPromptEqual(t, withWhitespaceContext, withoutContext)
		})
	}
}

func TestComposeImplementPromptPreservesMultilineContext(t *testing.T) {
	ticket := tracker.Ticket{Number: 42}
	context := "Use the existing GitRunner seam.\nDo not add a new adapter."

	got := composeImplementPrompt(ticket, nil, 1, implementPromptOptions{
		handoffPath: "/tmp/handoff-1.md", evidenceDirectory: "/tmp/evidence-1", additionalContext: context,
	})

	if !strings.Contains(got, context) {
		t.Fatalf("implement prompt = %q, want exact multi-line context %q", got, context)
	}
}

func TestComposeImplementPromptSeedsFreshRolloverSession(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Preserve context", Body: "Keep the session decisions."}
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "worker.go:10", Issue: "handle the error"}}

	got := composeImplementPrompt(ticket, findings, 2, implementPromptOptions{
		handoffPath: "/tmp/handoff-2.md", evidenceDirectory: "/tmp/evidence-2", rolloverSeedPath: "/tmp/handoff-1.md",
	})

	assertContainsAll(t, got,
		"Before doing any work, read the Rollover handoff document at /tmp/handoff-1.md.",
		"Ticket: #42",
		"Title: Preserve context",
		"Ticket body (including acceptance criteria):\nKeep the session decisions.",
		"Blocking findings:\n- [blocking] worker.go:10 — handle the error",
		"/handoff /tmp/handoff-2.md",
	)
}

func TestComposeGateAttemptPromptIncludesEvidenceDirectory(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Exercise failed Gate fixes", Body: "Keep behavior visible."}

	got := composeGateAttemptPrompt(ticket, implementPromptOptions{
		handoffPath: "/tmp/handoff-1-gate-2.md", evidenceDirectory: "/tmp/evidence-1",
	}, "tests failed")

	assertContainsAll(t, got,
		"use the vendored verify skill to exercise the change in the running software",
		"save Evidence to /tmp/evidence-1",
		"Gate attempt result:\ntests failed",
	)
}

func TestComposeReviewPromptWithoutTicket(t *testing.T) {
	got := composeReviewPrompt("", nil, "abc123", reviewPromptContext{
		diffPath: "/tmp/review.diff", evidence: noIterationReviewEvidence(),
	})
	want := `/code-review

Review the pre-computed diff at /tmp/review.diff against the recorded branch point abc123. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. Follow the Verdict contract for location rules. Cite each finding using the changed file's repository-relative path and its post-change, 1-based line. Determine it from the new-side (+) hunk arithmetic or confirm it in the file; a diff artifact line is never valid. Omit :line when no single line applies. This standalone review has no iteration Evidence directory. Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.`

	assertPromptEqual(t, got, want)
}

func TestComposeReviewPromptContainsFindingLineRule(t *testing.T) {
	got := composeReviewPrompt("", nil, "abc123", reviewPromptContext{
		diffPath: "/tmp/review.diff", evidence: noIterationReviewEvidence(),
	})

	assertContainsAll(t, got,
		"Follow the Verdict contract for location rules.",
		"Cite each finding using the changed file's repository-relative path and its post-change, 1-based line.",
		"Determine it from the new-side (+) hunk arithmetic or confirm it in the file; a diff artifact line is never valid.",
		"Omit :line when no single line applies.",
	)
}

func TestComposeReviewPromptReportsIterationEvidenceAvailability(t *testing.T) {
	evidenceDirectory := filepath.Join(t.TempDir(), "evidence-4")
	prompt := func() string {
		return composeReviewPrompt("", nil, "abc123", reviewPromptContext{
			diffPath: "/tmp/review.diff", evidence: inspectReviewEvidence(evidenceDirectory),
		})
	}

	assertContainsAll(t, prompt(), evidenceDirectory, "directory does not exist", "evidence.md is missing")
	if err := os.Mkdir(evidenceDirectory, 0o755); err != nil {
		t.Fatalf("create Evidence directory: %v", err)
	}
	assertContainsAll(t, prompt(), evidenceDirectory, "evidence.md is missing")

	evidencePath := filepath.Join(evidenceDirectory, "evidence.md")
	if err := os.WriteFile(evidencePath, []byte("Outcome: pass\n"), 0o644); err != nil {
		t.Fatalf("write evidence.md: %v", err)
	}
	assertContainsAll(t, prompt(), evidenceDirectory, "Read evidence.md", "supporting files")
}

func TestComposeReviewPromptWithTicket(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Concentrate prompts", Body: "Keep every byte.\n\nDo not change wording."}

	got := composeReviewPrompt("#42", &ticket, "abc123", reviewPromptContext{
		diffPath: "/tmp/review.diff", evidence: noIterationReviewEvidence(),
	})
	want := `/code-review

Review the pre-computed diff at /tmp/review.diff against the recorded branch point abc123. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. Follow the Verdict contract for location rules. Cite each finding using the changed file's repository-relative path and its post-change, 1-based line. Determine it from the new-side (+) hunk arithmetic or confirm it in the file; a diff artifact line is never valid. Omit :line when no single line applies. This standalone review has no iteration Evidence directory. Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.

Review the current diff against this ticket (#42).

Ticket title: Concentrate prompts

Ticket body:
Keep every byte.

Do not change wording.`

	assertPromptEqual(t, got, want)
}

func TestComposeReviewPromptWhitespaceOnlyContextMatchesWithoutContext(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Concentrate prompts", Body: "Keep every byte."}

	withoutContext := composeReviewPrompt("#42", &ticket, "abc123", reviewPromptContext{
		diffPath: "/tmp/review.diff", evidence: noIterationReviewEvidence(),
	})
	withWhitespaceContext := composeReviewPrompt("#42", &ticket, "abc123", reviewPromptContext{
		diffPath: "/tmp/review.diff", evidence: noIterationReviewEvidence(), additionalContext: " \n\t ",
	})

	assertPromptEqual(t, withWhitespaceContext, withoutContext)
}

func TestAppendPromptContext(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		context string
		want    string
	}{
		{
			name:    "non-blank context",
			prompt:  "base prompt",
			context: "  only the parser changes matter  ",
			want:    "base prompt\n\n## Additional context supplied by the user for this run\n\nonly the parser changes matter",
		},
		{
			name:    "empty context",
			prompt:  "base prompt",
			context: "",
			want:    "base prompt",
		},
		{
			name:    "whitespace-only context",
			prompt:  "base prompt",
			context: " \n\t ",
			want:    "base prompt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := appendPromptContext(test.prompt, test.context)
			assertPromptEqual(t, got, test.want)
		})
	}
}

func TestComposeReviewCarryoverPrompt(t *testing.T) {
	findings := []verdict.Finding{
		{Kind: verdict.Blocking, Location: "prompts.go:10", Issue: "first issue"},
		{Kind: verdict.Blocking, Location: "prompts_test.go:20", Issue: "second issue"},
	}

	got := composeReviewCarryoverPrompt(reviewPromptContext{diffPath: "/tmp/review-2.diff", findings: findings})
	assertContainsAll(t, got, "verify each carried-over finding", "Do not downgrade an unresolved [blocking] finding", "Keep a declined [nit] in the Verdict unless you accept its reason", "Nits:\n(none)")
	if !strings.HasSuffix(got, "Read the implementer feeds for this iteration for any nit decline reasons:\n- ") {
		t.Fatalf("carryover prompt = %q, want the existing empty-feed section", got)
	}
}

func TestComposeResumedReviewCarryoverPromptIncludesIterationEvidence(t *testing.T) {
	evidenceDirectory := t.TempDir()
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "review.go:10", Issue: "first issue"}}
	context := reviewPromptContext{
		diffPath: "/tmp/review-2.diff", evidence: inspectReviewEvidence(evidenceDirectory), findings: findings,
	}
	prompt := composeResumedReviewCarryoverPrompt(context)
	assertContainsAll(t, prompt, evidenceDirectory, "evidence.md is missing", "Apply the Evidence review rules")

	evidencePath := filepath.Join(evidenceDirectory, "evidence.md")
	if err := os.WriteFile(evidencePath, []byte("Outcome: pass\n"), 0o644); err != nil {
		t.Fatalf("write evidence.md: %v", err)
	}
	context.evidence = inspectReviewEvidence(evidenceDirectory)
	prompt = composeResumedReviewCarryoverPrompt(context)
	assertContainsAll(t, prompt, evidenceDirectory, "Read evidence.md", "supporting files")
}

func TestComposeReviewCarryoverPromptContainsFindingLineRuleAndRefreshesCarriedOverLocations(t *testing.T) {
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "review.go:10", Issue: "first issue"}}
	got := composeReviewCarryoverPrompt(reviewPromptContext{diffPath: "/tmp/review-2.diff", findings: findings})

	assertContainsAll(t, got,
		"Follow the Verdict contract for location rules.",
		"Cite each finding using the changed file's repository-relative path and its post-change, 1-based line.",
		"Determine it from the new-side (+) hunk arithmetic or confirm it in the file; a diff artifact line is never valid.",
		"Omit :line when no single line applies.",
		"For each carried-over finding, locate its current line in the current code before reporting it; do not copy its line number from an earlier Verdict.",
	)
}

func TestComposeReviewCarryoverPromptAppendsContextAfterBlockingFindings(t *testing.T) {
	findings := []verdict.Finding{
		{Kind: verdict.Blocking, Location: "review.go:10", Issue: "preserve this finding"},
	}
	context := "Ignore the vendored skills directory."

	got := composeReviewCarryoverPrompt(reviewPromptContext{
		diffPath: "/tmp/review-2.diff", findings: findings, additionalContext: context,
	})

	assertContainsAll(t, got,
		"Blocking findings:\n- [blocking] review.go:10 — preserve this finding",
		"## Additional context supplied by the user for this run\n\n"+context,
	)
	if strings.Index(got, "Blocking findings:") > strings.Index(got, "## Additional context") {
		t.Fatalf("resume prompt = %q, want blocking findings before context", got)
	}
}

func TestComposeReviewCarryoverPromptWhitespaceOnlyContextMatchesWithoutContext(t *testing.T) {
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "review.go:10", Issue: "first issue"}}

	withoutContext := composeReviewCarryoverPrompt(reviewPromptContext{diffPath: "/tmp/review-2.diff", findings: findings})
	withWhitespaceContext := composeReviewCarryoverPrompt(reviewPromptContext{
		diffPath: "/tmp/review-2.diff", findings: findings, additionalContext: " \n\t ",
	})

	assertPromptEqual(t, withWhitespaceContext, withoutContext)
}

func assertContainsAll(t *testing.T, got string, expected ...string) {
	t.Helper()
	for _, substring := range expected {
		if !strings.Contains(got, substring) {
			t.Errorf("text = %q, want it to contain %q", got, substring)
		}
	}
}

func assertPromptEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestRevisionPromptsCarryNits(t *testing.T) {
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "a.go:1", Issue: "block"}, {Kind: verdict.Nit, Location: "b.go:2", Issue: "nit issue"}}
	implement := composeImplementPrompt(tracker.Ticket{Number: 42}, findings, 2, implementPromptOptions{
		handoffPath: "handoff", evidenceDirectory: "evidence-2", rolloverSeedPath: "seed",
	})
	review := composeReviewCarryoverPrompt(reviewPromptContext{diffPath: "diff", findings: findings})
	for _, prompt := range []string{implement, review} {
		assertContainsAll(t, prompt, "Blocking findings:\n- [blocking] a.go:1 — block", "Nits:\n- [nit] b.go:2 — nit issue")
	}
}
