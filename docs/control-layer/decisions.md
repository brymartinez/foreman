# Decision log

This file records every decision point, in order, whether the user or an agent made it.
Add an entry whenever a choice shapes behavior, architecture, scope, or tooling. Never
rewrite an old entry. If a decision is reversed, mark the old entry
`Superseded by Dn` and add a new one.

D1 to D100 are in [decisions-archive.md](decisions-archive.md). To add an entry, find the
last number with `grep '^## D' decisions.md | tail -1` and append; don't read the whole
file.

Entry format:

```
## Dn. Title
- Date / Stage / Decided by (user | agent)
- Context: why a choice was needed
- Options: what was considered
- Decision: what was chosen
- Consequence: what it changes
```

## D101. The frame counts terminal columns, not runes

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** `truncate`, `pad`, and `wrap` measured text in runes. A CJK or emoji
  title takes two columns per rune, so at 60 columns the inbox pane's text ran over
  the task pane beside it and the layout's column arithmetic was wrong.
- **Decision.** `internal/tui/width.go` reports a rune's columns: none for a combining
  mark, two in the East Asian wide and fullwidth blocks, one otherwise. The table is
  written out rather than taken from a dependency, and the ambiguous-width runes
  foreman draws itself, such as · ✓ ▸ and …, stay one column, which is what a terminal
  outside a CJK locale draws.
- **Consequence.** A wide rune that would straddle a pane's last column is dropped and
  the line is padded instead, so no line exceeds its pane.

## D102 (amends D99). The wrapped PR row is bounded by the pane's floor

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 follow-up review. Decided by the
  agent after the user's request to fix the findings.
- **Context.** D99 wraps the PR status row inside the task pane, and the pane's body
  floor is five rows. A failing CI list wrapped that row onto two lines, which pushed a
  NEEDS_DECISION task's question off the pane at 60 by 12. The pane scrolls by whole
  rows, so the question could not be reached, which is the defect D99 fixed for the PR
  row itself.
- **Decision.** `taskRowLines` (5) names the most lines a task may draw, `layoutFor`
  sizes the pane from it, and the PR row wraps only into what is left once the
  question is counted. Its last line is truncated instead of wrapping further.
- **Consequence.** A task with a waiting question shows one line of PR status and its
  question; a task without one shows two lines of PR status, which fits the reviewer,
  CI, and the review badge at 60 columns.

## D103 (amends D99, D102). The PR row drops detail instead of being truncated

- Date: 2026-09-29. Issue midsoln/foreman#1, PR #2 review round 3 by Codex. The
  reviewer found one defect; it was accepted.
- **Context.** At 60 by 12 the task pane is 32 columns wide and a task with a waiting
  question has one line left for its PR, because the identity, the agent and state, the
  title, and the question take the other four. D102 left the rest of that row to
  truncation, so `PR #912 · reviewer: Codex · CI ✗ test, lint · review round 2: 3
  findings` showed the PR number and cut both badges, which R3 asks for. No key
  revealed them.
- **Decision.** `render.TaskPR` returns the row's facts, each worded as `status` prints
  it, beside a short form of the two that grow without a bound: `CI ✗` without the
  failing checks' names and `review ✗ 3` without the round. The pane takes the first
  that fits: the whole row, then the row without the reviewer, then the states alone.
  `Frame` passes the pane's body height to `taskPane`, so the row grows to `prRowLines`
  (3) where there is room and `status` keeps printing the whole row unchanged.
- **Consequence.** At 60 by 12 a waiting task shows `PR #912 · CI ✗ · review ✗ 3`, and
  at 60 by 24 the same task shows the row `status` prints. The reviewer and the check
  names are what a narrow pane loses, not whether CI or the review passed.

## D104. The log follows the latest delegation by default

- Date: 2026-09-29. PR #2 author comment. Requested by the user.
- **Context.** The task list is ordered by attention, so its first row can be older
  than the most recently delegated issue. The log followed that first row at startup.
  Cancellation also required focus on the task list.
- **Decision.** Store the time of each `Start` in the task file. Until the user moves
  the task cursor, select the newest start on each local refresh. A new delegation
  resumes this default. `c` confirms cancellation of the task shown in the log from
  either list pane. Color the status rows and messages, and draw a vertical divider
  between the lists. Existing task files without a start time remain readable.
- **Consequence.** The newest delegated issue's log appears by default, while a manual
  selection stays on its task through refreshes. A restart of a failed task becomes the
  latest start. Older task files with no start time retain their attention order among
  themselves until they are started again.

## D105. The log is a focusable pane with keyboard scrolling

- Date: 2026-09-30. PR #2 follow-up. Requested by the user.
- **Context.** Tab cycled only between inbox and tasks. Vim movement keys moved list
  cursors, while the log required Page Up and Page Down, which the user does not have.
- **Decision.** Make the log the third Tab stop. With log focus, j/k and arrows move by
  line, u/d move by page, ctrl-u/d move by half page, and g/G jump to the oldest
  available line or follow the tail. Keep Page Up, Page Down, and End as optional keys.
  Show the log keys in the footer while that pane has focus.
- **Consequence.** The log can be read with standard letter keys, and list navigation
  works as before when a list pane has focus.

## D106 (amends D104 and D105). A delegated task opens its log on demand

- Date: 2026-09-30. Issue midsoln/foreman#3. The user clarified that the log view
  should be removed and shown only after Enter on a DELEGATED WORK entry.
- **Decision.** The dashboard starts with two lists and no log. `h` and `l` focus the
  inbox and delegated work; Tab cycles between those lists. Enter on a delegated task
  opens a small log area below them. Escape closes it, and moving to another task
  closes it until Enter selects that task. The log is not a Tab stop. While delegated
  work is focused and its log is open, `u`/`d`, Ctrl+U/Ctrl+D, and `g`/`G` scroll it;
  `j`/`k` continue to select tasks. The task list shows the branch when it has room,
  and colored dividers separate the areas.
- **Consequence.** A log is never displayed merely because a task is highlighted or
  because a local refresh chose the newest delegation. The 60 by 12 layout still
  shows the selected task's PR and state when the log is open.

