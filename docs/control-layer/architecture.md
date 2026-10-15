# Architecture

```
cmd/foreman           wiring: build adapters, open the dashboard or run a task
internal/cli          argv -> Command (launch flags, __run, and __learn); no I/O beyond parsing
internal/config       config.yaml: directories, budgets, learning, and their validation
internal/routing      routing.yaml: profiles, rules, default, and their validation
internal/render       terminal formatting of app results
internal/tui          the dashboard: pure Model and Frame, one terminal loop
internal/app          orchestration + ports.go (the interfaces below)
  ├── github          GitHubClient and RepositoryManager over `gh`
  ├── linear          IssueTracker over Linear GraphQL using a personal API key
  ├── store           TaskStore over Markdown + YAML frontmatter
  ├── worktree        WorktreeManager over `git worktree`
  ├── agent           outcome contract, prompt, claude.go / codex.go adapters, selector,
  │                   evaluator
  ├── knowledge       KnowledgeStore: per-repository learnings, evaluations, knowledge.md
  ├── quota           QuotaChecker over the optional `quota-axi`
  ├── runner          spawns and checks the detached `foreman __run` process, and
  │                   spawns the detached `foreman __learn` process
  └── stages          resolves ~/.config/foreman/stages/<stage>.md + <repo>/.foreman/<stage>.md
internal/task         the durable model: Record, State, Delegation, Decision, Attempt
```

`app` depends only on the interfaces in `internal/app/ports.go`. Presentation stays in
`cmd/foreman`, and process handling stays in `runner` and the adapters.
`IssueTracker` carries source display identifiers and string comment IDs. Linear
queries resolve configured project URLs to project IDs, then page through issues
assigned to the authenticated user. Issue state types `completed` and `canceled`
map to closed; Linear's `Entity not found` errors map to `ErrGone` for tracked
issues and planning comments. Repository and PR operations remain on `GitHubClient`.
The detached runner receives `owner/repo#linear:<issue ID>` for Linear tasks and
`owner/repo#<number>` for GitHub tasks. Linear runner logs use the stable issue ID,
so tasks in one repository cannot share a `#0` log. The end-to-end fixture points
`FOREMAN_LINEAR_ENDPOINT` at a local GraphQL stand-in.

## Execution model

Every agent interaction is one headless turn that runs to completion and returns a typed
`agent.Outcome`. There are no interactive sessions and no terminal scraping.

```
app.Start, from the dashboard (enter on an inbox issue, or auto mode)
  ├─ gh: repo + default branch of the managed clone, fetch issue
  ├─ git: ensure worktree <root>/<owner>-<repo>/825-<slug> on branch <type>/825-<slug>
  ├─ store: task file, state WORKING, session id
  └─ spawn detached `foreman __run owner/repo#825` (pid + log recorded), exit

runner (one process per task, exits when the task leaves WORKING/REVIEWING):
  if the gate flagged a merge conflict:
    turn = implementer.Resume(session, merge origin/<base>, resolve, validate, push)
    pr_opened, same PR, head moved, new head contains the base head, every
                    reported check passed -> REVIEWING, then the review below
    blocked      -> NEEDS_DECISION with the agent's questions, exit
    anything else -> NEEDS_DECISION, "merge again" or "I fixed it", exit
    (a repair runner that cannot spawn or dies is also NEEDS_DECISION)
  if the PR has comments the agent has not answered (resumed from REVIEWED):
    turn = implementer.Resume(session, each comment, where it is, and how to reply)
    head unchanged -> mark them answered, keep CI, READY_FOR_HUMAN
    head moved     -> mark them answered, review the new head like any change
  if the verified PR head has no review (crash mid-review, or new commits):
    re-verify the PR, record its head, skip to the review below
  turn = implementer.Start(worktree, prompt)          # later: Resume(session, msg)
  until every planning document is on the issue (and no PR exists), the turn plans:
    planned      -> gh: post analysis, PRD, design, and plan as four issue comments
                    (edit the earlier comments on a re-plan), record their ids and URLs,
                    then turn = implementer.Start(new session, posted plan links)
  while implementing before a PR exists, a turn may return checkpoint:
    verify the expected branch, a clean worktree, and a new descendant commit;
    save the commit and one short next objective, then Start a fresh session
    from the issue, plan links, decisions, committed code, and that objective
    (no prior transcript); after eight checkpoints, fail instead of looping
  switch turn.Outcome.Status
    blocked      -> save each of Questions as a decision, state NEEDS_DECISION, exit
    failed       -> state FAILED, exit
    pr_opened    -> (the agent pushed and ran gh pr create) gh: confirm PR exists
                    record PR and its head commit, state REVIEWING
                    before every review: git fetch base and branch, then
                      git merge-tree origin/<branch> origin/<base>
                      clean    -> review; the branch is left as is
                      conflict -> implementer.Resume(session, merge the base, never
                                  rebase or force-push, push); recheck; after two
                                  turns still conflicting -> NEEDS_DECISION, exit
                    review = reviewer.Review(disposable read-only session,
                                             PR description + PR comments + git diff + earlier rounds)
                    clean    -> record review and the head it covered; read the PR's comments,
                and answer anything new before handing over; stay REVIEWING, exit
                    findings -> WORKING; implementer.Resume(session, findings); agent pushes
                                fixes and reports pr_opened again; re-verify; re-review
                    findings after fix_rounds reviewed fixes since the last answer ->
                                NEEDS_DECISION (A: another round, B: accept remaining), exit
  when the loop ends on a clean review and the evidence changed since the stored
  evaluation: spawn detached `foreman __learn owner/repo#825`, then exit

