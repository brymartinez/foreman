package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/brymartinez/foreman/internal/task"
)

const blockedWire = `{"status":"blocked","summary":"s","prNumber":null,"prUrl":null,"validation":[],
"question":{"issue":"Should retries apply to 4xx?","context":"Retries 5xx only.",
"options":[{"label":"A","description":"5xx only"},{"label":"B","description":"4xx + 5xx"}],
"recommendation":"A","impactIfWrong":"Repeated client errors."}}`

func TestParseOutcomeBlocked(t *testing.T) {
	got, err := ParseOutcome([]byte(blockedWire))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Blocked || got.Question == nil || got.Question.Recommendation != "A" || len(got.Question.Options) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestParseOutcomePROpenedNeedsPRFields(t *testing.T) {
	_, err := ParseOutcome([]byte(`{"status":"pr_opened","summary":"s","prNumber":null,"prUrl":null,"validation":[],"question":null}`))
	if err == nil || !strings.Contains(err.Error(), "prNumber") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseOutcomeRejectsSingleOptionQuestion(t *testing.T) {
	wire := `{"status":"blocked","summary":"s","prNumber":null,"prUrl":null,"validation":[],
"question":{"issue":"q","context":"c","options":[{"label":"A","description":"only"}],"recommendation":"A","impactIfWrong":"i"}}`
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
func TestSchemaIsStrictModeCompatible(t *testing.T) {
	var root any
	if err := json.Unmarshal(OutcomeSchema, &root); err != nil {
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
