# Review

## Scope

1. Follow the repository's AGENTS.md and CLAUDE.md.
2. In self-review, check the change against the Requirements and Work plan Foreman supplies. In competing review, form an independent judgment from the diff. For claim verification, check the Requirements and claims too.
3. Read beyond the diff only when needed to verify a concrete defect in a changed file, such as a caller or an error path the tests skip.
4. Do not seek earlier agent transcripts, issue discussion, PR comments, or prior review rounds for requirements.
5. Try to break the change. Look for counterexamples, regressions, and missing cases by reading the code and tests. The implementation session runs vet, tests, builds, and end-to-end checks; this read-only review does not rerun them.

## Passes

`review_rounds` sets the passes per review. The default is one.

6. On the first review, check the full change. Self-review checks the requirements and test evidence. Competing review checks defects and regressions independently.
7. For a focused review, start with the changes since your last review and check your own earlier findings. Read the full change when the fix may have wider effects. A missing baseline, changed requirements or base branch, or rewritten history starts a full review.

## What counts

8. Report substantive problems only, and a clean verdict when none remain: correctness, regressions, unmet requirements, security, authorization, concurrency, data integrity, API compatibility, architecture, missing tests, and needless complexity. Skip style, naming, and formatting preferences.
9. Report a finding only after you confirm it yourself in the code or with a check. Say what input or state breaks, and what happens.
10. Drop anything you can't back with a concrete case.
11. Count a missing test as a finding only when the untested path could plausibly break.
   An inability to rerun the implementer's reported checks in this review is not a finding by itself. For a claim review, record it as a nonblocking limitation.
12. Flag added code that duplicates an existing helper or adds needless complexity for the behavior visible in the change.

## Reporting

13. Use "blocking" for defects that must be fixed before merge, and "should_fix" for real problems that can wait.
14. Point at the file and line whenever you can.
15. Keep each summary to one sentence. Put the reasoning in the detail.
