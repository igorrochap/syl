---
name: code-review
description: Review the changes since a fixed point (commit, branch, tag, or merge-base) along two axes — Standards (repo conventions) and Spec (does it match the originating issue/PRD). Use when the user wants to review a branch, a PR, work-in-progress changes, or asks to "review since X".
---

Two-axis review of the diff between `HEAD` and a fixed point the user supplies:

- **Standards** — does the code conform to this repo's documented coding standards?
- **Spec** — does the code faithfully implement the originating issue / PRD / spec?

Both axes run as **parallel sub-agents** so they don't pollute each other's context, then this skill aggregates their findings.

The project's issue tracker should have been provided to you, including how it tracks reviews specifically (GitHub PR/issue comments, or a local `.scratch/` log) — this determines how you document findings in the Documenting section below. If these details are missing, ask the user before proceeding.

## Process

### 1. Pin the fixed point

Whatever the user said is the fixed point — a commit SHA, branch name, tag, `main`, `HEAD~5`, etc. If they didn't specify one, ask for it.

Capture the inputs once, using the minimum metadata calls needed to hand accurate inputs to the sub-agents:

- Resolve the ref with `git rev-parse <fixed-point>`.
- Record the full diff command, `git diff <fixed-point>...HEAD` (three-dot, so the comparison is against the merge-base), for the sub-agents to run.
- Record the commit subjects with `git log <fixed-point>..HEAD --oneline`.
- Verify that the diff is non-empty with `git diff <fixed-point>...HEAD --stat`.

The coordinator needs the resolved ref, command, commit list, and diff stat. A bad ref or empty diff should fail here — not inside two parallel sub-agents. Keep the full diff for the sub-agents to inspect.

### 2. Identify the spec source

Look for the originating spec, in this order:

1. Issue references in the commit messages (`#123`, `Closes #45`, GitLab `!67`, etc.) — fetch via the workflow in `docs/agents/issue-tracker.md`.
2. A path the user passed as an argument.
3. A PRD/spec file under `docs/`, `specs/`, or `.scratch/` matching the branch name or feature.
4. If nothing is found, ask the user where the spec is. If they say there isn't one, the **Spec** sub-agent will skip and report "no spec available".

Identify the source with commit subjects, user-provided references, and file paths. Pass the issue/PR reference or spec path directly to the Spec sub-agent; if no stable reference exists, forward the retrieved spec body as unexamined prompt input. Keep the spec as sub-agent input rather than performing the requirements analysis in the coordinator.

### 3. Identify the standards sources

Use the minimum listing/search calls needed to collect paths to anything in the repo that documents how code should be written, such as `CODING_STANDARDS.md` or `CONTRIBUTING.md`. Pass those paths to the Standards sub-agent; the coordinator needs the path list, not the file contents.

On top of whatever the repo documents, the Standards axis always carries the **smell baseline** — Fowler's code smells (_Refactoring_, ch.3), cataloged in [`skills/refactoring/references/smells.md`](../refactoring/references/smells.md), applying even when a repo documents nothing. Two rules bind it:

- **The repo overrides.** A documented repo standard always wins; where it endorses something the baseline would flag, suppress the smell.
- **Always a judgement call.** Each smell is a labelled heuristic ("possible Feature Envy"), never a hard violation — and, like any standard here, skip anything tooling already enforces.

### 4. Spawn both sub-agents in parallel

The coordinator's analysis ends at this handoff. Send a single message with two `Agent` tool calls. Use the `general-purpose` subagent for both. Put the fixed-point command, commit list, spec reference or input, and standards-source paths into the prompts so the sub-agents can do the substantive review with their own context.

**Standards sub-agent prompt** — include:

- The full diff command and commit list.
- The list of standards-source files you found in step 3, plus the path `skills/refactoring/references/smells.md` — tell the sub-agent to read it for the smell baseline (it has repo file access).
- The brief: "Report — per file/hunk where relevant — (a) every place the diff violates a documented standard: cite the standard (file + the rule); and (b) any baseline smell from `skills/refactoring/references/smells.md` you spot: name it and quote the hunk. Distinguish hard violations from judgement calls — documented-standard breaches can be hard, but baseline smells are always judgement calls, and a documented repo standard overrides the baseline. Skip anything tooling enforces. Under 400 words."

