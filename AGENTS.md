# Repository Agent Rules

This file applies to the repository root and all files under it.

## Subagent model defaults

- Default subagent model for implementation, review, and final review work in this repo: `gpt-5.4`
- Default reviewer reasoning effort: `high`
- Default implementer reasoning effort: `high`
- You may lower implementer reasoning effort to `medium` only for narrow, mechanical edits with clear tests and low integration risk.
- Prefer `gpt-5.3-codex` only when the user explicitly asks for a speed/cost tradeoff, or when the task is purely mechanical and the controller states why that downgrade is acceptable.
- Do not default to `gpt-5.2` in this repo.

## Coordination defaults

- During plan execution, keep one implementation task active at a time unless write scopes are fully disjoint.
- For code changes, complete the sequence: implementer -> spec review -> code quality review before marking a task complete.
- Do not let a slow reviewer block the whole rollout indefinitely; the controller should continue driving verification and coordination in the main thread.

## Pool safety during admin/dashboard work

- For admin UI, quota dashboard, and observability work, default to read-only behavior unless the user explicitly asks for write-path changes.
- Do not trigger pool mutation actions such as fill, prune, import, migrate, or persistence rewrites as a side effect of dashboard reads, probes, or page refreshes.
