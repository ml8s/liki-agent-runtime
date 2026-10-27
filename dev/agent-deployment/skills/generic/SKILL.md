---
name: generic
description: Development fixture skill for the agent-runtime dev stack. It proves that the skilltoolset binding preloads and serves skills in local development; it carries no methodology and must never be shipped to production images.
---

# generic (dev fixture)

Development-only fixture. The production skills ships in the domain content repository
image (`/skills` in production assembly images), not here.

Use this skill to smoke-test `list_skills` / `load_skill` locally:

1. `list_skills` should return `generic`.
2. `load_skill` should return this document.