foreman __learn       -> one evaluation of one task; output goes to <log_dir>/learning.log
  ├─ knowledge: take the repository's lock without waiting; held -> exit 0, a later
  │             refresh retries
  ├─ build the evidence packet from the task file (decisions, review findings,
  │  phase times, cost or tokens, tool-call counts, PR outcome) and the developer's
  │  PR comment bodies from gh; no logs, transcripts, CI, or tool arguments
  ├─ evaluator turn: the task's implementer agent, no tools, a scratch directory,
  │  learning.budget_usd; agent.ParseEvaluation reads its output
  └─ knowledge.Apply: store the evaluation, fold its proposals and revisions into
     learnings.yaml, rewrite knowledge.md. A failed turn writes nothing, and never
     changes the task, its alerts, or the runner's exit code

app.Resume            -> a REVIEWED task goes back to WORKING with its branch,
                         worktree, PR, and session kept; the runner answers the
                         comments
app.Respond           -> answer each waiting question in order; after the last,
                         state WORKING, spawn runner
                         runner: Resume(session, all answers); on ErrNoSession, a fresh
                         Start whose prompt lists all recorded decisions
app.Cancel            -> SIGTERM the runner, wait up to 30s for it to exit
                         runner: context cancelled -> agent process group stopped;
                         the failed step records FAILED, outcome cancelled, keeping
                         session, usage, branch, worktree, and PR
foreman [-a|--auto|--no-auto]
                      -> the dashboard, until q or ctrl+c. A launch flag sets auto
                         mode for this session and saves nothing.
                         every 1s : app.Tasks re-reads the task files and, when open,
                                    reads the bytes the runner added to the selected
                                    task's log; no GitHub call
                         every `refresh` seconds (60 by default), and on r:
                           app.InboxView, the refresh below
                         p -> app.History in the background: the PR history
                           in place of the dashboard, until esc
                         enter on an inbox issue -> agent, context file, app.Start
                           3 (routing on, never delegated) -> context file,
                             app.Route, show the profile and reason; enter ->
                             app.Start with the profile, esc -> agent choice
                         enter on a REVIEWED issue -> app.Resume, no prompts
                         enter on delegated work -> open that task's log below the lists
                         enter on a NEEDS_DECISION task -> the oldest unanswered
                           question below the lists; enter -> app.Respond with the
                           typed answer, then the next question; after the last,
                           the task's log. esc -> close; recorded answers stay
                         an open log whose task needs a decision -> the question
                         left/right, h/l -> focus the inbox/delegated work
                         esc -> close the log
                         s -> switch the open log and the task's state (render.Show)
                         R -> switch the open log between cleaned and raw lines
                         u, pgup, ctrl-u past the oldest loaded line -> read an
                           older chunk of the log file; g -> read back to its start
                         ? -> the keys and the version; any key closes it
                         c on a running task     -> confirm, app.Cancel
                         a -> toggle auto mode, saved in state.json (D144)
                         auto mode, after each successful refresh, finished start,
                           and 1s tick: app.Start with auto.agent for assigned,
                           triaged, never delegated issues, oldest first, while fewer
                           than auto.max_concurrent runs are active (D144). A selected
                           source refresh in progress or failed blocks auto starts. With routing
                           on: app.Route, then app.Start with the profile, no
                           confirmation; a wait holds the issue until the next refresh

app.InboxView         -> runner pid dead while WORKING/REVIEWING -> FAILED
                         runner pid dead in any other state -> clear the pid
                         readiness gate (gate.go), per task with a PR:
                           PR closed  -> stop the runner, PR_CLOSED
                           PR reopened -> READY_FOR_HUMAN or REVIEWING
                           PR_CLOSED leaving for any reason -> recreate a worktree
                             cleanup removed, from the kept branch in the managed
                             checkout; a failed restore warns, and the PR's state
                             still applies
                         and then, per stopped task with a PR, including FAILED:
                           PR merged -> DONE
                           REVIEWING + clean review, PR head moved -> WORKING, spawn
                             a runner that reviews the new commits
                           REVIEWING + clean review: gh pr checks (read only)
                             pending              -> record, wait
                             finished             -> READY_FOR_HUMAN, ✓ CI or ✗ CI: <checks>
                           READY_FOR_HUMAN: refresh the CI result; flag new commits
                         and then, per task the agents are done with (READY_FOR_HUMAN
                         or REVIEWED, PR open, no live runner):
                           read the PR's comments, drop the agent's own replies
                           any unanswered -> REVIEWED, shown in INBOX
                           none left      -> READY_FOR_HUMAN
                         and then, per READY_FOR_HUMAN task with an open PR:
                           mergeable CONFLICTING, (PR head, base head) never tried ->
                             record the pair, WORKING, spawn a runner
                           UNKNOWN or MERGEABLE -> nothing
                         gh search, then a fetch of tracked items no search returned
                         issue closed, dead runner the gate could not read -> clear
                           the pid
                         cleanup.on_done, per task with a worktree and no live runner:
                           DONE, PR_CLOSED, or issue closed -> git worktree remove
                           (kept if dirty, retried next refresh); branch -D only if
                           its PR merged; with branch deletion on, a failed merge
                           check keeps the worktree for the next refresh
                         Reconcile, per item: issue closed, no runner -> DONE, including
                           FAILED and PR_CLOSED; a DONE task stays DONE if it reopens
                         learning on, per cleanly reviewed task no runner drives
                         (not WORKING or REVIEWING), at most one per repository:
                           evidence fingerprint differs from the stored evaluation, the
                           repository is not locked, no failed attempt on the same
                           evidence in the last hour -> spawn `foreman __learn`
                           (a merged or closed task never evaluated is skipped)

