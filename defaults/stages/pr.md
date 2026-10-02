# Pull request

1. Open the pull request only when validation passes. If you can't make it pass, report "failed" and say why.
2. Title: conventional prefix plus what changed, under 72 characters. Example: `fix: retry push when the remote rejects a stale ref`.
3. Body, in this order:
   1. **What was wrong.** The problem from the issue and its root cause, in two or three sentences. For a feature, say what was missing and why it matters.
   2. **The fix.** What changed in behavior, not a file-by-file tour. Then one line each for every decision the developer answered, any deviation from the posted plan and why, and choices a reviewer might question.
   3. **How to test.** The commands you ran and their results, only those that matter. Then the steps a reviewer can follow to see the change work.
   4. **Risk.** What could break and where, such as callers, data, or config, in one or two lines. If the risk is low, say so and why.
   5. **Plan.** Links to the issue's Analysis, PRD, Design, and Implementation plan comments.
   6. `Closes #N`
4. Keep the body brief and easy to scan: short lists, about 25 lines or fewer. Link the planning comments for the full plan. Do not repeat their content.
5. When the developer answers a question later, add the decision to the fix section with `gh pr edit --body`.
6. Write the title and body in Basic English. Use common words and short, direct sentences. State what changed and what the tests showed. Cut filler, stock phrases, and claims without evidence. No em dashes or "This PR...".
7. The title and body carry no agent attribution. Leave out any "Generated with" or "Powered by" line.