## D107. A closed PR ends its task in PR_CLOSED, not FAILED

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** The gate mapped a closed PR to FAILED, which is the state a crashed
  agent gets: it notified "Run failed", colored the row red, and kept the task in
  DELEGATED WORK forever. The gate also skipped any task a runner was driving, so a PR
  closed mid-run was never seen and the agent kept working on a PR that was gone.
- **Decision.** Add the state `PR_CLOSED`. The gate watches every task with a PR, not
  only the ones waiting on GitHub, and fetches the PR for each; CI is still fetched
  only for a gated task or one coming back from `PR_CLOSED`. A PR closed without
  merging records the outcome `pr_closed` with the summary "PR #N closed without
  merging; the branch and worktree are kept", and only then is the runner stopped:
  `existing` refuses every runner write to a `PR_CLOSED` task, so the stop that
  follows is never saved as a failed or cancelled run and the developer is never told
  one failed. The pid comes from that locked write, not from the list that preceded
  it, and only the pid foreman stopped is cleared, so a run started since keeps its
  runner. A runner that will not stop keeps its pid and warns; the stop is driven by
  the record's pid, so every refresh tries again. `PR_CLOSED` is not in `attention`, so
  the task leaves DELEGATED WORK in the dashboard and in `foreman status`. Nothing
  notifies the developer, who closed the PR. `foreman start` and `foreman clean
  <issue>` accept the state, as they accept FAILED, and a start from `PR_CLOSED` drops
  the closed PR's number, which would otherwise have the gate end the new run.
  A task whose runner is still alive is startable and cleanable by neither: the
  retained pid means that runner still owns the worktree and the task file, and a
  start would both share them with a second runner and lift the `PR_CLOSED` state
  that holds the first one off.
  `PR_CLOSED` also survives the issue reconciliation that follows in the same refresh:
  a closed issue turns a delegated task DONE, and DONE is cleaned automatically, so
  without that the worktree and branch this decision promises would be removed and the
  gate would stop watching for the PR to reopen.
- **Consequence.** Closing a PR is how a developer calls off delegated work, and it
  reads as that rather than as a failure. The cost is one `gh pr view` per active task
  with a PR, per refresh. The task file, worktree, and branch survive, so the work can
  be resumed or cleaned on the developer's terms.

## D108. A reopened PR returns the task to delegated work

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** D107 leaves a task in `PR_CLOSED`. A developer who reopens the PR means
  to carry on, and foreman had no path back.
- **Decision.** The gate reads the CI of a reopened PR and derives the state from the
  review it already has: READY_FOR_HUMAN when the latest review is clean, covers that
  PR, names the head GitHub reports now, and CI has finished; REVIEWING otherwise. The
  head must match explicitly, not merely fail to differ: a review from before heads
  were recorded (D63) has none, and cannot be shown to cover the reopened PR. No agent
  is started. REVIEWING with a clean review is what the existing gate promotes once CI
  finishes, so the two paths converge instead of duplicating the rule.
- **Consequence.** Reopening is enough to get the task back in the list. Work that was
  not finished when the PR closed waits in REVIEWING for `foreman start`, rather than
  foreman spending an agent the developer did not ask for.

## D109 (amends D104). Dashboard rows keep the terminal's foreground

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** D104 colored whole task rows by state. On a light terminal the greens and
  yellows washed out, and under the selected row's reverse video a colored row became a
  colored background with background-colored text, which could not be read at all. The
  colored span also closed with SGR 0, which ended the reverse video mid-line.
- **Decision.** Row text carries no color. A row names the one word to accent, its
  state, and `highlight` wraps that word after the line is truncated and padded, so the
  column arithmetic (D101) is untouched and a truncated state is simply left plain. A
  selected row is drawn from the plain line in reverse video alone. `colored` ends with
  SGR 39 and `dimmed` with SGR 22, so neither clears another attribute. Left and Right
  join `h` and `l` as pane keys.
- **Consequence.** Color now means heading, divider, message, or task state. The
  selection reads on a light theme and a dark one, because reverse video over the
  default foreground is readable by construction.

## D110. mentions and refresh are settings

- Date: 2026-10-01. Issue midsoln/foreman#5. Requested by the user.
- **Context.** The inbox always showed items found only because they mention the
  developer, which is the noisiest of the four searches, and the dashboard's GitHub
  refresh was a 60 second constant.
- **Decision.** `mentions` (default true) and `refresh` (default 60 seconds, minimum 5)
  join the config. `mentions: false` filters the finished report, dropping entries whose
  only reason is `mentioned`, rather than dropping the searches: the snapshots keep
  every reason, so "no longer mentions you" still works and the items return as soon as
  the setting does. A `refresh` below 5 fails `config.Load` with the file and the
  minimum. Local task and log rereads stay at one second, and `r` still refreshes.
- **Consequence.** The quiet inbox costs nothing in accuracy, and the refresh rate is
  the developer's call within a floor that keeps one refresh from overlapping the next.

## D111 (amends D107 and D108). Check PR state before reaping its runner

- Date: 2026-10-01. Issue midsoln/foreman#5. Review findings.
- **Context.** A runner can exit before the next GitHub refresh. Reaping it during a
  local task reread would record FAILED before the gate could see that its PR closed.
  A runner that resisted a stop could also keep writing after its PR reopened.
- **Decision.** Local rereads defer reaping tasks with a PR. `status` and `inbox`
  identify dead runners, check PR state, then reap only tasks the gate left running.
  Before a `PR_CLOSED` task returns to active work, the gate stops its retained runner
  or clears its stale pid. A failed stop leaves the task closed for another refresh.
- **Consequence.** A dead runner with a PR can remain WORKING until the next GitHub
  refresh. A closed PR records `PR_CLOSED` without a failure notification, and a
  reopened PR cannot release an old live runner into the active task.

## D112. Codex can write the worktree's git directory

- Date: 2026-10-01. Requested by the user after midsoln/foreman#5 failed when Codex
  couldn't commit its fixes. D107 to D111 are taken on that issue's branch.
- **Context.** Foreman's worktrees are linked to a clone under `repositories/`. Their
  git data lives in that clone's `.git`, outside the worktree, and Codex's
  workspace-write sandbox only writes inside the worktree (plus the temp dirs). Codex
  could edit and test but not commit, so the task failed. The probe missed this
  because its clone was under `$TMPDIR`. Claude's Bash tool runs outside a sandbox, so
  it was not affected.
