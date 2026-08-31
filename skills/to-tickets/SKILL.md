---
name: to-tickets
description: Break a plan, spec, or the current conversation into a set of tracer-bullet tickets, each declaring its blocking edges, published to the configured tracker — edges as text in one file per ticket locally, or native blocking links on a real tracker.
disable-model-invocation: true
---

# To Tickets

Break a plan, spec, or conversation into **tickets** — tracer-bullet vertical slices, each declaring the tickets that **block** it.

The configured issue tracker is required. If it is missing, ask the user for it before you proceed. Do not add a tracker-label requirement to the planning artifacts.

## ASD-STE100 writing checklist

Apply this checklist to every ticket body before publication:

- Use short, declarative sentences.
- Use active voice.
- Put one instruction or condition in each sentence.
- Use the same term for the same concept throughout the ticket.
- State the subject of each sentence when it is not obvious.
- Place modifiers and pronouns so that they have one clear meaning.
- Preserve exact code, commands, identifiers, quotations, and established domain terms when a rewrite would change their meaning.
- Do not claim formal ASD-STE100 certification. This checklist is an operational writing aid.

Use stable paths, commands, configuration keys, interface names, established domain terms, and short decision-rich snippets when they remove ambiguity. Do not use brittle line numbers. Do not prescribe an unnecessary implementation recipe. Preserve the exact technical text when a plain-language rewrite would change its meaning.

## Process

### 1. Gather context

Work from the information already in the conversation. If the user passes a reference, such as a spec path, issue number, or URL, fetch it and read its full body and comments.

Preserve every applicable source requirement in one or more tickets. Track each source requirement so the approval view can show its proposed ticket coverage.

Ask the user about an ambiguity when it can change scope, behavior, dependencies, or acceptance. Ask focused questions before you draft the affected tickets. State a minor assumption in the affected ticket when it does not change the intended outcome.

### 2. Explore the codebase when useful

If the current context does not contain enough codebase information, explore the repository. Use the project's domain glossary vocabulary in ticket titles and descriptions. Respect applicable ADRs.

Look for a prefactoring opportunity that makes the requested behavior easier to implement. Include a prefactoring ticket only when it has a clear outcome and verification path.

### 3. Draft vertical slices

Break the work into **tracer bullet** tickets.

<vertical-slice-rules>

- Each slice cuts a narrow but complete path through every applicable layer. Use a vertical slice, not a horizontal slice of one layer.
- A completed slice is demoable or verifiable on its own.
- Each slice fits in one fresh context window.
- Sequence a necessary prefactoring ticket before the tickets that depend on it.

</vertical-slice-rules>

Give each ticket its **blocking edges** — the other tickets that must complete before it can start. A ticket with no blockers can start immediately.

**Wide refactors are the exception to vertical slicing.** A **wide refactor** is one mechanical change — such as renaming a column or retyping a shared symbol — whose blast radius reaches the whole codebase, so no vertical slice can land green. Sequence it as **expand–contract**. First expand: add the new form beside the old one so nothing breaks. Then migrate call sites in batches sized by blast radius, with each batch blocked by the expand ticket. Finally contract: delete the old form after no caller remains, with the contract ticket blocked by every migration batch. When a migration batch cannot stay green alone, let the batches share an integration branch and block a final integration-and-verification ticket.

### 4. Prepare the approval view

Draft every complete ticket body before the approval step. Keep the approval view compact so it remains usable for plans with ten or more tickets.

Show a numbered breakdown. For each ticket, show only:

1. **Title**: the short descriptive title.
2. **Blocked by**: the tickets that genuinely gate it, or `None — can start immediately`.
3. **Outcome**: the end-to-end behavior that the ticket makes work.
4. **Key acceptance outcomes**: the important observable outcomes from the complete body.

Show a concise coverage summary after the breakdown. Map every source requirement to one or more proposed ticket numbers. Do not use the number of tickets as proof of coverage.

Show complete bodies only when the user asks for them. When the plan has many tickets, show complete bodies in small batches and continue only when the user asks for the next batch.

Ask the user:

- Does the granularity feel right: too coarse or too fine?
- Are the blocking edges correct? Does each ticket depend only on tickets that genuinely gate it?
- Should any ticket be merged or split further?

Iterate until the user approves the breakdown and the coverage summary.

### 5. Publish the approved tickets

Publish only after the user approves the breakdown. Publish one complete ticket per ticket in dependency order, with blockers first. Do not close or modify a parent issue.

**Local files** → write one file per ticket under `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01` in dependency order. Each file's `Blocked by` section lists the numbers and titles it depends on. Use the local ticket template below. Never write a single combined file.

**A real issue tracker (GitHub, Linear, …)** → publish one issue per ticket in dependency order so blocking edges can reference real identifiers. Use the platform's native blocking or sub-issue relationship when it has one. Otherwise, put the blocking issues in the `Blocked by` section. Do not add a label requirement as part of publication.

