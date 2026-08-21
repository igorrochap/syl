# Agentic Workflow Orchestrator

A CLI tool that sets up and runs an agentic coding workflow (plan → implement → review) across projects, driving multiple agent CLIs from one place.

## Language

**Harness**:
An agent CLI the tool drives (Claude Code, Codex, OpenCode).
_Avoid_: agent tool, model runner, AI CLI

**Role**:
A stage of the workflow — planner, implementer, reviewer — each bound to a harness, a model, and an effort level.
_Avoid_: stage, step, phase

**Skill set**:
The canonical collection of skills vendored in this repo and installed into a project by `init`.
_Avoid_: prompts, commands

**Tracker**:
Where tickets live for a project — GitHub or local markdown files. Configured independently from the review log: a project can track issues on GitHub while keeping review documents local.
_Avoid_: issue system, backlog

**Review log**:
Where a project's review documents (verdicts and findings) are recorded — the tracker or local `.scratch/` files.
_Avoid_: review history, review docs

**Verdict**:
The reviewer's structured, machine-readable outcome for a loop iteration: approve (issue can be closed) or revise (with findings).
_Avoid_: review result, approval

**Session transcript**:
The durable record a harness writes for one session, and the source of truth for what a role did — what it said, which tools it used, and what it consumed.
_Avoid_: log, session file, history