- **Decision.** The Codex adapter reads the worktree's `.git` file and `commondir` to
  find the clone's `.git` and adds it with
  `-c sandbox_workspace_write.writable_roots=[...]` on `exec` and `exec resume`. A
  plain checkout adds nothing. The reviewer stays read-only. The probe checks the
  commit with the temp dirs excluded: it must fail without the root and succeed with
  it, on `exec` and `exec resume`.
- **Consequence.** Codex can commit and push from foreman's worktrees. It can write to
  the whole clone's `.git`, not just this worktree's entry, which is the same access a
  plain checkout gives. Network access for `gh` and `git push` is unchanged (D7).

## D113 (amends D111). Reap a dead runner only once its PR has been read

- Date: 2026-10-01. Issue midsoln/foreman#5. Review finding.
- **Context.** D111 reaped after the gate so a closed PR would be seen first, but it
  reaped every dead runner the pass had found. A failed `GetPullRequest` only warns and
  leaves the task WORKING, so the reap still recorded FAILED and notified. A later
  refresh could correct the state, never the notification.
- **Decision.** `gate` returns the keys whose PR it read. `reapable` keeps a dead runner
  only when its task had no PR, which takes no GitHub call to judge, or when the gate
  read that PR this pass. An unread task keeps its state for the next refresh.
- **Consequence.** GitHub being down delays a FAILED verdict instead of risking a wrong
  one. The condition is positive, so a future early return in `gate` leaves a task
  unread, and unread means unreaped.

## D114 (amends D110). Judge the gone note by the reasons the task stored

- Date: 2026-10-01. Issue midsoln/foreman#5. Review finding.
- **Context.** An item that leaves GitHub gets a CHANGED note built from the task
  record, not from a search result, so the entry carries no reasons. `hideMentionOnly`
  keeps an entry with no `mentioned` reason, so with `mentions: false` a mention-only
  item still announced itself on the way out.
- **Decision.** `Inbox` reads the stored reasons before `stopTracking` clears them and
  drops the note when the setting is off and those reasons were a mention alone. The
  check sits where the entry is built, not in `hideMentionOnly`, because the entry's
  empty reasons are what tell `render.InboxActivity` to always show a gone note.
  Filling them in would hide the note from the picker even with mentions on.
- **Consequence.** The setting covers the item's whole life, including its last note.
  The record still stops being checked either way: the setting hides the note, not the
  bookkeeping that ends the refreshes.

## D115 (amends D110 and D114). Resolve a tracked fetch's reasons before filtering

- Date: 2026-10-01. Issue midsoln/foreman#5. Review finding.
- **Context.** A search that hits its 1000 result limit can leave a tracked item out.
  `fetchTracked` then retrieves it with the synthetic `tracked` reason alone, and new
  activity puts it in CHANGED. `mentionOnly` saw no `mentioned` reason, so with
  `mentions: false` the item still printed.
- **Decision.** `storedReasons` snapshots each record's involvement before `Reconcile`,
  which calls `observe` and rewrites those records in place. `involvement` resolves an
  entry carrying `tracked` alone to that snapshot, and `hideMentionOnly` filters on the
  result. This covers NEW, CHANGED, and the dashboard's `Current`.
- **Consequence.** The setting holds when the searches are incomplete, which is when a
  mention-only item is most likely to arrive as a tracked fetch. An item foreman has
  never stored has no snapshot, so it keeps the reasons the search gave it.

## D116. Keep both worktree and issue decisions after the merge

- Date: 2026-10-01. PR midsoln/foreman#6 merge resolution.
- **Context.** The issue branch and main independently assigned D112 to different
  decisions. Both changes are needed and their references must stay unambiguous.
- **Decision.** Keep main's D112 for the Codex worktree fix and renumber the issue
  branch's later review decisions D113 to D115. Preserve both sets of changes.
- **Consequence.** Code comments and progress references identify the intended
  decision after PR #6 merges.

## D117 (amends D90). Start implementation with a fresh agent session

- Date: 2026-10-01. Foreman token usage investigation.
- **Context.** Planning and implementation shared one Claude session. On issue #7,
  the implementation inherited about 214,000 tokens of planning context and the
  session grew to about 386,000 tokens. Each later turn read that history again.
- **Decision.** After the runner posts the four plan comments, it starts a new
  implementer session from the task record and comment links. Planning questions
  still resume the planning session; later decisions and review findings resume
  the implementation session. The plan and implement stage files ask for a few
  cohesive milestones and focused checks during editing, with full validation
  after the code is complete.
- **Consequence.** Implementation no longer carries planning tool output in its
  context. The task file records the new session ID for later resumes. The local
  configuration trials Sonnet for Claude work and lowers the implementer budget
  from $100 to $25 per invocation.

## D118 (amends D85). Select the model for each agent turn

- Date: 2026-10-01. Requested during the token usage investigation.
- **Context.** Planning and implementation now use separate sessions, but both used
  the implementer's `model` setting. The reviewer had its own model setting. Codex
  reasoning effort came only from the Codex CLI configuration.
- **Decision.** Keep the task's chosen implementer for planning and implementation,
  and the competing agent for review. `claude.model` and `codex.model` select the
  implementation model, including PR creation. Add `plan_model` for planning and
  retain `review_model` for review; an empty stage model uses `model`. Codex also
  accepts `reasoning_effort`, `plan_reasoning_effort`, and
  `review_reasoning_effort`. An empty stage effort uses `reasoning_effort`, and an
  empty base effort leaves the Codex CLI default. Pass the chosen model and effort
  on fresh and resumed turns. Show the resolved values in `config --show` and write
  the fields when `config --edit` creates a new file. The PR instructions require
  Basic English and cut filler and unsupported claims.
- **Consequence.** The local configuration can use `sonnet` and `gpt-6-luna` at
  `xhigh` for planning, then `opus` and `gpt-6-sol` at `medium` for implementation
  and review. PR creation uses the implementation model. Existing configs still
  work because empty new fields use the prior model or CLI effort.

## D119. A REVIEWED state, and how a pull request comment is identified

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** A task that reached READY_FOR_HUMAN stopped listening. Nothing read the
  pull request's conversation, so a comment the developer left reached no agent.
