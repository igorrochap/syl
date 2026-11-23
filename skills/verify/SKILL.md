---
name: verify
description: "Exercise a change in the running software and record Evidence for the reviewer."
---

# Verify the change in the running software

Exercise the change in the software itself, then record what happened in the Evidence directory syl supplied for this iteration. Automated Gates and unit tests do not replace this step.

## Read the project recipe

If `.syl/verify.md` exists, read it first and follow its instructions for starting the software, preparing data, and driving the changed behavior. The recipe is project-owned and describes how this particular project is run. Do not edit it.

If the recipe is absent, identify the project type and use the safest practical way to exercise the changed behavior:

- **CLI:** build or run the command with representative arguments, including the changed path. Check its output, exit status, and any files it should create or update.
- **Server:** start the service with the project's normal development command, send representative requests to the changed endpoint or flow, and inspect responses and relevant logs. Stop the service when finished.
- **Web application:** start the app with its normal development command, open the affected page in a browser, and exercise the changed flow. Inspect the visible result and browser or server errors. Capture screenshots when they help a reviewer judge the change.
- **Library:** run a small consumer or example that calls the changed public API and inspect its result. If the library has no runnable consumer, explain that limitation and exercise the closest available integration path.

Use existing project commands and local conventions where they are discoverable. Keep any services, data, and external effects controlled. Exercise the behavior the ticket changes; do not claim success based only on a successful build or test suite.

## Record Evidence

Write `evidence.md` in the supplied Evidence directory. If it already exists for this iteration, update it to reflect the current change and preserve relevant earlier exercises and supporting files. Describe each meaningful exercise with:

- **What was exercised:** the user-visible behavior or ticket acceptance criterion.
- **How it was exercised:** commands, inputs, browser steps, and relevant setup.
- **What was observed:** concrete output, visible behavior, or errors.
- **Outcome:** `pass`, `fail`, or `not exercised`, with a short explanation.

Link supporting screenshots, logs, and other files with paths relative to the Evidence directory. Save those supporting files in that directory too. Mark each image a human should use to judge whether the change is PR-worthy with `PR-worthy` next to its link in `evidence.md`; leave other images unmarked. Evidence stays with the Run and must not be committed to the branch.

If you cannot exercise the change, record `not exercised` and state the specific reason in `evidence.md`—for example, a missing runtime, unavailable service, or blocked access. Also describe the closest check you could perform and its result. Do not leave the reason implicit or report an unexercised change as passing.
