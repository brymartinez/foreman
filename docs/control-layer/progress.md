# Control Layer Progress

## Current stage

Knowledge export and ingest (#79) is complete. `foreman knowledge export owner/repo`
writes active guidance and a `foreman-knowledge/1` YAML payload. `foreman knowledge
ingest FILE` checks the repository identity, restores into an empty store without an
agent, skips an export it already holds, and otherwise groups learnings in one
tool-less merge turn while Go builds evidence and status. Maintainer corrections
decide conflicts; an unordered pair stops the ingest. Saves are all or nothing under
the repository lock. Vet, Go tests, build, and the `knowledge_transfer` end-to-end
scenario pass.

Reset of failed delegated runs (#72) is complete. `x` on a FAILED task confirms, then
`App.Reset` resolves the run's PR, saves a record with only that link, and deletes the
worktree, local branch, and runner log. Auto mode skips the reset issue. A manual start
continues an open prior PR from its remote branch, or starts a new branch with a closed
or merged PR as prompt context. Vet, Go tests, build, and the `reset` end-to-end
scenario pass.

Plan reconciliation (#71) is complete. Before a planning turn, the runner reads the
issue and its comments from GitHub or Linear and adopts Foreman's marked planning
comments. A full plan is implemented without posting; a partial plan posts only the
missing documents. Plan markers carry a description fingerprint, and a plan written
for another description asks the developer to keep or revise it. A failed read fails
the task before planning. The `plan_reuse` end-to-end scenario covers adoption.

Issue #70 is complete. New worktrees use the full branch name as nested directories.
Restarts and recreation keep the stored worktree path. Nested creation and cleanup,
branch selection paths, and legacy path reuse have regression coverage.

Automatic PR feedback replies are disabled. PR comments stay on GitHub for the
developer; Foreman no longer resumes an agent to answer them at handover or on a
dashboard refresh. Legacy REVIEWED tasks return to READY_FOR_HUMAN on refresh.
Vet, Go tests, build, and the feedback end-to-end scenario pass.

Branch naming for new tasks fast-forwards the managed checkout's default branch,
then reads repository `AGENTS.md` and the files it names as branch naming guidance
before making a worktree. A checkout that cannot fast-forward safely, or a named
guidance file that is missing or too large, stops the start with the reason (#67). Manual starts show an editable proposal; auto
starts wait when the convention or workstream is unclear. With no repository rule,
Linear's suggestion or the existing conventional default applies. Restarts keep
their stored branch. Vet, Go tests, build, and the end-to-end scenarios pass.

Stage 61 (per-repository evaluation and learning, for issue #61) is complete.
Claude's schema response is now accepted as evaluator output rather than rejected as
a tool call; other tool calls still discard the turn. Evaluations isolate Claude
from installed plugins, settings, and MCP servers. Vet, Go tests, build, and the
learning end-to-end scenario pass.
Task usage records tool-call counts by name. A detached `foreman __learn` evaluates a
task when its run ends on a clean review, and a refresh starts it again when the
task's evidence changes. One evaluator turn proposes learnings; `internal/knowledge`
sanitizes them, promotes a learning after two tasks or a maintainer source, and
retires it on a maintainer correction or a newer task's observation. Each repository
keeps its own `learnings.yaml`, evaluations, and rendered `knowledge.md` under
`knowledge_dir`. Planning and implementation prompts carry up to 8 learnings and 2000
characters from the task's repository; review and selector prompts carry none.
`learning.enabled` and `learning.budget_usd` control it. The `learning` end-to-end
scenario covers promotion over two tasks, a maintainer retirement, and a second
repository that learns nothing. Vet, Go tests, build, and the end-to-end script
pass.

Stage 62 (pin each task to one GitHub account) is complete. The first launch stores
the active `gh` login and noreply email on the task. Every launch and implementer turn
sets the worktree's commit identity, and fails the task before the agent runs when
`gh` or the SSH origin's key is on another account. The `account` end-to-end scenario
covers the pin, the commit author, and both mismatches.

Stage 58 (per-repository Linear issue sources) is complete. Configuration maps GitHub
repositories to Linear projects. The dashboard selects GitHub, Linear, or both issue
sources, groups inbox rows, and pauses auto starts after a partial refresh. Linear
tasks keep their source and issue ID across runner starts and source filters. Planning
comments go to Linear, while branches, PRs, reviews, and CI stay on GitHub. The mixed
end-to-end scenario covers filtering, delegation through clean PR review, and failed
refresh gating. Vet, Go tests, build, and the full end-to-end script pass.
Review round 1 fixes keep manual Linear starts from holding auto slots, retain GitHub
PR search hits for mapped repositories, and map missing Linear entities to `ErrGone`.
The posted PRD requires removal of the `v` all-repositories toggle.

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

- A reset run that pushed its branch but opened no PR leaves that branch on GitHub.
  The next start may propose the same name from the base, and the agent's push then
  fails until the developer picks another name or deletes the remote branch.
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
- A PR_CLOSED task cannot be restarted: the inbox offers FAILED delegated work.
  Reopening the PR brings it back.
- The dashboard does not show which searches hit the 1000-result limit, or the
  inbox's change notes for tasks it does not offer.
- `gh pr checks` handling is verified against gh's documented behavior and fixtures,
  not a live PR, because the user declined the live probe.
- Validation results are self-reported by the agent. CI is the independent
  check, and foreman gates on it.
- A search that returns 1000 results is flagged, and lost reasons aren't judged
  while one is truncated.
- A PR comment posted after a merged or closed task's last evaluation is not learned,
  because nothing watches finished PRs. Codex evaluations have no spend cap.
- The release workflow has not run: the repo has no GitHub remote yet.
- Conflict repair is checked against the stand-in `gh` and fixtures, not a live
  conflicting PR. GitHub reports UNKNOWN until it has computed mergeability, so a
  repair can start one refresh late.

## Next stage

Compare completed task usage, outcomes, and the PR history's phase times before
changing the models or budgets. For code quality, take refactor candidate 5
(`cmd/foreman` wiring) next.
