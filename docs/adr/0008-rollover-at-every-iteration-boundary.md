# Roll over the implementer at every iteration boundary

Supersedes the agent-driven Rollover trigger of [ADR 0004](0004-implementer-session-rollover.md).

When the loop continues from iteration N to iteration N+1 — a revise Verdict, or
an approve Verdict whose nits exceed the nit budget, with iterations remaining —
syl resumes the last implementer session of iteration N for one **handoff turn**.
The handoff turn's prompt is `/handoff <path>` with the iteration handoff path
(`handoff-<N>.md`) and asks for nothing else: no code changes, no Gates, no
Evidence. Iteration N+1 then starts a fresh implementer session seeded from the
handoff document. No implementer turn judges its own remaining context.

## Why this shape and not the obvious one

ADR 0004 let the implementer decide when to roll over, on the grounds that it
knows its own context state. In practice the trigger never fired: no Run in any
project wrote a handoff document. The implementer session grew across every
iteration — one measured Codex Run went from 19k to 220k context tokens over five
iterations — and each model request re-sends the whole session history, which
counts against subscription usage limits even when cached. Most of that history
is stale tool output; what the next iteration needs is small: the working tree,
the decisions, and the reviewer findings.

The iteration boundary is the natural cut. The reviewer has just summarised what
is left to do as findings, and the next turn is a `/fix-review` pass that does not
need the old exploration verbatim.

## Considered options

- **Keep the agent-driven trigger.** Rejected: it does not fire.
- **A numeric syl-side backstop** (model→context-window table and an occupancy
  threshold, #174). Rejected: it would only ever force a Rollover at the next
  iteration boundary, and this decision always rolls over there, so the table and
  threshold buy nothing.
- **Roll over between Gate attempt turns too.** Rejected: a red Gate is almost
  always caused by the change the implementer just made; the session that made it
  is best placed to fix it, and Gate attempt turns are short. Gate attempt turns
  always resume the previous turn's session, and syl no longer allocates
  per-Gate-attempt handoff paths. Run records from earlier versions that contain
  `handoff-<N>-gate-<M>.md` documents still read cleanly.
- **Have a fresh session write the handoff.** Rejected: only the session that did
  the work holds the decisions worth handing off, so the handoff turn never falls
  back to a fresh session.

## Key decisions

- **No handoff turn when the loop ends**: approve within the nit budget, the last
  allowed iteration, an exhausted Gate attempt limit, a failed or cancelled Run.
- **A failed or empty handoff turn is not a Run failure.** syl prints a warning
  that names the iteration and the expected path, and iteration N+1 resumes the
  session of iteration N — the pre-Rollover behaviour.
- **No session to resume.** If iteration N reported no implementer session,
  there is nothing to hand off: syl prints the same warning and iteration N+1
  starts a fresh session without a seed, with the same prompt a failed
  resume falls back to.
- **Gate attempt turns and the handoff turn are resume-only.** They never fall
  back to a fresh session; if the session cannot be resumed, the Gate attempt
  turn fails the Run, and the handoff turn skips the Rollover with a warning.
- **syl does not run the Gates after the handoff turn**; it changes no code.
- **Usage**: the handoff turn's tokens belong to iteration N's implementer usage
  entry. Iteration N+1's entry counts only its fresh session.
- Applies to every implementer Harness (Codex and Claude). The reviewer does not
  roll over.
