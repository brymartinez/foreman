# Architecture

```
cmd/foreman           wiring: build adapters, open the dashboard or run a task
internal/cli          argv -> Command (launch flags, knowledge export and ingest, __run);
                      no I/O beyond parsing
internal/config       config.yaml: directories, budgets, learning, default source, and
                      their validation, rejecting unknown keys
internal/routing      configured profiles and defaults, with validation
internal/render       terminal formatting of app results
internal/tui          the dashboard: pure Model and Frame, one terminal loop
internal/app          orchestration + ports.go (the interfaces below)
  ├── github          GitHubClient, RepositoryManager, and AccountProvider over `gh`
  ├── linear          IssueTracker over Linear GraphQL using a personal API key
  ├── store           TaskStore over Markdown + YAML frontmatter
  ├── worktree        WorktreeManager over `git worktree`; RemoteAuth over `ssh -T`
  ├── agent           outcome contract, role prompts, claude.go / codex.go adapters,
  │                   knowledge merger
  ├── knowledge       KnowledgeStore: per-repository learnings, evaluations, knowledge.md,
  │                   and their export, merge, and atomic replace
  ├── quota           QuotaChecker over the optional `quota-axi`
  ├── runner          spawns and checks the detached `foreman __run` process
  └── stages          resolves ~/.config/foreman/stages/<stage>.md + <repo>/.foreman/<stage>.md
internal/task         the durable model: Record, State, Delegation, Decision, Attempt
```

`app` depends only on the interfaces in `internal/app/ports.go`. Presentation stays in
`cmd/foreman`, and process handling stays in `runner` and the adapters.
`IssueTracker` carries source display identifiers and string comment IDs. Linear
queries resolve the singular `linear_team` URL to a team ID within the authenticated
workspace, then page through its open issues assigned to the authenticated user,
across every project and including issues without a project. The team has no
repository mapping. Config accepts a team URL with an optional `/overview` or
`/issues` suffix and rejects the retired `linear_projects` and `linear_teams` keys.
Issue state types `completed` and `canceled`
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
  ├─ require the exact triaged label for either start path
  ├─ gh: repo + default branch of the managed clone, fetch issue
  ├─ git: ensure worktree <root>/<owner>-<repo>/<branch> with branch slashes as directories
  ├─ store: task file, first stage (Record.NextStage), session id, launch claim
  │         (one locked write)
  ├─ pin: the active gh account (kept on restarts), worktree commit identity, SSH
  │       push account check; a mismatch is FAILED and nothing launches
  └─ spawn detached `foreman __run owner/repo#825` with the claim's token in
     FOREMAN_LAUNCH (pid + log recorded), exit

runner (one process per task; before each stage it rereads the triaged label and
confirmed scope from ordered triage decisions/correction comments, or the issue
description when there are none; a changed scope invalidates plan and reviews;
before each agent turn it writes the stage that turn
runs in: PLANNING, IMPLEMENTING, SELF_REVIEW, ADVERSARIAL_REVIEW, or OPENING_PR):
  if the gate flagged a merge conflict (IMPLEMENTING):
    turn = implementer.Resume(session, merge origin/<base>, resolve, validate, push)
    pr_opened, same PR, head moved, new head contains the base head, every
                    reported check passed -> SELF_REVIEW, then the review below
    blocked      -> NEEDS_DECISION with the agent's questions, exit
    anything else -> NEEDS_DECISION, "merge again" or "I fixed it", exit
    (a repair runner that cannot spawn or dies is also NEEDS_DECISION)
  if the verified PR head has no review (crash mid-review, or new commits):
    re-verify the PR, record its head, skip to the review below
  planner = Planner.Start or Resume(retained planner session, confirmed triage)
    planned -> validate document shape and scope fingerprints; post or edit the two
               issue comments idempotently; continue in retained implementer session
    blocked / failed -> save outcome and stop
  implementer = Start or Resume(retained implementation session, checked plan)
    checkpoint -> verify clean descendant commit and continue same session
    pr_opened / review_ready -> verify Git and PR facts; enter SELF_REVIEW
  for each reviewed revision:
    self-reviewer = fresh read-only session
      first pass: pinned base...head; follow-up: verified reviewedHead..fixedHead
      findings -> implementer.Resume, then self-reviewer reviews the fix range
    adversarial reviewer = fresh read-only session
      first pass: pinned base...head; follow-up: verified reviewedHead..fixedHead
      findings -> implementer.Resume, then return directly to adversarial reviewer
      clean -> record verdict and covered head; run final-check and CI handoff gates
  missing or uncertain session / unavailable provider -> preserve progress and pause;
    resume when available or wait for explicit role replacement
  quota and fix limits pause work; they never skip review or accept unresolved findings
  task lifecycle starts no learning or diagnostic model calls

  with review_before_pr captured on the task and no PR yet, the same four-role
  review sequence covers the local diff before the implementer pushes and opens it


app.Resume            -> disabled for legacy REVIEWED tasks; the next refresh
                         returns them to READY_FOR_HUMAN
app.Respond           -> answer each waiting question in order; after the last,
                         the record's next stage, spawn runner
                         runner: Resume(session, all answers); on ErrNoSession, a fresh
                         Start whose prompt lists all recorded decisions
app.Cancel            -> SIGTERM the runner, wait up to 30s for it to exit
                         runner: context cancelled -> agent process group stopped;
                         the failed step records FAILED, outcome cancelled, keeping
                         session, usage, branch, worktree, and PR
