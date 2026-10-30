package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

const blockedWire = `{"status":"blocked","summary":"s","prNumber":null,"prUrl":null,"validation":[],
"questions":[{"issue":"Should retries apply to 4xx?","context":"Retries 5xx only.",
"options":[{"label":"A","description":"5xx only"},{"label":"B","description":"4xx + 5xx"}],
"recommendation":"A","impactIfWrong":"Repeated client errors."},
{"issue":"Cap the retry count?","context":"No cap today.",
"options":[{"label":"A","description":"3 tries"},{"label":"B","description":"no cap"}],
"recommendation":"A","impactIfWrong":"Unbounded retries."}]}`

func TestParseOutcomeBlocked(t *testing.T) {
	got, err := ParseOutcome([]byte(blockedWire))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Blocked || len(got.Questions) != 2 || got.Questions[0].Recommendation != "A" || got.Questions[1].Issue != "Cap the retry count?" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseOutcomePROpened(t *testing.T) {
	_, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":912,"prUrl":"u","validation":[],"questions":[]}`))
	if err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("empty validation err = %v", err)
	}
	got, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":null,"prUrl":null,"validation":[{"command":"go test ./...","passed":true}],"questions":[]}`))
	if err != nil || got.PRNumber != 0 || len(got.Validation) != 1 {
		t.Fatalf("missing number must parse as 0 for branch lookup: %+v %v", got, err)
	}
}

func TestParseOutcomePlanned(t *testing.T) {
	got, err := ParseOutcome([]byte(`{"status":"planned","summary":"s","prNumber":null,"prUrl":null,"validation":[],"questions":[],
"documents":{"prd":"p","plan":"### Task 1"}}`))
	if err != nil || got.Status != Planned || got.Documents[task.PRD] != "p" || got.Documents[task.ImplementationPlan] != "### Task 1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := ParseOutcome([]byte(`{"status":"planned","summary":"s","documents":null}`)); err == nil || !strings.Contains(err.Error(), "no documents") {
		t.Errorf("planned without documents err = %v", err)
	}
}

