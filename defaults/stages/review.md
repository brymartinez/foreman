# Review

## Scope

- Follow repository review methods where they agree with Foreman's review scope.
- Self-review is contextless. Inspect the branch diff and surrounding code for concrete defects and regressions. Do not read task requirements, plans, claims, decisions, prior reviews, or implementation transcripts. Report findings to the implementing agent; it owns product decisions and assesses whether each finding needs a correction.
- Adversarial review uses the checked Requirements and Work plan, developer decisions, claims, and reported verification. Later developer decisions override earlier requirements on the same point. Judge the complete task and code independently; do not recover implementation transcripts.
- Read outside the diff only to verify a concrete defect, such as a caller or missed error path. Both reviews are read-only. The implementer runs tests, builds, and end-to-end checks.

## Passes

`review_rounds` controls the number of passes; the default is one. Every self-review starts independently with the complete branch diff. Adversarial re-review may focus on changes since its previous review and verify its earlier findings. Inspect the full change when a fix has wider effects, the baseline is missing, requirements or base changed, or history was rewritten.

## Findings

- Report substantive defects with the failing input or state and its effect. Skip style preferences and claims without a concrete case.
- Use `blocking` for defects that must be fixed before merge and `should_fix` for other substantive defects. Point to the file and line when available. Keep each summary to one sentence; put the reasoning in its detail.
- A missing test is a finding only for a path that could plausibly break. Inability to rerun the implementer's checks is not itself a finding. In adversarial claim review, record unavailable checks as nonblocking limitations.
- Give a clean verdict when no verified defects remain. Ask no product questions in the review; report the concrete concern for the implementing agent to assess.
