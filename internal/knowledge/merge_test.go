package knowledge

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func maintainer(kind Kind, text string) Proposal {
	return Proposal{Kind: kind, Text: text, Scope: Reusable, Source: Maintainer, Why: "maintainer said so", Cites: []string{"D1"}}
}

func prepare(t *testing.T, local, incoming Snapshot) *Merge {
	t.Helper()
	m, err := Prepare(local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// together groups every item into one learning with the given text.
func together(m *Merge, kind Kind, text string) []Group {
	return []Group{{Members: m.Refs(), Kind: kind, Text: text}}
}

func merged(t *testing.T, m *Merge, groups []Group) Snapshot {
	t.Helper()
	x, err := m.Apply(groups)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func evaluationTasks(x Snapshot) []string {
	var out []string
	for _, ev := range x.Evaluations {
		out = append(out, ev.Task)
	}
	slices.Sort(out)
	return out
}

func TestEquivalentLearningsFromTwoMachinesMergeWithBothTasksEvidence(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, observed(Convention, "Wrap errors with the operation name.")))
	mustApply(t, b, repo, eval("2", 3, observed(Convention, "Errors are wrapped with what was being done.")))

	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	if m.Unchanged() || !m.NeedsGrouping() || len(m.Items) != 2 {
		t.Fatalf("merge = %+v", m)
	}
	x := merged(t, m, together(m, Convention, "Wrap returned errors with the operation name."))
	if len(x.Learnings) != 1 {
		t.Fatalf("learnings = %+v", x.Learnings)
	}
	l := x.Learnings[0]
	if l.ID != "L1" || l.Status != Active || l.Tasks() != 2 || l.Text != "Wrap returned errors with the operation name." {
		t.Fatalf("merged learning = %+v", l)
	}
	if got := evaluationTasks(x); !slices.Equal(got, []string{"1", "2"}) {
		t.Fatalf("evaluations = %v", got)
	}
	if x.Evaluations[1].Proposals[0].Supports != nil {
		t.Fatalf("imported proposal supports = %v", x.Evaluations[1].Proposals[0].Supports)
	}
}

func TestDistinctLearningsKeepTheirOwnEvidenceAndGetNewIDs(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, observed(Convention, "Wrap errors with the operation name.")))
	mustApply(t, b, repo, eval("2", 3, observed(Practice, "Run the linter before pushing.")))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	x := merged(t, m, m.Singletons())
	if len(x.Learnings) != 2 || x.Learnings[0].ID != "L1" || x.Learnings[1].ID != "L2" || x.NextID != 2 {
		t.Fatalf("learnings = %+v, next id %d", x.Learnings, x.NextID)
	}
	if x.Learnings[1].Evidence[0].Task != "2" || x.Learnings[1].Status != Candidate {
		t.Fatalf("imported learning = %+v", x.Learnings[1])
	}
}

func TestATaskIDTakenByADifferentTaskIsRenamedSoBothCount(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	text := "Wrap errors with the operation name."
	mustApply(t, a, repo, eval("1", 0, observed(Convention, text)))
	mustApply(t, b, repo, eval("1", 5, observed(Convention, text)))

	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	x := merged(t, m, together(m, Convention, text))
	renamedID := renamed(taskKey{"1", day(5)})
	if got := evaluationTasks(x); !slices.Equal(got, []string{"1", renamedID}) {
		t.Fatalf("evaluations = %v", got)
	}
	if l := x.Learnings[0]; l.Tasks() != 2 || l.Status != Active {
		t.Fatalf("learning = %+v", l)
	}
	if !validName(renamedID, taskID) {
		t.Fatalf("renamed id %q is not a valid task id", renamedID)
	}

	// The other machine ingesting the merged result renames this machine's task 1
	// and recognizes its own under the renamed id.
	back := prepare(t, snapshot(t, b, repo), x)
	y := merged(t, back, together(back, Convention, text))
	if got := evaluationTasks(y); len(got) != 2 || !slices.Contains(got, "1") || slices.Contains(got, renamedID) {
		t.Fatalf("evaluations after the return trip = %v", got)
	}
	if l := y.Learnings[0]; l.Tasks() != 2 || len(l.Evidence) != 2 {
		t.Fatalf("learning after the return trip = %+v", l)
	}
	if again := prepare(t, y, x); !again.Unchanged() {
		t.Fatalf("a second return trip changes something: %+v", again.Items)
	}
}