foreman -c            -> open the config in $VISUAL, $EDITOR, or vi, before the config
                         loads, so an invalid file can be fixed; check it after
```

Nothing runs in the background except the runner working on a task and a learner
evaluating a finished one. The readiness and liveness checks happen on each dashboard
refresh. Every task write goes through
`App.update`, which posts an alert to the Discord webhook when a task enters
NEEDS_DECISION, FAILED, READY_FOR_HUMAN, or REVIEWED, and marks a phase change on the
task's attempt.

## Design rules

These are the constraints the code is built around, with the reason for each and the
alternatives that were rejected. Change one only on purpose, and update this section in
the same commit. The commit message records why it changed.

### Processes

- **No daemon.** Nothing runs while the dashboard is closed except a task's own runner
  and a learner it started. The gate, liveness checks, cleanup, and the learning due
  check run on each dashboard refresh. Rejected: a
  daemon, a watcher, or a runner that waits for CI.
- **The dashboard is the only interface.** Bare `foreman` opens it; `-a`/`--auto` and
  `--no-auto` set auto mode for the session, `-s github|linear` selects an inbox source,
  and `-c` edits the config. The only other
  verbs are the hidden `__run` and `__learn`. Task subcommands were dropped because they duplicated the
  dashboard, and a piped `foreman` still prints usage.
- **One detached runner per task.** `App.Start`, `App.Respond`, `App.Resume`, and the gate spawn
  `foreman __run owner/repo#N` with `Setsid`, so closing the terminal doesn't stop it.
  The runner inherits a locked file named for its PID in `log_dir`. A runner is alive
  when `kill(pid, 0)` succeeds and that file is locked; this guards against PID reuse
  without process-list access. Older runners without a lock still use `ps`. A dead
  runner is reaped only after the gate has read its PR, so a
  GitHub outage delays a FAILED verdict instead of risking a wrong one.
- **The runner owns its log.** `Spawn` passes the log file itself as stdout and stderr,
  and the runner timestamps its own lines. A writer in the launcher needs a pipe, which
  dies when the dashboard exits and kills the runner on its next write.
- **Stopping is clean.** Agents run in their own process group, which gets SIGTERM on
  cancel with a 5-second `WaitDelay`. `App.Cancel` waits up to 30 seconds and never
  SIGKILLs the runner, because that would leave the agent running. Any SIGTERM, SIGINT,
  or SIGHUP counts as a cancel and records FAILED with outcome `cancelled`.
- **A removed task file stops its runner.** Runner writes go through `existing`, which
  saves nothing once the file or its delegation is gone. Deleting the file is a way to
  drop a task.

### Runner

- **The runner is a step loop.** `RunTask` claims the task, runs any pending conflict
  repair, reads waiting comments, and hands a `taskRun` to its loop.
  `firstStep` picks the first step from the record alone. Each step is one method that
  returns the next:

  | Step | Does | Next |
  |---|---|---|
  | `stepAnswer` | a turn on the waiting comments | stopped or blocked: done. Head unchanged: handover. Else review |
  | `stepPickUpReview` | re-verify the PR, record its head | PR not open: implement. Else review |
  | `stepImplement` | a plan, implement, or fix turn | plan posted or checkpoint verified: implement. Not REVIEWING: done. Else review |
  | `stepReview` | conflict check, review, escalate | findings: implement. Clean: handover. Else done |
  | `stepHandover` | read comments that arrived during the run | comments: answer. Else done |

  Every implementer turn goes through `runTurn` (turn, then the PR check) and
  `finishTurn` (one write through the uncancelled context, the cancel outcome, and
  usage). Each caller passes its own mapping: `applyTurn`, `applyFeedbackTurn`, or the
  conflict repair's. The code is in `internal/app/run.go` and `internal/app/turn.go`.
- **Idempotent steps.** Creating the worktree and the task file each check whether the
  thing already exists, and so does the agent's PR step (`gh pr view` before
  `gh pr create`). That lets a crashed runner be restarted from the
  task file.

### Task files

