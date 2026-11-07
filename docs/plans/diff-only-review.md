# Simplify branch selection and code review

Use tracker and Git metadata for branch selection. Give each code reviewer the
complete diff for the current revision. Keep workflow state, verification of Git
and tracker facts, budgets, and handoff in Go.

## 1. Remove repository prose from branch selection

- [ ] Use the existing default branch proposal for new tasks. Prefer a valid
  Linear branch suggestion when present. Keep manual editing, existing task and
  PR branch reuse, name validation, collision checks, and auto-mode abstention
  when the candidate is invalid or unavailable.
- [ ] Remove the Markdown guidance parser and the branch-proposer model call.
  Remove checkout synchronization used solely to read that guidance after
  confirming worktree creation still fetches and uses the correct base.
- [ ] Verify new GitHub and Linear proposals, invalid tracker names, manual
  overrides, and existing task and PR branch reuse with focused tests.

## 2. Make permissions explicit and trust structured review results

- [ ] Configure headless Claude and Codex turns to run with no human approval
  prompts. Pre-authorize the commands needed for each role. Reviewers can read
  Git diffs and repository files. Implementers can edit, run checks, and publish
  through the existing worktree and Git permissions. Keep review writes and
  publishing restricted and keep tool restrictions on selector and learning
  turns. Verify the flags for both start and resume against the installed CLIs.
- [ ] Remove the review parser's inference from words about permissions, tests,
  and sandboxes. Require explicit findings and limitations. Reject a verdict
  that contradicts the structured result. If a reviewer cannot inspect the diff,
  treat the review as incomplete and keep it from advancing the task.
- [ ] Verify adapter arguments, successful access to the exact diff command,
  and parser handling of defects, unavailable evidence, and incomplete
  reviews. Keep text-only plan checks on their existing structured verdict
  contract and cover them in the parser tests.

## 3. Review the complete diff in one call per reviewer

- [ ] Give self-review and competing review the same code-only prompt with the
  verified base and head revisions. Each review, including one after fixes,
  reads the complete branch diff. Reviewers can read surrounding code when
  needed to verify a finding. Remove file grouping, budget splitting,
  incremental baselines, and previous findings from code review inputs.
- [ ] Keep confirmed scope and checked Requirements and Work plan in planning
  and implementation. Move responsibility for meeting acceptance criteria to
  the implementer and its validation. Retire the separate claims requirement
  and claim-based code review gate. Keep historical records readable and ensure
  in-flight tasks can resume under the new rules.
- [ ] Preserve separate self-review and competing review stages, existing
  review budgets and limits, the fix loop, and Git and CI handoff checks. Verify
  that a verdict still applies to the revision reviewed. Keep full-suite checks
  in the implementation flow.
- [ ] Update the default review instructions, README, and architecture rules in
  place. Check the installed-stage migration instructions because setup
  preserves user-edited stage files.
- [ ] Verify that both reviewers get the whole diff after a fix, get no task
  context, and each run once per review round. Cover local and published PR
  review, older task records, and changes to the head before handoff.

## Implementation order

Finish each numbered stage and run its focused checks before starting the next.
Branch selection has a separate set of callers. Permission handling and code
review share the review parser and prompts, so make those edits in order. Use
one owner for the runner, persisted review state, and tests. Run Go vet, the Go
unit suite, and a CLI build at the end. Run local end-to-end tests only if
requested.