func TestTheSameTaskFromTwoMachinesCountsOnce(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	text := "Wrap errors with the operation name."
	mustApply(t, a, repo, eval("1", 0, observed(Convention, text)))
	if err := b.Replace(repo, Restore(snapshot(t, a, repo), repo)); err != nil {
		t.Fatal(err)
	}
	mustApply(t, b, repo, eval("2", 3, observed(Convention, "Errors carry the operation name.")))

	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	x := merged(t, m, together(m, Convention, text))
	l := x.Learnings[0]
	if len(l.Evidence) != 2 || l.Tasks() != 2 || l.Status != Active {
		t.Fatalf("learning = %+v", l)
	}
	if got := evaluationTasks(x); !slices.Equal(got, []string{"1", "2"}) {
		t.Fatalf("evaluations = %v", got)
	}
}

func TestANewerEvaluationOfTheSameTaskReplacesTheOlderOne(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, observed(Convention, "Wrap errors with the operation name.")))
	if err := b.Replace(repo, Restore(snapshot(t, a, repo), repo)); err != nil {
		t.Fatal(err)
	}
	mustApply(t, b, repo, eval("2", 3, observed(Fact, "The API package has no tests.")))
	// Machine A re-evaluates task 1 after new feedback, and it no longer supports
	// the convention.
	later := eval("1", 0, observed(Practice, "Run the linter before pushing."))
	later.At, later.Fingerprint = day(9), "fp-1-later"
	mustApply(t, a, repo, later)

	m := prepare(t, snapshot(t, b, repo), snapshot(t, a, repo))
	x := merged(t, m, m.Singletons())
	for _, ev := range x.Evaluations {
		if ev.Task == "1" && ev.Fingerprint != "fp-1-later" {
			t.Fatalf("kept the older evaluation of task 1: %+v", ev)
		}
	}
	for _, l := range x.Learnings {
		if l.Text == "Wrap errors with the operation name." && (l.Status != Retired || len(l.Evidence) != 0) {
			t.Fatalf("the withdrawn convention kept task 1's support: %+v", l)
		}
	}
	if !slices.ContainsFunc(x.Learnings, func(l Learning) bool { return l.Text == "Run the linter before pushing." && l.Tasks() == 1 }) {
		t.Fatalf("the newer evaluation's learning is missing: %+v", x.Learnings)
	}
}

func TestAnExportAlreadyIngestedAddsNothing(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, observed(Convention, "Wrap errors with the operation name.")))
	mustApply(t, b, repo, eval("2", 3, observed(Convention, "Errors carry the operation name.")))
	export := snapshot(t, b, repo)
	m := prepare(t, snapshot(t, a, repo), export)
	if err := a.Replace(repo, merged(t, m, together(m, Convention, "Wrap errors with the operation name."))); err != nil {
		t.Fatal(err)
	}
	if again := prepare(t, snapshot(t, a, repo), export); !again.Unchanged() {
		t.Fatalf("repeat ingest is not a no-op: %+v", again.Items)
	}
	if self := prepare(t, snapshot(t, a, repo), snapshot(t, a, repo)); !self.Unchanged() {
		t.Fatal("ingesting a store into itself changes it")
	}
}

func TestApplyRejectsAGroupingThatDoesNotPlaceEveryLearningOnce(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, observed(Convention, "Wrap errors with the operation name.")))
	mustApply(t, b, repo, eval("2", 3, observed(Practice, "Run the linter before pushing.")))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	for name, groups := range map[string][]Group{
		"missing":  {{Members: []string{"local:L1"}, Kind: Convention, Text: "x"}},
		"twice":    {{Members: []string{"local:L1", "import:L1"}, Kind: Convention, Text: "x"}, {Members: []string{"import:L1"}, Kind: Practice, Text: "y"}},
		"unknown":  {{Members: []string{"local:L1", "import:L1", "import:L9"}, Kind: Convention, Text: "x"}},
		"bad kind": {{Members: []string{"local:L1", "import:L1"}, Kind: "rumour", Text: "x"}},
		"unsafe":   {{Members: []string{"local:L1", "import:L1"}, Kind: Convention, Text: "See #12."}},
	} {
		if _, err := m.Apply(groups); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// retiredBy builds a store whose only learning a maintainer retired when the
// evaluation at evaluatedAt recorded it.
func retiredBy(t *testing.T, evaluatedAt time.Time) Store {
	t.Helper()
	s, _ := newStore(t)
	l := activeLearning(t, s)
	ev := eval("3", 4)
	ev.At = evaluatedAt
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "Maintainer made the script optional.", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, ev)
	return s
}

func TestAMaintainerRetirementBeatsObservedSupport(t *testing.T) {
	a := retiredBy(t, day(5))
	b, _ := newStore(t)
	text := "Run the e2e script before pushing."
	mustApply(t, b, repo, eval("7", 20, observed(Practice, text)))
	mustApply(t, b, repo, eval("8", 21, observed(Practice, text)))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	x := merged(t, m, together(m, Practice, text))
	if l := x.Learnings[0]; l.Status != Retired {
		t.Fatalf("observed support revived a maintainer retirement: %+v", l)
	}
}