- **Decision.** Add the state REVIEWED. Foreman reads a pull request's three comment
  threads (the conversation, the inline comments and their replies, and each submitted
  review with a body) and records each comment in the task file as its kind, id,
  author, url, time, and a digest of its text. A comment with no record is unanswered,
  and one whose digest changed is unanswered again, which is what makes an edit of the
  text count and a reaction not. The kind is part of the identity because a review id
  and a comment id come from different sequences and can be the same number. The text
  itself is not stored: it is read from GitHub whenever a turn needs it.
- **Consequence.** The task file stays small and survives a comment being deleted,
  which drops it and releases the task. A task that was already READY_FOR_HUMAN when
  this shipped has its first fetch record what is there, so comments the developer has
  already read count once, and then never again.

## D120. The agent's replies carry a marker

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** The agent answers comments through the developer's own `gh` login, so a
  reply it posts has the same author as a comment the developer writes. The issue
  requires that the developer's own comments count and that the agent's replies do not.
- **Decision.** The agent starts every reply with `<!-- foreman:reply -->`, and foreman
  drops any comment whose body carries it. Markdown hides it. This follows the marker
  `planCommentBody` already puts on the planning comments. The alternatives were
  rejected: filtering by author cannot separate the two, and having the agent report the
  ids it answered would add a field to the outcome schema for every turn of every task
  and would trust the agent's report over GitHub's record.
- **Consequence.** An agent that forgets the marker reads its own reply as new feedback.
  D123 bounds what that costs, and the developer can see the rounds in the log.

## D121. Feedback is read where the gate already reads GitHub, and resuming is its own command

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** Nothing runs between commands except a task's own runner (D5), so a
  comment can only be found when foreman already talks to GitHub.
- **Decision.** The readiness gate reads the conversation of each task whose pull
  request is open and which is READY_FOR_HUMAN or REVIEWED, with no live runner. It
  reads on every refresh rather than only when the pull request's `updatedAt` moved,
  because an edit to an inline comment is not reliably reflected there and a missed edit
  would fail the issue's acceptance criteria. `foreman resume <issue>` and the picker's
  selection call `App.Resume`, which keeps the branch, worktree, pull request, and
  session and only sets WORKING; `Start` rebuilds the delegation, so it would lose the
  session that wrote the pull request and now points a REVIEWED task at `resume`.
- **Consequence.** Three REST calls per handed-over task per refresh, for the few tasks
  that are waiting on the developer. A comment is noticed within one refresh interval,
  60 seconds by default. The runner's pid is judged by whether the process is alive, so
  a pid a killed runner left behind cannot hide a comment for good.

## D122. A feedback turn is judged by whether the pull request head moved

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** Feedback that needs no code change must come back to the developer
  without another review; feedback that needs one must go through the normal flow. The
  agent cannot be trusted to say which it was, and the outcome schema has no status for
  it.
- **Decision.** Foreman records the pull request head before the turn and compares it
  with the head it verifies afterwards. An unchanged head means the agent only replied,
  so the CI result is restored and the task returns to READY_FOR_HUMAN when the same bar
  the gate applies is met: a clean review of that head and CI that has finished.
  Anything else leaves the task under review, and the gate hands it over. The comments a
  turn was given count as answered only once that turn ends with `pr_opened`, so a run
  that failed, was cancelled, or ended `blocked` leaves them for the next one. An
  answered decision still comes first, so a run carrying one delivers it and picks the
  comments up on its way out.
- **Consequence.** No new outcome status and nothing for the agent to misreport. A
  reply-only turn costs one agent turn and no reviewer turn.

## D123. A run answers comments before it hands the task back, at most three times

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** A comment that arrives while an agent is working must not interrupt it,
  and must not be left unread when the task goes back to the developer.
- **Decision.** The runner reads the conversation again before it hands over, after a
  clean review and after a feedback turn that pushed nothing, and answers anything new
  in the same run. One run answers at most `maxFeedbackRounds` (3) times; past that it
  records the rest as REVIEWED for the developer to resume.
- **Consequence.** A comment written during a run is answered by that run. The cap is
  what stops an agent that forgot the reply marker (D120) from answering itself without
  end, and it needs no configuration to do it.

## D124. INBOX offers only work the developer can move

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** INBOX listed every issue a search returned, including ones an agent was
  already working on, which invited a second delegation of the same issue.
- **Decision.** `InboxEntry` carries the state of the run foreman has for the item.
  INBOX keeps an item it has never delegated, under the rules it already had, and a
  delegated one only in FAILED, which starts again, or REVIEWED, which resumes. The
  dashboard's row shows that state as its accent word. `render.InboxActivity` now reports
  exactly the changes the picker does not show, by asking `app.CanDelegate`, so the two
  views cannot drift.
- **Consequence.** Work in hand stays in DELEGATED WORK alone, and INBOX is a list of
  things to start. A delegated task in WORKING with new activity now reports that
  activity in the interactive view instead of being silently dropped.

## D125. The cleaned log renders Codex items, and the adapter stops printing its own

- Date: 2026-10-01. Issue midsoln/foreman#7.
- **Context.** `cleanLine` printed every Codex event as its bare type, so a run showed a
  column of the word `completed`. The substance of a run is in the event's `item`
  object, which nothing parsed.
- **Decision.** Render each item by its own type: an assistant message, a command as it
  starts and again with its exit code and the first line of its output, file changes,
  web searches, MCP calls, and item errors, plus turn failures with their message and
  each turn's token totals. Reasoning, the todo list, and `item.updated` stay in the raw
  log, which `--raw` still prints unchanged. Each detail is cut to 160 characters. The
  Codex adapter no longer writes its own `turn completed` line, because the cleaned log
  now renders that event, and two sources for one fact would drift.
- **Consequence.** A Codex run is readable in the dashboard. The event shape was checked
  against a real `codex exec --json` run on 0.156.1: the discriminator inside `item` is
  `type`, not `item_type` as the posted design assumed.

## D126 (amends D63, D110, and D124). Waiting PR feedback stays reachable

- Date: 2026-10-01. Issue midsoln/foreman#7, review round 1 of PR #10.
- **Context.** Two paths lost comments that were still waiting. With `mentions` off,
  `hideMentionOnly` dropped a delegated REVIEWED or FAILED task whose only involvement
  was a mention, so the picker and the dashboard could not offer it. A FAILED feedback
  run restarted with the other agent lost its PR number under D63, and the runner reads
  comments through that number, so the new agent never saw them.