app.Reset             -> FAILED with no live runner only
                         gh: the stored PR (must be on the task's branch) or every
                         PR on the branch in any state (more than one, or a full
                         page of 100, refuses); any GitHub error or doubt ->
                         nothing changes
                         store: replace the record with one holding only the PR
                         in `reset`, state DISCOVERED, no delegation, and the
                         files to remove with the removal marked running
                         git: worktree remove --force, only while the path is a
                         worktree of the run's repository on the task's branch;
                         branch -D; delete the log
                         store: clear the list, or keep what failed for the next
                         start and mark the removal finished
foreman [-a|--auto|--no-auto] [-s github|linear|all] [--mentions|--no-mentions]
                      -> the dashboard, until q or ctrl+c. A launch flag overrides
                         its setting for this launch and saves nothing. The source is
                         -s, else default_source, else github; auto mode starts off
                         until a choice is saved.
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
                         legacy REVIEWED issues return to READY_FOR_HUMAN on refresh
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
                         x on a FAILED task      -> confirm, app.Reset
                         a -> toggle auto mode, saved in state.json (D144)
                         auto mode, after each successful refresh, finished start,
                           and 1s tick: app.Start with auto.agent for assigned,
                           triaged, never delegated issues, oldest first, while fewer
                           than auto.max_concurrent runs are active (D144). A selected
                           source refresh in progress or failed blocks auto starts. With routing
                           on: app.Route, then app.Start with the profile, no
                           confirmation; a wait holds the issue until the next refresh

app.InboxView         -> runner pid dead in a stage -> FAILED
                         runner pid dead in any other state -> clear the pid
                         readiness gate (gate.go), per task with a PR:
                           PR closed  -> stop the runner, PR_CLOSED
                           PR reopened -> READY_FOR_HUMAN or WAITING_FOR_CI with a clean
                             review of its head and requirements, else the stage it needs
                           review stage, no runner, clean review of an earlier head or
                             description (reopened, or a pre-stage file) -> spawn a
                             runner that reviews it
                           PR_CLOSED leaving for any reason -> recreate a worktree
                             cleanup removed, from the kept branch in the managed
                             checkout; a failed restore warns, and the PR's state
                             still applies
                         and then, per stopped task with a PR, including FAILED:
                           PR merged -> DONE
                           WAITING_FOR_CI, PR head or description changed -> the
                             review stage it needs, spawn a runner that reviews it
                           WAITING_FOR_CI: gh pr checks (read only)
                             pending              -> record, wait
                             finished             -> READY_FOR_HUMAN, ✓ CI or ✗ CI: <checks>
                           READY_FOR_HUMAN: refresh the CI result; flag new commits
                         and then, per legacy REVIEWED task with an open PR:
                           clear stored feedback and return to READY_FOR_HUMAN
                         and then, per READY_FOR_HUMAN task with an open PR:
                           mergeable CONFLICTING, (PR head, base head) never tried ->
                             record the pair, IMPLEMENTING, spawn a runner
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
  and nothing else. The gate, liveness checks, and cleanup run on each dashboard
  refresh. Rejected: a daemon, a watcher, or a runner that waits for CI.
- **The dashboard is the only interface for tasks.** Bare `foreman` opens it;
  `-c` edits the config. The other verbs are
  `knowledge export` and `knowledge ingest`, which move a repository's knowledge and
  need no stage files, and the hidden `__run`. Task subcommands were
  dropped because they duplicated the dashboard, and a piped `foreman` still prints
  usage.
- **Launch flags override; files configure.** A dashboard launch flag overrides one
  setting for one launch and writes nothing: `-s github|linear|all` over
  `default_source`, `--mentions`/`--no-mentions` over `mentions`, and
  `-a`/`--auto`/`--no-auto` over the choice the `a` key saves in `state.json`. Without a
  flag, the config key applies, then the built-in default (`github`, mentions shown).
  A missing saved auto choice means off, and `config.yaml` has no auto on/off key, so
  toggling never rewrites the config. `internal/cli` records only what was typed, and
  `cmd/foreman` resolves it against config, because the piped-usage path parses argv
  before any config loads. `-c` takes no launch flag. Command flags such as
  `knowledge export -o` and `knowledge ingest --repo --rebind --agent --dry-run` are
  operation inputs, not overrides; ingest without `--agent` uses `auto.agent`. Every
  other setting lives in `config.yaml` or `routing.yaml` and has no flag.
- **Unknown config keys are errors.** `config.Load` decodes with yaml's known-fields
  check, so a misspelled or retired key fails naming the key and line instead of
  silently keeping a default. The file must be one YAML document, so no key hides in a
  later one. An empty file, or an empty trailing document, still loads.
- **One detached runner per task.** `App.Start`, `App.Respond`, `App.Resume`, and the gate spawn
  `foreman __run owner/repo#N` with `Setsid`, so closing the terminal doesn't stop it.
  The runner inherits a locked file named for its PID in `log_dir`. A runner is alive
  when `kill(pid, 0)` succeeds and that file is locked; this guards against PID reuse
  without process-list access. Older runners without a lock still use `ps`. A dead
  runner is reaped only after the gate has read its PR, so a
  GitHub outage delays a FAILED verdict instead of risking a wrong one.
- **A launch claims the task before it spawns.** The write that moves a task into a
  runnable stage also stores `launch: {token, pid, since}`, with this foreman's pid, under the task
  lock. Any start while that pid runs gets "already starting" and changes nothing. The
  launch clears the pid when it records the runner, so a dashboard that outlives the
  launch holds nothing; from then on the live runner holds starts off. A failed spawn
  removes the claim. A claim whose launcher died is replaced by the next start. The
  runner gets the token in `FOREMAN_LAUNCH`, and `RunTask` works the task only if the
  token matches. It then drops the claim and records its pid. Without the claim, two
  overlapping starts could each spawn a runner in one worktree.
- **The runner owns its log.** `Spawn` passes the log file itself as stdout and stderr,
  and the runner timestamps its own lines. A writer in the launcher needs a pipe, which
  dies when the dashboard exits and kills the runner on its next write.
- **Stopping is clean.** Agents run in their own process group, which gets SIGTERM on
  cancel with a 5-second `WaitDelay`. `App.Cancel` waits up to 30 seconds and never
  SIGKILLs the runner, because that would leave the agent running. Any SIGTERM, SIGINT,
  or SIGHUP counts as a cancel and records FAILED with outcome `cancelled`.
- **A removed task file cancels launch and stops its runner.** Launch stops before
  spawning if a preflight read finds no record. If it disappears after spawning,
  launch waits for the new runner to stop and reports a failed stop. Runner writes go
  through `existing`, which saves nothing once the file or its delegation is gone.
  Deleting the file is a way to drop a task.

### Runner

- **The runner is a step loop.** `RunTask` takes the task over from its launch claim, runs any pending conflict
  repair, reads waiting comments, and hands a `taskRun` to its loop.
  `firstStep` picks the first step from the record alone. Each step is one method that
  returns the next:

  | Step | Does | Next |
  |---|---|---|
  | `stepAnswer` | legacy PR feedback turn, disabled by the CLI | unused |
  | `stepPickUpReview` | re-verify the PR, record its head | PR not open: implement. Else review |
  | `stepImplement` | resume planner or implementer; then validate and publish the plan | planned: implement. Checkpoint: resume implementer. Commit or PR: review. Else done |
  | `stepReview` | conflict check, self-review then adversarial review | findings: implement, then return to the finding reviewer. Clean: handoff or open PR |
  | `stepOpenPR` | adopt or open the PR for a cleanly reviewed local commit | done |
  | `stepHandover` | legacy comment handover, disabled by the CLI | unused |

  Every implementer turn goes through `runTurn` (turn, then the PR check) and
  `finishTurn` (one write through the uncancelled context, the cancel outcome, and
  usage). Each caller passes its own mapping: `applyTurn`, `applyFeedbackTurn`, or the
  conflict repair's. The code is in `internal/app/run.go` and `internal/app/turn.go`.
- **Each task runs as one GitHub account.** The first launch stores the login and
  noreply address (`<id>+<login>@users.noreply.github.com`) that `gh api user` reports
  as `account` and `account_email`. Restarts and resumes keep the pin. A task file
  from before pinning takes the active account at its next launch. `checkAccount`
  runs before every launch, in `runTurn` before every implementer turn, and before
  each checkpoint repair resume, because the agent commits, pushes, and opens PRs
  itself and `gh auth switch` can happen at any time. It fails the task when gh is on
  another login, and sets `user.name` and `user.email` with `git config --worktree`
  (`extensions.worktreeConfig`). Then, for each SSH URL in
  `git remote get-url --push --all origin`, it runs `ssh -T` against the URL's host
  with the SSH command git push uses (`GIT_SSH_COMMAND`, `core.sshCommand`, `GIT_SSH`,
  then `ssh`), so `~/.ssh/config` and any `-i` pick the same key. It fails the task
  when GitHub's greeting names another login. A failed lookup also fails the task. The
  error names both accounts and the fix. Rejected: plain `git config`, which linked
  worktrees share with the main checkout; `GIT_AUTHOR_*` environment, which a commit
  made in the worktree outside the runner would not get; and asking the agent to
  check, which cannot be enforced. Published commits are never rewritten. HTTPS push
  URLs get the identity but no push check.
- **Idempotent steps.** Creating the worktree and the task file each check whether the
  thing already exists, and so does the agent's PR step (`gh pr view` before
  `gh pr create`). That lets a crashed runner be restarted from the
  task file.