func TestMaintainerSupportBeatsAnObservedRetirement(t *testing.T) {
	a, _ := newStore(t)
	l := activeLearning(t, a)
	ev := eval("9", 10)
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "The script was skipped.", Source: Observed}}
	mustApply(t, a, repo, ev)
	b, _ := newStore(t)
	mustApply(t, b, repo, eval("20", 1, maintainer(Practice, l.Text)))

	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	x := merged(t, m, together(m, Practice, l.Text))
	if got := x.Learnings[0]; got.Status != Active || got.History[len(got.History)-1].Action != "restore" {
		t.Fatalf("maintainer support lost to an observation: %+v", got)
	}
}

func TestTheLaterMaintainerCorrectionWinsWhenTheOrderIsReliable(t *testing.T) {
	text := "Run the e2e script before pushing."
	for name, tc := range map[string]struct {
		retiredAt, supportedAt time.Time
		want                   Status
	}{
		"retirement later": {day(9), day(2), Retired},
		"support later":    {day(5), day(9), Active},
	} {
		t.Run(name, func(t *testing.T) {
			a := retiredBy(t, tc.retiredAt)
			b, _ := newStore(t)
			ev := eval("20", 0, maintainer(Practice, text))
			ev.At = tc.supportedAt
			mustApply(t, b, repo, ev)
			m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
			x := merged(t, m, together(m, Practice, text))
			if got := x.Learnings[0].Status; got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMaintainerCorrectionsWithoutAReliableOrderConflict(t *testing.T) {
	text := "Run the e2e script before pushing."
	a := retiredBy(t, day(9))
	b, _ := newStore(t)
	// The support's task started long before, but its evaluation recorded the
	// correction 30 seconds after the retirement.
	ev := eval("20", 0, maintainer(Practice, text))
	ev.At = day(9).Add(30 * time.Second)
	mustApply(t, b, repo, ev)

	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	_, err := m.Apply(together(m, Practice, text))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || len(conflict.Conflicts) != 1 || len(conflict.Conflicts[0].Sides) != 2 {
		t.Fatalf("err = %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "local:L1") || !strings.Contains(msg, "import:L1") || !strings.Contains(msg, text) {
		t.Fatalf("conflict does not report both sides: %s", msg)
	}
}

func TestAFlaggedConflictTakesTheLaterMaintainerText(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, maintainer(Convention, "Indent YAML with two spaces.")))
	mustApply(t, b, repo, eval("2", 6, maintainer(Convention, "Indent YAML with four spaces.")))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	groups := together(m, Convention, "Indent YAML consistently.")
	groups[0].Conflict = "the two corrections ask for different indentation"
	x := merged(t, m, groups)
	if l := x.Learnings[0]; l.Text != "Indent YAML with four spaces." || l.Status != Active {
		t.Fatalf("learning = %+v", l)
	}

	// Corrections recorded ten seconds apart cannot be ordered, even when their
	// tasks started days apart.
	c, _ := newStore(t)
	ev := eval("2", 6, maintainer(Convention, "Indent YAML with four spaces."))
	ev.At = day(1).Add(10 * time.Second)
	mustApply(t, c, repo, ev)
	m = prepare(t, snapshot(t, a, repo), snapshot(t, c, repo))
	groups = together(m, Convention, "Indent YAML consistently.")
	groups[0].Conflict = "the two corrections ask for different indentation"
	var conflict *ConflictError
	if _, err := m.Apply(groups); !errors.As(err, &conflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestMaintainerCorrectionsAreOrderedByWhenTheyWereRecorded(t *testing.T) {
	// The tasks started together, but the second correction came a day later.
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, maintainer(Convention, "Indent YAML with two spaces.")))
	ev := eval("2", 0, maintainer(Convention, "Indent YAML with four spaces."))
	ev.TaskAt, ev.At = day(0).Add(10*time.Second), day(2)
	mustApply(t, b, repo, ev)
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	groups := together(m, Convention, "Indent YAML consistently.")
	groups[0].Conflict = "the two corrections ask for different indentation"
	if l := merged(t, m, groups).Learnings[0]; l.Text != "Indent YAML with four spaces." {
		t.Fatalf("learning = %+v", l)
	}
}

func TestAnObservedRetirementDoesNotCountAsTheMaintainersCorrection(t *testing.T) {
	// A maintainer backed the learning; a newer task's observation then retired it.
	text := "Run the e2e script before pushing."
	a, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, maintainer(Practice, text)))
	ev := eval("9", 10)
	ev.Revisions = []Revision{{ID: "L1", Action: Retire, Reason: "The script was skipped.", Source: Observed}}
	mustApply(t, a, repo, ev)
	if l := learnings(t, a, repo)[0]; l.Status != Retired {
		t.Fatalf("setup: %+v", l)
	}
	b, _ := newStore(t)
	mustApply(t, b, repo, eval("20", 12, observed(Practice, text)))

	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	if l := merged(t, m, together(m, Practice, text)).Learnings[0]; l.Status != Active {
		t.Fatalf("the observed retirement beat the maintainer's support: %+v", l)
	}
}

func TestALaterMergeKeepsAMaintainerCorrectionThatWon(t *testing.T) {
	text := "Run the e2e script before pushing."
	a, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, maintainer(Practice, text)))
	ev := eval("9", 10)
	ev.Revisions = []Revision{{ID: "L1", Action: Retire, Reason: "The script was skipped.", Source: Observed}}
	mustApply(t, a, repo, ev)
	b, _ := newStore(t)
	mustApply(t, b, repo, eval("20", 12, observed(Practice, text)))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	if err := a.Replace(repo, merged(t, m, together(m, Practice, text))); err != nil {
		t.Fatal(err)
	}

	// New observed support from a third machine merges into the restored learning.
	c, _ := newStore(t)
	mustApply(t, c, repo, eval("30", 15, observed(Practice, text)))
	m = prepare(t, snapshot(t, a, repo), snapshot(t, c, repo))
	if l := merged(t, m, together(m, Practice, text)).Learnings[0]; l.Status != Active {
		t.Fatalf("a later merge undid the maintainer's correction: %+v", l)
	}
}

