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
Where tickets live for a project — GitHub, GitLab, or local markdown files. Configured independently from the review log: a project can track issues on GitHub or GitLab while keeping review documents local.
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

**Worktree**:
A dedicated git checkout that `syl implement --worktree` creates for one issue, so the implement/review loop runs without touching the checkout the user is already in.
_Avoid_: sandbox, workspace

**Origin root**:
The directory holding the project's config, run artifacts, and local Tracker. Always the checkout the user invoked `syl` from, regardless of where a run executes.
_Avoid_: home directory, project root

**Work root**:
The directory where the git runner and Harness adapters operate for the current run. Equal to the origin root unless `--worktree` is set, in which case it is the worktree path.
_Avoid_: working directory, execution root

**Syl home**:
The user-level directory holding state that spans Projects — the Project registry and Live-run markers. Defaults to `~/.syl`.
_Avoid_: global dir, user root, global .syl

**Project**:
A directory initialized for syl — an origin root holding a valid `.syl/config.toml` — identified by its absolute, symlink-resolved path.
_Avoid_: repo, workspace

**Project registry**:
The user-level list of Projects syl has been used in. A Project joins it the first time syl successfully loads its config (or `init` completes); the registry records only where Projects are, never what they're configured with.
_Avoid_: project index, catalog

**Project health**:
Whether a registered Project is usable right now: ok, missing (directory gone), uninitialized (no config), or invalid (config fails to load). Always derived when observed; never removes a Project from the registry — only forgetting does.
_Avoid_: project status, state

**Run**:
One recorded execution of `syl implement` or a standalone `syl review` for a project, with its artifacts kept under the origin root. `syl plan` sessions are not Runs.
_Avoid_: job, execution, session

**Run status**:
The lifecycle outcome a Run records for itself: running, approved, exhausted (max iterations reached on a revise verdict), failed, or cancelled.
_Avoid_: result, state

**Activity**:
What a running Run is doing right now: preparing, implementing, reviewing, or awaiting-answer. Distinct from Role — preparing and awaiting-answer belong to no Role.
_Avoid_: stage, step, phase

**Live-run marker**:
A user-level pointer a Run creates when it starts and removes when it ends, so what is running right now can be found without walking any Project's history. Points to the Run; holds none of its state. A marker whose Run is Interrupted is an orphan, and dismissing it never touches the Run.
_Avoid_: lock, active state, index

**Interrupted**:
A Run whose status is still running but whose process is gone. Always derived when observed, never recorded.
_Avoid_: crashed, stale, dead

**resume**:
Re-enter an existing Harness session, keeping its history.

**attach**:
Start a fresh interactive Harness session. `resume` re-enters an existing session; `attach` starts a fresh one.

**Rollover**:
When an implementer session, judging itself low on context, writes a handoff document and the loop continues the next iteration in a fresh session seeded from it. syl signals the target path via `/handoff <path>` and detects the rollover by that file's existence; the seed supplements — never replaces — the ticket and blocking findings syl already supplies.
_Avoid_: restart, reset, compaction-recovery

**Handoff document**:
The compaction summary a Rollover produces — decisions made, files explored, and approach — written to the path syl designates (in run artifacts) or the OS temp directory when none is given. References other artifacts by path rather than duplicating them.
_Avoid_: summary, context dump
