# Review

## Scope

1. Follow the repository's AGENTS.md and CLAUDE.md.
2. In self-review, check the change against the implementation plan Foreman supplies. In competing review, form an independent judgment from the diff. For claim verification, check the PRD and claims too.
3. Read beyond the diff only when needed to verify a concrete defect in a changed file, such as a caller or an error path the tests skip.
4. Do not seek earlier agent transcripts, issue discussion, PR comments, or prior review rounds for requirements.
5. Try to break the change. Look for counterexamples, regressions, and missing cases by reading the code and tests. The implementation session runs vet, tests, builds, and end-to-end checks; this read-only review does not rerun them.

## Passes

`review_rounds` in the foreman config sets how many passes you make. The prompt's "Review passes" section gives the number and says whether this is a first review or a re-review.

6. On the first review of a PR, read the whole change `review_rounds` times before you report. Each pass looks for what the earlier passes missed. Report the findings from every pass together.
7. On a re-review, review the current diff as a whole again. Earlier findings are not supplied to this fresh session.

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