- **Decision.** The mention filter keeps every delegated entry `CanDelegate` accepts.
  `Start` keeps the PR number, URL, and head for another agent on the same branch when
  the task has comments waiting. The new agent's first turn answers them in a fresh
  session. With no comments waiting, another agent still starts over as D63 says.
- **Consequence.** A task that needs the developer stays selectable under any setting,
  and switching agents does not drop feedback. A restart with another agent that keeps
  the PR can, if the comment read fails, pick up at the review of a head the new
  reviewer wrote. The next refresh reads the comments again.

## D127 (amends D122). A resume with no comments to answer runs no turn

- Date: 2026-10-01. Issue midsoln/foreman#7, review round 2 of PR #10.
- **Context.** A resumed REVIEWED task whose comment was deleted before the runner read
  it, or whose comments could not be read, had no feedback for its first turn. Its
  clean review left nothing pending, so `RunTask` ran an implementation turn that
  could redo the issue and start another review.
- **Decision.** When the task's clean review covers its PR head, `unanswered` ends the
  run itself. If the read worked and nothing is waiting, the task goes back to
  READY_FOR_HUMAN, or to REVIEWING when CI has not finished, so the gate hands it over.
  If the read failed, the task stays REVIEWED with its comments waiting, for the
  developer to resume again. A task with open findings or an unreviewed head keeps its
  normal flow, because the run has that work to do anyway and reads the comments
  again before it hands over (D123).
- **Consequence.** No agent turn runs without comments to answer when the code is
  already reviewed. A GitHub outage during a resume costs the developer one more
  resume.


## D128 (amends D90, D119, and D122). Merge PR feedback with fresh implementation sessions

- Date: 2026-10-01. Merge resolution for PR #10 against main.
- **Context.** Main now starts implementation in a fresh session after planning (D117),
  while issue #7's branch resumed the planning session after posting the plan. Both
  branches also used D117 and D118 for unrelated decisions.
- **Decision.** Keep the fresh implementation session and stage-specific model from
  main. Keep issue #7's feedback loop: a REVIEWED task resumes its current
  implementation session with the comments, or starts from durable task context if
  that session is gone. Renumber issue #7's decisions D119 through D127 and keep
  both branches' decision histories.
- **Consequence.** Planning history does not burden implementation or feedback turns,
  and issue #7's comments remain actionable after the merge.


## D129. Concise planning comments and PR descriptions

- Date: 2026-10-01. Issue midsoln/foreman#13.
- **Context.** The four planning comments and the PR description held useful detail but
  took too long to read, and the PR often repeated the plan. The writing style comes
  only from the default stage files, which `internal/agent/prompt.go` embeds.
- **Decision.** Edit the defaults only. `plan.md` gains a Style section: each document
  opens with a `**Summary.**` of two or three sentences, keeps references, constraints,
  decisions, and test steps, and cuts repeated background. `pr.md` limits the body to
  about 25 lines and adds a Plan item that links the four planning comments instead of
  repeating them. `implement.md` rule 20 asks every written output to lead with the
  result. No Go change.
- **Consequence.** New installs and new runs get the shorter style. `make setup` does
  not overwrite installed copies in `~/.config/foreman/stages/`, so existing users copy
  the new defaults by hand. Existing comments and PRs stay as they are.


## D130. Repair a merge conflict on a handed-over PR once per head pair

- Date: 2026-10-02. Issue midsoln/foreman#17.
- **Context.** A READY_FOR_HUMAN PR can start conflicting with its base after handoff.
  Nothing read mergeability, so the developer found out at merge time. Issue #12 covers
  conflicts before the competing review and is out of scope here.
- **Decision.** The gate asks `gh pr view` for `mergeable`. When an open
  READY_FOR_HUMAN PR with no live runner and no comments waiting reports CONFLICTING,
  the gate reads the base head with `gh api repos/<repo>/git/ref/heads/<base>`. In one
  locked update it records the PR head and base head on the delegation
  (`conflict_head`, `conflict_base`), sets `conflict_pending`, and moves the task to
  WORKING. Then it launches the runner. UNKNOWN or MERGEABLE starts nothing. The
  runner resumes the implementer session with `agent.ConflictMessage`: fetch the base,
  merge it (no rebase, no force push), resolve, validate, push. A lost session gets a
  fresh turn whose prompt carries the same rules. Foreman accepts the repair only if
  the PR is the same one, its head moved, and `git merge-base --is-ancestor` finds the
  recorded base head in the new head after `git fetch origin`. A verified repair goes
  to REVIEWING and through the normal review, CI, and handoff. Anything else ends in
  NEEDS_DECISION: a `blocked` turn keeps its questions, and a failed turn, a runner
  error, or a failed check adds one question (A: merge again, B: I fixed it). A
  cancelled repair stays FAILED. The pair is never tried again automatically. A
  `start` that keeps the PR also keeps the pair, and a repair a dead runner left
  pending runs in the restarted run.
- **Consequence.** One agent run per conflicting (PR head, base head). A new commit on
  either branch allows one more attempt. The branch and worktree are never removed
  by a repair.


## D131 (amends D130). Keep every attempted pair, and fail a repair on failed checks

- Date: 2026-10-02. Issue midsoln/foreman#17, review round 1 of PR #18.
- **Context.** D130 kept only the last (PR head, base head) pair. A base that moved
  from A to B and back to A, with the same PR head, made the first pair look new, so
  the gate tried it a second time. D130 also accepted a repair whose `pr_opened`
  reported a failed validation command, and sent it to review and handoff.
- **Decision.** The delegation keeps `conflict_attempts`, a list of every attempted
  pair, oldest first. The runner repairs the last one. `ConflictAttempted` checks the
  whole list. `checkRepair` also rejects a repair when any reported validation entry
  has `passed: false`, so the task goes to NEEDS_DECISION and names the failed commands.
  `conflict_head` and `conflict_base` from D130 never shipped, so no file carries them.
- **Consequence.** No pair is tried twice. The list grows by one entry per automatic
  repair, and each entry needs a new head pair and an agent run.


## D132 (amends D130). A repair runner that fails to start or dies needs a decision

