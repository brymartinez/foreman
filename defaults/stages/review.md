# Review

## Scope

- Follow repository review methods within the scope specified in the prompt.
- Read the assigned diff using the command in the prompt. The first pass covers the full branch change. A follow-up covers the assigned fix range. Inspect nearby code and tests to trace callers and data boundaries when needed.
- Review the code independently. Leave requirements, implementation plans, developer decisions, PR text, validation reports, and past review findings to the implementer.
- Collect all concrete defects before returning. The implementer runs tests, builds, and end-to-end checks.

## Passes and tools

Follow the prompt's instructions for the number of review passes. Use the supplied git diff command from the current working directory. Run it as a standalone command. If you cannot inspect the supplied diff, report a blocking limitation and explain what is unavailable.

## Findings

- Report substantive defects with a failing input or state and its effect. Skip style preferences.
- Distinguish defects that must be fixed before merge from other substantive defects. Point to the file and line when available. Give each finding a one-sentence summary and explain the reasoning separately.
- Report a missing test when it leaves a plausible failure path uncovered. Use a limitation to describe evidence you cannot inspect.
- Report no defects only after you have inspected the supplied diff and found none. Report concrete concerns for the implementer to assess.
- Use the response format provided in the prompt. Write in plain English.
