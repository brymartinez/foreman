# Control Layer Progress

## Current stage

The Codex Git permission repair is complete. New and resumed implementation turns use
a scoped permission profile for linked-worktree Git metadata. A model-free Codex
sandbox probe covers staging, committing, and a tracking push. The runner stop test
skips when its required `ps` command is unavailable in an agent sandbox.

Stage 43 (foreman is TUI-first, for issue #43) is complete. Bare `foreman` opens the
dashboard, `-a`/`--auto` and `--no-auto` set auto mode for the session, and `-c` edits
the config. The task and config subcommands are gone. The dashboard gained `?` (keys
and version), `R` (raw log lines), and log paging back to the file start.
`scripts/e2e.sh` drives the dashboard through `scripts/tui-drive.py`.
Stage 40 (bounded implementation checkpoints with fresh sessions and small durable
handoffs) is complete. Stage 39 (routing new dashboard tasks to a profile from `routing.yaml`, with the
optional `quota-axi` check, for issue #39) is complete. Stage 31 (answering decisions
in the dashboard), stage 29 (repository include filter and the `v` view toggle), stage
38 (shorter planning documents and the `make config` notice for differing stage files),
and stage 37 (the `## Working silently` prompt section for issue #37) are complete.
Completed runs are
needed to measure the effect of the model and budget choices. The ranked refactor
candidates below are the next code-quality stages.

## Refactor candidates

The stage 28 review of every Go package, ranked by how much each change would help.
Findings 1 to 4 were fixed in stage 28.

1. `internal/app/run.go` `RunTask`: phases implied by flags. Fixed.
2. The turn, verify, and record sequence copied four times. Fixed.
3. `implement` took positional `nil` arguments, and its doc sat on `needsPlan`. Fixed.
4. FAILED transitions written by hand. Fixed with `failTask`.
5. `cmd/foreman/main.go` `run` (about 105 lines) parses, loads config, wires every
   adapter, and runs the dashboard or a task. Splitting wiring from dispatch would let
   tests cover the wiring without the binary.
6. `internal/app/gate.go` `gate` (about 115 lines) mixes GitHub reads, state
   transitions, and note building. Each check could be its own function returning the
   change to apply.
7. `internal/app/start.go` `Start` (about 97 lines) holds the guards, worktree setup,
   and launch in one function.
8. `internal/store/markdown.go` `toRecord` (about 101 lines) and `fromRecord` (about 69
   lines) map every field in one block, which is hard to scan.
9. `internal/render/render.go` `Show` (about 115 lines) and `internal/tui/tui.go` `Run`
   (about 98 lines) could split into one helper per section or event.
10. `internal/agent/prompt.go` `ImplementationPrompt` (about 95 lines). Section
    builders would let tests check one section at a time.

## Known limitations

- Implementation checkpoint validation commands are agent reported, as with PR
  validation. Foreman verifies the branch, clean worktree, and advancing commit.
  A headless agent may still run a long turn before it elects to checkpoint.
- Routing's selector flow (`3` in the agent choice) is covered by unit tests with a stub
  source. `scripts/e2e.sh` checks a retried routed task.
- The gate runs only while the dashboard is open, because the design rules out
  daemons. Nothing else runs while it is closed except a task's own runner.
  Notifications come from the runner and from dashboard refreshes.
- A PR_CLOSED task cannot be restarted: the inbox offers only FAILED and REVIEWED
  delegated work. Reopening the PR brings it back.
- The dashboard does not show which searches hit the 1000-result limit, or the
  inbox's change notes for tasks it does not offer.
- `gh pr checks` handling is verified against gh's documented behavior and fixtures,
  not a live PR, because the user declined the live probe.
- Validation results are self-reported by the agent. CI is the independent
  check, and foreman gates on it.
- A search that returns 1000 results is flagged, and lost reasons aren't judged
  while one is truncated.
- An agent that leaves the reply marker off reads its own reply as new feedback. That
  costs one extra round, bounded by the per-run cap.
- PR feedback has been checked against the stand-in `gh` and fixtures, not a live PR.
- The release workflow has not run: the repo has no GitHub remote yet.
- Conflict repair is checked against the stand-in `gh` and fixtures, not a live
  conflicting PR. GitHub reports UNKNOWN until it has computed mergeability, so a
  repair can start one refresh late.

## Next stage

Retry issue #46 with the repaired Codex adapter after installing this build. Its
worktree still has uncommitted first-stage changes. After that, compare completed
task usage and outcomes before changing the models or budgets. For code quality,
take refactor candidate 5 (`cmd/foreman` wiring) next.
