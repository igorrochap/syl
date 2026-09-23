# Syl home is a third root, resolved once and injected

The Project registry and Live-run markers mean almost every `syl` command now
writes user-level state. That directory — **syl home**, `~/.syl` by default and
overridable with `SYL_HOME` — is resolved once in `main`, alongside the origin
and work roots, and passed explicitly to whatever needs it, extending ADR 0002's
rule that collaborators ask for the root they need by name. Expanding `~` at the
point of use (as the worktree root default does) would make every test that
loads a config register its temp directory in the developer's real registry;
injection plus `SYL_HOME` lets tests and sandboxes isolate it.
