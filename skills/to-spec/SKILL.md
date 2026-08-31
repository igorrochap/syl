---
name: to-spec
description: Turn the current conversation into a spec and publish it to the configured issue tracker — no interview, just synthesis of what you've already discussed.
disable-model-invocation: true
---

This skill takes the current conversation and codebase understanding and produces a spec (you may know this document as a PRD). The workflow is synthesis-only. Use information that is already available. Do not conduct an interview. Ask only for the configured issue tracker when that required input is missing.

The configured issue tracker is required. If it is missing, ask the user for it before you proceed.

## ASD-STE100 writing checklist

Apply this checklist to the spec before publication:

- Use short, declarative sentences.
- Use active voice.
- Put one instruction or condition in each sentence.
- Use the same term for the same concept throughout the spec.
- State the subject of each sentence when it is not obvious.
- Place modifiers and pronouns so that they have one clear meaning.
- Do not claim formal ASD-STE100 certification. This checklist is an operational writing aid.

## Technical references

Use stable paths, commands, configuration keys, interface names, established domain terms, and short decision-rich snippets when they remove ambiguity. Do not use brittle line numbers. Do not prescribe an unnecessary implementation recipe. Preserve the exact technical text when a plain-language rewrite would change its meaning.

## Process

1. Explore the repository when the current context does not contain enough codebase information. Use the project's domain glossary vocabulary throughout the spec. Respect applicable ADRs.

2. Identify the highest existing test seam for the requested behavior. Record that seam and any needed new seam in `Testing Decisions`. Do not pause to interview the user or request seam approval.

3. Synthesize the spec from the conversation and repository evidence:

   - State the user's problem and the user-visible solution.
   - Preserve every material requirement, constraint, decision, and dependency that is already known.
   - Write a complete, non-duplicative set of user stories. Each story must describe distinct user value or behavior.
   - Record unresolved material ambiguity in `Open Questions`. Do not turn an open question into an unstated assumption.
   - Record implementation and testing decisions only when the available evidence supports them.

4. Run the completeness and clarity check below. Publish the spec to the configured issue tracker only after every check passes. A spec is a planning artifact. Do not add a `Branch:` line or a branch suggestion to the spec issue.

## Completeness and clarity check

Before publication, confirm all of the following:

- Every material source requirement appears in the Problem Statement, Solution, User Stories, Implementation Decisions, Testing Decisions, Out of Scope, Open Questions, or Further Notes section.
- The User Stories section covers every distinct user value or behavior. Each story maps to a source requirement or an explicit user need. No two stories duplicate the same value or behavior.
- The Coverage Check shows how the stories cover the source requirements. Do not use the number of stories as proof of completeness.
- Every unresolved material ambiguity appears in `Open Questions`. No section hides an assumption that could change the intended outcome.
- Implementation Decisions record technical references only when they remove ambiguity.
- The spec uses the ASD-STE100 writing checklist and uses terms consistently.
- The spec has no missing required input or unexplained contradiction.

## Spec template

<spec-template>

## Problem Statement

Describe the problem from the user's perspective. State who experiences it and what impact it has.

## Solution

Describe the intended user-visible solution. State the behavior and outcome without prescribing unnecessary implementation steps.

## User Stories

Write a complete, numbered, non-duplicative set of user stories. Each story must describe one distinct user value or behavior. Use this form:

1. As an <actor>, I want a <feature or behavior>, so that <benefit or outcome>.

Include stories for every applicable user role, primary path, alternate path, boundary, failure behavior, and important constraint. Do not set a minimum or maximum list length.

<user-story-example>
1. As a mobile bank customer, I want to see the balance on my accounts, so that I can make informed decisions about my spending.
</user-story-example>

## Coverage Check

Map every material source requirement or user need to one or more user story numbers. Confirm that every user story maps back to a distinct source requirement or user need. Use this mapping to prove coverage.

- <source requirement or user need> → User stories <numbers>

## Implementation Decisions

Record decisions that are supported by the available context. These can include:

- Modules or boundaries to build or modify.
- Interfaces to modify.
- Technical clarifications from the developer.
- Architectural decisions.
- Schema changes.
- API contracts.
- Specific interactions.

## Testing Decisions

Record how the behavior will be verified at the highest practical seam. Include:

- The external behavior that a good test observes.
- The modules or boundaries that require tests.
- Relevant prior art in the codebase.
- Any positive, negative, boundary, or failure behavior that needs verification.

## Out of Scope

Describe the related work that this spec does not cover.

## Open Questions

List every unresolved material question. State why each question matters. Write `None` when no unresolved material ambiguity remains.

## Further Notes

Record other context that helps a reader understand the spec. Do not put requirements or decisions here when a named section is more precise.

</spec-template>