func TestAWithdrawnMaintainerConfirmationIsNotACorrection(t *testing.T) {
	a, _ := newStore(t)
	l := activeLearning(t, a)
	// Task 3's maintainer support of the live learning records a confirm, then its
	// re-evaluation withdraws it.
	mustApply(t, a, repo, eval("3", 4, maintainer(Practice, l.Text)))
	mustApply(t, a, repo, eval("3", 4))
	ev := eval("9", 10)
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "The script was removed.", Source: Observed}}
	mustApply(t, a, repo, ev)
	got := learnings(t, a, repo)[0]
	if got.Status != Retired || !slices.ContainsFunc(got.History, func(c Change) bool { return c.Action == "confirm" }) {
		t.Fatalf("setup: %+v", got)
	}
	b, _ := newStore(t)
	mustApply(t, b, repo, eval("20", 12, observed(Practice, l.Text)))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	if x := merged(t, m, together(m, Practice, l.Text)).Learnings[0]; x.Status != Retired {
		t.Fatalf("a withdrawn confirmation restored the learning: %+v", x)
	}
}

func TestMaintainerSupportBeatsObservedRetirementsOnBothSides(t *testing.T) {
	text := "Run the e2e script before pushing."
	retire := func(s Store, task string, startDay int) {
		ev := eval(task, startDay)
		ev.Revisions = []Revision{{ID: "L1", Action: Retire, Reason: "The script was skipped.", Source: Observed}}
		mustApply(t, s, repo, ev)
	}
	a, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, maintainer(Practice, text)))
	retire(a, "9", 10)
	b, _ := newStore(t)
	mustApply(t, b, repo, eval("20", 1, observed(Practice, text)))
	mustApply(t, b, repo, eval("21", 2, observed(Practice, text)))
	retire(b, "22", 11)
	for _, s := range []Store{a, b} {
		if l := learnings(t, s, repo)[0]; l.Status != Retired {
			t.Fatalf("setup: %+v", l)
		}
	}
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	if l := merged(t, m, together(m, Practice, text)).Learnings[0]; l.Status != Active {
		t.Fatalf("observed retirements beat the maintainer's support: %+v", l)
	}
}

func TestMaintainerRewordingsTheAgentCallsEquivalentDoNotConflict(t *testing.T) {
	a, _ := newStore(t)
	b, _ := newStore(t)
	mustApply(t, a, repo, eval("1", 0, maintainer(Convention, "Wrap errors with the operation name.")))
	mustApply(t, b, repo, eval("2", 0, maintainer(Convention, "Errors carry the operation name.")))
	m := prepare(t, snapshot(t, a, repo), snapshot(t, b, repo))
	l := merged(t, m, together(m, Convention, "Wrap returned errors with the operation name.")).Learnings[0]
	if l.Text != "Wrap returned errors with the operation name." || l.Status != Active {
		t.Fatalf("learning = %+v", l)
	}
}