// A document already on the issue may come back empty to keep its comment. The runner
// knows which documents exist, so it rejects an empty one that has no comment.
func TestParseOutcomePlannedKeepsAnEmptyDocumentForTheRunner(t *testing.T) {
	got, err := ParseOutcome([]byte(`{"status":"planned","summary":"s","documents":{"prd":" ","plan":"p"}}`))
	if err != nil || got.Documents[task.PRD] != " " || got.Documents[task.ImplementationPlan] != "p" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseOutcomeCheckpointRequiresShortNextObjectiveAndPassingValidation(t *testing.T) {
	valid := `{"status":"checkpoint","summary":"committed the first part","nextObjective":"Add the log pager","prNumber":null,"prUrl":null,"validation":[{"command":"go test ./...","passed":true}],"questions":[],"documents":null}`
	got, err := ParseOutcome([]byte(valid))
	if err != nil || got.Status != Checkpoint || got.NextObjective != "Add the log pager" || len(got.Validation) != 1 {
		t.Fatalf("checkpoint = %+v, %v", got, err)
	}
	for name, wire := range map[string]string{
		"missing next objective": strings.Replace(valid, `"Add the log pager"`, "null", 1),
		"failed validation":      strings.Replace(valid, `"passed":true`, `"passed":false`, 1),
		"no validation":          strings.Replace(valid, `[{"command":"go test ./...","passed":true}]`, `[]`, 1),
		"long next objective":    strings.Replace(valid, `"Add the log pager"`, `"`+strings.Repeat("x", 501)+`"`, 1),
	} {
		if _, err := ParseOutcome([]byte(wire)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestParseOutcomePreservesFailedCheckpointChecksForRepair(t *testing.T) {
	raw := []byte(`{"status":"checkpoint","summary":"committed","nextObjective":"Continue work","prNumber":null,"prUrl":null,"validation":[{"command":"pnpm test","passed":true},{"command":"pnpm lint","passed":false}],"questions":[],"documents":null}`)
	got, err := ParseOutcome(raw)
	var failed *CheckpointValidationError
	if !errors.As(err, &failed) || len(failed.Failed) != 1 || failed.Failed[0] != "pnpm lint" {
		t.Fatalf("failed checks = %+v, err = %v", failed, err)
	}
	if got.Status != Checkpoint || len(got.Validation) != 2 || got.NextObjective != "Continue work" {
		t.Fatalf("repairable checkpoint lost: %+v", got)
	}
}

func TestImplementationPromptOffersCheckpointsOnlyBeforeAPullRequest(t *testing.T) {
	b := TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, IssueBody: "A detailed issue body", Branch: "feat/7", BaseBranch: "main", Worktree: "/wt"}
	if p := ImplementationPrompt(b); !strings.Contains(p, `status "checkpoint"`) {
		t.Fatal("initial implementation prompt lacks checkpoint guidance")
	}
	b.CheckpointHead, b.NextObjective = "abc123", "Add the log pager"
	if p := ImplementationPrompt(b); !strings.Contains(p, "Add the log pager") || strings.Contains(p, "A detailed issue body") {
		t.Fatal("checkpoint continuation did not use the small handoff")
	}
	b.PR = 912
	if p := ImplementationPrompt(b); strings.Contains(p, `status "checkpoint"`) {
		t.Fatal("PR feedback prompt offered checkpoints")
	}
	b.PR, b.Planning = 0, true
	if p := ImplementationPrompt(b); strings.Contains(p, `status "checkpoint"`) {
		t.Fatal("planning prompt offered checkpoints")
	}
}

func TestParseOutcomeRejectsSingleOptionQuestion(t *testing.T) {
	wire := `{"status":"blocked","summary":"s","prNumber":null,"prUrl":null,"validation":[],
"questions":[{"issue":"q","context":"c","options":[{"label":"A","description":"only"}],"recommendation":"A","impactIfWrong":"i"}]}`
	if _, err := ParseOutcome([]byte(wire)); err == nil || !strings.Contains(err.Error(), "two options") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseOutcomeRejectsUnknownStatus(t *testing.T) {
	if _, err := ParseOutcome([]byte(`{"status":"thinking","summary":"s"}`)); err == nil {
		t.Fatal("accepted unknown status")
	}
}

// Codex --output-schema is strict: every object must require all its properties and forbid extras.
func TestSchemasAreStrictModeCompatible(t *testing.T) {
	for name, schema := range map[string][]byte{"outcome": OutcomeSchema, "review": ReviewSchema, "selection": SelectionSchema} {
		t.Run(name, func(t *testing.T) { checkStrict(t, schema) })
	}
}

func checkStrict(t *testing.T, schema []byte) {
	var root any
	if err := json.Unmarshal(schema, &root); err != nil {
		t.Fatal(err)
	}
	var visit func(path string, n any)
	visit = func(path string, n any) {
		switch v := n.(type) {
		case map[string]any:
			if props, ok := v["properties"].(map[string]any); ok {
				if v["additionalProperties"] != false {
					t.Errorf("%s: additionalProperties must be false", path)
				}
				var req []string
				for _, r := range v["required"].([]any) {
					req = append(req, r.(string))
				}
				for k := range props {
					if !slices.Contains(req, k) {
						t.Errorf("%s: property %q not required", path, k)
					}
				}
			}
			for k, c := range v {
				visit(path+"."+k, c)
			}
		case []any:
			for _, c := range v {
				visit(path+"[]", c)
			}
		}
	}
	visit("$", root)
}

func TestReviewerIsTheOtherAgent(t *testing.T) {
	if task.Claude.Reviewer() != task.Codex || task.Codex.Reviewer() != task.Claude {
		t.Fatal("reviewer must be the competing agent")
	}
}

func TestParseReview(t *testing.T) {
	clean, err := ParseReview([]byte(`{"verdict":"clean","summary":"ok","findings":[]}`))
	if err != nil || !clean.Clean {
		t.Fatalf("clean: %+v %v", clean, err)
	}
	cr, err := ParseReview([]byte(`{"verdict":"changes_requested","summary":"s","findings":[{"category":"defect","severity":"blocking","file":"a.go","line":12,"summary":"nil deref","detail":"d"},{"category":"defect","severity":"should_fix","file":null,"line":null,"summary":"missing test","detail":""}]}`))
	if err != nil || cr.Clean || len(cr.Findings) != 2 || cr.Findings[0].Line != 12 || cr.Findings[1].File != "" {
		t.Fatalf("changes: %+v %v", cr, err)
	}
	for _, bad := range []string{
		`{"verdict":"clean","summary":"s","findings":[{"category":"defect","severity":"blocking","file":null,"line":null,"summary":"x","detail":""}]}`,
		`{"verdict":"changes_requested","summary":"s","findings":[]}`,
		`{"verdict":"lgtm","summary":"s","findings":[]}`,
		`{"verdict":"changes_requested","summary":"s","findings":[{"category":"defect","severity":"nit","file":null,"line":null,"summary":"x","detail":""}]}`,
	} {
		if _, err := ParseReview([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestParseOutcomeRejectsBlockedWithoutQuestions(t *testing.T) {
	wire := `{"status":"blocked","summary":"s","prNumber":null,"prUrl":null,"validation":[],"questions":[]}`
	if _, err := ParseOutcome([]byte(wire)); err == nil || !strings.Contains(err.Error(), "no questions") {
		t.Fatalf("err = %v", err)
	}
}

func TestFeedbackMessageNamesEachCommentAndItsReply(t *testing.T) {
	items := []FeedbackItem{
		{Kind: task.ReviewComment, ID: 5502, Author: "brymartinez", Body: "## Still wrong\nThis retries 4xx.",
			URL: "https://github.com/a/b/pull/912#discussion_r5502", Path: "retry.go", Line: 42, InReplyTo: 5501},
		{Kind: task.IssueComment, ID: 4401, Author: "dependabot[bot]", Body: "Bump the dep first.",
			URL: "https://github.com/a/b/pull/912#issuecomment-4401"},
		{Kind: task.ReviewSummary, ID: 6601, Author: "brymartinez", Body: "Two things.",
			URL: "https://github.com/a/b/pull/912#pullrequestreview-6601"},
	}
	got := FeedbackMessage("a/b", 912, items)
	for _, want := range []string{
		"has 3 comments you have not answered",
		ReplyMarker,
		// An inline reply goes to the thread's first comment, not to the reply itself.
		`gh api --method POST repos/a/b/pulls/912/comments/5501/replies -f body="..."`,
		"1. brymartinez, a reply in an inline thread on retry.go:42",
		"2. dependabot[bot], a comment on the pull request",
		"3. brymartinez, a review summary",
		`gh api --method POST repos/a/b/issues/912/comments -f body="..."`,
		// The comment's own Markdown is quoted, so it cannot read as an instruction.
		"> ## Still wrong\n> This retries 4xx.",
		`end with the JSON outcome "pr_opened" for PR #912`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, ReplyMarker); n != 1 {
		t.Errorf("the reply marker is stated %d times, want once", n)
	}
}

func TestImplementationPromptCarriesFeedbackForAFreshTurn(t *testing.T) {
	got := ImplementationPrompt(TaskBrief{
		Key: task.Key{Repo: "a/b", Number: 7}, Title: "Add retries", PR: 912,
		Feedback: []FeedbackItem{{Kind: task.IssueComment, ID: 4401, Author: "brymartinez", Body: "Cap it.", URL: "https://x"}},
	})
	for _, want := range []string{"## Pull request feedback", "comments on pull request #912", ReplyMarker, "> Cap it."} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestMergeMessage(t *testing.T) {
	got := MergeMessage("feat/7-retries", "main", []string{"go.mod", "retry.go"})
	for _, want := range []string{"- go.mod\n", "- retry.go\n", "`git merge origin/main` on feat/7-retries", "Never rebase", "never force-push",
		"do not run `git merge --abort`", "Leave the merge in progress", `end with "blocked"`, "I resolved the files; verify, commit, push", "pr_opened"} {
		if !strings.Contains(got, want) {
			t.Errorf("merge message missing %q:\n%s", want, got)
		}
	}
}

func TestImplementationPromptMergeSection(t *testing.T) {
	b := TaskBrief{Key: task.Key{Repo: "a/b", Number: 7}, Branch: "feat/7-retries", BaseBranch: "main"}
	if got := ImplementationPrompt(b); strings.Contains(got, "## Merge conflicts") {
		t.Errorf("merge section without a merge:\n%s", got)
	}
	b.Merge = &MergeBrief{Base: "main", Files: []string{"retry.go"}}
	got := ImplementationPrompt(b)
	for _, want := range []string{"## Merge conflicts", "- retry.go\n", "`git merge origin/main`", "Never rebase"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestParseOutcomeReviewReadyNeedsAProposedPRAndPassingValidation(t *testing.T) {
	valid := `{"status":"review_ready","summary":"committed the change","nextObjective":null,"prNumber":null,"prUrl":null,"validation":[{"command":"go test ./...","passed":true}],"questions":[],"documents":null,"prTitle":" feat: retry 5xx ","prBody":"## Problem\n\nRetries.\n\nCloses #7\n"}`
	got, err := ParseOutcome([]byte(valid))
	if err != nil || got.Status != ReviewReady || got.PRTitle != "feat: retry 5xx" || got.PRBody != "## Problem\n\nRetries.\n\nCloses #7" || len(got.Validation) != 1 {
		t.Fatalf("review_ready = %+v, %v", got, err)
	}
	for name, wire := range map[string]string{
		"missing title":     strings.Replace(valid, `" feat: retry 5xx "`, "null", 1),
		"blank description": strings.Replace(valid, `"## Problem\n\nRetries.\n\nCloses #7\n"`, `"  "`, 1),
		"failed validation": strings.Replace(valid, `"passed":true`, `"passed":false`, 1),
		"no validation":     strings.Replace(valid, `[{"command":"go test ./...","passed":true}]`, `[]`, 1),
		"a PR number":       strings.Replace(valid, `"prNumber":null`, `"prNumber":12`, 1),
		"a PR URL":          strings.Replace(valid, `"prUrl":null`, `"prUrl":"u"`, 1),
	} {
		if _, err := ParseOutcome([]byte(wire)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestParseOutcomeReturnsTheClaimsOfAChange(t *testing.T) {
	const claims = `"claims":[{"criteria":["AC1","AC2"],"behavior":" retries 5xx ","evidence":"retry.go:40, retry_test.go","check":"go test ./..."}]`
	for _, status := range []string{
		`"status":"pr_opened","prNumber":912,"prUrl":"u"`,
		`"status":"review_ready","prNumber":null,"prUrl":null,"prTitle":"feat: x","prBody":"Closes #7"`,
	} {
		got, err := ParseOutcome([]byte(`{` + status + `,"summary":"s","validation":[{"command":"go test ./...","passed":true}],"questions":[],` + claims + `}`))
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		want := []task.Claim{{Criteria: []string{"AC1", "AC2"}, Behavior: " retries 5xx ", Evidence: "retry.go:40, retry_test.go", Check: "go test ./..."}}
		if task.ClaimsDigest(got.Claims) != task.ClaimsDigest(want) || len(got.Claims[0].Criteria) != 2 {
			t.Errorf("%s: claims = %+v", status, got.Claims)
		}
	}
}

func TestParseOutcomeLeavesNullOrEmptyClaimsForTheRunnerToRequire(t *testing.T) {
	for _, claims := range []string{`"claims":null`, `"claims":[]`, `"x":0`} {
		got, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":912,"prUrl":"u","validation":[{"command":"go test ./...","passed":true}],"questions":[],` + claims + `}`))
		if err != nil || len(got.Claims) != 0 {
			t.Errorf("%s: claims = %+v, %v", claims, got.Claims, err)
		}
	}
}

func TestParseOutcomeRejectsAnIncompleteClaim(t *testing.T) {
	_, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":912,"prUrl":"u","validation":[{"command":"go test ./...","passed":true}],"questions":[],
"claims":[{"criteria":["AC1"],"behavior":"retries 5xx","evidence":"retry.go","check":"go test"},{"criteria":["AC2"],"behavior":"caps retries","evidence":" ","check":"go test"}]}`))
	if err == nil || !strings.Contains(err.Error(), "claim 2") || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("err = %v, want claim 2's missing evidence named", err)
	}
}

func TestOutcomeSchemaRequiresANullableClaimsArray(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type  json.RawMessage `json:"type"`
			Items struct {
				Required []string `json:"required"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(OutcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	claims, ok := schema.Properties["claims"]
	var types []string
	_ = json.Unmarshal(claims.Type, &types)
	if !ok || !slices.Contains(schema.Required, "claims") || !slices.Equal(types, []string{"array", "null"}) {
		t.Fatalf("claims schema = %+v, required %v", claims, schema.Required)
	}
	for _, field := range []string{"criteria", "behavior", "evidence", "check"} {
		if !slices.Contains(claims.Items.Required, field) {
			t.Errorf("a claim does not require %s", field)
		}
	}
}

func TestParseOutcomeReadsCheckScopes(t *testing.T) {
	got, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":912,"prUrl":"u","questions":[],
"validation":[{"command":"go test ./internal/app","passed":true,"scope":"focused"},{"command":"go test ./...","passed":true,"scope":"full"},{"command":"go vet ./...","passed":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []task.CheckScope{task.FocusedCheck, task.FullCheck, task.FocusedCheck}
	for i, c := range got.Validation {
		if c.Scope != want[i] {
			t.Errorf("check %d scope = %q, want %q", i, c.Scope, want[i])
		}
	}
	_, err = ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":912,"prUrl":"u","questions":[],"validation":[{"command":"go test ./...","passed":true,"scope":"everything"}]}`))
	if err == nil || !strings.Contains(err.Error(), "everything") {
		t.Fatalf("unknown scope err = %v", err)
	}
}

func TestParseOutcomeReadsAFullSuiteReport(t *testing.T) {
	const prefix = `{"status":"review_ready","summary":"s","prTitle":"feat: x","prBody":"Closes #7","validation":[{"command":"go test ./...","passed":true,"scope":"full"}],"fullSuite":`
	got, err := ParseOutcome([]byte(prefix + `{"status":"run","revision":" abc123 ","docOnly":[{"path":"docs/guide.md","evidence":"no test or build reads docs/"}],"explanation":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f := got.FullSuite; f == nil || f.Status != FullSuiteRun || f.Revision != "abc123" || len(f.DocOnly) != 1 || f.DocOnly[0].Path != "docs/guide.md" {
		t.Fatalf("full suite = %+v", got.FullSuite)
	}
	got, err = ParseOutcome([]byte(prefix + `{"status":"unavailable","revision":null,"docOnly":null,"explanation":"the repository documents no test command"}}`))
	if err != nil || got.FullSuite == nil || got.FullSuite.Status != FullSuiteUnavailable || got.FullSuite.Explanation != "the repository documents no test command" {
		t.Fatalf("unavailable = %+v, %v", got.FullSuite, err)
	}
	for _, absent := range []string{`null}`, `null,"x":0}`} {
		if got, err := ParseOutcome([]byte(prefix + absent)); err != nil || got.FullSuite != nil {
			t.Errorf("absent report = %+v, %v", got.FullSuite, err)
		}
	}
	for name, report := range map[string]string{
		"a run without a revision":          `{"status":"run","revision":" ","docOnly":[],"explanation":null}`,
		"an unavailable suite unexplained":  `{"status":"unavailable","revision":null,"docOnly":null,"explanation":" "}`,
		"a documentation-only path unnamed": `{"status":"run","revision":"abc","docOnly":[{"path":"","evidence":"e"}],"explanation":null}`,
		"an unknown status":                 `{"status":"skipped","revision":"abc","docOnly":[],"explanation":null}`,
	} {
		if _, err := ParseOutcome([]byte(prefix + report + `}`)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Evidence is the implementer's judgment. An empty entry leaves its path uncovered for
// the runner to report, rather than failing the turn's output.
func TestParseOutcomeKeepsEmptyEvidenceForTheRunnerToJudge(t *testing.T) {
	got, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":9,"prUrl":"u","validation":[{"command":"go test ./...","passed":true,"scope":"full"}],
"fullSuite":{"status":"run","revision":"abc","docOnly":[{"path":"README.md","evidence":" "}],"explanation":null}}`))
	if err != nil || got.FullSuite == nil || len(got.FullSuite.DocOnly) != 1 {
		t.Fatalf("got %+v, %v", got.FullSuite, err)
	}
}

func TestParseOutcomeIgnoresAFullSuiteReportOnACheckpoint(t *testing.T) {
	got, err := ParseOutcome([]byte(`{"status":"checkpoint","summary":"s","nextObjective":"next part","validation":[{"command":"go test ./internal/app","passed":true,"scope":"focused"}],
"fullSuite":{"status":"bogus","revision":null,"docOnly":null,"explanation":null}}`))
	if err != nil || got.FullSuite != nil {
		t.Fatalf("checkpoint = %+v, %v", got.FullSuite, err)
	}
}

func TestOutcomeSchemaRequiresANullableFullSuiteAndCheckScopes(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type     json.RawMessage `json:"type"`
			Required []string        `json:"required"`
			Items    struct {
				Required []string `json:"required"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(OutcomeSchema), &schema); err != nil {
		t.Fatal(err)
	}
	full, ok := schema.Properties["fullSuite"]
	var types []string
	_ = json.Unmarshal(full.Type, &types)
	if !ok || !slices.Contains(schema.Required, "fullSuite") || !slices.Equal(types, []string{"object", "null"}) {
		t.Fatalf("fullSuite schema = %+v, required %v", full, schema.Required)
	}
	for _, field := range []string{"status", "revision", "docOnly", "explanation"} {
		if !slices.Contains(full.Required, field) {
			t.Errorf("fullSuite does not require %s", field)
		}
	}
	if !slices.Contains(schema.Properties["validation"].Items.Required, "scope") {
		t.Error("a check does not require scope")
	}
}
