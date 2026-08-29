# syl

`syl` sets up and runs an agentic coding workflow. The workflow has three
Roles: plan, implement, and review. Each Role uses a Harness — an agent CLI
such as Claude Code or Codex. `syl` drives these Harnesses from one
command-line tool.

Named after Sylphrena, a spren from the Stormlight Archive who bonds with
someone and amplifies what they can do — which is what this harness does for
coding agents.

## Table of contents

- [Language](#language)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Usage guide](#usage-guide)
  - [1. Initialize a project](#1-initialize-a-project)
  - [2. Configure the workflow](#2-configure-the-workflow)
  - [3. Plan work](#3-plan-work)
  - [4. Implement an issue](#4-implement-an-issue)
  - [5. Review working-tree changes](#5-review-working-tree-changes)
  - [Answer a QUESTION from a harness](#answer-a-question-from-a-harness)
- [Configuration reference](#configuration-reference)
- [Run artifacts](#run-artifacts)
- [Caveats and troubleshooting](#caveats-and-troubleshooting)
- [Development](#development)

## Language

This section gives the words to use when you talk about `syl`. Use these
words. Do not use other words for the same ideas.

**Harness**
An agent CLI that `syl` drives. Example: Claude Code, Codex.

**Role**
A stage of the workflow. There are three Roles: plan, implement, review. Each
Role uses one Harness, one model, and one effort level.

**Skill set**
The set of skills stored in this repository. `syl init` installs the Skill
set into a project.

**Tracker**
The place where tickets live for a project. A Tracker is GitHub or local
markdown files. The Issues Tracker and the Review log use their own,
independent setting.

**Review log**
The place where review documents live for a project. A review document holds
a Verdict and its findings. The Review log is the Tracker or local files
under `.scratch/`.

**Verdict**
The structured result that the reviewer Role returns for one iteration. A
Verdict is `approve` or `revise`. An `approve` Verdict means the issue can
close. A `revise` Verdict carries findings for the next iteration.

**Worktree**
A dedicated git checkout that `syl implement --worktree` creates for one
issue, so the implement/review loop runs without touching the checkout you
are already in.

**Origin root**
The directory holding the project's config, run artifacts, and local
Tracker. Always the checkout you invoked `syl` from, regardless of where a
run executes.

**Work root**
The directory where the git runner and Harnesses operate for the current
run. Equal to the origin root unless `--worktree` is set, in which case it
is the worktree path.

## Installation

Install the latest prebuilt binary on Linux or macOS. This method does not
need Go.

```sh
curl -fsSL https://raw.githubusercontent.com/igorrochap/syl/main/scripts/install.sh | sh
```

The installer checks the release checksum. It writes `syl` to `~/.local/bin`
by default. Use `--dir` and `--version` to pick another directory or another
release:

```sh
curl -fsSL https://raw.githubusercontent.com/igorrochap/syl/main/scripts/install.sh \
  | sh -s -- --dir /usr/local/bin --version v1.2.3
```

Add the selected directory to `PATH`.

To update an installed binary in place, run:

```sh
syl update
```

The command checks GitHub for a newer release, verifies the platform archive
against its checksums, and leaves the existing binary untouched if the
download or verification fails.

To build from source, install Go 1.23 or later. Run this command from the
repository root:

```sh
go build -o syl ./cmd/syl
```

You can also run `syl` without a binary:

```sh
go run ./cmd/syl --help
```

## Quick start

This example sets up a project and implements one issue.

```sh
# Scaffold the project. syl asks questions about Trackers, Roles, and skills.
syl init

# Implement issue #42. syl runs implement, then review, in a loop.
syl implement 42
```

`syl implement` stops when the reviewer Role returns an `approve` Verdict, or
when the loop reaches the configured iteration limit. Read the transcript in
your terminal, or read the saved [run artifacts](#run-artifacts) after the
command exits.

## Usage guide

### 1. Initialize a project

Run `syl init` from the project root:

```sh
syl init
```

`syl init` asks these questions:

- Which optional skills to install, in addition to the core Skill set.
- Which Tracker to use for issues: `github` or `local`.
- Which Tracker to use for the review log: `github` or `local`.
- Which Harness, model, and effort level to use for each Role: plan,
  implement, review.

`syl init` then:

- Writes the project configuration to `.syl/config.toml`.
- Installs the Skill set into the project.
- Links a Claude Code integration, when Claude Code is a configured Harness.
- Adds `.syl/runs/` to `.gitignore`.
- Writes `scripts/quality.sh` with the seven language-neutral quality gates.

`syl init` shows every planned change and asks for confirmation before it
writes anything. Run `syl init` again to review or add optional skills; it
never overwrites a file that already holds your changes.

`syl init` also writes `skills-lock.json`, recording the vendored content hash
for each installed skill. Run `syl sync` to review drift against the current
vendored Skill set. `syl sync --dry-run` only prints the report; `syl sync --all`
applies safe updates automatically while still requiring explicit
confirmation for locally modified skills. Optional skills are never installed
by `sync`.

The generated `scripts/quality.sh` is the project's quality-gate entry point.
Run it with no argument to run every gate, or pass a gate name to run one; use
`--list` to see the names. Each gate starts as `SKIP  not configured`. Fill a
gate by replacing its stub with the project's command: exit 0 for a pass and a
non-zero exit for a failure. `syl init` leaves an existing quality script
untouched, and `syl sync` does not manage it.

The `style` and `complexity` gates compare the branch with a base. Set
`QUALITY_BASE_REF` to choose that base explicitly, which is useful for a
stacked branch on a local machine. The base selection order is
`QUALITY_BASE_REF`, then `GITHUB_BASE_REF` (trying `origin/<name>` and
`<name>`), then `origin/main` and `main`. A set but invalid base stops the gate
instead of falling back. Each diff gate reports the selected base and changed
file count; both its file list and its analysis use the merge base.

### 2. Configure the workflow

`syl` reads `.syl/config.toml`. A generated file looks like this:

```toml
# Generated by syl init.
[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-opus-5"
effort = "high"
# mcp = true inherits user/project MCP configuration; false strips it for Claude.
# Codex ignores this field. Omitted defaults are true for plan and implement, and false for review.
mcp = true

[roles.implement]
harness = "codex"
model = "gpt-5.6-luna"
effort = "xhigh"
# mcp = true inherits user/project MCP configuration; false strips it for Claude.
# Codex ignores this field. Omitted defaults are true for plan and implement, and false for review.
mcp = true

[roles.review]
harness = "claude"
model = "claude-sonnet-5"
effort = "medium"
# mcp = true inherits user/project MCP configuration; false strips it for Claude.
# Codex ignores this field. Omitted defaults are true for plan and implement, and false for review.
# Hooks that require MCP may cause one blocked-then-retried tool call in lean sessions.
mcp = false

[loop]
max_iterations = 3

[notifications]
enabled = true

[worktree]
root = "~/.syl/worktrees"
# A shell command used to provision dependencies in a fresh worktree.
# Example: setup = "npm ci"
setup = ""
# Additional origin paths to copy into a fresh worktree.
# Example: copy = [".env", ".env.local"]
copy = []
```

Edit this file directly to change Trackers, Roles, the iteration limit,
notifications, the centralized worktree root, the dependency setup command, or
the copied artifact paths. `worktree.copy` is for ignored, non-reproducible
artifacts that the setup hook cannot regenerate. Copying `.env` moves its
secrets into `~/.syl/worktrees/...`, a location you may not expect. Do not use
it for derivable dependency directories such as `node_modules`, `vendor`, or
`.venv`; let `worktree.setup` create those for the worktree. `syl` rejects a
config file that has an unknown key, so remove a setting instead of leaving a
stale one behind.

### 3. Plan work

```sh
syl plan "add offline mode"
```

`syl plan` opens the configured planner Role in an interactive Harness session.
The planner produces tickets on the configured Issues Tracker, and `syl` reports
which tickets were created when the session exits. Use the flags to change the
planning sequence:

- `--spec` publishes a spec before producing tickets.
- `--grill` grills the topic before producing tickets.
- `--grill --with-docs` uses the docs-grounded grilling variant.

`--with-docs` requires `--grill`. Planning always uses an attached interactive session.

### 4. Implement an issue

```sh
syl implement 42
```

Use `--worktree` to run the loop in a dedicated checkout while leaving the
current checkout untouched. A fresh git worktree starts with only the tracked
tree, so ignored dependencies such as `node_modules`, `vendor`, `.venv`,
`target`, and `dist` are absent. Set `[worktree] setup = "npm ci"` (or the
project's equivalent shell command) to provision those dependencies before the
Harness starts. For non-reproducible artifacts, set `[worktree] copy = [".env",
".env.local"]`; each path is copied from the origin checkout and must not land
untracked and unignored. Copying `.env` places secrets in
`~/.syl/worktrees/...`, which may be an unexpected location. Do not copy
derivable dependency directories such as `node_modules`, `vendor`, or `.venv`;
the setup hook owns those. v1 provisions the checkout; the setup hook provisions
its dependencies. The hook and copy list run only for `--worktree`; a bare `syl
implement` uses the dependencies and artifacts already present in the origin
checkout. Add `--base <ref>` to choose the ref the worktree branch starts from.
The worktree is retained for review; the final summary prints the command to
remove it after the changes are committed or abandoned.

`syl implement`:

1. Resolves issue `#42` through the Issues Tracker.
2. Creates a branch for the issue.
3. Runs the implement Role, then the review Role, for one iteration.
4. Repeats step 3 until the reviewer returns an `approve` Verdict, blocking
   findings run out, or the loop reaches `loop.max_iterations`.
5. Prints a summary: iteration count, final Verdict, remaining nits, and a
   diff stat.

The legacy `syl implement '#42'` form remains supported.

### 5. Review working-tree changes

```sh
syl review
```

`syl review` runs the reviewer Role once, against the current working-tree
changes, and prints the Verdict. Pass an issue reference to log the review
against that issue:

```sh
syl review 42
```

When the Review log is `github`, an issue reference is required; `syl`
cannot log a review with no place to log it.

For implement and review Roles, `syl` runs Claude Code in a terminal session
and reads complete assistant messages from the session transcript. `syl`
refuses to start these Roles from inside another Claude Code session because
Claude Code does not persist the nested transcript that `syl` needs.

Add `--raw` to pass the Harness output through untouched, instead of the
parsed Verdict view:

```sh
syl review 42 --raw
```

### 6. Inspect run usage

```sh
syl usage
syl usage 20260817T101500.000000000Z-42
```

With no argument, `syl usage` reads the latest run. The named form reads a
specific directory under `.syl/runs/`. Claude and Codex usage is recorded per
role and iteration; Codex lines show raw input/output totals with cached-input
and reasoning-output shares. The raw `usage.json` artifact is available for
direct querying as well.

### Answer a QUESTION from a harness

A Harness can pause mid-run and print a `QUESTION`. When it does, `syl`
prints the question and waits for your answer on stdin. Type your answer,
then submit it with an empty line or end-of-file (Ctrl-D).

## Configuration reference

| Key | Values | Meaning |
| --- | --- | --- |
| `tracker.issues` | `github`, `local` | Where issues live. |
| `tracker.reviews` | `github`, `local` | Where review documents live. |
| `roles.<role>.harness` | `claude`, `codex`, `opencode` | Harness for the Role. |
| `roles.<role>.model` | any string | Model identifier passed to the Harness. |
| `roles.<role>.effort` | `low`, `medium`, `high`, `xhigh` | Effort level passed to the Harness. |
| `roles.<role>.mcp` | `true`, `false` | Whether Claude sessions inherit user/project MCP configuration. Defaults to `true` for plan and implement, `false` for review. Codex ignores it. |
| `loop.max_iterations` | integer | Cap on implement/review iterations for `syl implement`. |
| `notifications.enabled` | `true`, `false` | Send a desktop or terminal-bell notification on completion or on a `QUESTION`. |
| `worktree.root` | path | Central root for implementation worktrees. Defaults to `~/.syl/worktrees`. |
| `worktree.setup` | shell command string | Run after a fresh `--worktree` checkout is provisioned, with that worktree as cwd, to regenerate its dependencies. Omitted or empty means no setup step. |
| `worktree.copy` | list of paths | Copy these additional paths from the origin checkout into a fresh `--worktree` checkout. Use for ignored, non-reproducible artifacts; untracked-and-unignored copies are refused. Do not use for derivable dependency directories such as `node_modules`, `vendor`, or `.venv`. |

`<role>` is `plan`, `implement`, or `review`.

`opencode` is a recognized Harness value in the configuration, but `syl` does
not yet drive it; select `claude` or `codex` for `roles.implement.harness`
and `roles.review.harness` today.

## Run artifacts

Each `syl implement` run writes its artifacts under
`.syl/runs/<timestamp>-<issue-number>/`:

```text
.syl/runs/20260817T101500.000000000Z-42/
  metadata.txt                     # branch name and branch point
  sessions.txt                     # harness session IDs per iteration
  iteration-01-implement.feed      # implement role, parsed event feed
  iteration-01-implement.transcript
  iteration-01-review.feed         # review role, parsed event feed
  iteration-01-review.transcript
  iteration-01-verdict.txt         # review role, formatted verdict
  usage.json                       # per-role token usage and tracking status
  summary.txt                      # iteration count, final verdict, nits, diff stat
```

`syl init` adds `.syl/runs/` to `.gitignore`. Keep an artifact directory when
you need to audit a run; delete it when you do not.

Artifacts always land in the origin repository's `.syl/runs/`, even for a
run that executed in a worktree with `--worktree`. Run history survives the
worktree being removed.

## Caveats and troubleshooting

- **`syl implement` needs a clean starting point.** It checks that `HEAD`
  does not move underneath it during a run; do not run another command that
  changes `HEAD` in the same working tree while `syl implement` runs.
- **Hash-prefixed issue references need quotes.** Prefer `42`. The legacy
  `#42` form remains supported as `'#42'` or `"#42"`.
- **`syl init` refuses to replace unmanaged files.** It links a Claude Code
  integration only when the existing path is absent or is already a symlink
  `syl` manages; a real directory or file at that path stops `syl init` with
  an error instead of overwriting it.
- **Unknown configuration keys fail the load.** Remove an obsolete key from
  `.syl/config.toml` rather than leaving it in place.
- **A `--worktree` run leaves its changes in the worktree, not your
  checkout.** Uncommitted changes from the implement/review loop stay in
  the worktree directory; look there, not in the checkout you ran `syl`
  from, to inspect or commit them.
- **A fresh worktree contains only the tracked tree, plus configured copies.**
  Ignored build artifacts and dependency directories are not copied. Configure
  `[worktree] setup` to regenerate derivable dependencies, and
  `[worktree] copy = [".env"]` for non-reproducible ignored artifacts that the
  hook cannot recreate. Copying `.env` moves secrets into
  `~/.syl/worktrees/...`, a location you may not expect; do not configure
  dependency directories such as `node_modules`, `vendor`, or `.venv`.
- **`syl` never removes worktrees automatically.** A worktree created by
  `--worktree` is retained after the run finishes, even after the changes
  are committed or abandoned. Run the removal command the final summary
  prints, or worktrees accumulate under `worktree.root` indefinitely.

## Development

Run the local checks with:

```sh
gofmt -w $(git ls-files '*.go')
go vet ./...
go test ./...
go test -race ./...
scripts/install_test.sh
```

Pull requests run formatting, ShellCheck, module-file, vet, installer, test,
and race-detector checks. Pushes to `main` run those checks and then build
release archives for Linux and macOS on amd64 and arm64.

Successful `main` builds use Conventional Commit messages to create semantic
GitHub releases. Each release contains a checksummed archive for every
supported platform, which the installer consumes.
