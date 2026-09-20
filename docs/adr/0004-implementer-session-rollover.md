# Persist the implementer session across iterations, with agent-driven context rollover

The implementer now resumes its Harness session across loop iterations, the same
way the reviewer already does, instead of starting a fresh `adapter.Run` each
iteration. This removes the per-iteration cost of reloading every skill and rule
and — more importantly — preserves the decisions the implementer made and the
files it read. The one risk this introduces, context-window compaction over a
long loop, is handled by a **Rollover**: at the end of a turn the implementer
self-assesses and, if it judges itself low on context, invokes `/handoff <path>`
to write a handoff document; the next iteration then starts fresh, seeded from
that document.

## Why this shape and not the obvious one

The obvious design — the one originally proposed — was for syl to *measure* the
implementer's context window between iterations and trigger a handoff when it
crossed a threshold. syl cannot do that cheaply or accurately: Claude is driven
through a pty (see ADR 0001), `harness.Event` carries no token or context fields,
and nothing records a model's context-window limit. Any orchestrator-side measure
would require a new per-turn transcript re-read plus a model→limit table, and would
still lag a turn behind. The implementer, by contrast, knows its own context state
natively. So the trigger is **agent-driven**, decided at **end of turn** by the
session that actually holds the context.

## Considered options

- **Orchestrator-measured trigger** (the original proposal) — rejected: no live
  signal exists; see above.
- **A numeric syl-side backstop** (static model→limit table + post-turn occupancy
  check that forces a rollover the agent missed) — deferred, not rejected. It is a
  ticketed fast-follow if coarse self-assessment proves unreliable.
- **Config flag to opt out of persistence** — rejected: the reviewer resumes
  unconditionally with no flag; the implementer matches it for symmetry and zero
  config surface.

## Key decisions

- **Signal = file existence, not output parsing.** syl passes a run-artifact path
  (`.syl/runs/<run>/handoff-<iter>.md`) as the `/handoff` argument. The file being
  present after the turn *is* the rollover signal; absent means resume as normal.
- **Seed supplements, never replaces.** syl keeps supplying the ticket and blocking
  findings every iteration; the handoff document adds only the soft context
  (decisions, exploration, approach).
- **Resume failure degrades to today's behavior** — a plain fresh `Run` with ticket
  and findings, reusing the reviewer's fallback pattern.
- **`handoff` skill gains a target-path argument** (default OS temp when none given)
  and **loses `disable-model-invocation: true`**. The flag is removed because, in
  practice, it causes the model to sometimes refuse a skill even when the prompt
  invokes it explicitly; removing it makes the explicit `/handoff` invocation
  reliable. A spurious auto-handoff without syl's path argument writes to OS temp
  and is harmlessly ignored by syl's existence check.

## Consequences

Scope is implementer-only; extending Rollover to the reviewer is a ticketed
follow-up. The trigger clause is added to both the implement prompt and the
fix-review (revise) prompt, since revise iterations invoke `/fix-review`. New
domain term **Rollover** is recorded in `CONTEXT.md`.
