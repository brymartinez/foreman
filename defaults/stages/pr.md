# Pull request

1. Open the pull request only when validation passes. If you can't make it pass, report "failed" and say why.
2. Title: conventional prefix plus what changed, under 72 characters. Example: `fix: retry push when the remote rejects a stale ref`.
3. Body, in this order:
   1. **What was wrong.** The problem from the issue and its root cause, in two to four sentences. For a feature, say what was missing and why it matters.
   2. **The fix.** What changed in behavior, not a file-by-file tour. List every decision the developer answered, any deviation from the posted plan and why, plus choices a reviewer might question, one line each.
   3. **How to test.** Each command you ran and its result, then the steps a reviewer can follow to see the change work.
   4. **Risk.** What could break and where, such as callers, data, or config. If the risk is low, say so and why in one sentence.
   5. `Closes #N`
4. When the developer answers a question later, add the decision to the fix section with `gh pr edit --body`.
5. Write it plainly. No em dashes, no filler, no "This PR...".
6. The title and body carry no agent attribution. Leave out any "Generated with" or "Powered by" line.