- Date: 2026-10-02. Issue midsoln/foreman#17, review round 2 of PR #18.
- **Context.** After the gate records a repair attempt, `launch` marked the task FAILED
  when the runner could not spawn. A runner that died mid-repair was reaped as FAILED.
  Neither path asked the developer, and both left `conflict_pending` set, while the
  PRD wants every failed repair in NEEDS_DECISION with its blocker.
- **Decision.** `failRepair` holds the NEEDS_DECISION path: it clears the pending flag,
  records the reason, and adds the question (A: merge again, B: I fixed it). The
  runner's failed repairs, `launch` with a spawn error, and `reap` for a dead runner
  all use it when a repair is pending. A cancelled repair still stays FAILED (D130).
- **Consequence.** Every repair that does not finish reaches the developer as a
  decision. The attempt stays spent, so a refresh does not retry it.

## D133 (amends D63 and D78). Resolve PR merge conflicts before each review

- Date: 2026-10-01. Issue midsoln/foreman#12.
- **Context.** The runner started a review whenever a PR head had none, without checking
  the PR against its base branch. A conflicting PR got a clean review, and the
  conflict resolution it still needed was never reviewed.
- **Decision.** `RunTask` calls `resolveConflicts` right before every review pass: the
  first, one after a fix, one after answered PR feedback, one after a decision, and one
  in a resumed run. It verifies the PR, takes the base GitHub reports for it, and calls
  `WorktreeManager.MergeConflicts`. `worktree.Git` fetches the base and the branch,
  then runs `git merge-tree --write-tree --name-only` on `origin/<branch>` and
  `origin/<base>`. That merge runs in memory, so a clean base update writes no commit
  and changes neither the branch nor the worktree. GitHub's `mergeable` field was
  rejected: it reads `UNKNOWN` for a while after a push and names no files. On a
  conflict, the implementer gets one turn (`agent.MergeMessage`, or a `## Merge
  conflicts` section in a fresh prompt) to merge `origin/<base>` into the branch,
  never rebase or force-push, run the checks, commit, push, and report `pr_opened`.
  That turn records no review, so it uses no fix round, and the review that follows
  uses the normal settings. If the agent cannot resolve a file safely it leaves the
  merge in progress and reports `blocked` with the files. After two merge turns that
  still leave conflicts, foreman asks the developer itself (A: I resolved the files in
  the worktree; B: try again). A failed check fails the task. The gate is unchanged: it
  compares only PR heads (D63), so a base-only change after a clean review starts no
  run.
- **Consequence.** Every review covers a PR that merges into its latest base. Each
  pass costs one `git fetch` and needs git 2.38 or newer. The stand-in agent in
  `scripts/e2e.sh` now pushes its branch, because the check reads `origin/<branch>`.

## D134 (amends D31, D67, and D133). Review round 1 of PR #16

- Date: 2026-10-02. Issue midsoln/foreman#12, review round 1 of PR #16.
- **Context.** The reviewer found three gaps. `verifyPR` rejected a PR whose base no
  longer matched the task's, so a maintainer who retargeted the open PR failed the
  task before the conflict check. A blocked merge turn's question could leave out
  conflicting files. And a resumed review whose PR was no longer open ran the
  implementer instead of failing.
- **Decision.**
  - `verifyPR` still requires the task's base for a new PR (D31). The task's own PR
    may target another base. `resolveConflicts` checks against the base GitHub
    reports and records it as the task's `BaseRef`, so later checks, prompts, and the
    reviewer's `git diff origin/<base>...HEAD` use it. `Start` keeps the base of a PR
    it keeps.
  - A blocked merge turn's questions get the conflicting files and the worktree
    appended to their context when the agent's text does not name every file.
  - A resumed review whose PR is no longer open still runs the implementer, as D67
    decided. That happens before the review pass, so the conflict check is not
    involved, and the gate records a closed PR as PR_CLOSED on its own (D107).
    Changing it is outside issue #12.
- **Consequence.** A retargeted PR is checked and reviewed against its new base. The
  developer always sees which files to resolve.

## D135 (amends D134). Always list the conflicts in a blocked merge question

- Date: 2026-10-02. Issue midsoln/foreman#12, review round 2 of PR #16.
- **Context.** D134 added the conflict list only when the question's text did not
  contain every file name. A substring search treats `a.go` as named when the text
  names only `dir/a.go`, so the developer could miss a file.
- **Decision.** `withConflicts` always appends the conflicting files and the worktree
  to each question from a blocked merge turn.
- **Consequence.** The list can repeat files the agent already named. The developer
  always sees every file to resolve.

## D136 (amends D130 and D133). Merge issue #17's conflict repair into issue #12's branch

- Date: 2026-10-02. Merge resolution for PR #16 against main.
- **Context.** Main merged issue #17 (D130 to D132), which repairs conflicts on a
  handed-over PR from the gate. Issue #12's branch had used D129 to D131 for its
  check before each review, and main had used D129 for issue #13.
- **Decision.** Keep both flows. The gate's repair (D130) handles a READY_FOR_HUMAN
  PR; its repaired head then goes through `resolveConflicts` before its review like
  any other pass (D133). Renumber issue #12's decisions to D133 through D135.
  `implement` takes both the repair from the delegation and the merge request. The
  e2e repair stub now pushes its merge, because the check reads `origin/<branch>`.
- **Consequence.** A conflict found before a review and one found after the handoff
  both reach the implementer, and both are reviewed.


## D137. The dashboard lower pane switches to the show view

- Date: 2026-10-01. Issue midsoln/foreman#11.
- **Context.** After a failure the developer had to leave the dashboard and run
  `foreman show <issue>` to see the outcome, failing checks, and review findings.
- **Decision.** With the lower pane open, `s` switches it between the log and the text
  `render.Show` writes for the selected record. It works with either list focused.
  The text is rendered on each frame from the record in memory, so it needs no new
  port and follows every reread of the task file. Lines are cut at the pane width by
  display column, not at spaces, because show rows are paths and URLs. The show view
  opens at its top; returning to the log follows its tail. The pane opens on the log,
  and closing it or picking another task returns it to the log. Enter on an open pane
  keeps the current view. The existing scroll keys scroll whichever view is open.
- **Consequence.** The developer reads a task's whole state without leaving the
  dashboard. At 60x12 the pane shows one line at a time, but every line is reachable.

## D138. Foreman's prompt carries a test-first, readable-test rule