- **One Markdown file per source-qualified issue**, named `<owner>-<repo>-<number>.md`
  for GitHub or `<owner>-<repo>-linear-<issue UUID>.md` for Linear. Legacy files
  without `issue_source` still identify GitHub issues. Linear files also store the
  display identifier separately from the stable UUID. Planning comments store
  Linear's string ID in `external_comment_id`; legacy GitHub comments retain
  their numeric `comment_id`. The tasks directory can be an Obsidian vault.
  Frontmatter holds machine state. Saves
  replace only the keys foreman owns (snake_case) through a `yaml.Node`, so user keys
  and comments keep their place. The body is the user's: foreman's only edit is
  appending a line to `## Decisions` when an answer is recorded.
- **Writes are locked.** The runner and the dashboard can write one file at once.
  `TaskStore.Update` takes a flock on `<tasks_dir>/.foreman.lock`, re-reads, applies,
  and saves atomically. The inbox refresh merges in only fields that come from GitHub.
- **Every write goes through `App.update`**, which sends the alert when the state
  enters NEEDS_DECISION, FAILED, READY_FOR_HUMAN, or REVIEWED. Call sites never notify
  on their own. A test fails on any other `Store.Update` or `Store.Save` in `app`.
- **Attempts record where the time went.** An attempt is one run of work that produces
  one PR. `App.Start` opens one unless it keeps the PR or the current attempt has no PR
  yet, and stores the task log's size
  as the attempt's first byte; the runner stamps the first agent turn. `App.update`
  calls `Record.Track`, which derives the phase from the state (`PhaseOf`: planning,
  implementation, review and fixes, CI wait, paused, ended) and adds a mark when it
  changes, so the runner and the dashboard refresh both record boundaries. Track also
  stamps the first READY_FOR_HUMAN as the handoff and the merge or close as the end.
  The marks tile the time, so the phase totals add up to the first turn to the first
  handoff. Rejected: a timestamp at each of the forty-odd state assignments, where one
  missed site would break the totals silently. Tasks from before attempts have none
  and are not given one, since their start cannot be known.
- **States mean one thing each.** FAILED is stopped and restartable, including a
  cancelled run; a separate CANCELLED state was rejected. A PR closed without merging
  is `PR_CLOSED`, not FAILED: it is how the developer calls off work, so it sends no
  alert, keeps the branch, and returns to READY_FOR_HUMAN or REVIEWING if the PR is
  reopened. A closed issue takes precedence and makes the task DONE. The runner can't write to a `PR_CLOSED` task, so the stop that
  follows is never saved as a failure.
- **Names are computed once.** `task.WorkName` is `<issue>-<slug>` (at most 5 words
  and 40 characters, conventional prefix removed). The default branch is
  `<type>/<workName>`, with the type from the title prefix, the labels, then `feat`.
  A new Linear task uses Linear's `branchName` when provided; an invalid name stops
  before worktree creation. Without one, it uses the default branch name. The chosen
  branch is stored at the first start, so renaming the issue changes nothing. Old
  `agent/…` branches still work.

### Agents

- **Claude and Codex are interchangeable.** Every feature works with either as the
  implementer and the other as the initial reviewer. An unavailable reviewer can be
  replaced by a fresh review session with the implementer's agent.
- **Headless turns with a structured outcome.** Each turn ends in a final message that
  matches `internal/agent/outcome.schema.json`: `planned`, `checkpoint`, `pr_opened`, `blocked` with
  questions, or `failed`. `agent.ParseOutcome` is the only way an implementer's output
  enters the program, and it rejects questions with fewer than two options or duplicate
  labels. The reviewer, the routing selector, and the evaluator have their own schemas and
  parsers, `agent.ParseReview`, `agent.ParseSelection`, and `agent.ParseEvaluation`.
  Rejected: interactive sessions with terminal scraping.
- **Permissions.** The dangerous bypass flags are never used. Claude runs with
  `acceptEdits` and the fixed `Bash,WebFetch,WebSearch` tool list; the list is not
  configurable, because removing `Bash` broke every task. Codex implementation turns
  use a `foreman-git` permission profile with worktree writes and network access. The
  profile grants the linked worktree's Git directory and the clone's Git object, ref,
  log, and top-level files used by add, commit, fetch, and push. Reviews stay read-only.
- **The agent pushes and opens the PR.** Foreman then checks with `gh` that the PR is
  open, in the task's repo, from the task branch, against the task's base, and records
  the verified number and head. A PR the developer retargets keeps its new base.
- **Resume first, rebuild only when the session is gone.** Answers, findings, and
  comments resume the recorded session. Only `agent.ErrNoSession` falls back to a fresh
  turn whose prompt carries the durable state; any other resume error fails the task.
- **Planning is its own session.** Until the four planning documents are posted, and
  while there is no PR, the turn plans. The runner, not the agent, posts them as issue
  comments with a `<!-- foreman:<doc> -->` marker and edits them on a re-plan. There is
  no approval gate. Implementation then starts a fresh session, so it doesn't carry the
  planning context (about 200k tokens on one task).
