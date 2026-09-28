# Syl home is a third root, resolved once and injected

The Project registry and Live-run markers mean almost every `syl` command now
writes user-level state. That directory — **syl home**, `~/.syl` by default and
overridable with `SYL_HOME` — is resolved once in `main`, alongside the origin
and work roots, and passed explicitly to whatever needs it, extending ADR 0002's
rule that collaborators ask for the root they need by name. Expanding `~` at the
point of use (as the worktree root default does) would make every test that
loads a config register its temp directory in the developer's real registry;
injection plus `SYL_HOME` lets tests and sandboxes isolate it.

## Syl home travels as a value, not a path

`main` opens syl home once as a `sylhome.Dir` and injects that value, never a
bare path string. `Dir` is the only way to reach the Project registry and
Live-run markers, so "syl home is required" is checked once at `sylhome.Open`,
and no caller builds paths under syl home or recomputes marker file names. The
package keeps the `syl` prefix on purpose: `home` alone would read as the OS
home directory, which `usage`, the Claude transcript reader, and worktree root
expansion already call `homeDir`. `sylhome` holds only where Projects are and
which Runs are live; deciding that a Run is Interrupted (and so may be
dismissed) stays with the Run's own state.
