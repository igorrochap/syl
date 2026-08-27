---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---

Implement the work described by the user in the spec or tickets.

Read [`skills/code-quality/SKILL.md`](../code-quality/SKILL.md) before you write code — it holds the bar the change must clear.

Use /tdd where possible, at pre-agreed seams.

Run typechecking regularly and single test files regularly as you go.

Before you report the work done, run the project's quality gate and fix every failure it reports; [`skills/code-quality/references/gate-contract.md`](../code-quality/references/gate-contract.md) says how to find and run it. Report the work done once the gate is green. When the project has no gate, say so in the report.

Once done, use report what you've done and why.

Do not make assumptions without the confirmation of the user, clarify any questions you may have before implementation.