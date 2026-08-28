---
name: code-quality
description: The quality bar for code — control flow shape, tests, architecture, naming, error handling, change discipline — and how to find and run a project's quality gates. Use when writing or changing code in any project, when a change is ready to verify, or when reviewing a change against the bar.
---

# Code Quality

The bar every change clears, in any language. The norms here are about **shape** — the form code takes on the page — because shape is what an agent can obey while writing. Thresholds belong to the gates, and the tools that enforce them belong to the project: [`references/gate-contract.md`](references/gate-contract.md) says how to find and run them.

## Control flow and expression shape

- Exit early with a guard clause: handle the failing or trivial case, return, and let the rest of the function read as the main path.
- After a `return`, continue at the same indentation — the guard has already taken the other branch.
- Keep one level of indentation as the usual case. A second level is a signal to extract a function.
- Write a conditional as a statement, not as a conditional expression inlined into a value.
- Give a compound boolean condition a name — a variable or a predicate function — and branch on the name.

These are the readable form of a complexity limit. An agent cannot count branches while it writes; it can hold a shape.

## Test discipline

- Every change of behaviour arrives with a test that fails without it.
- Put the test at the seam — the interface a caller uses — so the test survives a refactor of what sits behind it.
- Test through the public interface; leave internal details free to change.
- Leave the project's coverage at or above where you found it.

## Architecture

- Read `CONTEXT.md` before you write code.
- Read the ADRs covering the area you touch, and follow the decisions they record.
- Use the glossary's words for the glossary's concepts, in names, comments, and messages alike.
- Add depth: a module earns its place by putting real behaviour behind a small interface. See [`skills/codebase-design/SKILL.md`](../codebase-design/SKILL.md) for deep modules and seams.

## Naming and comments

- Name a function for the action it performs — a verb phrase.
- Name a boolean for the question it answers, so it reads as a predicate at the call site.
- Spell words out in full.
- Name a thing for its role, not its type.
- A comment says **why**: the reason, the constraint, the option rejected.
- A comment that narrates what the code does is a name waiting to be made — extract the block into a well-named function and delete the comment.

## Error handling and dead code

- Handle each error once: either act on it, or add context and return it to the caller who will.
- Every error reaches someone who can act on it, carrying enough context to act.
- Delete code you replace. Git holds the history, so a commented-out block is dead weight.

## Change discipline

- Apply the rule of three: two occurrences are a coincidence, the third earns the abstraction.
- Keep a refactor and a change of behaviour in separate commits, so each one can be read and reverted on its own.
- Leave every file you touch in better condition than you found it.

## Running the gates

A project states its quality bar as gates and exposes them through one entry point. Read [`references/gate-contract.md`](references/gate-contract.md) for the gate names, what each one examines, and the commands.

## Related skills

- [`skills/refactoring/references/smells.md`](../refactoring/references/smells.md) — the code smells that name what is wrong with a piece of code.
- [`skills/codebase-design/SKILL.md`](../codebase-design/SKILL.md) — deep modules, interfaces, and seams.