- **Implementation checkpoints reset context within the implement phase.** Before a
  PR exists, an implementer may finish one tested part with `checkpoint`, one short
  next objective, and reported passing validation. Foreman accepts it only when the
  worktree is clean on the task branch and HEAD advances from the last accepted
  commit. It saves the commit, objective, and usage together, clears the old session,
  and starts a fresh one. The new prompt includes the title and issue URL, plan links,
  recorded decisions, developer context and stage instructions, commit, and objective;
  it omits the issue body and previous transcript. A retry keeps the objective only
  when the worktree still points at the accepted commit. Checkpoints are limited to
  eight per task. Planning, PR feedback, merge repair, and review fixes do not
  checkpoint. Validation remains agent reported; Foreman verifies Git state.
  If a checkpoint reports failed validation, Foreman resumes the same session
  with the failed commands and allows two repair turns. A passing checkpoint
  then follows the usual Git checks; repeated failed validation fails the task.
- **Models and effort are set per step:** `plan_model`, `model` (implementation, fixes,
  feedback, merges, PR creation), and `review_model`; Codex also takes reasoning effort
  per step. Each turn logs an `agent:model:step` line. A routed task's stored profile
  replaces the plan and implementation settings for its turns; review settings never
  change.

### Routing

- **`routing.yaml` sits next to `config.yaml`** and holds the selector, named
  profiles, ordered rules (`when`, `profiles`, `why`), and a `default`. A profile sets
  `agent` and may set `plan_model`, `model`, and, for Codex, the two efforts; omitted
  fields inherit `claude.*` or `codex.*`. Budgets, notifications, repository filters,
  cleanup, auto mode, and review settings stay in `config.yaml`. No file means routing
  is off. A file that fails to load is an error on every route, and nothing that needed
  a route starts; the rest of the dashboard still works.
- **Only starts of never-delegated issues are routed:** auto mode, and `3` in the agent
  choice. `1` and `2` override it.
- **Fit comes first.** A selector turn (Claude with `claude.plan_model` unless
  `selector` says otherwise) reads the repository, title, labels, body (6000 runes), up
  to 10 of the newest non-foreman comments (1000 runes each, 5000 in all), and the
  context file (4000 runes), and names a rule or 0. Each input has its own budget, and
  `routing.yaml` rejects rules longer than 2500 runes, so the prompt stays under 20000
  runes without cutting any input short. A failed read of the issue or its comments is
  a route error, so the selector never chooses from part of the issue. It runs with no tools in a scratch
  directory. A selector failure, an unknown rule, or 0 uses `default`, and the reason
  says so.
- **Quota only removes candidates.** Candidates are the chosen rule's profiles in the
  order listed, or `default` alone. `quota-axi` removes those whose agent it confirms
  exhausted (a fresh `all_models` reading at 0%) or not set up (`notSetUp: true`
  on a fresh provider row). Another profile runs only when the rule lists it. With
  no candidate left, nothing starts: the dashboard shows why, and auto mode keeps
  the selection and routes again after the next GitHub refresh. A stale, missing,
  or failed reading is unknown. When quota is unknown, the default runs if it is a
  candidate, else the first, and the reason says `quota unknown`. Ranking by percent
  left was rejected: it would override the cost order the rule sets.
- **The choice is stored resolved** on the delegation (`profile`, `route_reason`), so
  editing either file never changes a running task. The runner writes
  `route:<profile>:<agent>:<reason>` once, before the run's first `agent:model:step`
  line (`route_logged`). A restart with the same agent keeps the profile; another agent
  drops it. The reviewer starts as the competing agent and uses its review settings.
- Rejected: routing inside `app.Start` (a manual start could not confirm first) and in
  the runner (the task is already WORKING, so it could neither wait nor change agents).
- **Dollar budgets cap Claude only.** Codex reports no cost and its tokens arrive only
  when a turn ends, so foreman records Codex tokens and never invents a price.
- **Validation is self-reported.** Foreman records what the agent ran and doesn't re-run
  it, because that would run untrusted commands outside the sandbox. CI is the
  independent check.

### Stage instructions

- **The developer's workflow lives in stage files**, not in Go. `plan`, `implement`,
  `review`, and `pr` each resolve `stages/<stage>.md` next to the config file, then the
  committed `.foreman/<stage>.md` in the task's worktree. Foreman never parses them and
  never reads CLAUDE.md or AGENTS.md.
- **Repository PR conventions take precedence.** The implementation prompt tells the
  agent to read the repository's `AGENTS.md` and PR template before opening or updating
  the PR. Their title and body rules override conflicting global `pr.md` defaults.
- **The global files are required.** The dashboard and the runner fail naming the
  missing files; `foreman -c` and a piped `foreman` don't check them. `make config` (`scripts/install-stages.sh`) installs the defaults from
  `defaults/stages/` and never overwrites an installed file. It names each installed
  file that differs from its default and prints the `cp` command that replaces it.
- **Prompts hold protocol, not workflow:** the role, the issue, paths and branch, push
  and `gh pr` commands, `Closes #N`, the question format, the JSON outcome, and
  precedence (task context and decisions beat stage files). Two rules are built in, so
  every repo gets them with or without stage files: the test-first `## Tests` section,
  and the `## Working silently` section. A planning turn gets a short form of the test
  rule instead: the plan describes the behavior to verify, not every test name, and the
  implementer writes those tests first.
