---
name: debug
description: Reproduce and repair failures in the Go HTTP, HTMX, PostgreSQL, BGG, chat, or RAG flows of this project.
---

# Diagnose and repair

Read applicable instructions and inspect existing file changes. Capture input, expected behavior, actual behavior, and the smallest reproducible path. Distinguish a regression from a pre-existing failure; an empty Git diff is not evidence of a clean baseline, and Git metadata may be absent.

Trace the failing boundary with targeted searches and the narrowest useful logs. For HTTP issues inspect route, auth middleware, handler, service, repository, and rendered page/partial as applicable. For chat and RAG inspect streaming lifetime, provider errors, embedding configuration, and vector-store choice. Avoid exposing `.env`, credentials, and user data.

If asked only to diagnose, report cause and evidence without edits. If repair is requested, make the smallest complete fix, add a focused regression check when warranted, run it, and report the result and any reproduction gap.
