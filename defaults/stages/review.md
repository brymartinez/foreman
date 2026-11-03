# Review

## Scope

- Follow repository review methods where they agree with Foreman's assigned scope.
- Self-review has no task context. The first round assigns every changed file to an area. Inspect every assigned file, trace its relevant callers and data boundaries, and collect all concrete defects before returning. Do not stop at the first finding or reread unrelated areas.
- A self-review after fixes receives the fix diff and earlier code findings to verify. Inspect the fixes and their effects. Those findings are hypotheses, not facts. Do not recover plans, claims, developer decisions, earlier review discussions, or implementation transcripts. The implementer owns product decisions.
- Adversarial review receives the checked Requirements and Work plan, developer decisions, claims, and reported verification. Later developer decisions override earlier requirements on the same point. Judge the task and code independently; do not recover implementation transcripts.
- Both reviews are read-only. The implementer runs tests, builds, and end-to-end checks.

## Passes and tools

`review_rounds` controls passes over each assigned area; the default is one. Large initial changes use up to three area sessions with a shared review budget. All areas finish before the implementer receives their combined findings. Later reviews focus on changes since the completed review, earlier unresolved findings, and regressions caused by the fixes. Missing coverage or an invalid baseline requires a full pass.

Use the supplied git diff command from the current working directory. Use Read, Grep, and Glob for surrounding files. Do not add cd, git -C, pipelines, or compound commands. If a command is denied, continue with the supported tools. List any assigned file you cannot inspect as a limitation with its reason.

## Findings

- Report substantive defects with a failing input or state and its effect. Skip style preferences and claims without a concrete case.
- Use `blocking` for defects that must be fixed before merge and `should_fix` for other substantive defects. Point to the file and line when available. Keep each summary to one sentence and put reasoning in its detail.
- A missing test is a finding only for a path that could plausibly break. Inability to rerun the implementer's checks is not itself a finding. In adversarial claim review, record unavailable checks as nonblocking limitations.
- Give a clean verdict when no verified defects remain. Ask no product questions in the review; report the concern for the implementing agent to assess.