**Spec sub-agent prompt** — include:

- The diff command and commit list.
- The issue/PR reference or spec path from step 2; if step 2 retrieved an external body, pass it through as prompt input without analyzing it.
- The brief: "Report: (a) requirements the spec asked for that are missing or partial; (b) behaviour in the diff that wasn't asked for (scope creep); (c) requirements that look implemented but where the implementation looks wrong. Quote the spec line for each finding. Under 400 words."

If the spec is missing, skip the Spec sub-agent and note this in the final report.

#### Coordinator boundary after the handoff

Once the sub-agents are spawned, the coordinator's work is limited to collecting their reports, aggregating them, and — when applicable — documenting the review. The coordinator must not read the diff, standards sources, smell catalog, or any source files, and must not run builds or tests. Its only tool use after spawning is what is required to collect sub-agent results and produce or post the review output.

### 5. Aggregate

Present the two reports under `## Standards` and `## Spec` headings, verbatim or lightly cleaned. Do **not** merge or rerank findings — the two axes are deliberately separate (see _Why two axes_).

End with a one-line summary: total findings per axis, and the worst issue _within each axis_ (if any). Don't pick a single winner across axes — that's the reranking the separation exists to prevent.

## Why two axes

A change can pass one axis and fail the other:

- Code that follows every standard but implements the wrong thing → **Standards pass, Spec fail.**
- Code that does exactly what the issue asked but breaks the project's conventions → **Spec pass, Standards fail.**

Reporting them separately stops one axis from masking the other.

## Documenting

**Tool-driven mode.** When the invoking prompt explicitly says that the tool records the verdict, skip the rest of this section. Do not read prior review documents or write a new review document; the verdict block is the only record. Choose this mode only from that prompt instruction, never from an inference about who invoked the skill.

If this project tracks reviews on GitHub, the verdict block you print at the
end (per the Verdict contract below) is the record — it's posted as a PR/issue
comment by the tool that invoked you. Skip the rest of this section entirely;
do not also write a `.scratch` review document.

Otherwise (local review tracking): do not write review findings into the issue
file itself — the issue file is the spec/checklist, not a review log. Instead
create a **separate review document** in the project's
`.scratch/<project>/reviews/` directory (sibling to `.scratch/<project>/issues/`),
named after the issue: `<number>-<slug>.md`, matching the issue's own filename.

Follow the format already established by existing review documents in that
directory (e.g. `reviews/12-add-optional-end-to-end-benchmark-verification.md`,
`reviews/15-extract-and-correlate-baseline-schemathesis-problems.md`):

- `# Review: <number> — <title>` heading.
- A line naming what was reviewed: the diff/commit range and files touched.
- `## Standards` and `## Spec` sections (this skill's two axes), each stating
  the problem and the suggested fix for every finding, quoting the relevant
  hunk or spec line.
- A closing `## Verdict` stating whether the issue can be closed, or what must
  be fixed first, per the Notes section below.

If a review document for this issue already exists (a prior round), add a new
`## Round N` section rather than overwriting prior rounds — see the
issue-15 review for the multi-round convention.

## Notes

If non valuable findings are found, tell the user the issue should be close and the user should move on to the next issue.

Valuable findings are:
- Spec not fully implemented or implemented wrong
- Medium or higher risk of bugs
- Codebase's standards violation and/or /refactoring smells found.
- Bad code (hard to read, hard to maintaing, etc.), like: really long functions, bad naming, etc. For a catalog, use /refactoring

## Verdict contract

Every review MUST end with exactly this block. Replace the placeholders with the review's values; do not put any text after the block:

```text
VERDICT: approve | revise
SUMMARY: <one line>
FINDINGS:
- [blocking] <file:line> — <issue>
- [nit] <file:line> — <issue>
```

`approve` means the issue can be closed. Every finding must be tagged either `[blocking]` or `[nit]`. A review with zero findings still emits the block, with an empty `FINDINGS` list and no finding lines.