### Linear repository selection

- **Discovery does not allocate a repository.** New team issues use their Linear
  UUID as the inbox identity and stay in memory. Reconciliation reuses an existing
  delegation's repository by UUID. Legacy undelegated Linear snapshots are ignored
  during refresh, and no empty-repository task is saved.
- **Resolve before preparing work.** Selecting a new issue reads its description
  and marked triage comments for explicit GitHub repository links. A single allowed
  destination prefills a repository prompt. The developer confirms or edits it
  before agent routing or branch proposal. Missing or conflicting evidence requires
  an explicit repository. External triage documents are not fetched automatically.
  Repository filters are checked before a branch proposal prepares a checkout.
- **Auto mode uses the same resolver.** It starts only with one allowed repository
  in the issue or triage links. Otherwise the issue waits for a manual choice.
  Existing assignment, triaged-label, quota, and branch rules still apply.
- **Delegations keep their repository.** A start rejects an issue UUID already
  delegated to another repository. Task keys, filenames, branches, and runner
  arguments stay unchanged for existing delegations.

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
- **The state is the stage.** A delegated task in progress is in one of PLANNING,
  IMPLEMENTING, SELF_REVIEW, ADVERSARIAL_REVIEW, OPENING_PR, or WAITING_FOR_CI. Each
  step writes its stage before its agent turn, so the task file, the dashboard list,
  and the `s` pane show what runs, across refreshes and restarts. Any turn that can
  change the branch is IMPLEMENTING: code, review fixes, answers, and merges. Findings
  from either review go back to IMPLEMENTING, and a new change is self-reviewed before
  the adversarial review. Which step runs next still comes from the record
  (`firstStep`, `Record.NextStage`), never from the state, so a stale state cannot
  skip a step. Task files that hold the old WORKING or REVIEWING load as the stage
  the record needs (`Record.UpgradeState`), and the next write saves it. Runners
  started before the upgrade are not supported; stop them before upgrading. Rejected:
  keeping WORKING and REVIEWING with a separate stage field, which could disagree.
- **Attempts record where the time went.** An attempt is one run of work that produces
  one PR. `App.Start` opens one unless it keeps the PR or the current attempt has no PR
  yet, and stores the task log's size
  as the attempt's first byte; the runner stamps the first agent turn. `App.update`
  calls `Record.Track`, which maps the state to its phase (`PhaseOf`: planning,
  implementing with review fixes, self-review, adversarial review, opening PR, CI
  wait, paused, ended) and adds a mark when it changes, so the runner and the
  dashboard refresh both record boundaries. Attempts recorded before the stages have
  a `review` phase that holds review rounds and fixes together; history shows it as
  "legacy review and fixes" and the separate reviews as unavailable, with any stage
  time the attempt recorded after the upgrade beside it. Track also
  stamps the first READY_FOR_HUMAN as the handoff and the merge or close as the end.
  The marks tile the time, so the phase totals add up to the first turn to the first
  handoff. Rejected: a timestamp at each of the forty-odd state assignments, where one
  missed site would break the totals silently. Tasks from before attempts have none
  and are not given one, since their start cannot be known.
- **States mean one thing each.** FAILED is stopped and restartable, including a
  cancelled run; a separate CANCELLED state was rejected. A PR closed without merging
  is `PR_CLOSED`, not FAILED: it is how the developer calls off work, so it sends no
  alert, keeps the branch, and returns to READY_FOR_HUMAN, WAITING_FOR_CI, or the
  stage it needs if the PR is reopened. A closed issue takes precedence and makes the task DONE. The runner can't write to a `PR_CLOSED` task, so the stop that
  follows is never saved as a failure.
- **A reset discards the run, never the PR link.** `App.Reset` deletes only the run the
  developer confirmed: its start time, worktree, branch, and runner log must still match
  the task's run. It resolves the run's PR before anything is deleted, because a runner can die after opening a PR and before
  recording it. It then saves a record with no delegation and a `reset` block (the PR,
  its URL, head branch, and state), and only then removes the worktree with its
  uncommitted work, the local branch, and the runner log. The record lists those
  files with the removal marked running, and a start refuses the issue until it
  finishes, because the start would make a worktree at the same path. The mark
  records the owning process; once that process has exited, the mark no longer
  blocks, however long the owner ran. A start also refuses when a reset lands between its read and its write.
  The branch is deleted in the checkout the task's worktree was made from, which the
  delegation stores as `repo_dir`; for an older task without it, the worktree names its
  repository once its remote shows the task's repository. With neither, the reset deletes nothing, because a branch of that name in
  any checkout may be other work; that task can still restart. A forced removal takes
  the worktree only while it belongs to that checkout's repository and holds the task's
  branch, so another worktree made at the path is kept. A
  failed removal names what is left and keeps only that listed; a worktree removed
  while its branch was kept leaves only the branch. A start never deletes a listed
  worktree or branch, because nothing proves it is still the reset run's: it refuses
  while either is still there, for the developer to remove by hand, and drops the ones
  already gone. Only a listed runner log is removed again. Nothing else is removed without the confirmation: a start that continues an
  open PR refuses when something already exists at the worktree path it needs. Before it removes anything, a start claims the reset record under
  the task lock with a random owner token, and only that owner can save the run, so a
  second start cannot clear the path under the first one's new worktree. A start that
  gives up releases its claim. The PR link is saved before any of this. Nothing on GitHub changes. Deleting the file
  outright was rejected: auto mode would see a new issue and start it, and the PR link
  would be lost. Auto mode refuses a record with `reset`, and INBOX offers it for a
  manual start even when only tracking finds it. The next start reads the PR again. If
  it is open on the recorded branch, the worktree is made from `origin/<branch>`; a
  local branch of that name is fast-forwarded to it, or refused if it has commits the
  PR lacks. The branch name
  proposal is skipped, and the run continues that PR. If it is closed or
  merged, the start proposes a new branch and refuses the old one, whose commits are
  still on GitHub. Either way the delegation stores `prior_pr` for reconciliation.
  When the issue has a complete plan, Foreman reuses a matching clean check or
  checks it before adopting it for the new implementation session, which receives
  that plan as its task context. The `reset`
  block is cleared. The reset record keeps no plan links, attempts, decisions, or
  session. With an open PR and an absent or partial plan, the run plans before reviewing
  that PR. Every run reads the issue's planning comments before it plans.
