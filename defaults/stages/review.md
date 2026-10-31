# Review

## Scope

- Follow the repository's AGENTS.md and CLAUDE.md. In self-review, compare the change with Foreman's Requirements and Work plan. In competing review, judge the diff independently. For claim verification, check Requirements and claims too.
- Read outside the diff only to verify a concrete defect, such as a caller or missed error path. Do not seek requirements in earlier transcripts, issue discussion, PR comments, or prior reviews.
- Try to break the change by inspecting code and tests. This review is read-only; the implementer runs vet, tests, builds, and end-to-end checks.

## Passes

`review_rounds` controls the number of passes; the default is one. First review: inspect the full change. Self-review checks requirements and test evidence; competing review checks defects and regressions. On a focused review, inspect changes since your last review and your earlier findings. Review the full change if a fix has wider effects, or if the baseline is missing, requirements or base branch changed, or history was rewritten.

## Findings

- Report substantive defects: incorrect behavior, regressions, unmet requirements, security or authorization flaws, concurrency or data integrity risks, API incompatibility, architecture problems, missing tests, or needless complexity. Skip style preferences.
- Confirm each finding in code or with a check. Give the failing input or state and its effect; drop claims without a concrete case. Flag duplication of an existing helper or needless added complexity.
- Flag a missing test only for a path that could plausibly break. Inability to rerun the implementer's checks is not itself a finding; for claim review, record it as a nonblocking limitation.
- Use `blocking` for defects that must be fixed before merge and `should_fix` for problems that can wait. Point to the file and line when possible. Keep each summary to one sentence; put reasoning in the detail. Give a clean verdict if nothing remains.
