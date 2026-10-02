# Control Layer Progress

## Current stage

Stage 30 (dashboard auto mode for issue #30) is complete. Completed runs are needed to
measure the effect of the model and budget choices. The ranked refactor candidates
below are the next code-quality stages.

## Refactor candidates

The stage 28 review of every Go package, ranked by how much each change would help.
Findings 1 to 4 were fixed in stage 28.

1. `internal/app/run.go` `RunTask`: phases implied by flags. Fixed.
2. The turn, verify, and record sequence copied four times. Fixed.
3. `implement` took positional `nil` arguments, and its doc sat on `needsPlan`. Fixed.
4. FAILED transitions written by hand. Fixed with `failTask`.
5. `cmd/foreman/main.go` `run` (about 226 lines) parses, loads config, wires every
   adapter, and dispatches every command in one switch. Splitting wiring from dispatch
   would let tests cover the wiring without the binary.
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

- The gate runs only when `inbox` or `status` runs, or while the dashboard is open,
  because the design rules out daemons. Nothing runs between commands
  except a task's own runner. Notifications come from the runner and from those
  commands.
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

Compare completed task usage and outcomes before changing the models or budgets. For
code quality, take refactor candidate 5 (`cmd/foreman` wiring) next.