- **Names are chosen once, before worktree creation.** GitHub tasks default to
  `<type>/<issue>-<slug>`. The type comes from the title prefix, the labels, or
  `feat`, and the slug is capped at five words and 40 characters. Linear tasks
  use the tracker suggestion when present. Without one, the default is
  `<type>/linear-<identifier>-<title-slug>`. An invalid tracker suggestion
  requires a manual choice. The manual prompt allows editing before confirming,
  and auto mode waits for a choice if the proposal is invalid or reserved by a
  prior PR. Foreman validates the chosen name before creating the worktree.
  A new branch starts from the fetched remote base. Its worktree is
  `<worktrees_dir>/<owner>-<repo>/<branch>`, with each branch slash as a directory
  separator. Restarts keep the stored branch and worktree path, including paths
  from the older flat convention.

### Agents

- **Claude and Codex are interchangeable.** The configured or explicitly selected profile determines the implementer. The
  adversarial role uses its configured provider and retained session; quota cannot skip it.
- **Headless turns with a structured outcome.** Each turn ends in a final message that
  matches `internal/agent/outcome.schema.json`: `planned`, `checkpoint`, `pr_opened`,
  `review_ready`, `blocked` with questions, or `failed`. `agent.ParseOutcome` is the
  only way an implementer's output
  enters the program, and it rejects questions with fewer than two options or duplicate
  labels. Reviewers have their own schema and parser, `agent.ParseReview`.
  Rejected: interactive sessions with terminal scraping.
- **Permissions.** The dangerous bypass flags are never used. Claude runs with
  `acceptEdits` and the fixed `Bash,WebFetch,WebSearch` tool list; the list is not
  configurable, because removing `Bash` broke every task. Codex implementation turns
  use a `foreman-git` permission profile with worktree writes and network access. The
  profile grants the linked worktree's Git directory and the clone's shared Git
  directory for add, commit, fetch, and push. Permission roots are directories,
  including when Git's top-level files already exist. All Codex turns explicitly
  set `approval_policy="never"`. Reviews stay read-only, with Git diff and file
  reads available without human approval.
- **The agent pushes and opens the PR.** Foreman then checks with `gh` that the PR is
  open, in the task's repo, from the task branch, against the task's base, and records
  the verified number and head. A PR the developer retargets keeps its new base.
- **Resume retained implementation context.** Answers, PR comments, and review
  findings resume the implementation session when present. A missing or uncertain session pauses the role with progress preserved. Resume it
  when available or wait for explicit role replacement; other provider errors stop the turn.
- **Planning is its own retained role.** The planner receives the issue description and
  confirmed triage decisions, writes Requirements and Work plan, and checks both against
  that scope before returning `planned`. Foreman keeps document shape validation,
  source fingerprints, and idempotent comment publication in Go. The implementer then
  receives the checked documents in its retained session. It follows the implementation
  plan in order and tests each behavior change first. If the plan is incomplete, conflicts
  with the code, or cannot be followed, it reports `blocked` instead of revising the plan,
  adding scope, changing expected behavior, or skipping work. A changed confirmed scope
  returns to the planner and implementer before review.
- **No task-triggered model calls sit outside the four roles.** Automatic learning and
  diagnostic calls do not run in the task lifecycle. Standalone knowledge commands
  remain separate operations.
- **Models and effort are set per step:** `plan_model`, `model` (implementation, fixes,
  merges, PR creation), and `review_model`; Codex also takes reasoning effort
  per step. Each turn logs an `agent:model:step` line. The chosen profile fixes provider and model settings across every role and retry.

### Profiles and quota

- **Profile choice is deterministic.** A manual explicit choice or the configured
  default supplies the profile. No model call selects a profile or routes in prose.
  The chosen profile remains fixed across retries.
- **Check provider quota before each role call.** Confirmed exhaustion or absent setup
  pauses the role with its session and progress preserved. Unknown quota does not
  change the selected profile. Quota never skips review or transfers a role silently.
- **Dollar budgets cap Claude only.** Codex reports no cost and its tokens arrive only
  when a turn ends, so foreman records Codex tokens and never invents a price. The
  Codex log states that the per-call dollar setting is unsupported. Each runner call
  (including planning, self-review, and adversarial review)
  saves its stage, model, times, reported usage, and error before processing its outcome.
  Totals include failed calls when usage was reported. Claude input includes cache
  creation and cache reads; cached input is also shown separately. Codex reasoning is
  a detail of output, so input plus output is the token total. Older totals cannot be
  reconstructed from missing usage. Calls with no final provider usage keep what is
  known; Foreman does not estimate missing tokens or cost.
- **Runner limits apply across restarts.** `budgets.task_tokens` (2,000,000) caps reported
  task input plus output; `budgets.planning_tokens` (400,000) covers planning turns. Limits are checked before each call and
  can be exceeded by the last call. Cached input counts. Reaching a limit fails the
  task with the setting to change. `budgets.call_timeout_seconds` (1,800) cancels a
  call and its process group. Zero disables each limit; negative values are invalid.
  These limits use the current config. Knowledge export and ingest are separate commands.
- **Validation is self-reported.** Foreman records what the agent ran and doesn't re-run
  it, because that would run untrusted commands outside the sandbox. CI is the
  independent check. Each check carries a scope, `focused` or `full` (absent reads as
  focused). The repository's own documentation decides which checks form its full
  supported suite; Foreman never chooses them.
