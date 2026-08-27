# Gate contract

A **gate** is one automated check a change must pass. This contract fixes the name of each gate and what it examines; a project fills each gate with its own tools and sets its own thresholds. The names are the shared language: ask about a gate by name, report a failure by name.

## Entry point

Every gate runs through `scripts/quality.sh`:

```
scripts/quality.sh          # run every gate
scripts/quality.sh <gate>   # run one gate, e.g. scripts/quality.sh complexity
```

Run one gate while you work on what it covers; run the script with no argument before you call a change done. An unrecognised gate name is an error — the script names the valid gates and exits non-zero rather than falling back to running everything.

If `scripts/quality.sh` is absent, the project has no gates. Say so to the user, and ask which commands to run instead of inferring them from the repository.

## The gates

**`format`** — the layout of the source: whitespace, line breaks, import order, and anything else the project's formatter owns. Passes when the files are already in formatted form. Fails when running the formatter would change a file; the fix is to run the formatter and commit the result.

**`vet`** — correctness suspicions a compiler accepts but a maintainer would not: unreachable code, misused APIs, ignored results, shadowed values. Passes when the analysers report nothing. Fails on a report; each one is either a real defect or a construct to rewrite.

**`style`** — the conventions the project has chosen to enforce: naming, package layout, forbidden constructs, lint rules. Passes when the linters are silent. Fails on any diagnostic; a rule that no longer serves the project is changed in the linter's configuration, not suppressed at the call site.

**`complexity`** — how much branching and nesting a single unit carries, measured against the project's limit. Passes when every unit is under the limit. Fails when one is over; the fix is to extract functions and flatten the control flow, not to raise the limit.

**`coverage`** — the share of the code exercised by the test suite, measured against the project's floor. Passes when the suite is at or above the floor. Fails when a change drops it below; the fix is a test at the seam the change touched.

**`tests`** — the test suite itself. Passes when every test passes. Fails on a failing, flaky, or skipped-without-reason test; a red suite blocks everything downstream, so fix it before reading any other gate's output.

**`architecture`** — the structural rules the project records: allowed dependency directions, layer boundaries, module ownership, and the decisions in the ADRs. Passes when the code respects them. Fails on a violation; the fix is to move the code or to change the decision through an ADR, in that order of preference.
