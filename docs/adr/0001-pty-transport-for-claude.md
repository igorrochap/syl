# Drive Claude Code through a pty and read the session transcript

syl drives Claude Code through a pseudo-terminal and learns what happened by reading the
session transcript, instead of using the documented headless interface
(`claude --print --output-format stream-json`). We measured that `--print` consumes about
2.5× more of the Claude 5-hour usage bar than an interactive session doing identical work,
so the obvious interface is the expensive one.

## Why this is not the obvious choice

A reader who finds syl allocating a pty, choosing a session id, and tailing a JSONL file
under `~/.claude/projects/` will reasonably ask why we do not simply parse the structured
stream that Claude Code offers for exactly this purpose. The answer is cost, and it is not
visible in any documentation or in token accounting.

## The evidence

Six controlled experiments in one project, on the same working-tree diff, same model
(`claude-sonnet-5`), same `service_tier`, on a quiet machine, arms minutes apart. "Per point"
is weighted tokens consumed per percentage point of the 5-hour bar; **higher is cheaper**.

| # | Comparison | interactive /pt | `--print` /pt | ratio |
|---|---|---|---|---|
| 1 | flow: manual `/code-review` vs `syl review` | 81,805 | 31,362 | 2.61× |
| 2 | flow: same, fresh usage window | 65,655 | 32,843 | 2.00× |
| 3 | identical prompt, interactive ran first | 74,538 | 33,352 | 2.24× |
| 4 | identical prompt, `--print` ran first | 62,833 | 24,027 | 2.61× |
| 5 | `--print` minus `--include-partial-messages` | — | 20,923 vs 24,983 | no effect |
| 6 | pty transport vs `--print` | 57,727 | 32,720 | 1.76× |

Experiments 3 and 4 are the isolation: a temporary binary composed the review prompt with
production code and routed it to the interactive path, so the arms differed by exactly four
flags. Experiment 4 is the best-matched pair recorded — 23 requests against 22, cache reads
within 2% — and the interactive arm did 12% more work while moving the bar less than half as
far. Reversing the order changed nothing.

Experiment 5 eliminated the output-formatting flags. Experiment 6 confirmed a pty session is
metered like a terminal session, returning roughly 43% of the bar.

The asymmetry survives every weighting we tried — raw tokens, cache reads at zero, cache reads
at full rate, output only, per-request counting, and price-weighted cost. No linear scheme
reconciles the two modes. The mechanism is Anthropic-side and undocumented; a report has been
prepared separately. Full method and raw data: `.scratch/syl-usage-debug-handoff.md` and
`.scratch/research-headless-metering.md`.

## Consequences

The session transcript, not the process output, is syl's source of truth for Claude runs. That
is a more durable interface than a stream shape gated behind flags that only work under
`--print`, and it removes a latent fragility: verdict extraction previously depended on
`text_delta` events, so dropping one flag silently produced reviews with no assistant text at
all.

Since the process never exits on its own, syl determines completion itself: the OSC-9 escape
Claude Code emits when it goes idle is the fast path, the transcript is the authority, and an
idle timeout is the backstop. Sessions end with SIGTERM; writing `/exit` into the pty does not
reliably terminate them.

syl refuses to run when `CLAUDE_CODE_CHILD_SESSION` is set. That marker disables transcript
persistence in child processes, which would leave syl with no source of truth at all — a
silent failure rather than a loud one.

Claude support now requires a pty, and therefore `github.com/creack/pty`. syl targets Linux
and macOS only, so this costs no supported platform. `syl plan` keeps its existing interactive
attach path, and the Codex adapter is unaffected.
