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
	reviewFindingLocationInstruction = `Follow the Verdict contract for location rules. Cite each finding using the changed file's repository-relative path and its post-change, 1-based line. Determine it from the new-side (+) hunk arithmetic or confirm it in the file; a diff artifact line is never valid. Omit :line when no single line applies.`

	questionProtocolInstruction = `If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

The QUESTION: marker must begin at the start of its own line; the block format is otherwise unchanged.
Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.`

	implementerEvidenceInstruction = `Before ending this implementer turn, use the vendored verify skill to exercise the change in the running software and save Evidence to %s. Do not commit Evidence; it stays with this Run.`

	implementPrompt = `/implement

Implement the ticket below in the current project. Use the vendored implement skill and leave the working tree with the requested changes for review. Do not commit or push changes.

` + questionProtocolInstruction + `

At the end of this turn, self-assess your remaining context. Only if you judge that you are low on context, invoke ` + "`/handoff %s`" + ` to prepare the next iteration. Do not invoke it otherwise.

` + implementerEvidenceInstruction + `

Ticket: %s
Title: %s

Ticket body (including acceptance criteria):
%s`

	reviseImplementPrompt = `/fix-review

Fix every [blocking] finding; you cannot decline a [blocking] finding. Fix every [nit] finding, or decline a [nit] only with a reason. Report each declined nit with its location and reason in your final response. Use the vendored fix-review skill. Leave the working tree with the changes for review and do not commit or push changes.

` + questionProtocolInstruction + `

At the end of this turn, self-assess your remaining context. Only if you judge that you are low on context, invoke ` + "`/handoff %s`" + ` to prepare the next iteration. Do not invoke it otherwise.

` + implementerEvidenceInstruction + `

Ticket: %s

Blocking findings:
%s

Nits:
%s`

	gateAttemptImplementPrompt = `Continue implementing the ticket below to address the red Gate attempt. Use the vendored implement skill and leave the working tree with changes for review. Do not commit or push changes. No reviewer findings apply to this Gate attempt.

` + questionProtocolInstruction + `

At the end of this turn, self-assess your remaining context. Only if you judge that you are low on context, invoke ` + "`/handoff %s`" + ` to prepare the next implementer turn. Do not invoke it otherwise.

` + implementerEvidenceInstruction + `

Ticket: %s
Title: %s

Ticket body (including acceptance criteria):
%s

Gate attempt result:
%s`

	rolloverSeedPrompt = `Before doing any work, read the Rollover handoff document at %s. It supplements the ticket and all carried-over findings in this prompt; it does not replace them.

Title: %s

Ticket body (including acceptance criteria):
%s`

	gateAttemptRolloverSeedPrompt = `Before doing any work, read the Rollover handoff document at %s. It supplements the ticket and Gate attempt result in this prompt; it does not replace them.

Title: %s

Ticket body (including acceptance criteria):
%s`

	reviewPrompt = `/code-review

Review the pre-computed diff at %s against the recorded branch point %s. This file is the authoritative diff for this review. Do not run Git to re-derive the diff. You may still open individual files for surrounding context. %s %s Do not modify files. Do not read or write review documents; the invoking tool records the verdict. The verdict block you print is the only record. End the review with the mandatory verdict block from the code-review skill.

` + questionProtocolInstruction

	reviewResumePrompt = `The updated diff for this iteration is at %s. %s Apply the Evidence review rules in the code-review skill supplied for this review to the Evidence directory in this prompt. For each carried-over finding, locate its current line in the current code before reporting it; do not copy its line number from an earlier Verdict. Re-examine the changes and verify each carried-over finding is resolved. Keep the tag of each unresolved finding. Do not downgrade an unresolved [blocking] finding to [nit]. Evaluate each declined nit and its reason. Keep a declined [nit] in the Verdict unless you accept its reason. Check that the fixes introduced no new problems, and end with the mandatory verdict block from the code-review skill.

Blocking findings:
%s

Nits:
%s`
)

