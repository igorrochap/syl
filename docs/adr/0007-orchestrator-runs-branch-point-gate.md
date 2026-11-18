# The orchestrator runs the branch-point Gate; the agent's own gate run is advisory

After every implementer turn, syl runs the project's Gates itself and does not
trust the implementer's report that the gate is green. It runs the copy of
`scripts/quality.sh` from the **branch point**, extracted into run artifacts,
against the Work root, with `QUALITY_BASE_REF` set to the branch point. A red Gate
sends the work back to the implementer without a review. The implementer still
runs the gate during its turn as a fast inner loop, but that run only informs the
implementer and decides nothing. The rule behind this comes from the Claude Code
team's verification practice: *if a change can modify the check that judges it,
the check is broken.*

## Why this shape and not the obvious one

The obvious design is to keep today's behaviour: the `implement` skill tells the
agent to run the gate, and the agent reports success. The obvious fix to that is to
have syl run the working-tree `scripts/quality.sh`. Both let the change judge
itself. The agent's report cannot be checked, and the working-tree script is part
of the diff, so a change can delete a gate, raise the complexity limit, or lower
the coverage floor. That is easy to do even without bad intent, by tidying up a
check the agent finds inconvenient.

## Considered options

- **Trust the implementer's self-report.** Rejected: it is unverifiable and
  costs a full review round whenever the agent's report is wrong.
- **syl runs the working-tree script.** Rejected: the change can weaken the
  script that judges it.
- **syl runs each gate as its own process, in parallel.** Rejected: only the
  project's script knows which gates share state (for example `vet` running
  `go mod tidy` while `tests` and `coverage` both run the suite). The gate
  contract instead asks the entry point to run independent gates concurrently and
  to print a summary block, with one `PASS`, `FAIL`, or `SKIP` line per gate,
  for syl to read.
- **Reviewer reads branch-point copies of skills too.** Rejected for now:
  harnesses resolve skills from their working directory. Protected-path flagging
  (below) makes such edits visible to the reviewer instead.

## Consequences

- Tool configuration the script reads, such as `.golangci.yml`, is still editable
  by the change. `gate.protected_paths`, which by default holds `scripts/quality.sh`,
  the vendored skill directories, and `.syl/verify.md`, makes syl flag any diff that
  touches those paths to the reviewer. Loosening that the ticket didn't ask for is a
  blocking finding.
- A ticket that legitimately changes the gate script is judged by the old script
  until it merges. That is the safe direction to fail in.
- Red Gates are counted as **Gate attempts**, separately from iterations
  (`loop.max_gate_attempts`, default 3). Reaching the limit ends the Run as
  `exhausted`, and the Run record names which limit was reached.
- A project without `scripts/quality.sh` records "no gate" and proceeds to review;
  the PR's Verification section states "none".
