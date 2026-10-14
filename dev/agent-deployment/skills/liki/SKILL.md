---
name: liki
description: Development fixture skill for the liki-agent-runtime dev stack. It proves that the skilltoolset binding preloads and serves skills in local development; it carries no methodology and must never be shipped to production images.
---

# liki (dev fixture)

Development-only fixture. The real liki skill ships in the liki repository
image (`/skills` in production assembly images), not here.

Use this skill to smoke-test `list_skills` / `load_skill` locally:

1. `list_skills` should return `liki`.
2. `load_skill` should return this document.
