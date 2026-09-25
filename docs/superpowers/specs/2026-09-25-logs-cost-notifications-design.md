# Logs, cost controls, and notifications

## Goal

Make detached Foreman runs observable, timestamped, and bounded without changing
the implementer-plus-reviewer lifecycle.

## Design

- `foreman logs owner/repo#N` renders a clean event view from the task's runner log.
  `--follow` follows a live run and `--raw` preserves the underlying JSONL for
  debugging.
- The runner timestamps every log line at write time. Agent-specific parsers add
  compact summaries for Claude results and Codex `turn.completed` usage events.
- Task frontmatter stores cumulative usage: Claude dollar spend, Codex input/cache/
  output/reasoning tokens, and per-agent turn counts.
- Claude receives `--max-budget-usd` for implementer and reviewer turns. The default
  is $3.00 for implementation and $1.00 for review. The existing three-round review
  limit remains the safety bound, and a clean review ends the loop immediately.
- Codex has no per-session USD field or dollar cap in its JSONL protocol. Foreman
  records its token usage and uses the configured model; the log reports USD as
  unavailable rather than inventing a conversion.
- Notifications fire only when a task enters `NEEDS_DECISION`, `FAILED`, or
  `READY_FOR_HUMAN`. The default notifier uses native macOS notifications and
  `notify-send` on Linux; durable task state remains the source of truth.

## Rejected alternatives

- Terminal-only completion messages do not reach the user after a detached runner
  finishes.
- Raw JSONL as the default log view is difficult to scan and leaks adapter details
  into the normal workflow.
- Skipping review reduces spend but changes the established quality contract.