- **Handoff needs a full-suite result that covers the change.** `full_suite_gate` is
  copied onto every task when it starts (the CLI always enables it); a restart keeps
  it, and a task file without it keeps the workflow it started with. A `pr_opened` or
  `review_ready` turn reports `fullSuite`: `run` with the revision the suite ran on and,
  for each path changed since, documentation-only evidence; `unavailable` with an
  explanation; or null. Foreman verifies a `run` report against Git alone and stores
  it as `full_result` with the head it was checked against. It covers that head when
  the revision is a commit SHA (a ref such as HEAD or a branch moves, so it is refused)
  and is the head or its ancestor, at least one `full` check ran and every one
  passed, and every path `git diff --name-only <revision> <head>` lists (both sides of
  a rename) has an exact entry with nonempty evidence. A turn that reuses an earlier
  run lists only the checks it ran; when it names no full check, Foreman takes the
  full checks it recorded for a run on that revision, and a revision it has no
  passing run for covers nothing. Foreman cannot judge whether
  evidence is sound. The implementer judges it under the stage instructions: a path is
  documentation-only only with repository-specific evidence that it cannot affect
  behavior or the supported suite; a name, extension, or directory alone is never
  evidence, and absent or ambiguous evidence makes the path a validation input. No
  path or extension list decides this in Foreman.
- **A stored result stops covering a head when the head moves.** A turn that reports
  no result keeps the old one, which then covers nothing until a new report passes the
  check. A merge of the base branch is judged by the same rule as any change, through
  the paths it brings in. A merge that brings in a behavior or validation input, or a
  path of uncertain effect, leaves the earlier result stale; a merge that brings in
  only paths verified documentation-only keeps it reusable.
- **The first change sent to review needs a covering result; fixes do not.** An
  initial `review_ready` or `pr_opened` without one is resumed once in the same
  session to supply it, and a second miss fails the task. Review fixes, answers,
  merges, and conflict repairs run focused checks only, and their prompt, resumed or
  rebuilt after a lost session, says not to rerun the full suite; a report they send
  is recorded. The one rerun a changed input needs belongs to the final check after
  the review is clean.
  Checkpoints need no full result.
