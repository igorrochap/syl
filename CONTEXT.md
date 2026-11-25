# Agentic Workflow Orchestrator

A CLI tool that sets up and runs an agentic coding workflow (plan → implement → review) across projects, driving multiple agent CLIs from one place.

## Language

**Harness**:
An agent CLI the tool drives (Claude Code, Codex, OpenCode).
_Avoid_: agent tool, model runner, AI CLI

**Role**:
A stage of the workflow — planner, implementer, reviewer — each bound to a harness, a model, and an effort level.
_Avoid_: stage, step, phase

**Sandbox mode**:
The command-execution policy a Role's Codex sessions run under: `full-access`, `workspace-write`, or `read-only`. Not a Worktree.

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

**Nit budget**:
The maximum number of nit findings an approve Verdict may carry while still ending a Run as approved. An approve Verdict over the budget sends its findings into another iteration.

**Gate**:
One automated check a change must pass (format, vet, style, complexity, coverage, tests, architecture), run through the project's single entry point. syl runs the Gates itself after each implementer turn, out of the agent's reach; a red Gate sends the work back to the implementer before any review.
_Avoid_: CI, checks, quality check

**Gate attempt**:
One run of the Gates within an iteration. Counted separately from iterations — an iteration is one implementer turn plus one reviewer turn, and a red Gate consumes no iteration. The count resets when the Gates go green.
_Avoid_: retry, gate iteration

**Evidence**:
What the implementer records after exercising its change in the running software — what it exercised, how, what it observed, and the outcome — plus supporting files such as screenshots and logs. Kept per iteration in the Run record, never committed to the branch, and judged by the reviewer alongside the diff. Only images marked PR-worthy leave the Run record.
_Avoid_: proof, test results, artifacts

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

**Run record**:
The durable contents and file format of one Run directory under `<origin root>/.syl/runs/`, including its metadata, sessions, artifacts, summary, verdicts, handoff documents, and run state.
_Avoid_: run files, run folder format

**Run status**:
The lifecycle outcome a Run records for itself: running, approved, exhausted (a loop limit was reached — max iterations on a revise verdict, or max Gate attempts on a red Gate; the Run record names which), failed, or cancelled.
_Avoid_: result, state

**Activity**:
What a running Run is doing right now: preparing, implementing, gating, reviewing, awaiting-answer, committing, pushing, or publishing. When an Activity span is open and no question is pending, a Run's current Activity equals the Activity of its innermost open span; while a question is pending, the Activity is awaiting-answer and the Role span remains open. Distinct from Role — preparing, gating, awaiting-answer, committing, pushing, and publishing belong to no Role.
_Avoid_: stage, step, phase

**Activity span**:
One recorded, timed occurrence of an Activity in a Run. An Activity span can have child spans.
_Avoid_: progress line, activity entry

**Activity log**:
The append-only file of Activity spans in a Run record.
_Avoid_: activity transcript

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
At every iteration boundary where the loop continues, syl resumes the implementer session of the ending iteration for one handoff turn that invokes `/handoff <path>`, and the next iteration starts a fresh session seeded from the handoff document. syl detects the Rollover by that file's existence; without it, the next iteration resumes the old session. Gate attempt turns never roll over. The seed supplements — never replaces — the ticket and all carried-over findings syl already supplies.
_Avoid_: restart, reset, compaction-recovery

**Handoff document**:
The compaction summary a Rollover produces — decisions made, files explored, and approach — written to the path syl designates (in run artifacts) or the OS temp directory when none is given. References other artifacts by path rather than duplicating them.
_Avoid_: summary, context dump
