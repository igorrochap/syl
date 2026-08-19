package orchestration

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

const (
	questionProtocolInstruction = `If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.`

	implementPrompt = `/implement

Implement the ticket below in the current project. Use the vendored implement skill and leave the working tree with the requested changes for review. Do not commit or push changes.

` + questionProtocolInstruction + `

Ticket: %s
Title: %s

Ticket body (including acceptance criteria):
%s`

	reviseImplementPrompt = `/fix-review

Address ONLY the reviewer's [blocking] findings listed below. Use the vendored fix-review skill. Leave the working tree with the changes for review and do not commit or push changes.

` + questionProtocolInstruction + `

Ticket: %s

Blocking findings:
%s`

	reviewPrompt = `/code-review

Review the pre-computed diff at %s against the recorded branch point %s. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

` + questionProtocolInstruction

	reviewResumePrompt = `This is an incremental re-review in the existing reviewer session. Do not spawn fresh Standards/Spec sub-agents. The implementer has addressed your blocking findings, listed below. The updated diff for this iteration is at %s. Re-examine the changes, verify each blocking finding is resolved, check that the fixes introduced no new problems, and end with the mandatory verdict block from the code-review skill.

Blocking findings:
%s`
)

func composeImplementPrompt(ticket tracker.Ticket, blocking []verdict.Finding, iteration int) string {
	if iteration == 1 {
		return fmt.Sprintf(implementPrompt, "#"+strconv.Itoa(ticket.Number), ticket.Title, ticket.Body)
	}
	return fmt.Sprintf(reviseImplementPrompt, "#"+strconv.Itoa(ticket.Number), formatBlockingFindings(blocking))
}

func composeReviewPrompt(ticketRef string, ticket *tracker.Ticket, branchPoint, diffPath string) string {
	prompt := fmt.Sprintf(reviewPrompt, diffPath, branchPoint)
	if ticket == nil {
		return prompt
	}
	return fmt.Sprintf("%s\n\nReview the current diff against this ticket (%s).\n\nTicket title: %s\n\nTicket body:\n%s", prompt, ticketRef, ticket.Title, ticket.Body)
}

func composeReviewResumePrompt(diffPath string, blocking []verdict.Finding) string {
	return fmt.Sprintf(reviewResumePrompt, diffPath, formatBlockingFindings(blocking))
}

func formatBlockingFindings(findings []verdict.Finding) string {
	if len(findings) == 0 {
		return "(none)"
	}
	var builder strings.Builder
	for _, finding := range findings {
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
	}
	return strings.TrimSuffix(builder.String(), "\n")
}
