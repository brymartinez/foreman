# Plan

Write two short Markdown documents before changing code. Foreman posts each as an issue comment for a person to read and the next agent to use.

- Use B1 English: short sentences, common words, one idea per sentence. Explain needed code terms once.
- Start each document with `**Summary.**` and one plain sentence. State each rule once; do not copy the issue or triage notes. Include file paths only when useful. Omit line numbers, code listings, and commit messages.

## Requirements (`prd`)

State what must work, for whom, and how to check it. Give each rule an ID such as R1. Include limits and behavior that must stay the same. Use Foreman's confirmed triage rules, not ordinary discussion.

## Work plan (`plan`)

List ordered tasks. For each, name the code area, change, requirement IDs, and behavior to test first. Do not repeat the Requirements. Explain a choice only when its reason matters.

Before reporting, check that every confirmed rule appears in Requirements and every requirement has a task. Resolve gaps between the documents. Report `blocked` with a clear question if a product choice is missing.
