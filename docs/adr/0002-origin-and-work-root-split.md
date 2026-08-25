# Split the origin root from the work root

Worktree support means a `syl implement` run can execute in a different
directory than the one holding the project's config, tickets, and run
history. `syl` models this as two roots rather than one: the **origin root**
(where `.syl/config.toml`, run artifacts, and the local Tracker live) and the
**work root** (where the git runner and the Harness adapters operate). Every
collaborator `App` constructs is bound to one root or the other, and a
command reads or writes through whichever root owns that concern — never the
one that happens to be current.

## Why this is not the obvious choice

A reader who sees `syl implement` accept `--worktree` might expect the
simplest implementation: `os.Chdir` into the worktree for the duration of the
run, or have the process re-exec itself from the new directory. Both keep a
single root and avoid threading two paths through every collaborator
constructor. `syl` does neither.

## Considered options

- **`chdir` into the worktree.** Changing the process's working directory is
  global, mutable state — every goroutine and every library call sees it,
  including ones that have nothing to do with the worktree (config loading,
  Tracker access). It also does not survive concurrent operations or make it
  obvious at a call site which root a path is relative to.
- **Re-exec `syl` from inside the worktree.** This avoids global mutable
  state but starts a second process, doubling startup cost and complicating
  error propagation and output streaming back to the original terminal.
- **An interface method on the Harness adapter to swap roots.** This keeps a
  single root at the `App` level but pushes the split down into every
  adapter implementation, and still leaves config, run artifacts, and the
  Tracker with no principled home when the Harness root and the config root
  differ.

`syl` instead resolves both roots once, at `App` construction, and passes
the one each collaborator needs explicitly: config, run artifacts, and the
local Tracker are always constructed against the origin root; the git
runner and Harness adapters are constructed against whichever root the
current run is using (the origin root itself, unless `--worktree` is set).
Anyone adding a command or a collaborator has to decide which root it needs
and ask for that one by name — there is no ambient "current directory" to
get wrong.

## Consequences

Run artifacts stay in the origin root regardless of where the run executed,
so `.syl/runs/` history survives the worktree being removed later.

Agent configuration (`.claude`, `.agents`, `CLAUDE.md`) is copied into the
worktree rather than symlinked back to the origin checkout. A symlink would
let the Harness write through it into the user's original checkout, which
defeats the isolation `--worktree` exists to provide.
