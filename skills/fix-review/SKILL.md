---
name: fix-review
description: Fix problems pointed out in a code review session.
---

Fix the problems pointed out in the code review.

The findings are usually given to you directly in the current conversation (e.g. a "Blocking findings" list in the prompt) — work from those first. Only look for a review document if you're explicitly pointed at one; where that document lives depends on how the project tracks reviews (a local `.scratch/<project>/reviews/` file, or a GitHub PR/issue comment) — don't assume `.scratch`.

If there's a suggested order of work, follow it. If not, resolve the most critical findings first and report what you didn't resolve and why.

After the corrections, run the project's quality gate again — a correction can break a gate that was green before. [`skills/code-quality/references/gate-contract.md`](../code-quality/references/gate-contract.md) says how to find and run it.

/refactoring skill is your biggest friend if the findings are only about coding standards