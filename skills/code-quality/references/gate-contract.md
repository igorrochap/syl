# Gate contract

A **Gate** is one automated check a change must pass. This contract fixes the name of each Gate and what it examines; a project fills each Gate with its own tools and sets its own thresholds. The names are the shared language: ask about a Gate by name, report a failure by name.

## Entry point

Every Gate runs through `scripts/quality.sh`:

```
scripts/quality.sh          # run every Gate
scripts/quality.sh <gate>   # run one Gate, e.g. scripts/quality.sh complexity
```

Run one Gate while you work on what it covers; run the script with no argument before you call a change done. An unrecognised Gate name is an error — the script names the valid Gates and exits non-zero rather than falling back to running everything.

If `scripts/quality.sh` is absent, the project has no gates. Say so to the user, and ask which commands to run instead of inferring them from the repository.

## No-argument run

With no argument, the entry point runs every Gate and prints a summary block. The block is the machine-readable result syl reads. It begins with one `QUALITY GATE` header line, followed by exactly one result line for each selected Gate:

```
QUALITY GATE
  format        PASS
  vet           PASS
  style         PASS
  complexity    PASS
  coverage      PASS
  tests         FAIL   <optional one-line detail>
  architecture  SKIP   <optional one-line detail>
```

Each result line starts with two spaces, the Gate name left-aligned in a 14-character field, and one result: `PASS`, `FAIL`, or `SKIP`. An optional one-line detail may follow the result. Result lines follow the fixed order in [The Gates](#the-gates), limited to the selected Gates, regardless of when the Gates finish. Any diagnostic output after those lines is outside the summary block.

The entry point exits non-zero when any selected Gate fails.

The no-argument entry point should run independent Gates concurrently. This is a recommendation; a script that runs Gates one after another is compliant. The script decides which Gates are independent. Gates that share mutable state run one after another. Shared mutable state includes files a Gate rewrites and a test suite that two Gates both run.

The `style` and `complexity` Gates use a base ref. Set `QUALITY_BASE_REF` to
choose the base explicitly, which is useful for a stacked branch on a local
machine. Base refs have this order of precedence: `QUALITY_BASE_REF`, then
`GITHUB_BASE_REF` (try `origin/<name>` and `<name>`), then the default
`origin/main` and `main`. If either explicitly supplied variable names no
existing ref, the Gate stops with an error and does not use a lower level.
Each diff Gate reports the selected base and changed-file count. The list of
changed files and the analysis use the same merge base.

## The Gates

**`format`** — the layout of the source: whitespace, line breaks, import order, and anything else the project's formatter owns. Passes when the files are already in formatted form. Fails when running the formatter would change a file; the fix is to run the formatter and commit the result.

**`vet`** — correctness suspicions a compiler accepts but a maintainer would not: unreachable code, misused APIs, ignored results, shadowed values. Passes when the analysers report nothing. Fails on a report; each one is either a real defect or a construct to rewrite.

**`style`** — the conventions the project has chosen to enforce: naming, package layout, forbidden constructs, lint rules. Passes when the linters are silent. Fails on any diagnostic; a rule that no longer serves the project is changed in the linter's configuration, not suppressed at the call site.

**`complexity`** — how much branching and nesting a single unit carries, measured against the project's limit. Passes when every unit is under the limit. Fails when one is over; the fix is to extract functions and flatten the control flow, not to raise the limit.

**`coverage`** — the share of the code exercised by the test suite, measured against the project's floor. Passes when the suite is at or above the floor. Fails when a change drops it below; the fix is a test at the seam the change touched.

**`tests`** — the test suite itself. Passes when every test passes. Fails on a failing, flaky, or skipped-without-reason test; a red suite blocks everything downstream, so fix it before reading any other gate's output.

**`architecture`** — the structural rules the project records: allowed dependency directions, layer boundaries, module ownership, and the decisions in the ADRs. Passes when the code respects them. Fails on a violation; the fix is to move the code or to change the decision through an ADR, in that order of preference.
