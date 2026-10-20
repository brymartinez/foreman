package agent

import (
	"fmt"
	"strings"
)

// fullSuiteRules tell the implementer how to tag its checks and report the full suite.
// Foreman cannot judge evidence. It checks that the report names every path changed
// since the full run, so the judgment of each path stays with the implementer.
const fullSuiteRules = `- Use the checks the repository documents: its AGENTS.md, README, contributing guide, Makefile, scripts, and CI configuration. The full supported suite is the set those sources name for a complete check of the repository.
- Tag every check in "validation" with "scope": "focused" for a check of the paths you changed, "full" for a part of the full supported suite.
- Run focused checks while you edit, at checkpoints, and after review fixes. Run the full suite once before the first review, and again only when a later change affects behavior or a validation input.
- With "pr_opened" or "review_ready", report the full suite in "fullSuite":
  - When you ran it: status "run", revision the commit it ran on, explanation null. If revision is the commit you hand in, docOnly is []. If it is an earlier commit, docOnly lists every path changed since it (` + "`git diff --name-only <revision> HEAD`" + `), each with its evidence.
  - When the repository has no identifiable full suite: status "unavailable", revision and docOnly null, and an explanation of what could not be verified. Still run and report the relevant checks.
  - Null when this turn has no full result to report. Foreman then runs a final check before handoff.
- A path is documentation-only only when repository-specific evidence shows it cannot affect behavior or the supported suite, such as the test, build, or generation configuration that shows nothing reads it. Cite that evidence. A name, extension, or directory alone is not evidence: README, Markdown, reStructuredText, and .txt files can be test, build, or generated inputs, and a documentation-only file can live outside a docs directory.
- Source, tests, configuration, build scripts, generated code, and any other input to the supported checks are not documentation-only. If the evidence is absent or you are unsure, treat the path as an input and run the full suite again.
- Judge the paths a merge of the base branch brings in the same way. A merge that brings in a behavior or validation input, or a path of uncertain effect, needs a new full run. A merge that brings in only paths verified documentation-only leaves the earlier run valid.
`

// fullSuiteSection asks a fresh implementation turn to tag its checks and report the
// full suite.
func fullSuiteSection(s *strings.Builder) {
	s.WriteString("## Validation\n\nForeman hands off a change only when a full-suite result covers its final commit.\n\n")
	s.WriteString(fullSuiteRules + "\n")
}

// FullSuiteMessage ends a resume message for a task with the full-suite gate, so a fix,
// an answer, or a merge reports its checks the same way.
const FullSuiteMessage = "\nTag each check with its scope and report the full suite in \"fullSuite\":\n\n" + fullSuiteRules

// MissingFullCheckMessage resumes a turn that finished a change without a full-suite
// result that covers it.
func MissingFullCheckMessage(status Status, head, reason string) string {
	return fmt.Sprintf("Your %q outcome for %s has no full-suite result that covers it: %s. Foreman needs one before it hands the change on. Run the repository's full supported suite on the commit, or give documentation-only evidence for every path changed since your last full run. If a check fails, fix it and commit; a new commit is reviewed again. End with %q, the same other fields, and the report in \"fullSuite\":\n\n%s", status, short(head), reason, status, fullSuiteRules)
}

// FinalCheck is a change whose full-suite result does not cover it at handoff.
type FinalCheck struct {
	Head     string
	Reason   string   // why the stored result does not cover Head
	Revision string   // the commit of the last full run, if any
	Changed  []string // the paths changed since Revision
	Local    bool     // a task reviewed before its PR, which ends with review_ready
	PR       int
}

// FinalCheckMessage asks the implementer for the full result the handoff needs. The
// turn is an implementer turn, so it fixes a failing check itself, and a commit it
// makes goes through review again.
func FinalCheckMessage(c FinalCheck) string {
	var s strings.Builder
	fmt.Fprintf(&s, "## Final full check\n\nThe review is clean, but Foreman hands off a change only with a full-suite result that covers its head. The change at %s has none: %s.\n\n", short(c.Head), c.Reason)
	if c.Revision != "" && len(c.Changed) > 0 {
		fmt.Fprintf(&s, "Paths changed since the full run on %s:\n\n", short(c.Revision))
		for _, p := range c.Changed {
			fmt.Fprintf(&s, "- %s\n", p)
		}
		s.WriteString("\n")
	}
	s.WriteString("1. Judge each changed path under the rules below. If every one is verified documentation-only, do not rerun the suite. Report the earlier revision with evidence for every path.\n")
	s.WriteString("2. Otherwise run the repository's full supported suite on the head. If a check fails, fix the cause, commit, then rerun the focused checks and the full suite on the new commit. A new commit is reviewed again before handoff.\n")
	s.WriteString("3. If the repository has no identifiable full suite, report it as unavailable with what could not be verified.\n\n")
	if c.Local {
		s.WriteString("Do not push or open a pull request. End with \"review_ready\", the same prTitle and prBody unless a fix changed what the change does, the validation, and \"fullSuite\". ")
	} else {
		fmt.Fprintf(&s, "Push any fix to PR #%d. End with \"pr_opened\" for PR #%d, the validation, and \"fullSuite\". ", c.PR, c.PR)
	}
	s.WriteString("Report \"failed\" if you cannot make the full suite pass.\n\n")
	s.WriteString(fullSuiteRules)
	return s.String()
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
