# Track live Runs with per-Run markers, and derive liveness instead of storing it

`syl ui` must answer "what is running right now, across every Project?" without
walking every Project's `.syl/runs/` history. Each Run therefore creates a
**Live-run marker** — one small pointer file in `<syl home>/active/` — when it
starts, and removes it when it ends. The marker only points at the Run; the Run's
status, activity, and iteration live solely in the Run's own run state file.
Whether a Run is actually alive is never stored anywhere: a Run whose recorded
status is still running but whose pid is gone is **Interrupted**, and that is
computed each time it is observed. Project health (missing, uninitialized,
invalid) is derived the same way.

## Why this shape and not the obvious one

The obvious design is a single `state.json` — globally, per Project, or both —
listing the running Projects and their current Runs, or a `running: true` flag
in the Project registry. Two properties of syl rule that out:

- **Many concurrent writers.** `--worktree` lets several `syl implement` Runs
  proceed in the same Project at once, and more run across Projects. A shared
  file would need a read-modify-write under a cross-platform file lock on every
  activity transition; without the lock, updates are silently lost. A marker
  has exactly one writer — its own Run — and create/remove are atomic.
- **Crashes leave stale truth.** A `kill -9`'d Run never flips a flag or removes
  an entry, so any stored "running" must be re-validated against the pid anyway.
  Once that check exists, the stored bit is a second source of truth that can
  disagree with the first. Deriving liveness keeps the run state file the only
  source of truth; the marker is merely an index into it.

## Considered options

- **Scan every Project's `.syl/runs/` on each poll** — works (finished Runs are
  immutable and can be memoized), but cost grows with all history ever recorded
  rather than with what is live.
- **Shared global/per-Project state file** — rejected for the reasons above.
- **Heartbeat timestamps** — rejected: needs a background writer and still
  misreads long Harness turns as stale.

## Consequences

Orphaned markers from crashed Runs persist until dismissed in the UI; dismissing
deletes only the marker, never the Run, which stays Interrupted in its Project's
history. Runs recorded before run state files existed have no marker and no
status; they read as completed when `summary.txt` exists and unknown otherwise.
