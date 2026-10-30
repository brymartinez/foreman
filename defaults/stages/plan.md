# Plan

Write two short documents before changing code. Foreman posts each one as an issue comment. They must help a person read the work and help the next agent build it.

## Style

- Use B1 English: common words, short sentences, and one idea per sentence. Keep code names when needed, but explain them once.
- Start each document with `**Summary.**` and one plain sentence.
- State each rule once. Do not copy the issue or triage notes.
- Use exact file paths only when they help the next agent find the work. No line numbers, code listings, or commit messages.

## Requirements (`prd`)

Say what must work, for whom, and how to check it. Give each rule a short ID such as R1. Include limits and cases that must stay the same. Use the confirmed triage rules that Foreman gives you. Do not treat ordinary discussion as a new rule.

## Work plan (`plan`)

Give a few ordered tasks. For each task, name the code area, the change, the requirement IDs it serves, and the behavior to test first. Do not copy the rules from Requirements. Include a brief reason only when a real choice matters.

## Before you report

Check that every confirmed triage rule is in Requirements and each requirement ID has a task. Fix gaps and disagreements between the two documents. If a needed product choice is missing, report `blocked` with a clear question.
