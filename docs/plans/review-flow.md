# Implementation-owned review loop

## Behavior

The implementer keeps its session while it assesses findings, fixes valid defects,
rejects invalid findings with evidence, and asks the developer for decisions.
If the session is unavailable, rebuild it from the checked plan, answered decisions,
committed work, and outstanding findings.

The initial self-review assigns the complete changed-file inventory to up to three
balanced areas, keeping source files and colocated tests together. Fresh, read-only
sessions review their assigned files and relevant callers without task context.
The existing budget is shared across the batch. All areas finish before their
combined, deduplicated findings return to the implementer.

After fixes, self-review uses the completed review baseline, fix diff, unresolved
code findings, and their effects. It receives no plan, claims, decisions, or
implementation transcript. Missing coverage or an invalid baseline requires a full
pass. The implementer may reject findings by number and reason.
Rejecting every finding without changing the reviewed commit ends this loop early.
Any code change receives fresh scoped self-review. Keep the existing fix limit;
at the limit, the implementer may reject the findings or ask for a decision.

The competing agent then reviews the full task context: checked requirements and
plan, answered decisions, implementation claims, reported checks, and the code.
The implementer still handles its findings, but cannot waive this review's verdict.
Do not fall back to the implementer's agent for the adversarial review.
A changed commit must pass review before handoff. Existing full-suite gates remain.

## Implementation and verification

1. Add regression checks for context separation and the retained implementation
   session. Run them before changing behavior.
2. Add a typed self-review assessment with the review round and numbered rejected
   findings. Validate it against the current review and verified commit. Persist
   the reasons without changing the reviewer's verdict.
3. Route findings to the retained implementation session. Rebuild authoritative
   task context after session loss. Give decisions to the adversarial reviewer.
4. Update stage defaults, README, and architecture rules in place. Verify parser,
   storage round trips, early exit, partial rejection, stale assessment, round
   limits, session loss, and both local and published-PR flows.
5. Run vet, focused and full Go suites, build, and affected end-to-end scenarios.
   Review the diff and install the verified binary and updated review instructions.