type implementPromptOptions struct {
	handoffPath       string
	evidenceDirectory string
	rolloverSeedPath  string
	additionalContext string
}

type reviewPromptContext struct {
	diffPath             string
	evidence             reviewEvidence
	findings             []verdict.Finding
	additionalContext    string
	implementerFeedPaths []string
}

func composeImplementPrompt(
	ticket tracker.Ticket,
	findings []verdict.Finding,
	iteration int,
	options implementPromptOptions,
) string {
	var prompt string
	if iteration == 1 {
		prompt = fmt.Sprintf(
			implementPrompt,
			options.handoffPath, options.evidenceDirectory,
			"#"+strconv.Itoa(ticket.Number), ticket.Title, ticket.Body,
		)
	} else {
		prompt = fmt.Sprintf(
			reviseImplementPrompt,
			options.handoffPath, options.evidenceDirectory, "#"+strconv.Itoa(ticket.Number),
			formatFindings(findings, verdict.Blocking), formatFindings(findings, verdict.Nit),
		)
	}
	if options.rolloverSeedPath != "" {
		prompt = fmt.Sprintf(
			"%s\n\n%s",
			prompt,
			fmt.Sprintf(rolloverSeedPrompt, options.rolloverSeedPath, ticket.Title, ticket.Body),
		)
	}
	return appendPromptContext(prompt, options.additionalContext)
}

func composeGateAttemptPrompt(
	ticket tracker.Ticket,
	options implementPromptOptions,
	gateAttemptResult string,
) string {
	prompt := fmt.Sprintf(
		gateAttemptImplementPrompt,
		options.handoffPath, options.evidenceDirectory,
		"#"+strconv.Itoa(ticket.Number), ticket.Title, ticket.Body, gateAttemptResult,
	)
	if options.rolloverSeedPath != "" {
		prompt = fmt.Sprintf(
			"%s\n\n%s",
			prompt,
			fmt.Sprintf(gateAttemptRolloverSeedPrompt, options.rolloverSeedPath, ticket.Title, ticket.Body),
		)
	}
	return appendPromptContext(prompt, options.additionalContext)
}

func composeReviewPrompt(ticketRef string, ticket *tracker.Ticket, branchPoint string, context reviewPromptContext) string {
	prompt := fmt.Sprintf(
		reviewPrompt,
		context.diffPath, branchPoint, reviewFindingLocationInstruction, context.evidence.promptInstruction(),
	)
	if ticket == nil {
		return appendPromptContext(prompt, context.additionalContext)
	}
	prompt = fmt.Sprintf("%s\n\nReview the current diff against this ticket (%s).\n\nTicket title: %s\n\nTicket body:\n%s", prompt, ticketRef, ticket.Title, ticket.Body)
	return appendPromptContext(prompt, context.additionalContext)
}

func appendPromptContext(prompt, additionalContext string) string {
	additionalContext = strings.TrimSpace(additionalContext)
	if additionalContext == "" {
		return prompt
	}
	return fmt.Sprintf("%s\n\n## Additional context supplied by the user for this run\n\n%s", prompt, additionalContext)
}

func composeReviewCarryoverPrompt(context reviewPromptContext) string {
	prompt := fmt.Sprintf(
		reviewResumePrompt,
		context.diffPath, reviewFindingLocationInstruction,
		formatFindings(context.findings, verdict.Blocking), formatFindings(context.findings, verdict.Nit),
	)
	prompt = appendPromptContext(prompt, context.additionalContext)
	return prompt + "\n\nRead the implementer feeds for this iteration for any nit decline reasons:\n- " + strings.Join(context.implementerFeedPaths, "\n- ")
}

func composeResumedReviewCarryoverPrompt(context reviewPromptContext) string {
	return "This is an incremental re-review in the existing reviewer session. Do not spawn fresh Standards/Spec sub-agents. " +
		context.evidence.promptInstruction() + " " + composeReviewCarryoverPrompt(context)
}

