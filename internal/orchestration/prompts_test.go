package orchestration

import (
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestComposeImplementPromptFirstIteration(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Concentrate prompts", Body: "Keep every byte.\n\nDo not change wording."}

	got := composeImplementPrompt(ticket, nil, 1, "")
	want := `/implement

Implement the ticket below in the current project. Use the vendored implement skill and leave the working tree with the requested changes for review. Do not commit or push changes.

If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.

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

func TestComposeImplementPromptRevisionWithBlockingFindings(t *testing.T) {
	ticket := tracker.Ticket{Number: 42}
	findings := []verdict.Finding{
		{Kind: verdict.Blocking, Location: "prompts.go:10", Issue: "first issue"},
		{Kind: verdict.Blocking, Location: "prompts_test.go:20", Issue: "second issue"},
	}

	got := composeImplementPrompt(ticket, findings, 2, "")
	want := `/fix-review

Address ONLY the reviewer's [blocking] findings listed below. Use the vendored fix-review skill. Leave the working tree with the changes for review and do not commit or push changes.

If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.

Ticket: #42

Blocking findings:
- [blocking] prompts.go:10 — first issue
- [blocking] prompts_test.go:20 — second issue`

	assertPromptEqual(t, got, want)
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
			withoutContext := composeImplementPrompt(ticket, test.blocking, test.iteration, "")
			withWhitespaceContext := composeImplementPrompt(ticket, test.blocking, test.iteration, " \n\t ")
			assertPromptEqual(t, withWhitespaceContext, withoutContext)
		})
	}
}

func TestComposeImplementPromptPreservesMultilineContext(t *testing.T) {
	ticket := tracker.Ticket{Number: 42}
	context := "Use the existing GitRunner seam.\nDo not add a new adapter."

	got := composeImplementPrompt(ticket, nil, 1, context)

	if !strings.Contains(got, context) {
		t.Fatalf("implement prompt = %q, want exact multi-line context %q", got, context)
	}
}

func TestComposeReviewPromptWithoutTicket(t *testing.T) {
	got := composeReviewPrompt("", nil, "abc123", "/tmp/review.diff", "")
	want := `/code-review

Review the pre-computed diff at /tmp/review.diff against the recorded branch point abc123. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.`

	assertPromptEqual(t, got, want)
}

func TestComposeReviewPromptWithTicket(t *testing.T) {
	ticket := tracker.Ticket{Number: 42, Title: "Concentrate prompts", Body: "Keep every byte.\n\nDo not change wording."}

	got := composeReviewPrompt("#42", &ticket, "abc123", "/tmp/review.diff", "")
	want := `/code-review

Review the pre-computed diff at /tmp/review.diff against the recorded branch point abc123. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

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

	withoutContext := composeReviewPrompt("#42", &ticket, "abc123", "/tmp/review.diff", "")
	withWhitespaceContext := composeReviewPrompt("#42", &ticket, "abc123", "/tmp/review.diff", " \n\t ")

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

func TestComposeReviewResumePrompt(t *testing.T) {
	findings := []verdict.Finding{
		{Kind: verdict.Blocking, Location: "prompts.go:10", Issue: "first issue"},
		{Kind: verdict.Blocking, Location: "prompts_test.go:20", Issue: "second issue"},
	}

	got := composeReviewResumePrompt("/tmp/review-2.diff", findings, "")
	want := `This is an incremental re-review in the existing reviewer session. Do not spawn fresh Standards/Spec sub-agents. The implementer has addressed your blocking findings, listed below. The updated diff for this iteration is at /tmp/review-2.diff. Re-examine the changes, verify each blocking finding is resolved, check that the fixes introduced no new problems, and end with the mandatory verdict block from the code-review skill.

Blocking findings:
- [blocking] prompts.go:10 — first issue
- [blocking] prompts_test.go:20 — second issue`

	assertPromptEqual(t, got, want)
}

func TestComposeReviewResumePromptAppendsContextAfterBlockingFindings(t *testing.T) {
	findings := []verdict.Finding{
		{Kind: verdict.Blocking, Location: "review.go:10", Issue: "preserve this finding"},
	}
	context := "Ignore the vendored skills directory."

	got := composeReviewResumePrompt("/tmp/review-2.diff", findings, context)

	for _, expected := range []string{
		"Blocking findings:\n- [blocking] review.go:10 — preserve this finding",
		"## Additional context supplied by the user for this run\n\n" + context,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("resume prompt = %q, want %q", got, expected)
		}
	}
	if strings.Index(got, "Blocking findings:") > strings.Index(got, "## Additional context") {
		t.Fatalf("resume prompt = %q, want blocking findings before context", got)
	}
}

func TestComposeReviewResumePromptWhitespaceOnlyContextMatchesWithoutContext(t *testing.T) {
	findings := []verdict.Finding{{Kind: verdict.Blocking, Location: "review.go:10", Issue: "first issue"}}

	withoutContext := composeReviewResumePrompt("/tmp/review-2.diff", findings, "")
	withWhitespaceContext := composeReviewResumePrompt("/tmp/review-2.diff", findings, " \n\t ")

	assertPromptEqual(t, withWhitespaceContext, withoutContext)
}

func assertPromptEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}
