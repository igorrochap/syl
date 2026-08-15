---
name: implement
description: "Implement a piece of work based on a spec or set of tickets."
disable-model-invocation: true
---

Implement the work described by the user in the spec or tickets.

Use /tdd where possible, at pre-agreed seams.

Run typechecking regularly, single test files regularly, and the full test suite once at the end.

Once done, use report what you've done and why.

Do not make assumptions without the confirmation of the user, clarify any questions you may have before implementation.

Before start to implement, create a new branch following the pattern <type>/<context>
where:
**type**: feat, fix, refactor, etc.
**context**: the problem the branch solves in only a few words