- **Agents work silently.** Text between tool calls is billed as output tokens, and
  nobody reads a session live. Neither CLI has a flag to stop it, so the implementation
  and review prompts both carry `silentWorkSection`, which asks for no progress text and
  keeps the final outcome, questions, planning documents, PR text, and findings in full.
  Resumed turns keep it because they continue the same session. The log still gets
  every event: filtering it would save no tokens and would lose diagnostics.
- **pr.md goes into the implementation turn,** because the agent opens the PR there.
  The review prompt never gets it.

### Review

- **The reviewer starts as the competing agent** in a disposable session
  (`--no-session-persistence` / `--ephemeral`) that never sees the implementer's
  session. Before each review, a fresh `quota-axi` report can move the review to
  the implementer's agent if it confirms the competitor is not set up or exhausted.
  An authentication failure from the competitor also triggers that fallback, even
  when quota is unknown. Foreman records the actual reviewer and keeps it on resume.
  The fallback is a fresh session with the agent's review settings. Reviews are
  strictly read-only:
  Claude gets only `Read, Grep, Glob` and read-only git, and Codex the read-only
  sandbox. Letting it run tests was rejected, because Claude's Bash has no sandbox.
- **The PR description is the only requirements.** The prompt carries the description,
  the PR comments, `git diff origin/<base>...HEAD`, and earlier rounds, but not the
  issue, PRD, decisions, or validation claims, so the review stays independent. A blank
  description stops the review.
- **A review covers one PR head.** Each review records the head it read. A REVIEWING
  task whose head moved is reviewed again; new commits on a READY_FOR_HUMAN PR are only
  flagged, because that PR is the developer's now. A crash mid-review restarts at the
  review, not the implementation.
- **Conflicts are checked before every review** with `git merge-tree` on
  `origin/<branch>` and `origin/<base>`, which writes nothing (needs git 2.38+).
  GitHub's `mergeable` was rejected for this: it reads UNKNOWN after a push and names no
  files. The implementer merges the base, never rebases or force-pushes. After two
  turns that still conflict, the developer is asked.
- **Two round settings.** `review_rounds` (3) is how many passes the reviewer makes over
  a PR. `fix_rounds` (3) is how many reviewed fixes run before the developer is asked to
  accept the rest or go again. The cap is checked after a review, so no fix reaches the
  developer unreviewed, and answering resets it.

### CI and handover

- **CI belongs to the repo owners.** Foreman reads `gh pr checks --json` and never runs,
  configures, or fixes CI. Every check gates, not just required ones; failing beats
  pending beats passing, and no checks passes as `✓ CI (no checks)`. A clean review
  with finished CI hands over even when CI failed, with the failing checks listed.
- **PR feedback.** The gate reads the three comment threads of each handed-over PR on
  every refresh. A comment is identified by kind, id, and a digest of its text, so an
  edit counts again; the text itself is never stored. The agent prefixes its replies
  with `<!-- foreman:reply -->` and foreman drops those, because replies post under the
  developer's own login. A turn that leaves the head unchanged only replied and goes
  straight back; one that moved it is reviewed. A run answers new comments at most
  three times before handing over. Enter on a REVIEWED issue resumes with the branch,
  PR, and session kept; a start rebuilds them.
- **A handed-over PR that conflicts is repaired once per (PR head, base head) pair.**
  Every attempted pair is kept, so none is tried twice. A repair is accepted only if
  the head moved, contains the base head, and every reported check passed; anything
  else asks the developer.
- **Cleanup never loses work.** `git worktree remove` without `--force`, so a dirty
  worktree stays. The local branch is deleted (`-D`, for squash merges) only when its PR
  merged. Unless `cleanup.on_done` is off, each refresh cleans the worktrees of DONE
  and PR_CLOSED tasks once their runner has stopped. A PR_CLOSED task whose PR reopens or
  merges gets its worktree back from the kept branch first, so a reopened PR can resume
  and a merged one has its branch deleted by the next cleanup. A runner launched for a
  task whose worktree was removed recreates it too. There is no manual cleanup: a FAILED worktree is the developer's to remove.

### Inbox

- **Four searches,** open items only: assigned issues, mentioning issues, mentioning
  PRs, and review requests, each up to gh's maximum of 1000. A search that hits 1000 is
  reported as truncated, and lost reasons ("no longer assigned to you") are judged only
  when every search was complete.
- **INBOX is work to start:** issues never delegated, plus delegated ones in FAILED or
  REVIEWED. Work an agent holds stays in DELEGATED WORK, so an issue is never delegated
  twice. `mentions: false` hides mention-only items but never a delegated task that
  needs the developer.
