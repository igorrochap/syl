# Package layers and dependencies

This project records its package layers so that the architecture gate can keep
dependency direction stable. The layers follow the real dependency graph at
the time of this decision: the command is the composition root, the CLI
drives application work, orchestration depends on ports and support packages,
and concrete adapters depend on those ports rather than on their callers.

## Layers and permitted project-package edges

The gate ignores standard-library and third-party imports. The following are
the permitted imports between this repository's packages; an omitted edge is
not permitted.

| Layer | Package | May import |
| --- | --- | --- |
| Composition | `cmd/syl` | every package under `internal/`, plus `scripts` and `skills` |
| Command | `internal/cli` | `internal/adapters/git`, `internal/adapters/notify`, `internal/config`, `internal/harness`, `internal/initializer`, `internal/orchestration`, `internal/tracker`, `internal/ui`, `internal/updater`, `internal/usage`, `internal/version` |
| Application | `internal/orchestration` | `internal/config`, `internal/harness`, `internal/tracker`, `internal/ui`, `internal/usage`, `internal/verdict` |
| Application support | `internal/initializer` | `internal/config`, `internal/tui`, `internal/ui`, `scripts`, `skills` |
| Application support | `internal/updater` | `scripts` |
| Port | `internal/harness` | `internal/config` |
| Port adapter | `internal/adapters/gh` | `internal/tracker` |
| Port adapter | `internal/adapters/glab` | `internal/tracker` |
| Port adapter | `internal/harness/claude` | `internal/config`, `internal/harness`, `internal/harness/claude/transcript` |
| Port adapter | `internal/harness/codex` | `internal/config`, `internal/harness` |
| Support | `internal/usage` | `internal/harness/claude/transcript` |
| Support with no project-package edges | `internal/adapters/git`, `internal/adapters/notify`, `internal/config`, `internal/harness/claude/transcript`, `internal/tracker`, `internal/tui`, `internal/verdict`, `internal/version`, `scripts`, `skills` | none |
| Support with no project-package edges | `internal/ui` | none |

## Exceptions

These are deliberate edges in the table that a simpler three-layer model would
call exceptions:

- `cmd/syl` imports concrete adapters and application packages because it is
  the composition root that assembles the executable.
- `internal/cli` imports `internal/adapters/git` and
  `internal/adapters/notify` because the in-process CLI owns the defaults for
  those collaborators while still allowing callers to inject replacements.
- `internal/initializer` and `internal/updater` import `scripts` and
  `skills` because those packages hold the canonical embedded assets they
  install or execute.
- `internal/usage` imports the Claude transcript reader because usage
  recomputation is defined over the transcript format currently persisted by
  Claude runs.
- Tests are not part of the architecture check. Test packages intentionally
  import concrete collaborators to exercise public seams; those imports do
  not create production dependency edges.

## Consequences

The architecture gate runs in both CI workflows through `scripts/quality.sh`, so a
forbidden or unrecorded project-package import blocks the quality gate and the change.

Every new production package must be added as a row in the table and given a matching
`depguard` rule, with its file glob excluded from the catch-all rule (or added to the
explicit no-project-dependencies rule) before it is used. The catch-all rule rejects
project-package imports from files that do not match a package-specific rule, so omitting
an edge is not a supported shortcut.

When a dependency is needed, first move the dependency or introduce a port so the
existing layer direction remains valid. If the edge is deliberate, update this ADR's
table and the corresponding `depguard` rule in the same change; then update the
implementation.