- Date: 2026-10-02. Issue midsoln/foreman#19.
- **Context.** The only test rule lived in `defaults/stages/implement.md`: "Add or update
  tests when behavior changes." Stage files are user-owned, `make config` never
  overwrites them, and a repo without them gets nothing. Nothing asked for test-first
  work, passing-only commits, or test names a reviewer can read as the change.
- **Decision.** `ImplementationPrompt` writes a built-in `## Tests` section after the
  developer's stage instructions on every implementation turn: write the behavior test
  first, see it fail for the expected reason, implement and rerun, commit only passing
  tests, and name tests by condition and expected behavior, matching the assertions.
  It exempts changes with no new behavior and forbids rewriting existing tests for the
  rule. The planning prompt adds one rule: the plan names each test and the behavior it
  checks. A conflict repair turn and a merge-before-review turn (D133) get neither.
  `defaults/stages` stays unchanged so the text has one source. Nothing enforces the
  rule in code.
- **Consequence.** Every delegated turn in any repo, with Claude or Codex, carries the
  rule. An agent that ignores it is caught at review or by the developer at
  READY_FOR_HUMAN.

## D139 (amends D129). The PR description holds Problem, Fix, and Risk

- Date: 2026-10-02. Issue midsoln/foreman#21.
- **Context.** D129 left `defaults/stages/pr.md` with six body parts: what was wrong,
  the fix, how to test, risk, plan links, and `Closes #N`. Reviewers still found the
  body slow to scan. The test report and the plan links repeat what the linked issue
  holds.
- **Decision.** Edit `defaults/stages/pr.md` only. The body has `## Problem`, `## Fix`,
  and `## Risk`, then `Closes #N`. An optional `## Review notes` after Risk lists a
  decision, plan deviation, or limitation only when a reviewer needs it. Fix is kept
  short by judgment, with no bullet count. The body leaves out test commands, results,
  reviewer steps, and planning comment links. Later developer answers go under Review
  notes. Rule 6 drops "what the tests showed". The validation gate in rule 1 stays.
  No Go change.
- **Consequence.** New installs and new runs get the shorter format. `make setup` does
  not overwrite installed stage files, so existing users copy the new default by hand.
  Existing PRs stay as they are.

## D140 (amends D10, D33, and D90). The reviewer judges the PR against its description

- Date: 2026-10-02. Issue midsoln/foreman#22.
- **Context.** The review prompt carried the issue, the context file, the developer's
  decisions, the PRD, and the implementer's validation claims. These steered the
  reviewer toward the implementer's plan. The issue asks for an independent review
  with the PR description as the only stated requirements.
- **Decision.** `App.review` reads the PR with `GetPullRequest` and builds
  `agent.ReviewBrief` from the PR alone: its number, description, comments, branch,
  base, earlier review rounds, and `review_rounds`. `ReviewBrief` no longer embeds
  `TaskBrief`, so it has no field for the issue, PRD, context, decisions, plan, or
  validation. `PullRequest` gains `Body`, read through `gh pr view --json body`.
  - A blank description or a failed PR read returns an error before the reviewer
    runs. The existing review error path marks the task FAILED and records no review.
  - The PR's comments come in the prompt as feedback history, because the reviewer has
    no network. A failed comment read does not stop the review; the prompt says the
    comments could not be loaded. Comments with an empty body are left out.
  - The prompt says the description is the only requirements, not to follow its
    links, and that claims in it, in comments, or in commit messages are not evidence.
    A `## Review stance` section asks for a fresh look, counterexamples, regressions,
    missing cases, focused checks when its tools allow them, and only verified
    defects. When it can't run a check, it verifies the defect by reading the code.
  - Earlier rounds list each finding with severity, file, line, summary, and detail.
    A re-review checks each fix, then the whole change, and does not repeat resolved
    findings. `## Review passes` is unchanged.
  - The developer's review stage text keeps its place. Its header now says that a
    mention of the issue, PRD, or decisions means the PR description, and that the
    built-in review rules win. This covers older user stage files, which `make config`
    never overwrites. `defaults/stages/review.md` drops the issue, PRD, and decisions
    rules and adds the stance and re-review rules.
  - The comments reuse `agent.FeedbackItem` and its `where`, `author`, and `quote`
    helpers, not a new type.
- **Consequence.** A developer's answer to an escalated review ("leave the code as
  is") no longer reaches the reviewer. If the reviewer still finds the defect, the
  fix loop runs again until `fix_rounds` escalates.
  - Review round 1 of PR #24 asked to let Claude reviewers run test commands. The
    developer chose to keep both reviewers strictly read-only (D10, D33). Claude's
    Bash tool has no sandbox, so a test command would run repository code with
    write and network access. Codex's read-only sandbox also blocks Go builds. The
    reviewer's checks are therefore limited to reading code and git commands, and the
    prompt says so. `ReviewTools` is unchanged.

## D141. Each agent turn opens with an `agent:model:step` log line

- Date: 2026-10-02. Issue midsoln/foreman#25.
- **Context.** Task logs did not say which model ran a turn or where one turn began.
  Models are chosen per stage inside the adapters (`PlanModel`, `ReviewModel`), so a
  wrong model choice could only be found by reading config.
- **Decision.** `agent.StartInput` gains `Step`. `App.implement` sets it, and
  `App.review` sets `review`. The adapters call `logTurn` after their plan and review
  overrides, so the logged model is the `--model` or `-m` value passed to the CLI.
  - Steps: `plan`, `implement`, `review`, `feedback`, `merge`. A fix turn after review
    findings, an answer to a decision, and PR creation are `implement`. The merge gate
    and conflict repair are both `merge`. Feedback and merge keep their names though
    they use the implementation model.
  - An unset model logs `default`, the CLI's own choice.
  - Each adapter call writes its own line. A resume that finds its session gone and
    then starts fresh writes two lines.
  - An empty `Step` writes nothing, so callers that set none keep their old logs.
  - The line is plain text. `logs.cleanLine` passes it through, so it shows in
    `foreman logs`, `--raw`, and the dashboard pane with no change to `internal/logs`.
- **Rejected.** Resolving the model in the app would need a port change and a second
  copy of the override logic. Reading the model from Claude's `system init` event
  reports what the CLI used, not what foreman chose, and Codex has no such event.

## D142. The runner is a loop of named steps over one shared turn pipeline