- **Auto mode requires current triage.** A never-delegated, open, assigned issue needs
  the exact `triaged` label for auto mode. Without it, the inbox shows "awaiting triage"
  and still allows manual start. Labels come from the selected tracker on each refresh, including
  tracked-item lookups; they are not task-file state. Auto mode uses them only after a
  successful selected-source refresh. Starting a refresh or failing one blocks auto starts from
  the retained inbox until another refresh succeeds. The auto start's existing issue
  fetch checks the label again through the issue's source before making a worktree,
  so removal after the refresh also stops the start. Manual starts skip this label
  check. Dashboard start and routing requests carry the source-qualified key from
  the selected inbox item, including Linear's stable issue ID. Routing reads the
  issue and recent comments from that tracker. In-flight starts and auto routing
  retries use the same key to release slots and retain routing selections.
  A Linear branch uses the name supplied by Linear when available, otherwise its
  display identifier and title. GitHub branches keep their numeric issue name.
- **Runner issue source.** A runner reads the issue through the source stored on
  the task. Planning documents are posted through that source and retain its
  comment IDs for later edits. A Linear task still opens its PR on GitHub; the
  implementation prompt requires its Linear identifier in the PR title and its
  issue URL in the body, with no GitHub closing keyword. GitHub tasks retain
  their closing keyword.
- **Repository and source filters.** A nonempty `include_repositories` shows only those
  repositories and overrides `exclude_repositories`; otherwise the exclude list hides
  its repositories. Mapped repositories discover issues through Linear, while their
  GitHub PR search results remain in the refresh report. Unmapped repositories discover
  issues through GitHub. `-s github` and `-s linear` limit inbox
  discovery to one tracker. A missing Linear key blocks startup only when a visible
  repository needs it. A failed source leaves successful results and a warning, while
  auto mode waits for all selected sources to refresh successfully. The inbox groups
  selectable issues under GITHUB and LINEAR. `App.InboxSources` drops filtered items before
  `Reconcile`, so the configured view writes no records for them and reports no waiting
  task from them. Tracked lookups skip items outside the repository filter. Within the
  repository view, delegated tasks refresh through their stored source even when the inbox
  selects another source, and the selected source still controls which issues appear in
  INBOX. DELEGATED WORK reads the task store, so delegated tasks remain reachable across
  source filters. Linear completed and canceled issues map to closed; a stopped task
  becomes DONE on refresh. A merged GitHub PR also completes a task.
- **Lookup failures warn and retry** next refresh without blocking other items. A 404
  or 410 on an undelegated item is reported once and then no longer checked.
- **Delegation doesn't need a checkout:** foreman keeps a clone under
  `repositories_dir` and creates worktrees from it.

### Dashboard

- **Built on `golang.org/x/term` alone.** `Model.Key` is a pure transition and `Frame` a
  pure function of the model, so nearly all of the UI is unit tested; only `Run` touches
  the terminal. Bubble Tea was rejected as ten modules in a project with one dependency.
- **An open log can pause redraws.** `Space` toggles a model flag in the task log or PR
  history detail. The terminal loop draws the paused frame once, then processes input,
  refreshes, and log reads without writing another frame or resize clear until a key
  resumes it. A non-`Space` key resumes and takes its usual action.
- **It never blocks or misdraws:** the terminal check runs before config loads, so a
  piped `foreman` prints usage and exits 0; control characters in dynamic text become
  spaces; widths are counted in terminal columns; GitHub refreshes run off the input
  goroutine; quitting waits for a `Start`, `Cancel`, or `Respond` in flight.
- **Decisions are answered in the lower pane through `app.Respond`,** which resolves an
  option label, free text, or an empty answer (the recommendation). The
  prompt is its own mode: every rune is answer text, so an answer can start with `q`
  or `j`. It reads the question from the task record on each frame, so it always shows
  the oldest unanswered one. It takes half of the body, because a question is read in
  full before it is answered, and the answer line is always its last row. Opening it
  pins the cursor to the task, so a new delegation cannot move it mid-answer.
- **The log pane pages by byte offset.** Opening a log reads its last 128 KiB
  (`logChunk`); each tick reads only the bytes past the loaded end, so lines loaded
  by scrolling back stay. Scrolling past the oldest loaded line reads the chunk before
  it through `logs.Back` and `logs.Lines`, and `g` repeats that to the file start. A
  chunk is cut at a file line before cleaning, because cleaning can drop or expand a
  line. `R` shows raw lines and returns to the tail, since raw and cleaned lines do not
  line up. The scroll offset counts from the tail and grows with new lines, so a reader
  scrolled back keeps the same lines on screen.

- **The PR history is its own full-screen view.** `p` opens a narrow PR list beside
  the selected PR's vertical phase times, with a short full-width log below. Enter
  focuses the log, Tab switches focus, and Esc returns to the list before closing
  history.
  The lower log uses the dashboard's open-log height and scroll keys.

  `App.History` builds the rows when it opens and on `r`, and follows `v`. It reads
  recorded attempts, then older PRs from the task files and from the
  `foreman: verified PR` lines of every log in `log_dir`, which also finds attempts a
  restart replaced. Each PR is read with `gh pr view` through four workers; open PRs
  are dropped, and one gh cannot read stays last as `unknown`. Older PRs have no
  handoff time, so their timing is `unavailable`. A PR's log range starts at the last
  runner start before its verified line and ends at the first start after it, so a
  failed runner in between belongs to neither. When no start separates two PRs, both
  show the whole issue log, labeled. The
  history log reuses the log pane's paging inside the attempt's byte range, and
  rereads it when a reload changes the range.