- **A final check runs before every handoff that lacks coverage.** Opening the
  reviewed PR, moving to WAITING_FOR_CI after a clean competing review, and every move
  to READY_FOR_HUMAN (the gate's, a reopened PR's, a REVIEWED task's whose comments are
  cleared or answered, and an answer turn's that pushed nothing) each require a stored result that covers the head. Without
  one, the task goes to IMPLEMENTING for a final-check implementer turn (the gate
  launches its runner). The prompt names the head, why the result does not cover it,
  and the paths changed since the last full run. The agent reports the earlier run
  with evidence for every path, or runs the suite; a failure is fixed in the same
  turn. A turn that keeps the head and covers it hands off without another review; a
  new commit goes through review again. The turn gets the same one-resume rule. Three
  final checks per handoff, then the task fails with the reason. A final-check turn
  that reports `failed` or `blocked` stops immediately, preserving its failure
  summary or pending decision. A pending local PR opening cannot restart that turn.
- **An `unavailable` suite permits handoff with its explanation.** It covers any
  head, because the repository has no suite to rerun. The dashboard's task pane, which
  shows each check's scope and the full-suite state, and the PR-opening turn carry the
  explanation; the agent still reports the relevant checks it ran.

### Stage instructions

- **The developer's workflow lives in stage files**, not in Go. `plan`, `implement`, `self-review`, `review`, and `pr` each resolve `stages/<stage>.md` next to the config file, then the
  committed `.foreman/<stage>.md` in the task's worktree. Foreman never parses them and
  never reads CLAUDE.md or AGENTS.md.
- **Repository PR conventions take precedence.** Resolving `pr` also reads the first
  nonblank `pull_request_template.md` or `PULL_REQUEST_TEMPLATE.md` in `.github/`,
  the worktree root, then `docs/`. An unreadable template fails resolution. The
  template is supplied verbatim after the PR stage guidance in implementation and
  correction prompts. The agent preserves its sections, fills requested evidence,
  reports missing verification, and checks its proposal against the template before
  returning it. Global and repository `pr.md` guidance is fallback only where the
  template and repository rules leave a choice. Without a template, the existing
  global-then-repository guidance is unchanged. The agent still reads `AGENTS.md`
  and selects any named template when the repository uses multiple templates.
  Foreman does not parse Markdown or judge template compliance.
- **The five global files are required.** The dashboard and the runner fail naming the
  missing files; `foreman -c` and a piped `foreman` don't check them. `make config` (`scripts/install-stages.sh`) installs the defaults from
  `defaults/stages/` and never overwrites an installed file. It names each installed
  file that differs from its default and prints the `cp` command that replaces it.
- **The shipping config is an installed file.** `defaults/config.yaml` carries the
  selected model and budget settings into `make config` and release archives. Install
  it only when `config.yaml` is absent, without replacing a user's file. The Go
  defaults still resolve omitted keys and support running without an installed file.
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

- **Each task uses four roles.** The planner has one retained session. It receives
  confirmed triage and returns Requirements and Work plan after checking both against
  that scope. The implementer has one retained session across fixes, questions,
  checkpoints, merge repairs, final checks, and publishing. Both reviewers use
  a fresh read-only session for each pass. Foreman records the planner and
  implementer sessions and each pending action. Retried reviews use the saved
  diff range when it still matches the current revisions.
- **Session uncertainty pauses safely.** If a session is missing, its launch is
  uncertain, or its provider is unavailable, Foreman preserves progress and explains
  the reason. Work resumes when the session is available or the user explicitly
  replaces that role. Older task records remain readable; ambiguous recovery asks for
  a dashboard decision.
- **Both reviewers start broad, then check the fix.** The first pass reads the full
  pinned `base...head` diff. After a fix, Foreman uses `reviewedHead..fixedHead` only
  when Git verifies the earlier head is an ancestor and the base remains valid.
  Retargets, rewritten history, and empty or missing ranges start a new full baseline. The
  review prompt contains the role's stage instructions and the pinned diff command.
  Reviewers can read relevant code. The plan, implementation transcript, and earlier
  reviews stay outside their context.
- **Findings return to their reviewer.** The retained implementer accounts for every
  self-review finding as fixed or rejected with evidence tied to its round. The
  self-reviewer checks the fix range. After an adversarial finding, the implementer
  verifies that every finding is resolved before Foreman sends the fix diff directly
  to a fresh adversarial reviewer.
  A changed plan or invalidated base restarts at self-review. Only an explicit clean
  adversarial verdict advances the task.
- **No findings are waived.** Quota and fix limits pause work; they cannot skip a
  review or accept unresolved adversarial findings. If final validation changes code,
  the changed revision returns to the review that covers it. `review_rounds` (1) sets
  passes over a review diff and `fix_rounds` (3) sets reviewed fix attempts before a
  pause for the developer. There are no automatic learning or diagnostic model calls
  during a task.
- **Quota and profile choice are deterministic.** Foreman uses an explicitly selected
  profile or the configured default. It does not ask an agent to choose a profile or
  route itself in prose. Quota checks may pause a role; they never waive review.
- **Both reviewers are read-only.** Claude gets `Read, Grep, Glob` and read-only git.
  Codex uses its read-only permission profile. Reviewers report concrete defects and
  explicit limitations. A review that cannot inspect its assigned diff cannot advance.
- **Confirmed scope and publication remain verified.** The planner validates the
  documents against triage; Go retains document validation, fingerprints, and
  idempotent comment publication. Foreman checks the PR description separately.
  Screenshots requested or captured are attached in a marked PR comment after the PR
  exists, with retries checking for an existing comment first. Existing PR creation,
  branch verification, and repository PR conventions remain unchanged.
- **A code review covers a revision.** Foreman verifies review coverage before
  handoff. Changed heads and retargeted PRs need self-review then adversarial review.
  A changed checked scope or plan returns to the planner and implementer before
  review. Foreman flags new commits on a `READY_FOR_HUMAN` PR for the developer. A
  crash resumes the saved role and action.
- **Conflicts are checked before every review** with `git merge-tree` on
  `origin/<branch>` and `origin/<base>`, which writes nothing (needs git 2.38+).
  The implementer merges the base, never rebases or force-pushes. After two turns that
  still conflict, the developer is asked.

### CI and handover

- **CI belongs to the repo owners.** Foreman reads `gh pr checks --json` and never runs,
  configures, or fixes CI. Every check gates, not just required ones; failing beats
  pending beats passing, and no checks passes as `✓ CI (no checks)`. A clean review
  with finished CI hands over even when CI failed, with the failing checks listed.
- **PR feedback replies are disabled.** Foreman leaves PR conversation comments and
  review summaries for the developer to handle on GitHub. The runner does not start
  a reply turn at handover or on restart, and the dashboard does not turn new PR
  comments into REVIEWED tasks. A legacy REVIEWED task returns to READY_FOR_HUMAN on
  refresh; a restarted task whose only pending work is old feedback returns to
  WAITING_FOR_CI until CI finishes, without an agent turn. Reviews still read PR comments
  as history.
- **A handed-over PR that conflicts is repaired once per (PR head, base head) pair.**
  Every attempted pair is kept, so none is tried twice. A repair is accepted only if
  the head moved, contains the base head, and every reported check passed; anything
  else asks the developer.
- **Cleanup never loses work.** `git worktree remove` without `--force`, so a dirty
  worktree stays. The local branch is deleted (`-D`, for squash merges) only when its PR
  merged. If branch deletion fails after worktree removal, cleanup keeps the task pending
  and retries deletion from its recorded repository checkout. A missing local branch
  counts as deleted; an unknown or unusable checkout leaves cleanup pending and reports
  that the worktree is gone but branch status is unknown. A branch checked out in
  another worktree stays there until Git allows its deletion. The current
  `cleanup.delete_merged_branches` setting controls each retry. Unless `cleanup.on_done`
  is off, each refresh cleans the worktrees of DONE
  and PR_CLOSED tasks once their runner has stopped. A PR_CLOSED task whose PR reopens or
  merges gets its worktree back from the kept branch first, so a reopened PR can resume
  and a merged one has its branch deleted by the next cleanup. A runner launched for a
  task whose worktree was removed recreates it too. There is no manual cleanup: a FAILED worktree is the developer's to remove.

### Inbox

- **Four searches,** open items only: assigned issues, mentioning issues, mentioning
  PRs, and review requests, each up to gh's maximum of 1000. A search that hits 1000 is
  reported as truncated, and lost reasons ("no longer assigned to you") are judged only
  when every search was complete.
- **INBOX is work to start:** issues never delegated, plus delegated ones in FAILED,
  and reset ones.
  Work an agent holds stays in DELEGATED WORK, so an issue is never delegated
  twice. `mentions: false` hides mention-only items but never a delegated task that
  needs the developer.
- **Every start requires current triage.** A never-delegated, open, assigned issue needs
  the exact `triaged` label for auto mode or manual start. Without it, the inbox shows
  "awaiting triage". Labels come from the selected tracker on each refresh, including
  tracked-item lookups; they are not task-file state. Auto mode uses them only after a
  successful selected-source refresh. Starting a refresh or failing one blocks auto starts from
  the retained inbox until another refresh succeeds. The auto start's existing issue
  fetch checks the label again through the issue's source before making a worktree,
  so removal after the refresh also stops the start. Manual starts check it at the
  same boundary. Dashboard start and routing requests carry the source-qualified key from
  the selected inbox item, including Linear's stable issue ID. Routing reads the
  issue and recent comments from that tracker. In-flight starts and auto routing
  retries use the same key to release slots and retain routing selections.
  A Linear task uses Linear's suggested branch name when available. Without a
  suggestion, Foreman uses the display identifier and title. GitHub tasks
  use their numeric issue name by default.
- **Runner issue source.** A runner reads the issue through the source stored on
  the task. Planning documents are posted through that source and retain its
  comment IDs for later edits. A Linear task still opens its PR on GitHub; the
  implementation prompt requires its Linear identifier in the PR title and its
  issue URL in the body, with no GitHub closing keyword. GitHub tasks retain
  their closing keyword.
- **Repository and source filters.** A nonempty `include_repositories` shows only those
  repositories and overrides `exclude_repositories`; otherwise the exclude list hides
  its repositories. GitHub discovery remains independent of the Linear team. New
  Linear issues have no repository until delegation, so repository filters do not
  hide them. The resolved source (`-s`, else `default_source`) limits
  inbox discovery to `github` or `linear`; `all` becomes the empty source, which
  selects both. A missing Linear key blocks startup only when the source is `linear`
  or `all` and `linear_team` is configured. A failed source leaves successful results and a warning, while
  auto mode waits for all selected sources to refresh successfully. The inbox groups
  selectable issues under GITHUB and LINEAR. `App.InboxSources` drops filtered items before
  `Reconcile`, so the configured view writes no records for them and reports no waiting
  task from them. Tracked lookups skip items outside the repository filter. Within the
  repository view, delegated tasks refresh through their stored source even when the inbox
  selects another source, and the selected source still controls which issues appear in
  INBOX. DELEGATED WORK lists the stored tasks from the selected source, and a record
  with no source counts as GitHub. Refresh and runners still cover every task, and runs
  from a hidden source count toward auto.max_concurrent. Linear completed and canceled
  issues map to closed; a stopped task becomes DONE on refresh. A merged GitHub PR also completes a task.
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
  goroutine; quitting waits for a `Start`, `Cancel`, `Reset`, or `Respond` in flight.
- **Reset is offered only where it can act.** `x` and its footer hint appear for a
  FAILED selection; on a narrow footer the hint stays and later hints drop. The
  confirmation replaces the panes with the task file's path, the runner log's path,
  the worktree's path with its uncommitted or untracked work, and the local branch's
  name, wrapping paths at any column. It shows the run copied when `x` was pressed,
  and `y` resets that run, so a refresh cannot swap in another run. When the list does not fit the terminal, it
  says so and `y` does nothing; below the dashboard's minimum size `y` does nothing
  either. App.Reset checks for a live runner,
  because a FAILED task's pid stays recorded until a refresh sees it gone.
- **Decisions are answered in the lower pane through `app.Respond`,** which resolves an
  option label, free text, or an empty answer (the recommendation). The
  prompt is its own mode: every rune is answer text, so an answer can start with `q`
  or `j`. It reads the question from the task record on each frame, so it always shows
  the oldest unanswered one. It takes half of the body, because a question is read in
  full before it is answered, and the answer line is always its last row. Opening it
  pins the cursor to the task, so a new delegation cannot move it mid-answer.
- **Manual branch proposals use a focused prompt.** The model keeps the complete
  rationale, uncertainty, scroll position, and rune-index branch cursor separately.
  The frame wraps the rationale across the available columns and shows its line range
  above a fixed branch field and action hint. Up/Down and PageUp/PageDown scroll the
  rationale. Left/Right, Home/End, Backspace/Delete, and typed runes edit the name at
  the cursor; the field windows long names by terminal columns. Enter still trims and
  validates the name, and Esc cancels.
- **The log pane pages by byte offset.** Opening a log reads its last 128 KiB
  (`logChunk`); each tick reads only the bytes past the loaded end, so lines loaded
  by scrolling back stay. Scrolling past the oldest loaded line reads the chunk before
  it through `logs.Back` and `logs.Lines`, and `g` repeats that to the file start. A
  chunk is cut at a file line before cleaning, because cleaning can drop or expand a
  line. `R` shows raw lines and returns to the tail, since raw and cleaned lines do not
  line up. The scroll offset counts from the tail and grows with new lines, so a reader
  scrolled back keeps the same lines on screen.
  The runner's final log line includes the saved task state and last outcome summary,
  including failures recorded without a returned execution error.

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

- **Learning is explicit and separate from delegated tasks.** The `knowledge` commands
  export and ingest repository knowledge; the task runner does not evaluate finished tasks.
  The store is `<knowledge_dir>/<owner>/<repo>/` (default `<data>/knowledge`):
  `learnings.yaml` is the source of truth (id, kind, text, status, evidence entries,
  and history), `evaluations/<task>.yaml` holds each task's evaluation and evidence
  fingerprint, `attempts/<task>.yaml` the last attempt, `learning.lock` the
  repository lock, and `knowledge.md` the active learnings rendered for people.
  `Apply` writes the evaluation last, so a failed write leaves the task due for a
  retry. Paths
  come from a validated `owner/repo`, so one repository cannot reach another's
  directory. Writes use a temp file and rename.
- **The agent judges; Go keeps the books.** One evaluator turn reads the evidence and
  the repository's current learnings and returns proposals and revisions. Go decides
  what is stored, so the rules below are tested and a model cannot argue past them.
  Rejected: letting the agent edit `knowledge.md`, which nobody could audit.
- **Promotion rules** (`knowledge.Store.apply`):
  - A `one_off` proposal stays in the task's evaluation only.
  - A `reusable` proposal backed by a maintainer (a recorded decision or a developer's
    PR comment) is active at once. An `observed` one is a candidate until evidence
    from two tasks backs it.
  - The prompt numbers the task's answered decisions (D1...) and human PR comments
    (C1...). A fix-limit decision without a context conflict is left out, because its
    answer (another round or stop) says nothing a learning could cite. A `source: maintainer` item must cite some of them in `cites`, and
    nothing else (`Evaluation.MaintainerRefs`). Otherwise Go treats it as observed
    and logs it, so a model cannot invent a correction. The evaluator still judges
    whether the cited item says what it claims; the citations, kept in the evidence,
    make that judgment auditable.
  - A maintainer revision or retirement applies at once. An observed one applies only
    when its task started after the learning's latest evidence from other tasks; a
    learning with no other task's evidence cannot be retired by observation.
  - Observed work cannot revive a retired learning; a maintainer proposal can.
  - Re-evaluating a task first removes that task's earlier evidence and withdraws
    its earlier retire, restore, confirm (maintainer support of a live learning), or
    rewrite entries, so the new evaluation replaces them. Go then replays the entries
    still in force. The learning is retired when the last retire, restore, or
    confirm is a retire; otherwise its evidence sets its status. The text becomes
    the newest rewrite still in force, from each rewrite's stored `to` text, or the
    original text when none is left.
- **Learnings are generic.** `knowledge.Sanitize` rejects text with issue, PR, or
  commit references (including Linear or Jira keys such as `ABC-12`, but not names
  such as `UTF-8` or `SHA-256`), URLs, emails, absolute paths, tokens or keys, or more than 300 characters,
  and logs the item number and reason, never the text. `knowledge.md` carries no ids,
  issue or PR numbers, or evidence. Repository-relative paths and plain numbers
  stay, because "keep functions under 80 lines" or "internal/app depends only on its
  ports" is the kind of guidance a repository needs. Evidence notes in the YAML are
  redacted the same way.
- **The evaluator has no tools.** Claude runs with `--tools ""`, safe and
  restricted modes, and strict MCP config (with no MCP servers supplied); its
  `StructuredOutput` schema response is allowed because it only returns the answer.
  Codex has no switch that removes every tool, so its turn runs read-only with
  `--ignore-user-config`
  (no user MCP servers or plugins), `--ignore-rules`, web search off, and the shell,
  code mode, subagent, app, browser, and image features off. Either way, a turn that
  records any other tool call is discarded as a failed evaluation. Codex counts every
  completed item other than a message, reasoning, a todo list, or an error as a tool
  call, so a new tool type cannot slip through.
- **The evaluator reads every human comment on the PR**, not only those the task
  file recorded, and leaves out logins ending in `[bot]`, because the gate stops recording when the PR merges or closes. The
  merge changes the fingerprint, so the evaluation after it sees comments posted just
  before it. A comment posted after that evaluation starts nothing.
- **Self-review rounds are labelled as the implementer's own review.** The prompt
  says their findings are not the developer's word, and that a mistake specific to the
  task is `one_off`. Self-review findings alone can only make a candidate; promotion
  still needs a second task or a maintainer citation.
- **Time and cost are context, not a score.** The evaluator prompt says so, and says
  CI is not evidence. A long or expensive task is not a failure by itself.
- **Repository knowledge informs triage.** The separate triage skill researches
  relevant knowledge before publishing decisions. Planning, implementation,
  correction, and review prompts omit repository knowledge.
- **Knowledge moves between machines as one Markdown file.** `foreman knowledge
  export owner/repo` writes the rendered active guidance, then a fenced YAML payload
  after a `<!-- foreman-knowledge-payload -->` line: `format: foreman-knowledge/1`,
  the source `repo`, `next_id`, every learning with evidence and history, and every
  evaluation. `knowledge.Parse` rejects another format, unknown fields, an invalid
  repository or task id, unknown kinds, statuses, sources, or actions, duplicate ids,
  and learning text `Sanitize` would change, so an ingest stores only records the
  learner could have written. Attempts and the lock stay on their machine.
- **Ingest identity.** `foreman knowledge ingest FILE` stores the export under its
  own repository. `--repo` naming another one fails unless `--rebind` is given, which
  changes only where the records are stored.
- **Tasks match by identity, not id** (`knowledge.Prepare`). A task is its id, less
  any `.i<8 hex>` suffix an ingest added, plus its start time. The same task on both
  sides keeps one evaluation: the export's when it judged different evidence later,
  else the local one. The other side's evidence and history from that task are
  dropped, so no task backs a learning twice. A different task under a taken id is
  renamed `<id>.i<first 8 hex of sha256(id, start)>`, so a repeat ingest picks the
  same id and the suffix is recognized on the way back. Evidence and history are
  deduplicated by content. An export whose evaluations and learning entries the
  store already holds is a no-op with no agent call.
- **The agent groups; Go merges.** With learnings on both sides, one tool-less turn
  (`agent.Mergers`, `merge.schema.json`, the evaluator's restrictions) returns
  groups of `local:`/`import:` refs, each with a kind, text, and an optional
  conflict note. The prompt asks for equivalent guidance and for contradicting
  guidance on the same point to share a group, the latter with a conflict note,
  because Go compares corrections only within a group and cannot tell from text
  alone that two learnings contradict. `ParseMerge` rejects unknown, missing, or repeated refs, unknown
  kinds, and text `Sanitize` rejects. `Merge.Apply` unions each group's evidence and
  history and derives status by the promotion rules. A group keeps its first local
  member's id; others get ids from `next_id`. Evaluations' `supports` and revision
  ids are remapped, and a revision of a learning the merge dropped loses its id.
  With learnings on one side only, every learning is its own group and no agent
  runs. Rejected: letting the agent write merged records, which could invent
  evidence or status.
- **Merge conflicts.** In a group with any maintainer correction, the corrections
  decide the status, even when every member was retired by observation; with none,
  history and evidence replay by the promotion rules. When members disagree on
  retirement, or the agent flags a conflict, the winning correction's text is kept
  too; otherwise the agent's text is. That text is the learning's text when the
  correction was recorded, rebuilt from the rewrites in force by then, so a later
  observed rewrite never replaces what the maintainer backed. When the merge keeps
  that text, it adds a rewrite entry under the winning correction's task with an
  `ingest:` reason, and later merges read that correction's text from it rather
  than from other members' rewrites. That entry is a decision, not the task's own
  rewrite, so re-evaluating the task does not withdraw it; a newer rewrite or a
  later merge replaces it. A correction the same task records after the entry
  supersedes it, and its own text applies. Without a flag, corrections differ only by
  retirement, since the agent judged their texts the same guidance. A member's stance is its latest
  correction: maintainer evidence, a restore, or a confirm keeps it, and a
  retirement whose evaluation has a maintainer revision retires it. An observed
  retirement is not a correction, so it never stands in for the maintainer's
  stance, and an entry a re-evaluation withdrew (`revert-restore`, `revert-retire`)
  no longer counts. One side with a correction wins over observed work. Corrections are timed
  by the evaluation that recorded them (`Evaluation.At`, or the history entry's
  time), not by the task's start. Between corrections that disagree, the later one
  wins when it is at least a minute after the other; a closer or missing time is
  unresolved. An unresolved conflict returns `knowledge.ConflictError`
  listing both sides, the CLI exits 1, and nothing is written. A retire or restore
  entry is added under the winner's task when the replayed history would say
  otherwise. That entry is timed one second after the group's
  latest history entry, so a later merge, which orders history by time, replays the
  same decision. Its `ingest:` reason marks it as a decision, so it never counts as
  a correction itself.
- **Ingest saves all or nothing.** `App.IngestKnowledge` takes the repository lock
  without waiting, hashes `learnings.yaml` and `evaluations/`, merges, and hashes
  again before it saves; a change means another writer ran, and it fails with
  `ErrKnowledgeChanged`. `Store.Replace` stages `learnings.yaml`, `evaluations/`,
  and a regenerated `knowledge.md` in `.ingest/`, then writes `ingest.journal`
  listing the staged evaluations. Writing the journal commits the save. It then
  renames each staged file over the live one, removes evaluations the journal does
  not list, moves `learnings.yaml` last, and deletes the journal. Each step can run
  again, and `Store.Lock` finishes a committed save before anything else holds the
  lock, so a crash after the commit completes on the next learner, refresh, export,
  or ingest; a crash before it drops the stage. `Snapshot` refuses while a journal
  is pending, and `App.ExportKnowledge` takes the lock, so an export never mixes
  the old state and the new. No live file is ever missing, and a
  reader without the lock sees whole old or new files. A failed agent turn, invalid
  output, a conflict, the lock held, or `--dry-run` writes nothing.
- **Learning is a side job.** `learning.enabled: false` stops new evaluations, even
  from a learner spawned before the change, because `App.Learn` checks it too;
  triage can still read knowledge already stored.
  `learning.budget_usd` (default
  0.50) caps one Claude evaluation. Codex reports no cost, so its evaluations have no
  cap, like its other turns. Evaluation errors go to `learning.log` and never
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
  limit before routing and each model call and reads `providers[].notSetUp` and
  `providers[].quotaSemantics.effectiveAvailability`.
  A report printed with exit code 1 (one provider failed) is still read; a missing
  binary, a timeout, or no report makes quota unknown.
- **`FOREMAN_CONFIG`** points at another config file; `scripts/e2e.sh` uses it to run
  the built binary against a local origin, a stand-in `gh`, and stub agents.
  `scripts/tui-drive.py` (python3, standard library) runs the dashboard in a
  pseudo-terminal and plays keys against it, so the e2e scenarios start, answer, cancel,
  and resume work the way the developer does.
