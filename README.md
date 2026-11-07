# foreman

A local CLI that hands GitHub or Linear issues to Claude Code or Codex. It gives the
PR back to you once it is ready for your review.

```
foreman  →  pick issue #825 in the inbox, choose Claude
        ↓
  Claude plans and implements in its own worktree, pushes, and opens the PR
        ↓
  Claude self-reviews, Codex adversarially reviews, Claude fixes, repeat until clean
        ↓
  foreman reads the PR's CI checks on GitHub
        ↓
foreman  →  READY_FOR_HUMAN in delegated work
```

You review and merge. Foreman never merges, and it doesn't run as a background service.
Nothing happens while the dashboard is closed except the per-task runner that works on
an issue you started.

## Requirements

- Go 1.27 or newer, to build.
- [`gh`](https://cli.github.com), authenticated (`gh auth status`).
- [Claude Code](https://docs.claude.com/en/docs/claude-code) (`claude`) and/or
  [Codex](https://github.com/openai/codex) (`codex`), both logged in. Each agent reviews
  the other's work, so install both for the review loop.
- Optional: [`quota-axi`](https://github.com/kunchenguid/quota-axi) on `PATH`, so
  Foreman can avoid providers that have run out of quota.

## Install

Download the archive for your platform from
[GitHub Releases](https://github.com/brymartinez/foreman/releases), for example:

```bash
gh release download --repo brymartinez/foreman --pattern '*darwin_arm64.tar.gz'
tar -xzf foreman_*_darwin_arm64.tar.gz
mv foreman_*_darwin_arm64/foreman ~/.local/bin/
mkdir -p ~/.config/foreman/stages
if [ ! -e ~/.config/foreman/config.yaml ]; then
  install -m 600 foreman_*_darwin_arm64/config.yaml ~/.config/foreman/config.yaml
fi
cp -n foreman_*_darwin_arm64/stages/*.md ~/.config/foreman/stages/
cp -n foreman_*_darwin_arm64/routing.yaml ~/.config/foreman/   # optional: turns on routing
```

The binaries are not notarized. If you downloaded the archive in a browser, macOS may
block the binary; run `xattr -d com.apple.quarantine ~/.local/bin/foreman` to allow it.

To build from source instead, clone the repository and run:

```bash
make setup
```

`make setup` builds foreman, installs it to `~/.local/bin`, copies the shipping
`defaults/config.yaml` into `~/.config/foreman/config.yaml`, copies the default stage
instructions from `defaults/stages/` into `~/.config/foreman/stages/`, and copies the
starter `defaults/routing.yaml` to `~/.config/foreman/routing.yaml`. It never
overwrites a config, stage, or routing file you already have. When a stage file differs
from the current default, it prints `differs <path>` and the `cp` command that replaces
it. `make install` and `make config` run the
steps separately; set `BIN_DIR` or `CONFIG_DIR` to change where they go.

## Usage

Foreman is a dashboard. Everything you do with a task happens there.

```bash
foreman                         # dashboard for default_source (GitHub unless set)
foreman -a                      # auto mode on for this launch
foreman --no-auto               # auto mode off for this launch
foreman -s github               # show GitHub issues and delegated tasks
foreman --source linear         # show Linear issues and delegated tasks
foreman -s all                  # show both trackers
foreman --mentions              # show mention-only items for this launch
foreman -c                      # edit config in $VISUAL, $EDITOR, or vi
foreman knowledge export owner/repo -o knowledge.md
foreman knowledge ingest knowledge.md --dry-run
foreman --help
foreman --version
```

Dashboard launch flags override a setting for one launch and save nothing:

| Launch override | Effect |
|---|---|
| `-s`, `--source github\|linear\|all` | Select the issue tracker for INBOX, DELEGATED WORK, and auto mode's starts. `all` selects both. Overrides `default_source`. |
| `-a`, `--auto` | Turn auto mode on. Overrides the choice saved with `a`. |
| `--no-auto` | Turn auto mode off. Overrides the choice saved with `a`. |
| `--mentions` | Show items found only because they mention you. Overrides `mentions`. |
| `--no-mentions` | Hide those items. Overrides `mentions`. |

Conflicting flags, such as `--mentions --no-mentions` or `-s github -s all`, are a
usage error. The other flags are inputs to one command:

| Command | Flags |
|---|---|
| `foreman -c` | Open the config editor. Use it alone, without launch overrides. |
| `foreman knowledge export owner/repo` | `-o`, `--output FILE` |
| `foreman knowledge ingest FILE` | `--repo owner/repo`, `--rebind`, `--agent claude\|codex`, `--dry-run` |
| `foreman -h`, `--help` | Print command help. |
| `foreman --version` | Print the version. |

Precedence for the dashboard's source and mentions is the launch flag, then
`default_source` or `mentions` in `config.yaml`, then the built-in default
(`github`, and mention-only items shown). Auto mode has no config key: the launch
flag wins, then the choice saved by pressing `a`, and with no saved choice auto mode
is off. Every other setting lives in `config.yaml` or `routing.yaml` and has no
launch flag.

`-c` opens the config even if it cannot load. A missing or empty config is filled
with defaults before the editor opens. Foreman checks it again when the editor exits.

`knowledge export owner/repo` writes to stdout unless you pass `-o FILE` or
`--output FILE`. `knowledge ingest FILE` accepts `--dry-run`, `--agent claude|codex`,
and `--repo owner/repo`. Use `--rebind` with `--repo` to import an export under a
different repository. See [Moving knowledge between machines](#moving-knowledge-between-machines).

## Dashboard

Run `foreman` with no arguments to open a live view. This example shows the log after
pressing Enter on a delegated task:

```
foreman · 2 delegated · 3 in the inbox · auto on 1/3 · refreshed 10:01:22
▸ INBOX                           │ DELEGATED WORK
> midsoln/foreman#1  Be interactive │ testorg/example#340   Codex   READY_FOR_HUMAN
    assigned to you                │   Cache fleet lookups
                                   │   PR #912 · reviewer: Claude · CI ✓ · review ✓
  testorg/example#351  Retry 5xx  │
    mentions you                   │ > midsoln/foreman#2      Claude  IMPLEMENTING
────────────────────────────────────────────────────────────────────────────────
LOG · midsoln/foreman#2 · ~/.local/share/foreman/logs/2.log
2026-09-29T10:01:04Z claude Bash go test ./internal/tui
j/k task · u/d log page · ctrl-u/d half page · g/G ends · s show/log · R raw · esc close · ? help
```

The left pane holds issues you can delegate, and the right pane holds delegated work.
Press `←`/`→`, `h`/`l`, or `tab` to switch between them. In DELEGATED WORK, use `j`/`k`
to pick a task and Enter to open its log below the lists. Escape closes the log. With
the log open, `u`/`d` and `g`/`G` scroll it while `j`/`k` still select tasks. The log
opens at its tail. Scrolling past the oldest loaded line reads older lines from the log
file, and `g` reads back to its first line. Press `R` to switch between cleaned lines
and the raw agent JSONL; the log returns to its tail and the heading says `raw`. Press
`s`, with a log open, to swap it for everything recorded for the task: its state,
branch, worktree, PR, planning comments, CI, usage, and last outcome, scrolled with the
same keys. Press `s` again to return to the log. Press `c` from either list to cancel
the highlighted delegated task, after confirmation. Press `?` for the keys and the
foreman version; any key closes it.

Row text is drawn in your terminal's own foreground, so it reads on a light theme and a
dark one alike. Color is kept for the headings, the dividers, the message line, and each
task's state. The selected row is drawn in reverse video with no color inside it.

| Key | What it does |
|---|---|
| `tab`, `←` `→`, `h` `l` | switch between inbox and delegated work |
| `↑` `↓`, `k` `j` | move the focused list cursor |
| `enter` on an issue | delegate it: pick `1` for Claude, `2` for Codex, or `3` to route it, type an optional context file, then review and edit the proposed branch. A `FAILED` issue starts again on its stored branch |
| `↑` `↓`, `pgup` `pgdn` in the branch prompt | scroll the proposal rationale; the branch field stays visible |
| `←` `→`, `home` `end`, `backspace` `delete` in the branch prompt | move the branch cursor or remove a character; typing inserts at the cursor |
| `enter` on a task | open its log below, or the waiting question on a `NEEDS_DECISION` task |
| `c` | cancel the highlighted delegated task, after a `y`/`n` confirm |
| `x` on a `FAILED` task | confirm a reset of its local run, then offer the issue for a manual start |
| `a` | turn auto mode on or off; the choice is kept for the next launch |
| `r` | refresh the selected issue sources now |
| `p` | open merged and closed PR history; Enter or Tab focuses its log, and Esc returns |
| `space` with a log open | pause or resume log redraws; any navigation key resumes |
| `u` `d` with a log open | scroll up or down one page |
| `ctrl+u` `ctrl+d` with a log open | scroll up or down half a page |
| `g` `G` with a log open | jump to the log's first line or follow the tail |
| `R` with a log open | switch between cleaned and raw log lines |
| `s` with a log open | switch between the log and everything recorded for the task |
| `pgup` `pgdn`, `end` | optional log scroll keys for keyboards that have them |
| `esc` | close the log or abandon a prompt |
| `?` | show the keys and the version |
| `q`, `ctrl+c` | quit |

The branch prompt shows why Foreman proposed the name or needs a manual choice.
Its heading marks proposals that need your choice. The line range shows where you are
when the rationale is longer than the screen. The branch field scrolls horizontally
around the cursor for long names. Press Enter to start with the edited name, or Escape
to cancel.

Delegated work is re-read from the task files every second, so a state change shows up
on its own. The selected issue sources refresh every `refresh` seconds, 60 by default,
and when you press `r`. A refresh runs the searches, the readiness gate, and the DONE cleanup. The
dashboard refreshes only while it is open and starts no background process.

The dashboard needs a terminal at least
60 by 12; below that it says so. When stdin or stdout is not a terminal, `foreman`
prints the usage text and exits 0, so pipes and scripts are unaffected.

Enter on a `NEEDS_DECISION` task shows its oldest unanswered question below the lists:
the issue, context, options, recommendation, and impact. Type an option label or your
own answer and press Enter; an empty Enter takes the recommended option. With several
questions waiting, the
next one follows, and after the last the agent resumes and the pane shows its log.
`↑` `↓` and `pgup` `pgdn` scroll a long question. Every other key is answer text. Esc
closes the prompt; answers already recorded are kept, and Enter asks the next one. A
task whose log is open switches to its question on its own when it needs a decision.

Auto mode is off until you turn it on with `a` or `-a`. While it is on, it starts open issues
assigned to you that foreman has never delegated, from the selected source, oldest first, with `auto.agent` and
no context file. It looks after the first refresh, after each successful refresh, and
when a run ends. Only issues with the exact `triaged` label
start automatically; manual starts also require the exact `triaged` label. Mentions, FAILED
tasks, and previously REVIEWED tasks never start on their own. At most `auto.max_concurrent`
runs are active at once, and
runs you start by hand count toward that limit. Issues in one repository start one
at a time, because they share its checkout. You can still start an issue by hand
when the limit is full. The header shows `auto on 1/3` (active runs and the limit) or
`auto off`.

With [routing](#routing) on, auto mode routes each issue and starts it with the chosen
profile without asking. When no suitable profile can run, the issue's row says
`waiting:` and why, and auto mode tries it again after the next GitHub refresh.

Press `a` to turn auto mode off or on. Turning it off leaves running agents alone. The
choice is saved in `~/.local/share/foreman/state.json` and read at the next launch,
unless you launch with `-a` or `--no-auto`. A
start that fails is shown on the message line and is not retried until you reopen the
dashboard. An issue that lacks detail goes through the usual decision flow.

For GitHub repositories, the inbox searches for open issues assigned to you, open
issues and PRs that mention you, and PRs that request your review. For the configured
Linear team, it lists open issues assigned to the user named by `LINEAR_API_KEY`. INBOX groups
delegatable issues under GITHUB and LINEAR, with
what changed since the last refresh beneath each one. Each search returns up to 1000
results.

Set `mentions: false`, or launch with `--no-mentions`, to leave out the items found only because they mention you, in the
inbox and in the dashboard's INBOX pane. An item that is also assigned to you, or that
requests your review, still appears. The searches keep running either way, so the
changes an item reports stay accurate and the items come back the moment you turn the
setting on. A hidden item stays hidden when it leaves GitHub, so it does not announce
itself on the way out; foreman still stops checking it. It also stays hidden when a
search hits its result limit and foreman has to fetch it by key, because the setting
is applied to the involvement foreman recorded for it.

Missing undelegated items are checked until a successful
refresh with complete searches clears their involvement reasons, then stop being
refetched. Delegated work stays tracked. Missing items are not offered for new
delegation. A failed item lookup warns without aborting the inbox, keeps its stored
state, and retries next time. An undelegated issue that no longer exists or is
inaccessible in its source is dropped and not checked again. Failed PR or CI lookups
are counted as warnings in the header.

Local builds show the commit in `foreman --version` and in the `?` help, for example
`foreman 0.1.0-dev (72a70ce12345-dirty)`. The `dirty` suffix means the build included
uncommitted changes. Builds without Git metadata show `unknown`; release builds
show their stamped version.

INBOX shows repo-qualified identities such as `acme/widgets#825`. When you delegate
one, Foreman clones or reuses its source checkout under `repositories_dir`, then
creates the agent worktree under `worktrees_dir`, so you can run `foreman` from any
directory.

The optional context file is available to routing and remains on the task record. It
is not passed to planning or implementation; put requirements into the issue and
confirmed triage decisions before delegation.

### What a start does

1. For a new task, Foreman proposes a branch from the issue. Manual starts let
   you edit the name. Auto mode waits if the proposal is invalid or reserved by a
   prior PR. Foreman validates the chosen name and fetches the remote base when it
   creates a new branch. A branch named `fix/825-provider-retries` uses
   `<worktrees_dir>/<owner>-<repo>/fix/825-provider-retries`. Each slash in the branch
   name separates directories. Existing tasks keep their stored branch and worktree
   path, including paths from before this convention.
   - A GitHub issue defaults to a name like `fix/825-provider-retries`. Foreman
     takes the type from a title prefix (`fix: …`) or a label (`bug` → `fix`,
     `documentation` → `docs`). It uses `feat` if neither supplies a type.
     The slug is capped at five words and 40 characters.
   - A Linear issue uses the suggested branch when provided. If Linear returns no
     name, Foreman uses the type, identifier, and title, as in
     `fix/linear-web-42-fix-cache`. An invalid suggestion requires a manual choice.
2. It launches a detached runner that logs to `~/.local/share/foreman/logs/`. The
   runner keeps going after you quit the dashboard.
3. The runner has the agent implement the issue, run validation, push, and open a PR
   with `gh pr create`. Foreman then checks with GitHub that the PR exists and targets
   the right branch. When screenshots are requested or the agent captured them, the
   opening agent attaches them in a PR comment after the PR exists, or reports why it
   could not. It uses the repository's documented setup and sample data when taking
   screenshots; Foreman does not create sample data. It leaves the PR description as
   written for review.
4. Each task has four roles: planner, implementer, self-reviewer, and adversarial
   reviewer. Both reviewers start a fresh read-only session for each pass. Each role
   first reads the full branch diff, then reads the verified fix range on follow-ups.
   Both can inspect surrounding code and tests.
   Task requirements, plans, decisions, PR text, validation results, and earlier
   findings stay out of both code review prompts.
   Findings return to the implementer, then directly to the reviewer that raised
   them. The implementer fixes or rejects each self-review finding with evidence;
   adversarial findings require a clean adversarial review. Findings cannot be
   waived because of quota or a fix limit. A missing session or uncertain launch
   pauses the role until it is available or explicitly replaced. Retries resume
   retained planner and implementer sessions. A review retry uses the pinned range
   when it still matches the current revisions. Code changes need review.
   The implementer runs the repository's full documented suite before the first
   review and focused checks for each fix. Before handoff, Foreman needs a full-suite
   result that covers the final commit. If a fix changed behavior or a validation
   input since the last full run, the implementer runs the suite once more, and a fix
   it commits then is reviewed again. A repository with no identifiable full suite is
   handed off with the agent's explanation of what it could not verify.
   Foreman has no `wait_for_pr` flag or config key: the first push and PR happen
   before this review. Fixes found during review are committed and pushed to the
   same PR before the next review pass.
5. On the next GitHub refresh, foreman reads the PR's CI checks on
   GitHub, the same list as at the bottom of the PR page. Once they finish, the task is
   `READY_FOR_HUMAN`.
   - Passing checks show as `✓ CI`. Failing checks are listed as `✗ CI: test`, with
     links.
   - CI belongs to the repo's owners, so foreman never runs or fixes it. You decide
     whether a failure comes from the change or from CI itself.
   - A repo with no checks shows `✓ CI (no checks)`.
   - A merged PR marks the task DONE. A PR closed without merging marks it
     `PR_CLOSED` and stops the run.
6. Every review records the revision it covered. If the PR gets new commits
   before `READY_FOR_HUMAN`, the next refresh sends the change through both
   reviews again. Once the PR is ready, Foreman flags new commits for you.
   If the runner crashes during review, restart the `FAILED` task from the inbox
   to resume at review.

## Planning

Before any code, the planner writes two short documents: Requirements (`prd`)
and Work plan (`plan`). `plan.md` sets what each one covers and asks for B1 English:
common words and short sentences. Foreman reads
`## Triage decisions` and later `## Triage correction` comments in order; if there
are none, the issue description is the confirmed scope. It gives that scope and its
source links to the planner, which checks its documents against the scope before
returning them. Foreman posts each as an issue comment. Requirements says what must
work and how to check it.
Work plan gives ordered tasks that refer to requirement IDs without copying the rules.
Foreman checks the two documents together against triage. The implementer receives
both and validates the requirements against the implementation. The separate
text-only plan check covers changes to scope or planning documents. If the plan
changes after implementation starts, the implementer applies the updated plan
before the next code review.

The `s` view links the two comments. A task that plans again, for example after a
failed run, edits the same comments instead of adding new ones. A task that already
has an open PR but no complete plan runs planning before reviewing that PR.

## Stage instructions

Each stage of the work has an instruction file that holds the agents' working rules:
how to implement, when to ask, what a review counts as a finding, and how a PR
description reads. Foreman's own prompts keep only what it needs to run the task: the
worktree and branch, the push and `gh pr` commands, `Closes #N`, the read-only
reviewer, the question format, and the JSON response. The stage files add to the
agents' own `CLAUDE.md` and `AGENTS.md`, which the agents keep loading as usual.
Foreman never reads or changes those files.

| Stage | Given to | Global file | Repo file (committed) |
|---|---|---|---|
| `plan` | the planner, for the two planning documents | `~/.config/foreman/stages/plan.md` | `.foreman/plan.md` |
| `implement` | the implementer | `~/.config/foreman/stages/implement.md` | `.foreman/implement.md` |
| `self-review` | the self-reviewer | `~/.config/foreman/stages/self-review.md` | `.foreman/self-review.md` |
| `review` | the adversarial reviewer | `~/.config/foreman/stages/review.md` | `.foreman/review.md` |
| `pr` | the implementer, for the PR description | `~/.config/foreman/stages/pr.md` | `.foreman/pr.md` |

- The five global files are required. The dashboard won't open when one is missing,
  and says which. `make config` installs the defaults from `defaults/stages/`, and the
  release archives carry them in `stages/`. Edit your copies freely.
- Update installed stage files with the copy commands printed by `make config`.
  Review the new `self-review.md` and update customized `.foreman/` stage files to
  assign self-review and adversarial review separately.
- Repo files are optional.
- When both files exist, the agent gets both: global first, then the repo's more
  specific rules.
- The repository's `AGENTS.md` and PR template take precedence over the global
  `pr.md` defaults when they give different title or body rules.
- The checked Requirements and Work plan are the implementer's task specification. Answers
  to later blocking questions take precedence over stage instructions.
- Tasks read `.foreman/` from their worktree, so only the version committed on the base
  branch counts.
- A GitHub issue PR body includes `Closes #N`; a Linear issue PR links its issue URL.
  A review always ends in foreman's JSON format. Profile selection is deterministic: Foreman uses an explicitly selected profile or the configured default; it does not ask an agent to choose or route a profile in prose. Quota checks can pause work but cannot skip a review. Automatic learning and diagnostic calls do not run in a task.
  Otherwise the text is passed through as written.

## Cleaning up worktrees

When a refresh finds a task DONE, it removes the task's worktree. When the PR was
merged, it also deletes the local branch. A PR_CLOSED task keeps its worktree and
branch. A FAILED task also keeps them so you can retry the run.

To discard a FAILED run, select it in DELEGATED WORK and press `x`. Foreman shows
the task file, runner log, worktree, and local branch it will remove. Press `y` to
confirm. The replacement task record keeps the PR link, and Foreman offers the
issue for a manual start. The reset does not change GitHub; inspect the branch for
uncommitted work before confirming.

- DONE cleanup keeps a worktree with uncommitted or untracked changes. A later
  refresh removes it once it is tidy. A confirmed reset of a FAILED run removes
  its worktree even when it has such changes.
- A branch whose PR wasn't merged is kept, so a new start can pick the work up again.
- Remote branches are left to GitHub's "Automatically delete head branches" setting.

Turn either behavior off in the config, under `cleanup`.

## When the agent needs a decision

Agents make routine calls themselves. When something needs your judgment, such as a
product choice or conflicting requirements, the agent stops and returns one or more structured
questions. The task becomes `NEEDS_DECISION`, and the agent process ends. Nothing waits in
a terminal.

Foreman posts to a Discord webhook when a task needs a decision, fails or is cancelled,
becomes ready for review. Each post names the alert, the task
(`owner/repo#N`) and its state, and links the PR, or the issue when there is no PR yet.
Set the webhook URL in `notifications.webhook_url` (see Configuration). The URL is a
secret: keep the config file `chmod 600`. Foreman warns when the URL is missing or the
file is readable by other users, and never prints the URL. Delivery is best effort. A
failed post never changes a task, and Foreman appends the error to `notifications.log`
in the log directory. The dashboard shows the task's state the same either way.

In the dashboard, Enter on the `NEEDS_DECISION` task shows each waiting question in
turn: the issue, context, options, recommendation, and impact if wrong. You can type
an option label, write your own answer, or press Enter to take the recommendation. Esc
leaves the remaining questions waiting. Foreman saves each answer to the task file
and, once all are answered, resumes the current stage's session when one exists.
After review findings it resumes the implementation session with the findings and
your answer. If the session is unavailable, the replacement gets the checked plan,
answered decisions, and outstanding findings. The answer also appears in the PR description.

Foreman checks provider quota before each role call. Exhaustion pauses the role and
preserves its session and work. Quota never skips a review. Each new revision gets a
self-review before adversarial review. Both reviewers inspect the verified fix range
on follow-ups. Findings go back to the
implementer, then directly to the reviewer that raised them. The task pauses at its
fix limit until you choose another round; unresolved adversarial findings cannot be
accepted.

With `review_before_pr: true`, a new task is reviewed before its PR opens, so
automated reviews that run on new PRs see the finished change. The implementer
commits locally and proposes a PR title and description. Both reviewers read the
local diff and fixes stay local. After a clean review, the implementer pushes the
reviewed commit and opens the PR with the proposed title and description. Foreman
checks that the PR matches. Requested screenshots go in a PR comment after it opens. If the fix limit is reached
first, you choose another round or stopping the task without a PR. A task keeps the
setting it started with, including when you restart it.

## When you close a PR

Closing a delegated PR without merging it ends the task. On the next GitHub refresh,
foreman stops the agent if one is still running, moves the task to `PR_CLOSED`, and
drops it from DELEGATED WORK. Nothing is deleted: the task file, the worktree, and the
branch are kept.
Closing the issue as well leaves the task `PR_CLOSED` rather than DONE, so the worktree
is not cleaned up behind you.
The run did not fail, so there is no failure notification.

Reopen the PR and the task comes back on the following refresh. Foreman reads the PR's
CI and uses the review it already has: a clean review of the PR's current head with
finished CI makes the task `READY_FOR_HUMAN`, and with CI still running it waits in
`WAITING_FOR_CI`. A clean review of an earlier head starts a run that reviews the PR again. Without a clean review, the task shows the stage it needs next and
waits for a start.

If the agent does not stop at once, the task is `PR_CLOSED` but keeps its runner, and
every refresh tries again.

## States

| State | Meaning | Your move |
|---|---|---|
| `PLANNING` | The implementer writes Requirements and Work plan, and foreman posts them | Wait, or `c` on the task to stop it |
| `IMPLEMENTING` | The implementer changes the branch: code, review fixes, answers, or a merge | Wait, or `c` on the task to stop it |
| `SELF_REVIEW` | The implementer reviews its own change in a fresh session | Wait, or `c` on the task to stop it |
| `ADVERSARIAL_REVIEW` | The competing agent reviews the change | Wait, or `c` on the task to stop it |
| `OPENING_PR` | With `review_before_pr`, the implementer pushes the reviewed commit and opens its PR | Wait, or `c` on the task to stop it |
| `WAITING_FOR_CI` | The PR passed the adversarial review; foreman waits for CI | Wait |
| `NEEDS_DECISION` | The agent asked a question | Enter on the task to answer |
| `READY_FOR_HUMAN` | Review clean, CI finished (✓ or ✗ shown) | Review and merge the PR |
| `DONE` | PR merged or issue closed | Nothing |
| `FAILED` | The agent failed, the run was cancelled, or the runner died | `s` on the task to see why, then Enter on it in the inbox to retry or `x` on the task to reset its local run |
| `PR_CLOSED` | You closed the PR without merging it | Nothing, or reopen the PR |

## Task files

Each task is one Markdown file with YAML frontmatter, stored in
`~/.local/share/foreman/tasks/<owner>-<repo>-<number>.md`. The frontmatter holds the
state, session ids, PR, validation, review rounds, CI, and decisions. The body is yours,
so you can add notes there.

Foreman rewrites only the frontmatter keys it owns. Keys you add, such as `tags`,
`aliases`, or anything else, stay in place along with their comments.

### Obsidian

The files are ready for Obsidian. The first time you open the dashboard, Foreman
explains the configured task directory and offers to create it. To use a folder inside
your vault, point `tasks_dir` at it first:

```yaml
tasks_dir: ~/Documents/vault/foreman
```

Each task then appears as a note, and the frontmatter shows up as properties.

## Learning

Task runs do not evaluate or update repository knowledge. Knowledge export and ingest
remain explicit commands and operate separately from the four task roles.

What it learns is kept per repository under `knowledge_dir`:

```
~/.local/share/foreman/knowledge/<owner>/<repo>/
  knowledge.md            active learnings, for people and prompts
  learnings.yaml          every learning with its status, evidence, and history
  evaluations/<task>.yaml each task's assessment, signals, and proposals
  attempts/<task>.yaml    the last evaluation attempt
  learning.lock           one evaluation per repository at a time
```

A learning becomes active when the developer backs it, through a decision answer or a
PR comment, or when two tasks support it. The evaluator must cite the decision or
comment by id; a claim without a real citation from the task counts as one task's
observation. One task's observation stays a candidate.
The developer's correction retires or rewrites a learning at once; observed work does
so only from a newer task. Learnings are short and generic: foreman drops any with
issue or PR numbers, issue keys such as `ABC-12`, commit hashes, URLs, emails, absolute paths, or secret-shaped
text. Paths inside the repository are kept.

The separate triage skill can use repository knowledge while researching an issue.
Planning, implementation, correction, and review prompts omit repository knowledge.

Set `learning.enabled: false` to stop new evaluations. Triage can still use what is already learned. To forget a repository's knowledge, delete its directory. To remove
one learning, delete its entry from `learnings.yaml`. Prompts read that file, and the
next evaluation rewrites `knowledge.md`.

### Moving knowledge between machines

`foreman knowledge export owner/repo -o knowledge.md` writes the repository's
knowledge to one Markdown file: the active guidance to read, then a versioned YAML
block with every learning (active, candidate, and retired), its evidence and
history, and every evaluation. Without `-o` it writes to stdout.

`foreman knowledge ingest knowledge.md` stores it under the repository the export
names. `--repo owner/repo` must name the same repository unless you add `--rebind`.
A file that is not a valid export changes nothing.

- If this machine has no knowledge for the repository, the export is restored as it
  is, with no agent call.
- If both machines have learnings, one tool-less agent turn groups equivalent
  learnings, and foreman combines their evidence. The agent is `auto.agent` unless
  you pass `--agent claude` or `--agent codex`; a Claude turn is capped at
  `learning.budget_usd`.
- Ingesting the same export again changes nothing and calls no agent. The same task
  on both machines counts once; a different task under the same issue number keeps
  its own evidence under a renamed id.
- A maintainer's correction beats observed work. When two maintainer corrections
  disagree, the later wins if they were recorded at least a minute apart. Otherwise the ingest
  stops, prints both, and saves nothing.
- `--dry-run` prints the resulting `knowledge.md` without saving.

The ingest replaces `learnings.yaml`, `evaluations/`, and `knowledge.md` together. If
it is interrupted after it commits, the next learner, refresh, export, or ingest
finishes it. An export waits for no one: it fails while an evaluation holds the
repository's lock, so run it again. It does not touch tasks, worktrees, config, `attempts/`, or the lock, and
it fails without saving when an evaluation runs at the same time.

`knowledge.md` is the contract for other tools, such as the `foreman-triage` skill.
It starts with a `# Repository knowledge` heading and a line saying the learnings are
fallible, then has up to three sections, `## Conventions`, `## Repository facts`,
and `## Working practices`, each a list of one-sentence bullets. It holds no ids,
issue or PR numbers, or evidence. A reader should pick the file by the verified target
repository, use the bullets that fit the issue, and not publish the file wholesale.

## Configuration

Foreman reads `~/.config/foreman/config.yaml`, or the file named in
`FOREMAN_CONFIG`. Run `foreman -c` to create or edit your file. Every key is
optional; unset keys keep the built-in defaults below, even when no shipping config
has been installed. A key foreman does not know, such as a misspelling, stops it
with an error that names the key and its line.

The built-in defaults are not the same file as the shipped `defaults/config.yaml`,
which `make setup` installs when you have no config. The shipped file chooses some
different values on purpose: `mentions: false`, `opus` and `sonnet` Claude models,
Codex plan and reasoning settings, and $25 and $10 budgets. It leaves the webhook
URL, repository filters, and Linear team setting empty.

Built-in defaults:

```yaml
tasks_dir: ~/.local/share/foreman/tasks
worktrees_dir: ~/.local/share/foreman/worktrees
repositories_dir: ~/.local/share/foreman/repositories # source checkouts for delegated work
log_dir: ~/.local/share/foreman/logs
knowledge_dir: ~/.local/share/foreman/knowledge # what foreman learns, per repository
review_rounds: 1                     # complete diff passes per review call
fix_rounds: 3                        # fixes asked by either review before foreman asks you
review_before_pr: false              # open the PR before review; set true to review locally first
mentions: true                       # show items found only because they mention you
default_source: github               # dashboard issue source: github, linear, or all
refresh: 60                          # seconds between dashboard issue-source refreshes
auto:                                # on/off is not here; press a or launch with -a
  agent: claude                      # claude or codex
  max_concurrent: 3                  # active runs, manual ones included
budgets:
  task_tokens: 2000000               # reported input + output across task calls
  planning_tokens: 400000            # planning turns
  call_timeout_seconds: 1800         # cancels one call; 0 disables each limit
  implementer_usd: 3.00              # Claude maximum per implementation turn
  reviewer_usd: 1.00                 # Claude maximum per review call
learning:
  enabled: true                      # evaluate finished tasks; off keeps existing knowledge in prompts
  budget_usd: 0.50                   # Claude maximum per evaluation; 0 sets no cap; Codex has none
notifications:
  enabled: true                      # decision, failure, and readiness alerts
  webhook_url: ""                    # Discord webhook URL; a secret, keep this file chmod 600
claude:
  command: claude
  model: claude-opus-5               # implementation and PR; "" uses the CLI default
  plan_model: ""                     # "" uses model
  review_model: ""                   # "" uses model
codex:
  command: codex
  model: gpt-6-sol                   # implementation and PR; "" uses the CLI default
  plan_model: ""                     # "" uses model
  review_model: ""                   # "" uses model
  reasoning_effort: ""               # implementation; "" uses the CLI default
  plan_reasoning_effort: ""          # "" uses reasoning_effort
  review_reasoning_effort: ""        # "" uses reasoning_effort
include_repositories: []             # exact owner/name; when set, the inbox shows only these
exclude_repositories: []             # exact owner/name hidden from the inbox when include is empty
linear_team: ""                      # Linear team URL; independent of GitHub repositories
cleanup:
  on_done: true                   # each refresh removes DONE tasks' worktrees
  delete_merged_branch: true      # and the local branch, once the PR is merged
```

`FOREMAN_TASKS_DIR` overrides `tasks_dir` after Foreman reads the file. Directory
values beginning with `~/` expand to your home directory. The `stages/` directory
and `routing.yaml` sit beside the selected config file; neither has a separate
config key. Auto mode's on/off choice lives in
`~/.local/share/foreman/state.json`, outside `config.yaml`.

Each code reviewer gets one call per round. `review_rounds` controls how many
passes that call makes over the complete diff and defaults to one. Foreman records
B1 writing advice locally without adding a correction turn. Missing or unclear
requirements still block planning.

Planning drafts and their checks are saved in the local task file. A restart can
finish posting a checked draft without planning again. If a revision changes no
planning text, Foreman asks for a decision before spending another check. PR title
and description edits keep existing code reviews; empty descriptions block handoff.

Runner limits live under `budgets`: `task_tokens` (2,000,000), `planning_tokens`
(400,000), and `call_timeout_seconds` (1,800). Zero disables a limit. Token limits
count reported input and output, including cached input, and are checked between
calls. One call can exceed the remaining tokens. Task totals survive restarts;
planning totals use the saved call history. Older missing usage is not backfilled.
Dollar limits apply to Claude only. The task file stores each runner call with its
stage, model, usage, times, and error; the task view shows calls and tokens by stage.

To use Linear, set the team URL and a personal API key before starting Foreman:

```yaml
linear_team: https://linear.app/acme/team/ENG/overview
default_source: all
```

```bash
export LINEAR_API_KEY=your-personal-linear-api-key
foreman
```

Replace `linear_projects` or `linear_teams` with the singular `linear_team` string.
The old mapping settings are no longer accepted. Team URLs can end in `/overview`
or `/issues`, or stop at the team key.

Foreman discovers your assigned open issues across every project in the team,
including issues without a project. GitHub issues remain available independently.
Set `default_source: linear` or `all`, or pass `-s linear` or `-s all` for one
launch. Linear discovery needs `LINEAR_API_KEY` with access to that team.
`foreman -s github` does not need the key.

When you select a new Linear issue, Foreman reads its description and triage
comments for explicit GitHub repository links. It proposes a repository when those
links identify one allowed destination. Confirm or edit the `owner/repo` before
choosing an agent. Missing or conflicting links require you to enter a repository.
A triage document hosted elsewhere is not fetched automatically; enter the
repository when the issue and its triage comments do not identify it.

Repository filters apply after selection and do not hide unresolved Linear issues.
Auto mode infers the destination using the same links and leaves ambiguous issues
for manual selection. A delegated issue keeps its confirmed repository across
refreshes and restarts.

Foreman reads and comments on Linear issues but does not change their status.
Branches and pull requests stay on GitHub. A Linear task's PR title includes its
Linear identifier, and its body links to the Linear issue instead of using a GitHub
`Closes #N` line. New Linear tasks use the branch naming rules described under [What a start does](#what-a-start-does).

Planning, implementation, and review can use separate models. PR creation is part of
implementation. The agent chosen for the task plans and implements; the competing
agent reviews. Run `foreman -c` to set the values. For example:

```yaml
claude:
  model: opus
  plan_model: sonnet
  review_model: opus
codex:
  model: gpt-6-sol
  plan_model: gpt-6-luna
  review_model: gpt-6-sol
  reasoning_effort: medium
  plan_reasoning_effort: xhigh
  review_reasoning_effort: medium
```

`refresh` must be at least 5 seconds; foreman refuses to start with a smaller value.
Task files and the open log are re-read every second regardless, and `r` refreshes
the selected issue sources whenever you ask.

`include_repositories` and `exclude_repositories` accept exact repository names such as
`acme/widgets`. A nonempty include list limits the inbox to those repositories and the
exclude list is then ignored. With an empty include list, the exclude list hides its
repositories. The filter applies to fresh inbox results and tracked-task refreshes
from GitHub and Linear alike. DELEGATED WORK lists only tasks from the source `-s`
selects. Hidden tasks keep running, count toward `auto.max_concurrent`, and show again
in a dashboard that includes their source. A failed refresh of either selected
source keeps results from the successful source visible with a warning and pauses all
automatic starts until both sources refresh successfully.

## Agent profile

Foreman uses the explicitly selected provider for a manual start, or the configured
default for auto-starts and tasks without an explicit choice. Profile selection is
deterministic; no model reads an issue to choose a profile. Quota checks may pause a
role but cannot change its profile or waive review. The profile stays with the task
across retries.
