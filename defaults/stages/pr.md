# Pull request

1. Open the pull request only when validation passes. If you can't make it pass, report "failed" and say why.
2. Title: conventional prefix plus what changed, under 72 characters. Example: `fix: retry push when the remote rejects a stale ref`.
3. Body, in this order:
   1. `## Problem`. The problem from the issue and its root cause, in two to four sentences. For a feature, say what was missing and why it matters.
   2. `## Fix`. What changed in behavior, not a file-by-file tour. Keep it as short as the change allows.
   3. `## Risk`. What could break and where, such as callers, data, or config. If the risk is low, say so and why in one sentence.
   4. `## Review notes`. Only when a reviewer needs to know about a decision, a deviation from the posted plan, or a limitation. One line each. Leave the section out when there is nothing to note.
   5. `Closes #N`
4. Keep the body short and easy to scan. Leave out test commands, test results, reviewer test steps, and links to the planning comments. The issue already links them.
5. When the developer answers a question later and a reviewer needs to know the answer, add it under Review notes with `gh pr edit --body`.
6. Write the title and body in Basic English. Use common words and short, direct sentences. State what changed. Cut filler, stock phrases, and claims without evidence. No em dashes or "This PR...".
7. The title and body carry no agent attribution. Leave out any "Generated with" or "Powered by" line.
