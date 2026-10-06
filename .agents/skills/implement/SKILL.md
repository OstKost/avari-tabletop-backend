---
name: implement
description: Implement a focused feature across the Go server, templates, storage, and external-provider interfaces in this project.
---

# Implement a feature

Read root and applicable scoped instructions, nearby code, and relevant tests. Define concrete successful and failing examples. For a cross-boundary change, agree on route/input, service, persistence, and page/partial contracts before editing.

Implement the narrowest complete user-visible slice. Preserve handler → service → repository separation, auth checks, error behavior, and `context.Context` propagation. Keep full-page and HTMX responses aligned; consider SSE cancellation for chat, existing database rows for migrations, and `chromem`/`pgvector` behavior for RAG changes.

Run focused Go checks during implementation and broader checks if the affected flow crosses components. For UI work, exercise the interaction in a browser when available, including keyboard and mobile viewport. Report changed behavior, commands/results, and concrete limitations.
