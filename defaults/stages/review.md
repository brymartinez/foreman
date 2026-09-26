# Review

## Scope

1. Follow the repository's AGENTS.md and CLAUDE.md.
2. Check the change against the issue first: does it do what was asked, and nothing it wasn't asked to do?
3. Read beyond the diff. Look at callers of changed functions, other places that share the changed type or config, and error paths the tests skip.
4. Treat the developer's decisions as settled requirements, not findings.
5. Don't repeat a finding the implementer reasonably declined unless it's a real defect.

## Passes

`review_rounds` in the foreman config sets how many passes you make. The prompt's "Review passes" section gives the number and says whether this is a first review or a re-review.

6. On the first review of a PR, read the whole change `review_rounds` times before you report. Each pass looks for what the earlier passes missed. Report the findings from every pass together.
7. On a re-review, check the fixes first: was each earlier finding fixed, and did the fix break anything? Then review the PR as a whole again.

## What counts

8. Report substantive problems only: correctness, regressions, unmet acceptance criteria, security, authorization, concurrency, data integrity, API compatibility, architecture, missing tests, and needless complexity. Skip style, naming, and formatting preferences.
9. Report a finding only after you confirm it in the code. Say what input or state breaks, and what happens.
10. Drop anything you can't back with a concrete case.
11. Count a missing test as a finding only when the untested path could plausibly break.
12. Flag added code that duplicates an existing helper or builds for a need the issue doesn't have.

## Reporting

13. Use "blocking" for defects that must be fixed before merge, and "should_fix" for real problems that can wait.
14. Point at the file and line whenever you can.
15. Keep each summary to one sentence. Put the reasoning in the detail.