### Learning

- **Foreman evaluates each finished task and keeps what it learns per repository.**
  The store is `<knowledge_dir>/<owner>/<repo>/` (default `<data>/knowledge`):
  `learnings.yaml` is the source of truth (id, kind, text, status, evidence entries,
  and history), `evaluations/<task>.yaml` holds each task's evaluation and evidence
  fingerprint, `attempts/<task>.yaml` the last attempt, `learning.lock` the
  repository lock, and `knowledge.md` the active learnings rendered for people. Paths
  come from a validated `owner/repo`, so one repository cannot reach another's
  directory. Writes use a temp file and rename.
- **Two triggers, no daemon.** The runner spawns `foreman __learn` when its loop ends
  on a clean review. The refresh spawns it for a task with a clean review whose
  evidence fingerprint changed: review count, answered decisions, PR comments, PR
  number, head, and outcome, and the attempt's end. CI is not in the fingerprint, so a
  CI change never re-evaluates. A failed evaluation of the same evidence waits an hour.
  Merged or closed tasks that were never evaluated are not started, so turning
  learning on does not pay for old work. Rejected: evaluating inside the runner, which
  delays the handover and misses later feedback; a scheduled job, which is a daemon.
- **The agent judges; Go keeps the books.** One evaluator turn reads the evidence and
  the repository's current learnings and returns proposals and revisions. Go decides
  what is stored, so the rules below are tested and a model cannot argue past them.
  Rejected: letting the agent edit `knowledge.md`, which nobody could audit.
- **Promotion rules** (`knowledge.Store.apply`):
  - A `one_off` proposal stays in the task's evaluation only.
  - A `reusable` proposal backed by a maintainer (a recorded decision or a developer's
    PR comment) is active at once. An `observed` one is a candidate until evidence
    from two tasks backs it.
  - A maintainer revision or retirement applies at once. An observed one applies only
    when its task started after the learning's latest evidence from other tasks.
  - Observed work cannot revive a retired learning; a maintainer proposal can.
  - Re-evaluating a task first removes that task's earlier evidence, so it never
    counts twice.
- **Learnings are generic.** `knowledge.Sanitize` rejects text with issue, PR, or
  commit references, URLs, emails, local paths, tokens or keys, or more than 300 characters,
  and logs the item number and reason, never the text. `knowledge.md` carries no ids,
  numbers, or evidence. Evidence notes in the YAML are redacted the same way.
- **Time and cost are context, not a score.** The evaluator prompt says so, and says
  CI is not evidence. A long or expensive task is not a failure by itself.
- **Prompts get a bounded selection.** Planning and implementation turns, including
  fresh-session fallbacks and checkpoints, carry a `## Repository knowledge` section:
  at most 8 active learnings and 2000 characters from the task's repository, ranked
  by word overlap with the issue title, body, and labels, then by task count, then by
  recency. The section says the learnings may be wrong and lose to repository
  instructions, the issue, decisions, context, and verified facts. Review, selector,
  conflict, and merge prompts never carry it, so review stays independent. A read
  error drops the section and logs a line.
- **Learning is a side job.** `learning.enabled: false` stops new evaluations;
  prompts still read the knowledge already stored. `learning.budget_usd` (default
  0.50) caps one Claude evaluation. Evaluation errors go to `learning.log` and never
  change a task, an alert, or an exit code.

### Notifications

- **Alerts go to a Discord webhook** (`notifications.webhook_url`). The URL is a secret:
  foreman never prints it, and errors never contain it. Delivery
  errors go to `<log_dir>/notifications.log` and never change a task or an exit code.
  Native desktop notifications were dropped because they never reach a phone.

### Tooling

- **Go, standard library first.** The dependencies are `go.yaml.in/yaml/v3` (task
  files keep comments and key order) and `golang.org/x/term` (raw mode and terminal
  size). Add another only with the user's approval, and list it here.
- **Releases** are `scripts/release.sh` run by a tag workflow: static binaries for
  darwin and linux on arm64 and amd64, no Windows (the runner needs `setsid`), not
  signed. Local builds report `0.1.0-dev (<revision>)`, with `-dirty`
  when the tree had changes.
- **`quota-axi` is optional** (https://github.com/kunchenguid/quota-axi), found on
  `PATH`. Foreman runs `quota-axi --provider claude,codex --json` with a 10 second
  limit before each route and reads `providers[].notSetUp` and
  `providers[].quotaSemantics.effectiveAvailability`.
  A report printed with exit code 1 (one provider failed) is still read; a missing
  binary, a timeout, or no report makes quota unknown.
- **`FOREMAN_CONFIG`** points at another config file; `scripts/e2e.sh` uses it to run
  the built binary against a local origin, a stand-in `gh`, and stub agents.
  `scripts/tui-drive.py` (python3, standard library) runs the dashboard in a
  pseudo-terminal and plays keys against it, so the e2e scenarios start, answer, cancel,
  and resume work the way the developer does.
