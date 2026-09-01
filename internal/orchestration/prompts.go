package orchestration

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/config"
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

func composeImplementPrompt(ticket tracker.Ticket, blocking []verdict.Finding, iteration int, additionalContext string) string {
	var prompt string
	if iteration == 1 {
		prompt = fmt.Sprintf(implementPrompt, "#"+strconv.Itoa(ticket.Number), ticket.Title, ticket.Body)
	} else {
		prompt = fmt.Sprintf(reviseImplementPrompt, "#"+strconv.Itoa(ticket.Number), formatBlockingFindings(blocking))
	}
	return appendPromptContext(prompt, additionalContext)
}

func composeReviewPrompt(ticketRef string, ticket *tracker.Ticket, branchPoint, diffPath, additionalContext string) string {
	prompt := fmt.Sprintf(reviewPrompt, diffPath, branchPoint)
	if ticket == nil {
		return appendPromptContext(prompt, additionalContext)
	}
	prompt = fmt.Sprintf("%s\n\nReview the current diff against this ticket (%s).\n\nTicket title: %s\n\nTicket body:\n%s", prompt, ticketRef, ticket.Title, ticket.Body)
	return appendPromptContext(prompt, additionalContext)
}

func appendPromptContext(prompt, additionalContext string) string {
	additionalContext = strings.TrimSpace(additionalContext)
	if additionalContext == "" {
		return prompt
	}
	return fmt.Sprintf("%s\n\n## Additional context supplied by the user for this run\n\n%s", prompt, additionalContext)
}

func composeReviewResumePrompt(diffPath string, blocking []verdict.Finding) string {
	return fmt.Sprintf(reviewResumePrompt, diffPath, formatBlockingFindings(blocking))
}

func composePlanPrompt(options PlanOptions) string {
	trackerName := "local"
	if options.TrackerName == config.TrackerGitHub {
		trackerName = "GitHub"
	}
	topic := strings.TrimSpace(options.Topic)

	if !options.Grill && !options.Spec {
		return fmt.Sprintf("/to-tickets\n\nTopic: %s\n\nUse the to-tickets skill to produce tickets on the configured %s tracker.", topic, trackerName)
	}

	firstSkill := "to-spec"
	steps := make([]string, 0, 3)
	if options.Grill {
		firstSkill = "grill-me"
		if options.WithDocs {
			firstSkill = "grill-with-docs"
		}
		steps = append(steps, fmt.Sprintf("First use the %s skill to grill the user on this topic.", firstSkill))
	}
	if options.Spec {
		prefix := "First"
		if len(steps) > 0 {
			prefix = "After the grilling is complete,"
		}
		steps = append(steps, fmt.Sprintf("%s use the to-spec skill to publish a spec on the configured %s tracker.", prefix, trackerName))
	}
	previous := "spec"
	if !options.Spec {
		previous = "grilling"
	}
	steps = append(steps, fmt.Sprintf("After the %s is complete, use the to-tickets skill to produce tickets on the configured %s tracker.", previous, trackerName))
	return fmt.Sprintf("/%s\n\nTopic: %s\n\n%s", firstSkill, topic, strings.Join(steps, " "))
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
