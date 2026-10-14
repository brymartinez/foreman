# Control Layer Progress

## Current stage

Issue 58 is in progress. Configuration, source-qualified task records, the Linear
GraphQL adapter, and mixed inbox refresh are complete. The dashboard filters with
`-s`, groups source rows, warns on partial refresh, and pauses auto starts until all
selected sources refresh. GitHub-only selection works without a Linear key. Dashboard
start and routing now carry the Linear key, read Linear issues and comments, and
recheck the exact `triaged` label for auto starts. Manual starts skip that check.
The runner now reads the stored issue source and posts or edits planning comments
there. Linear PR prompts require the identifier in the title and issue URL in the
body, without a GitHub closing keyword. Delegated tasks refresh through their stored
source across inbox filters. A stopped Linear task becomes DONE when its issue closes,
including completed and canceled Linear states. Linear tasks remain available for
resume in a GitHub-only view, and legacy GitHub tasks in mapped repositories refresh
in a Linear-only view. The mixed-source end-to-end scenario and README remain for the
next checkpoint.

Reviewer availability fallback is complete. A fresh quota-axi report can move a
review to the implementer's agent when the competing agent is confirmed not set up
or exhausted. An authentication failure also triggers a fresh review session with
the implementer's agent. Foreman records that reviewer and keeps it on resume.
Vet, Go tests, and build pass. The end-to-end run passes the runner, review,
resume, and routing scenarios, but its notification scenario cannot bind a local
socket in this environment and reports six notification failures.

Checkpoint validation repair is complete. When an initial implementation
checkpoint reports failed checks, Foreman resumes the same agent session with
those commands for up to two repair turns. A passing checkpoint still needs a
clean, advancing commit. Vet, Go tests, build, and the runner end-to-end scenario
pass. The full end-to-end run stops in the notification scenario while selecting
an untriaged fixture issue (#44); that scenario does not exercise checkpoints.

The runner process check now uses an inherited lock file, and the end-to-end
script signals recorded PIDs. The process checks pass where process-list
commands are unavailable. Vet, tests, build, and the full end-to-end script pass.

Stage 54 (require triage before automatic issue starts, for issue #54) is
complete. The foreman-triage skill applies the `triaged` label after confirmed
publication. Foreman reads labels during inbox refresh, marks unlabelled assigned
issues as awaiting triage, and lets them start manually. Auto mode requires a
successful GitHub refresh and rechecks the label before starting. Vet, tests,
build, and the full end-to-end script pass.

Stage 55 (skip agents that quota-axi confirms are not set up, for issue #55) is
complete. Routing excludes a provider only when a fresh report says `notSetUp: true`.
It tries the selected rule's remaining profiles in order, or waits and retries after
the next GitHub refresh. Manual agent choices still bypass routing.

Stage 52 (side-by-side PR history, for issue #52) is complete. PR history opens
with a narrower list and vertical timing beside it, plus a short full-width log.
The list selects the timing and attempt log. Enter and Tab focus the log; Esc
returns to the list before closing history.

Stage 50 (pause log redraws, for issue #50) is complete. `Space` in a task log or PR
history log draws a paused frame, then `Run` writes nothing, not even a resize
clear, until `Space` or another key resumes it.

Stage 46 (reconcile stopped delegated tasks, for issue #46) is complete. A refresh
marks a stopped task DONE when its linked PR merges or its issue closes, including
FAILED and PR_CLOSED tasks. With `cleanup.on_done`, it removes clean DONE and
PR_CLOSED worktrees once the runner stops, and keeps the branch unless the PR merged.
A launch recreates a removed worktree, so a reopened PR can resume.

Stage 45 (PR history, for issue #45) is complete. `p` opens the merged and closed
Foreman PRs with their phase times and each attempt's log. Task files record
`attempts`, with phase marks written in `App.update`.
The Codex Git permission repair is complete. New and resumed implementation turns use
a scoped permission profile for linked-worktree Git metadata. A model-free Codex
sandbox probe covers staging, committing, and a tracking push.

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

- PR history shows timing only for attempts recorded since stage 45. Older PRs, and a
  task in flight when foreman was upgraded, show `unavailable`.
- A PR closed before its handoff is measured to GitHub's close time. When gh cannot
  read the PR, it is measured to the close the dashboard saw, which can be later.

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

Compare completed task usage, outcomes, and the PR history's phase times before
changing the models or budgets. For code quality, take refactor candidate 5
(`cmd/foreman` wiring) next.
