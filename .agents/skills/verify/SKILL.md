---
name: verify
description: Verify a change in this Go and server-rendered UI project with the narrowest meaningful checks and explicit evidence.
---

# Verify a change

Read root and applicable scoped instructions. Identify changed files, including untracked files; if Git is unavailable, inspect the actual files. Select a focused test first, then use `go test ./... -count=1`, `go vet ./...`, and a build when change risk warrants them.

For template changes, run renderer tests in `internal/handler` and check the affected browser flow when available. E2E needs a running app, PostgreSQL, browser dependencies, and sometimes external BGG/LLM access. `make test-rag` needs Ollama and models. Record missing prerequisites as unverified, never as passed.

Read failure excerpts before broadening logs. Do not weaken checks to obtain a pass. For review, leave source unchanged; for requested implementation, repair confirmed in-scope failures and rerun affected checks. Report exact commands, results, behavior observed, and remaining gaps.