- Date: 2026-10-02. Issue midsoln/foreman#26.
- **Context.** `App.RunTask` was a 144-line loop. Its phases were implied by the
  `reviewNext` and `answered` flags, three `continue`s, and the `feedback` and
  `rounds` counters. The sequence "run a turn, verify the PR, record the result,
  record a cancel, add usage" was written out four times: in `RunTask`,
  `answerFeedback`, `resolveConflicts`, and `repairConflict`. The FAILED transition
  with a `failed` outcome was written by hand at six sites. The issue asks for a review
  of every Go package and one focused refactor of the runner, with no change to CLI
  output, exit codes, the task file, or agent behavior.
- **Decision.** Keep the refactor inside `internal/app`, with no new port, package, or
  dependency.
  - `RunTask` claims the task, repairs a pending conflict, reads waiting comments, and
    runs `taskRun.loop`. A `step` enum names the five phases. `firstStep(rec, waiting)`
    is a pure function: waiting comments answer, a verified unreviewed head is
    reviewed, anything else implements. Each step is one `taskRun` method that returns
    the next step. The transitions match the old loop exactly.
  - `turn.go` holds the shared pipeline. `runTurn` runs `implement` and, on
    `pr_opened`, a `prCheck`: `verifiedPR` for normal turns, `checkRepair` for a
    conflict repair. Each check logs its own line. `finishTurn` writes through `final`,
    calls the caller's mapping, records a cancel with `applyStop`, and adds usage.
  - `implement` takes a `turnRequest` (findings, feedback, or merge) in place of three
    positional arguments, and its doc comment moves onto it from `needsPlan`.
  - `failTask(rec, summary, now)` sets FAILED with a `failed` outcome. The cancelled
    outcomes, `failRepair`, and the agent's own `failed` status keep their own code,
    because their status or state differs.
  - The "opened a PR without posting a plan" log line moves into `runTurn`. Only a
    planning turn can trigger it, because every other turn has a PR, so its output is
    unchanged.
  - Rejected: a `map[step]func` table, which adds indirection for five steps; a
    separate runner package behind a new interface, which would need the store,
    GitHub, and agents passed through it while the fakes already test `RunTask`.
  - New tests pin the failure paths before the change: a merge turn whose PR closed,
    a cancelled feedback turn, a plan comment deleted on GitHub, and a failed plan
    post. A table test covers `firstStep`.
- **Consequence.** A change to how a turn is recorded is made in `finishTurn` once.
  The full review ranked later refactors, recorded in `progress.md`: the `run`
  function in `cmd/foreman/main.go`, `gate`, `Start`, the store's record mapping,
  `render.Show` and `tui.Run`, and `agent.ImplementationPrompt`. None is built here.

## D143 (supersedes the native delivery in D57 and D59). Alerts go to a Discord webhook

- Date: 2026-10-03. Issue midsoln/foreman#32.
- **Context.** Desktop notifications through `osascript` or `notify-send` never reach
  the maintainer away from the machine that runs foreman, and do nothing on other
  platforms.
- **Decision.** `notifications.Discord` replaces `notifications.Native`. The triggers
  and the one-alert-per-transition rule in `App.update` stay as D59 set them.
  - The port is `Notifier.Notify(app.Alert)`. `Alert` carries the state, a cancelled
    flag, the task key, and a link: the PR URL when the task has one, else the issue
    URL. `Alert.Label` keeps the five alert texts in one place.
  - The post is `{"content": "**<label>** · owner/repo#N (<STATE>)\n<link>"}` with
    `allowed_mentions.parse` empty, so task text cannot ping anyone. A non-2xx status
    is an error. The client times out after 10 seconds.
  - The config key is `notifications.webhook_url`. `notifications.enabled` still
    defaults to true. Enabled with no URL, foreman runs without a notifier and prints a
    setup warning on stderr. The detached runner and `config --show` skip the stderr
    warning; `config --show` prints it in its own output.
  - The URL is a secret. `config --show` prints `Notification webhook: set` or `not
    set`. Delivery errors drop the `*url.Error` wrapper, which names the URL, and mask
    any remaining copy. A non-2xx error names the numeric status and Go's standard
    text, not the server's reason phrase, which could echo the URL. When the URL is set and the config file has any group or other
    permission bits, foreman warns and suggests `chmod 600`.
  - The notifier appends each delivery error, with a UTC timestamp, to
    `<log_dir>/notifications.log`. `App.notify` still ignores the error, so task state
    and exit codes never depend on Discord. This covers commands with no task log,
    such as `status` and `inbox`.
  - `scripts/e2e.sh` runs a python3 stand-in webhook in the notify scenario and turns
    notifications off in every other scenario.
- **Rejected.** Writing errors to the runner's task log, because `status`, `inbox`, and
  `cancel` also move tasks and have no log writer. Keeping `Notify(title, body)` and
  formatting in `app`, because Discord then gets no state or link as fields. Retries,
  which the issue leaves out of scope.

## D144. Agent context files hold only what is live

- Date: 2026-10-03. Decided by the user, following the agent's recommendation.
- **Context.** `AGENTS.md` has every session read `progress.md` first (about 14k
  tokens, mostly completed stages), and agents read the whole `decisions.md` (about
  35k tokens) to append an entry. Both only grow.
- **Options.** A. Leave the files as they are. B. Keep only live state in the files
  that are always read, move history to files read on demand, and append decisions
  without reading the log.
- **Decision.** B.
  - `progress.md` keeps the current stage, the refactor candidates, the known
    limitations, and the next stage. The completed stages move unchanged to
    `history.md`. The decision index in `progress.md` is dropped, because the `## Dn`
    headings in the decision logs already list every decision.
  - D1 to D100 move unchanged to `decisions-archive.md`. The append-only rule holds:
    entries are moved, not rewritten.
  - `AGENTS.md` says to append decisions after finding the last number with `grep`,
    and to read `architecture.md`, `capabilities.md`, `history.md`, and the archive
    only when the task needs them.
- **Consequence.** The required read at session start falls from about 14k tokens to
  about 1.5k. At each stage boundary, the finished stage's entry goes to `history.md`
  and `progress.md` is updated to the new current and next stage.
- **Rejected.** Deleting the history, which is the only record of how each stage was
  validated. Summarizing old decisions, which would rewrite them.