func appendGateResultPath(prompt, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return prompt
	}
	return fmt.Sprintf("%s\n\nThe orchestrator's Gate result for this iteration is recorded at %s.", prompt, path)
}

func composePlanPrompt(options PlanOptions) string {
	trackerName := planTrackerDisplayName(options.TrackerName)
	topic := strings.TrimSpace(options.Topic)
	setupStep := planRemoteSetupStep(options)

	if isTicketsOnlyPlan(options) {
		return composeTicketsOnlyPlanPrompt(topic, trackerName, setupStep)
	}

	firstSkill, steps := composePlanSteps(options, trackerName, setupStep)
	return fmt.Sprintf("/%s\n\nTopic: %s\n\n%s", firstSkill, topic, strings.Join(steps, " "))
}

func planTrackerDisplayName(trackerName config.Tracker) string {
	switch trackerName {
	case config.TrackerGitHub:
		return "GitHub"
	case config.TrackerGitLab:
		return "GitLab"
	default:
		return "local"
	}
}

func isTicketsOnlyPlan(options PlanOptions) bool {
	return !options.Grill && !options.Spec
}

func composeTicketsOnlyPlanPrompt(topic, trackerName, setupStep string) string {
	toTicketsStep := fmt.Sprintf("Use the to-tickets skill to produce tickets on the configured %s tracker.", trackerName)
	if setupStep == "" {
		return fmt.Sprintf("/to-tickets\n\nTopic: %s\n\n%s", topic, toTicketsStep)
	}
	return fmt.Sprintf("/to-tickets\n\nTopic: %s\n\n%s %s", topic, setupStep, toTicketsStep)
}

func composePlanSteps(options PlanOptions, trackerName, setupStep string) (string, []string) {
	firstSkill := "to-spec"
	steps := make([]string, 0, 3)
	if options.Grill {
		firstSkill = "grill-me"
		if options.WithDocs {
			firstSkill = "grill-with-docs"
		}
		steps = append(steps, fmt.Sprintf("First use the %s skill to grill the user on this topic.", firstSkill))
	}
	if setupStep != "" {
		steps = append(steps, setupStep)
	}
	if options.Spec {
		prefix := "First"
		if options.Grill {
			prefix = "After the grilling is complete,"
		}
		steps = append(steps, fmt.Sprintf("%s use the to-spec skill to publish a spec on the configured %s tracker.", prefix, trackerName))
	}
	previous := "spec"
	if !options.Spec {
		previous = "grilling"
	}
	steps = append(steps, fmt.Sprintf("After the %s is complete, use the to-tickets skill to produce tickets on the configured %s tracker.", previous, trackerName))
	return firstSkill, steps
}

func planRemoteSetupStep(options PlanOptions) string {
	if !options.NoRemote || !options.RemoteMissingAtStart {
		return ""
	}

	createRemoteCommand := ""
	switch options.TrackerName {
	case config.TrackerGitHub:
		createRemoteCommand = "gh repo create <name> --source . --remote origin --private"
	case config.TrackerGitLab:
		createRemoteCommand = "glab repo create <name> --private --remoteName origin"
	default:
		return ""
	}

	return fmt.Sprintf("Before the first tracker write, check whether this directory is a Git repository and whether it has an origin remote. If this directory is not a Git repository, offer to run `git init`. If the origin remote is missing, offer to create it with `%s`. Ask the user to confirm each command before running it. If the user declines any setup command, do not run the remaining setup commands, skip the to-spec and to-tickets steps, and end the session. Leave any local docs created by --with-docs in place.", createRemoteCommand)
}

func formatFindings(findings []verdict.Finding, kind verdict.FindingKind) string {
	var builder strings.Builder
	for _, finding := range findings {
		if finding.Kind != kind {
			continue
		}
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
	}
	if builder.Len() == 0 {
		return "(none)"
	}
	return strings.TrimSuffix(builder.String(), "\n")
}