Work the **frontier**: any ticket whose blockers are all done can start. For a purely linear chain, work from the first ticket to the last ticket.

Every published ticket body carries exactly one branch suggestion using `Branch: <type>/<slug>`. Use one of `feat`, `fix`, `refactor`, `chore`, `docs`, `test`, `perf`, `build`, or `ci` for the type. Write a lowercase slug from letters, digits, and single hyphens. Keep the full branch name at or below 60 characters. The harness falls back to the ticket title when a suggestion is missing or invalid. When a body contains multiple `Branch:` lines, the harness considers only the first. Therefore, write exactly one.

Put `Branch: <type>/<slug>` on the first physical line of every GitHub issue body. Keep the required H1 on the first line of every local ticket file. Put `Branch: <type>/<slug>` on the first line of the local ticket body, immediately after the H1 and its separator. Use `Status: todo` as the initial local tracker status.

### 6. Validate every body before publication

Apply the Ticket body rules and run the completeness and clarity check on every ticket body before you publish it:

- Every applicable source requirement appears in the body or is mapped to another ticket in the coverage summary.
- The body contains the required sections in the order shown below: `Context`, `What to build`, `Requirements and constraints`, `Acceptance criteria`, `Verification`, and `Blocked by`.
- `Edge cases` and `Non-goals` appear only when they contain useful information.
- The body follows the ASD-STE100 writing checklist. It uses explicit subjects, consistent terms, and unambiguous modifiers and pronouns.
- The body uses stable technical references only when they remove ambiguity. It has no brittle line numbers or unnecessary implementation recipes.
- The body has exactly one valid branch suggestion and the correct dependency references.

## Ticket body rules

Make each ticket self-contained for an implementer and a future reviewer. Give each ticket enough context, constraints, observable outcomes, verification evidence, and material edge cases to stand alone. Preserve every applicable source requirement.

Each acceptance criterion is atomic, observable, and independently passable or fail-able. Name applicable positive, negative, boundary, and failure behavior. Do not use umbrella wording such as “works correctly” or “handles edge cases.” Give each acceptance criterion at least one practical verification path in `Verification`.

Describe verification evidence without prescribing the implementation. A practical verification path can use an observed result, a test, a command, a stable interface, a configuration value, or a document inspection when that evidence directly demonstrates the criterion. Use exact technical text when changing it would change the meaning.

Use these required sections in this order:

1. `Context` — explain the current problem, affected actor, relevant source requirement, and constraints.
2. `What to build` — describe the end-to-end behavior from the user's perspective.
3. `Requirements and constraints` — state every applicable requirement, dependency, contract, and technical constraint.
4. `Acceptance criteria` — list the outcomes.
5. `Verification` — give at least one practical evidence path for each acceptance criterion.
6. `Blocked by` — list every ticket that genuinely gates this ticket, or state that it can start immediately.

Add `Edge cases` only when a material boundary or failure case needs its own explanation. Add `Non-goals` only when an explicit scope boundary prevents a likely misinterpretation. Keep both optional sections between `Requirements and constraints` and `Acceptance criteria`.

## Local ticket template

<local-ticket-template>

# <NN> — <Ticket title>

Branch: <type>/<concise-slug>

**Status:** todo

## Context

<The current problem, affected actor, source requirements, and constraints.>

## What to build

<The end-to-end behavior this ticket makes work, from the user's perspective.>

## Requirements and constraints

- <Every applicable requirement and constraint.>

## Edge cases

<Include this section only when it contains useful boundary or failure information.>

## Non-goals

<Include this section only when it contains useful scope boundaries.>

## Acceptance criteria

- [ ] AC1: <A specific outcome.>
- [ ] AC2: <A specific outcome.>

## Verification

- AC1: <Evidence that demonstrates AC1.>
- AC2: <Evidence that demonstrates AC2.>

## Blocked by

- None — can start immediately.

</local-ticket-template>

Omit the optional `Edge cases` and `Non-goals` sections when their placeholders would contain no useful information.

## GitHub issue template

<issue-template>

Branch: <type>/<concise-slug>

<!-- Include this section only when the source is an existing issue. Keep it after Branch. -->
## Parent

<A reference to the parent issue.>

## Context

<The current problem, affected actor, source requirements, and constraints.>

## What to build

<The end-to-end behavior this ticket makes work, from the user's perspective.>

## Requirements and constraints

- <Every applicable requirement and constraint.>

## Edge cases

<Include this section only when it contains useful boundary or failure information.>

## Non-goals

<Include this section only when it contains useful scope boundaries.>

## Acceptance criteria

- [ ] AC1: <A specific outcome.>
- [ ] AC2: <A specific outcome.>

## Verification

- AC1: <Evidence that demonstrates AC1.>
- AC2: <Evidence that demonstrates AC2.>

## Blocked by

- None — can start immediately.

</issue-template>

Omit the optional `Parent`, `Edge cases`, and `Non-goals` sections when they do not apply or contain useful information. The `Branch:` line remains the first physical line of the published GitHub issue body.
