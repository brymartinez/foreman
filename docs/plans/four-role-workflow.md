# Foreman's four agent roles

Run each task with a planner, an implementer, a self-reviewer, and an
adversarial reviewer. Go starts or resumes each role, verifies Git and tracker
facts, saves outcomes, and updates the dashboard. This plan builds on the
diff-only review work and changes the diff range for self-review follow-ups.

## Role and session contract

| Role | Instructions | Session and input |
| --- | --- | --- |
| Planner | `plan.md` | One retained session per task. Interpret confirmed triage, write Requirements and Work plan, and check both before returning `planned`. |
| Implementer | `implement.md` | One retained session from the first implementation turn through all fixes, questions, checkpoints, merge repairs, final checks, and publishing. Use the checked plan and the findings for the pending correction. |
| Self-reviewer | `self-review.md` | One logical role with a fresh, read-only session per pass. Read the full branch diff on the first pass and the verified fix diff on follow-ups. Persist the pending review and its range so Foreman can retry the same work after an interruption. |
| Adversarial reviewer | `review.md` | One retained, read-only session. Review the full pinned branch diff each round. Its conversation contains only its own review work. |

## 1. Persist the next action and the sessions

- [ ] Extend the task record with role sessions and a typed pending action.
  Record the current role, purpose, action ID, and any pinned revisions or
  source review round. Store the findings and correction target before
  launching the implementer. Keep the phase that will run after a pause or
  failure separate from the dashboard status.
- [ ] Use the existing task lock and runner claim to fence each action. Persist
  the provider session ID as soon as it is available. Save the outcome and next
  action together before starting another turn. Keep planner and implementer
  session IDs in separate fields.
- [ ] Resume a saved planner, implementer, or adversarial session on restart.
  Keep the implementer session across checkpoints. Add a read-only resume path
  for adversarial review that returns the review schema. Confirm Claude and
  Codex support it with the review permission settings and no approval prompts.
- [ ] Reconcile interrupted writes against Git and tracker state. Reuse a
  verified PR and already-posted plan comments. Ask the retained agent to
  reconcile an interrupted turn against existing work before retrying it.
  When a retained session is missing, its launch is uncertain, or its provider
  is unavailable, preserve progress and show the reason. Wait for availability
  or explicit replacement of that role. A fresh self-review retry uses the
  same pending range and excludes previous review messages.
- [ ] Keep older task records readable. Preserve a known implementer session.
  For an older record with no saved next action, recover the phase from its
  verified plan, worktree or PR head, and review history. When the role or
  pending action is ambiguous, show a decision in the dashboard.
- [ ] Verify pause and retry at each role, including a crash before the
  session ID arrives, after the ID is saved, and after Git or tracker writes.
  Verify that a completed action is not launched again, and two resumes
  cannot launch two runners for one task.

## 2. Put planning and implementation behavior in the stage files

- [ ] Give the planner the confirmed triage sources and `plan.md`. The planner
  returns `planned` with both documents or reports `blocked` or `failed`.
  Remove the separate plan-check call, revision loop, and checker prompt.
  Keep document validation, revision fingerprints, and idempotent comment
  publication in Go.
- [ ] Route a changed confirmed scope or plan through the planner and the
  retained implementer before review. Supply the newly checked plan on
  resume. Treat prior reviews as stale when the checked scope changes.
- [ ] Put acceptance-criteria validation, finding assessment, correction
  policy, and questions in `implement.md`. Reuse that role for existing
  validation, merge repair, and publishing actions. Keep Go's prompts focused
  on the stage instructions, the task inputs, and the structured output.
- [ ] Remove model calls outside the four roles during a task. Replace
  prose-based agent routing with an explicit profile choice or the configured
  default profile. Preserve deterministic quota checks. Stop automatic
  evaluation and diagnostic calls from the task lifecycle. Leave explicitly
  invoked standalone knowledge commands outside the task workflow.
- [ ] Verify the planning handoff, changed-scope flow, missing-plan outcomes,
  and correction prompts. Assert that a task launches calls only for the four
  named roles, including on auto-start and after handoff.

## 3. Route corrections back to their reviewer

- [ ] Add `self-review.md` as a separate stage file. Give each self-review only
  that file, its pinned diff command, and read-only access to relevant code.
  Use `base...head` for the initial review. After self-review findings, use
  `reviewedHead..fixedHead` when Git confirms the earlier head is an ancestor
  and the base is still valid. Start a new full baseline after a retarget,
  history rewrite, or missing range.
- [ ] Require the implementer to account for each self-review finding as fixed
  or rejected with evidence. Bind that response to the originating round.
  A fix-diff review checks the correction. Responsibility for findings outside
  that diff stays with the implementer.
- [ ] Give the adversarial reviewer `review.md` and the complete pinned branch
  diff every round. When it finds a defect, resume the implementer and then
  return directly to the adversarial role with the verified new head. Preserve
  this return target after a stop or crash. A revised plan or invalidated base
  starts review again from self-review.
- [ ] Advance only after an explicit clean adversarial verdict for the verified
  revision. Keep budget and quota limits as reasons to pause. Remove the
  quota-based review skip and the option to accept unresolved adversarial
  findings. If the final implementation validation changes code, return to
  the review that covers the changed revision before handoff.
- [ ] Verify both loops with committed fixtures. Assert the exact Git ranges,
  the role and session selected on every turn, and the absence of plan,
  implementation, and prior-review messages in self-review. Exercise retargets,
  out-of-order verdicts, provider limits, and incomplete reviews.

## 4. Update the rollout and verify the workflow

- [ ] Update `docs/control-layer/architecture.md`, README, and stage defaults
  in place. Add the self-review stage to the installer and document how to
  migrate existing stage overrides and routing configuration. Keep installation
  from overwriting user-edited files.
- [ ] Run focused Go tests after each stage. Run Go vet, the Go suite, the stage
  installer checks, and a CLI build at the end. Reproduce any baseline failure
  before attributing it to this change. Run local end-to-end scenarios only
  with explicit approval.

## Work order

Land the persisted action and session model first. Planning and review changes
both depend on it and edit the same runner, so apply them in order. Keep each
stage independently testable. Use the existing task store as the single writer
for session ownership, outcomes, and transitions.
