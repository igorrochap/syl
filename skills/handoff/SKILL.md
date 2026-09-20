---
name: handoff
description: Write a conversation handoff only when the caller explicitly asks a fresh agent or session to continue the work.
argument-hint: "[target-path]"
---

Write a handoff document summarising the current conversation so a fresh agent can continue the work.

Use the optional argument as the target path:

- For `/handoff <path>`, write the document to `<path>`, creating its parent directory when needed.
- For `/handoff` with no path, choose a descriptive Markdown filename in the user's OS temporary directory and write the document there.

Report the path after writing the document.

Include a "suggested skills" section in the document, which suggests skills that the agent should invoke.

Do not duplicate content already captured in other artifacts (specs, plans, ADRs, issues, commits, diffs). Reference them by path or URL instead.

Redact any sensitive information, such as API keys, passwords, or personally identifiable information.
