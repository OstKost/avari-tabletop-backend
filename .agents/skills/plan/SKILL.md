---
name: plan
description: Plan a substantial change to this Go board-game collection app with observable acceptance criteria and relevant checks.
---

# Plan a project change

Read `AGENTS.md` and instructions in affected directories. Trace the requested flow through `internal/handler`, `internal/service`, `internal/repository`, `web/`, and `migrations/` as relevant. For a small change, implement directly without a plan document.

For substantial or multi-session work, state the user-visible outcome, examples of success and failure, dependencies, owned paths, and verification commands. Resolve contracts for HTML/HTMX/SSE responses, data storage, and provider behavior before splitting work. Consider both local `chromem` and optional `pgvector` when touching RAG persistence.

Prefer one complete usable slice. If implementation was requested, continue through the plan without another approval round. A proposal or mock does not count as delivered behavior.
