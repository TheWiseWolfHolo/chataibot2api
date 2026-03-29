# Repository Agent Rules

This file applies to the repository root and all files under it.

## Execution defaults

- Default to **main-thread execution** in this repo. Do not use `spawn_agent` unless the user explicitly asks for subagents or parallel delegation.
- If the user explicitly asks to use subagents, prefer `gpt-5.4` for both implementation and review work in this repo.
- Default reviewer reasoning effort: `high`
- Default implementer reasoning effort: `high`
- You may lower implementer reasoning effort to `medium` only for narrow, mechanical edits with clear tests and low integration risk.
- Do not default to `gpt-5.2` in this repo.

## Coordination defaults

- During plan execution, keep one implementation task active at a time unless write scopes are fully disjoint.
- Prefer direct local verification in the main thread over waiting on auxiliary reviewers.

## Pool safety during admin/dashboard work

- For admin UI, quota dashboard, and observability work, default to read-only behavior unless the user explicitly asks for write-path changes.
- Do not trigger pool mutation actions such as fill, prune, import, migrate, or persistence rewrites as a side effect of dashboard reads, probes, or page refreshes.
