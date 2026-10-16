package knowledge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const repo = "acme/widgets"

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func day(n int) time.Time { return t0.Add(time.Duration(n) * 24 * time.Hour) }

func eval(task string, startedDay int, props ...Proposal) Evaluation {
	return Evaluation{Task: task, TaskAt: day(startedDay), At: day(startedDay + 1), Fingerprint: "fp-" + task, Assessment: "fine", Proposals: props, MaintainerRefs: []string{"D1", "C1"}}
}

func observed(kind Kind, text string, supports ...string) Proposal {
	return Proposal{Kind: kind, Text: text, Scope: Reusable, Source: Observed, Why: "seen in review", Supports: supports}
}

func newStore(t *testing.T) (Store, *bytes.Buffer) {
	var log bytes.Buffer
	return Store{Dir: t.TempDir(), Log: &log}, &log
}

func mustApply(t *testing.T, s Store, r string, ev Evaluation) {
	t.Helper()
	if err := s.Apply(r, ev); err != nil {
		t.Fatal(err)
	}
}

func learnings(t *testing.T, s Store, r string) []Learning {
	t.Helper()
	ls, err := s.Learnings(r)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func knowledgeMD(t *testing.T, s Store, r string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, r, "knowledge.md"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOneOffProposalStaysInTheEvaluationOnly(t *testing.T) {
	s, _ := newStore(t)
	mustApply(t, s, repo, eval("1", 0, Proposal{Kind: Fact, Text: "The flaky widget test needed a retry.", Scope: OneOff, Source: Observed}))
	if ls := learnings(t, s, repo); len(ls) != 0 {
		t.Fatalf("one-off became a learning: %+v", ls)
	}
	ev, err := s.Evaluation(repo, "1")
	if err != nil || ev == nil || len(ev.Proposals) != 1 || ev.Proposals[0].Scope != OneOff {
		t.Fatalf("evaluation = %+v, %v", ev, err)
	}
}

func TestObservedLearningIsCandidateUntilASecondTaskShowsIt(t *testing.T) {
	s, _ := newStore(t)
	text := "Wrap returned errors with the operation name."
	mustApply(t, s, repo, eval("1", 0, observed(Convention, text)))
	ls := learnings(t, s, repo)
	if len(ls) != 1 || ls[0].Status != Candidate {
		t.Fatalf("after one task: %+v", ls)
	}
	if strings.Contains(knowledgeMD(t, s, repo), text) {
		t.Fatal("a candidate reached knowledge.md")
	}

	mustApply(t, s, repo, eval("2", 3, observed(Convention, "Something reworded.", ls[0].ID)))
	ls = learnings(t, s, repo)
	if len(ls) != 1 || ls[0].Status != Active || ls[0].Tasks() != 2 {
		t.Fatalf("after two tasks: %+v", ls)
	}
	if !strings.Contains(knowledgeMD(t, s, repo), text) {
		t.Fatalf("active learning missing from knowledge.md:\n%s", knowledgeMD(t, s, repo))
	}
}

func TestSameTextFromASecondTaskPromotesWithoutAnID(t *testing.T) {
	s, _ := newStore(t)
	mustApply(t, s, repo, eval("1", 0, observed(Convention, "Use table-driven tests.")))
	mustApply(t, s, repo, eval("2", 2, observed(Convention, "use table-driven  tests")))
	ls := learnings(t, s, repo)
	if len(ls) != 1 || ls[0].Status != Active {
		t.Fatalf("got %+v", ls)
	}
}

func TestReevaluatingOneTaskNeverCountsAsTwoTasks(t *testing.T) {
	s, _ := newStore(t)
	mustApply(t, s, repo, eval("1", 0, observed(Convention, "Use table-driven tests.")))
	mustApply(t, s, repo, eval("1", 0, observed(Convention, "Use table-driven tests.")))
	ls := learnings(t, s, repo)
	if len(ls) != 1 || ls[0].Status != Candidate || len(ls[0].Evidence) != 1 {
		t.Fatalf("got %+v", ls)
	}
}

func TestMaintainerSourcedLearningIsActiveAtOnce(t *testing.T) {
	s, _ := newStore(t)
	p := observed(Convention, "Commit subjects use conventional prefixes.")
	p.Source, p.Cites = Maintainer, []string{"D1"}
	mustApply(t, s, repo, eval("1", 0, p))
	if ls := learnings(t, s, repo); len(ls) != 1 || ls[0].Status != Active {
		t.Fatalf("got %+v", ls)
	}
}

func activeLearning(t *testing.T, s Store) Learning {
	t.Helper()
	mustApply(t, s, repo, eval("1", 0, observed(Practice, "Run the e2e script before pushing.")))
	mustApply(t, s, repo, eval("2", 2, observed(Practice, "Run the e2e script before pushing.")))
	ls := learnings(t, s, repo)
	if len(ls) != 1 || ls[0].Status != Active {
		t.Fatalf("setup: %+v", ls)
	}
	return ls[0]
}

func TestMaintainerDecisionRetiresALearningAndKeepsWhy(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	ev := eval("0", -5)
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "Maintainer said the e2e script is optional now.", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, ev)
	got := learnings(t, s, repo)[0]
	if got.Status != Retired {
		t.Fatalf("status = %s", got.Status)
	}
	last := got.History[len(got.History)-1]
	if last.Action != Retire || last.Task != "0" || !strings.Contains(last.Reason, "optional") {
		t.Fatalf("history = %+v", got.History)
	}
	if strings.Contains(knowledgeMD(t, s, repo), "e2e") {
		t.Fatal("retired learning still in knowledge.md")
	}
}

func TestMaintainerDecisionRevisesTextAndKeepsThePreviousText(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	ev := eval("3", 4)
	ev.Revisions = []Revision{{ID: l.ID, Action: Revise, Text: "Run the e2e script before opening a pull request.", Reason: "Maintainer narrowed it.", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, ev)
	got := learnings(t, s, repo)[0]
	if got.Status != Active || got.Text != "Run the e2e script before opening a pull request." {
		t.Fatalf("got %+v", got)
	}
	last := got.History[len(got.History)-1]
	if last.Action != Revise || last.From != l.Text {
		t.Fatalf("history = %+v", got.History)
	}
	md := knowledgeMD(t, s, repo)
	if !strings.Contains(md, "before opening a pull request") || strings.Contains(md, "before pushing") {
		t.Fatalf("knowledge.md:\n%s", md)
	}
}

func TestObservedContradictionFromAnOlderTaskCannotRetire(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	ev := eval("0", 1)
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "Skipped the script.", Source: Observed}}
	mustApply(t, s, repo, ev)
	if got := learnings(t, s, repo)[0]; got.Status != Active {
		t.Fatalf("older observation retired it: %+v", got)
	}
}

func TestObservedContradictionFromANewerTaskRetires(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	ev := eval("9", 10)
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "The script was removed.", Source: Observed}}
	mustApply(t, s, repo, ev)
	if got := learnings(t, s, repo)[0]; got.Status != Retired {
		t.Fatalf("newer observation did not retire it: %+v", got)
	}
}

func TestReevaluationReplacesTheTasksEvidence(t *testing.T) {
	s, _ := newStore(t)
	activeLearning(t, s)
	mustApply(t, s, repo, eval("3", 4, observed(Fact, "The API package has no tests.")))

	// Task 2 re-evaluated after human feedback, no longer supporting the practice.
	mustApply(t, s, repo, eval("2", 2))
	ls := learnings(t, s, repo)
	if ls[0].Status != Candidate || ls[0].Tasks() != 1 {
		t.Fatalf("practice after task 2 withdrew: %+v", ls[0])
	}

	// Task 3 re-evaluated without its only learning.
	mustApply(t, s, repo, eval("3", 4))
	ls = learnings(t, s, repo)
	if ls[1].Status != Retired || len(ls[1].Evidence) != 0 {
		t.Fatalf("fact after its only evidence was replaced: %+v", ls[1])
	}
	if ev, _ := s.Evaluation(repo, "3"); ev == nil || len(ev.Proposals) != 0 {
		t.Fatalf("evaluation file not replaced: %+v", ev)
	}
}

func TestObservedSupportCannotReviveAMaintainerRetirement(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	ev := eval("3", 4)
	ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "No longer wanted.", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, ev)
	mustApply(t, s, repo, eval("4", 5, observed(Practice, "Run the e2e script before pushing.", l.ID)))
	mustApply(t, s, repo, eval("5", 6, observed(Practice, "Run the e2e script before pushing.")))
	for _, got := range learnings(t, s, repo) {
		if got.Status != Retired {
			t.Fatalf("observed support revived a retired learning: %+v", got)
		}
	}
}

func TestUnsafeProposalIsDroppedAndLoggedWithoutItsText(t *testing.T) {
	s, log := newStore(t)
	secret := "Push with ghp_abcdefghijklmnopqrstuvwxyz0123456789 always."
	mustApply(t, s, repo, eval("1", 0, observed(Practice, secret), observed(Convention, "Follow the layout in #12.")))
	if ls := learnings(t, s, repo); len(ls) != 0 {
		t.Fatalf("unsafe proposals stored: %+v", ls)
	}
	if strings.Contains(log.String(), "ghp_") || strings.Contains(log.String(), "#12") || !strings.Contains(log.String(), "proposal 1") {
		t.Fatalf("log = %q", log.String())
	}
	raw, _ := os.ReadFile(filepath.Join(s.Dir, repo, "evaluations", "1.yaml"))
	if strings.Contains(string(raw), "ghp_") || strings.Contains(string(raw), "#12") {
		t.Fatalf("evaluation file kept rejected text:\n%s", raw)
	}
}

func TestUnknownKindIsDropped(t *testing.T) {
	s, _ := newStore(t)
	mustApply(t, s, repo, eval("1", 0, observed("vibe", "Be nice.")))
	if ls := learnings(t, s, repo); len(ls) != 0 {
		t.Fatalf("got %+v", ls)
	}
}

func TestKnowledgeFileStatesFallibilityAndCarriesNoIDsOrNumbers(t *testing.T) {
	s, _ := newStore(t)
	for i, text := range []string{"Use table-driven tests.", "Errors wrap the operation name."} {
		p := observed(Convention, text)
		p.Source, p.Cites = Maintainer, []string{"D1"}
		mustApply(t, s, repo, eval(string(rune('1'+i)), i, p))
	}
	mustApply(t, s, repo, eval("7", 7, observed(Fact, "Candidates stay out.")))
	md := knowledgeMD(t, s, repo)
	if !strings.Contains(md, "may be wrong") || !strings.Contains(md, "take precedence") {
		t.Errorf("no fallibility note:\n%s", md)
	}
	if regexp.MustCompile(`\bL\d+\b|#\d|\d`).MatchString(md) {
		t.Errorf("knowledge.md carries ids or numbers:\n%s", md)
	}
	if strings.Contains(md, "Candidates stay out") {
		t.Errorf("candidate rendered:\n%s", md)
	}
}

func TestSelectReturnsActiveLearningsRankedByRelevanceWithinCaps(t *testing.T) {
	s, _ := newStore(t)
	texts := []string{
		"Database migrations live in a separate package.",
		"Use table-driven tests.",
		"Dashboard rendering keeps frames under one screen.",
		"Errors wrap the operation name.",
	}
	for i, text := range texts {
		p := observed(Convention, text)
		p.Source, p.Cites = Maintainer, []string{"D1"}
		mustApply(t, s, repo, eval(string(rune('a'+i)), i, p))
	}
	mustApply(t, s, repo, eval("z", 9, observed(Fact, "Dashboard candidates never appear.")))

	got, err := s.Select(repo, "Fix dashboard rendering flicker", 2, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != texts[2] {
		t.Fatalf("got %+v", got)
	}
	for _, l := range got {
		if l.Status != Active {
			t.Fatalf("selected a %s learning", l.Status)
		}
	}

	got, _ = s.Select(repo, "anything", 8, 60)
	total := 0
	for _, l := range got {
		total += len(l.Text)
	}
	if len(got) == 0 || total > 60 {
		t.Fatalf("char cap broken: %d chars in %d learnings", total, len(got))
	}
}

func TestSelectWithNoStoreIsEmpty(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.Select(repo, "x", 8, 2000)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRepositoriesKeepSeparateStores(t *testing.T) {
	s, _ := newStore(t)
	p := observed(Convention, "Shared words about widgets.")
	p.Source, p.Cites = Maintainer, []string{"D1"}
	mustApply(t, s, repo, eval("1", 0, p))
	if got, _ := s.Select("acme/gadgets", "widgets", 8, 2000); len(got) != 0 {
		t.Fatalf("other repository saw %+v", got)
	}
	if ls := learnings(t, s, "acme/gadgets"); len(ls) != 0 {
		t.Fatalf("other repository loaded %+v", ls)
	}
	if md := knowledgeMD(t, s, "acme/gadgets"); md != "" {
		t.Fatalf("other repository has knowledge.md:\n%s", md)
	}
	if evs, _ := s.Evaluations("acme/gadgets", 12); len(evs) != 0 {
		t.Fatalf("other repository has evaluations: %+v", evs)
	}
}

func TestHostileRepositoryAndTaskNamesAreRejected(t *testing.T) {
	s, _ := newStore(t)
	for _, r := range []string{"../etc", "acme/..", "..", "acme", "acme/widgets/x", "/abs/path", ""} {
		if err := s.Apply(r, eval("1", 0)); err == nil {
			t.Errorf("Apply accepted repo %q", r)
		}
		if _, err := s.Select(r, "x", 8, 100); err == nil {
			t.Errorf("Select accepted repo %q", r)
		}
	}
	if err := s.Apply(repo, eval("../1", 0)); err == nil {
		t.Error("Apply accepted a task id with a path")
	}
	if entries, _ := os.ReadDir(filepath.Dir(s.Dir)); len(entries) > 1 {
		for _, e := range entries {
			if e.Name() == "etc" {
				t.Fatal("wrote outside the store")
			}
		}
	}
}

func TestEvaluationsReturnsNewestFirstWithinLimit(t *testing.T) {
	s, _ := newStore(t)
	for i, id := range []string{"1", "2", "3"} {
		mustApply(t, s, repo, eval(id, i))
	}
	evs, err := s.Evaluations(repo, 2)
	if err != nil || len(evs) != 2 || evs[0].Task != "3" || evs[1].Task != "2" {
		t.Fatalf("got %+v, %v", evs, err)
	}
}

func TestLockAllowsOneEvaluationPerRepository(t *testing.T) {
	s, _ := newStore(t)
	unlock, err := s.Lock(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Busy(repo) {
		t.Error("Busy is false while locked")
	}
	if _, err := s.Lock(repo); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	other, err := s.Lock("acme/gadgets")
	if err != nil {
		t.Fatalf("another repository was blocked: %v", err)
	}
	other()
	unlock()
	if s.Busy(repo) {
		t.Error("Busy is true after unlock")
	}
	again, err := s.Lock(repo)
	if err != nil {
		t.Fatalf("relock: %v", err)
	}
	again()
}

func TestRecordAttemptRoundTripsAndStaysOutOfEvaluations(t *testing.T) {
	s, _ := newStore(t)
	if got, err := s.Attempt(repo, "7"); err != nil || got != nil {
		t.Fatalf("no attempt yet: %+v %v", got, err)
	}
	if err := s.RecordAttempt(repo, "7", Attempt{Fingerprint: "fp-7", At: t0}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Attempt(repo, "7")
	if err != nil || got == nil || got.Fingerprint != "fp-7" || !got.At.Equal(t0) {
		t.Fatalf("attempt = %+v %v", got, err)
	}
	if evs, err := s.Evaluations(repo, 0); err != nil || len(evs) != 0 {
		t.Fatalf("an attempt must not read as an evaluation: %+v %v", evs, err)
	}
}

func TestRecordAttemptRejectsAnInvalidTaskID(t *testing.T) {
	s, _ := newStore(t)
	if err := s.RecordAttempt(repo, "../7", Attempt{Fingerprint: "fp", At: t0}); err == nil {
		t.Fatal("RecordAttempt accepted a task id that leaves the repository directory")
	}
}

func TestApplyThatFailsToWriteTheLearningsStoresNoEvaluation(t *testing.T) {
	s, _ := newStore(t)
	// A directory where knowledge.md goes makes its write fail after the YAML.
	if err := os.MkdirAll(filepath.Join(s.Dir, repo, "knowledge.md", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(repo, eval("1", 0, observed(Practice, "Run go vet first."))); err == nil {
		t.Fatal("want an error")
	}
	ev, err := s.Evaluation(repo, "1")
	if err != nil || ev != nil {
		t.Errorf("evaluation = %+v, %v; a failed apply must leave the task due for a retry", ev, err)
	}
}

func TestMaintainerSourceWithoutAValidCitationCountsAsObserved(t *testing.T) {
	for name, cites := range map[string][]string{
		"no citation":               nil,
		"a decision the task lacks": {"D7"},
		"one valid, one invented":   {"C1", "C9"},
	} {
		t.Run(name, func(t *testing.T) {
			s, log := newStore(t)
			p := observed(Convention, "Wrap errors with context.")
			p.Source, p.Cites = Maintainer, cites
			mustApply(t, s, repo, eval("1", 0, p))
			if ls := learnings(t, s, repo); len(ls) != 1 || ls[0].Status != Candidate || ls[0].Evidence[0].Source != Observed {
				t.Fatalf("learnings = %+v, want one observed candidate", ls)
			}

			mustApply(t, s, repo, eval("2", 2, observed(Convention, "Wrap errors with context.")))
			retire := eval("3", 1)
			retire.Revisions = []Revision{{ID: "L1", Action: Retire, Reason: "claimed", Source: Maintainer, Cites: cites}}
			mustApply(t, s, repo, retire)
			if ls := learnings(t, s, repo); ls[0].Status != Active {
				t.Errorf("an older task's uncited correction retired the learning: %+v", ls[0])
			}
			if !strings.Contains(log.String(), "cites no maintainer evidence") {
				t.Errorf("log does not note the downgrade:\n%s", log.String())
			}
		})
	}
}

func TestMaintainerEvidenceKeepsItsCitations(t *testing.T) {
	s, _ := newStore(t)
	p := observed(Convention, "Wrap errors with context.")
	p.Source, p.Cites = Maintainer, []string{"C1"}
	mustApply(t, s, repo, eval("1", 0, p))
	ls := learnings(t, s, repo)
	if len(ls) != 1 || ls[0].Status != Active || !slices.Equal(ls[0].Evidence[0].Cites, []string{"C1"}) {
		t.Fatalf("learnings = %+v, want an active learning citing C1", ls)
	}
}

func TestATaskCannotRetireItsOwnLearningByObservation(t *testing.T) {
	s, _ := newStore(t)
	p := observed(Practice, "Pin tool versions in the Makefile.")
	p.Source, p.Cites = Maintainer, []string{"D1"}
	mustApply(t, s, repo, eval("5", 0, p))
	// Task 5 re-evaluated: it still backs the learning and also observes against it.
	again := eval("5", 0, p)
	again.Revisions = []Revision{{ID: "L1", Action: Retire, Reason: "skipped it once", Source: Observed}}
	mustApply(t, s, repo, again)
	if got := learnings(t, s, repo)[0]; got.Status != Active {
		t.Fatalf("the task's own observation retired its learning: %+v", got)
	}
}

func TestReevaluationWithdrawsTheTasksEarlierRetirement(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	retire := eval("9", 10)
	retire.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "the script was removed", Source: Observed}}
	mustApply(t, s, repo, retire)
	if got := learnings(t, s, repo)[0]; got.Status != Retired {
		t.Fatalf("setup: %+v", got)
	}
	mustApply(t, s, repo, eval("9", 10))
	got := learnings(t, s, repo)[0]
	if got.Status != Active || !strings.Contains(knowledgeMD(t, s, repo), "e2e script") {
		t.Fatalf("the replaced retirement still holds: %+v", got)
	}
	if last := got.History[len(got.History)-1]; last.Action != "revert-retire" || last.Task != "9" {
		t.Errorf("history = %+v", got.History)
	}
}

func TestReevaluationWithdrawsTheTasksEarlierRewrite(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	revise := eval("9", 10)
	revise.Revisions = []Revision{{ID: l.ID, Action: Revise, Text: "Run the e2e script nightly.", Reason: "moved to nightly", Source: Observed}}
	mustApply(t, s, repo, revise)
	if got := learnings(t, s, repo)[0]; got.Text != "Run the e2e script nightly." {
		t.Fatalf("setup: %+v", got)
	}
	mustApply(t, s, repo, eval("9", 10))
	if got := learnings(t, s, repo)[0]; got.Text != l.Text || got.Status != Active {
		t.Fatalf("the replaced rewrite still holds: %+v", got)
	}
}

func TestReevaluationKeepsALaterTasksRetirement(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	for _, id := range []string{"8", "9"} {
		ev := eval(id, 10)
		ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "removed", Source: Maintainer, Cites: []string{"C1"}}}
		mustApply(t, s, repo, ev)
	}
	mustApply(t, s, repo, eval("8", 10))
	if got := learnings(t, s, repo)[0]; got.Status != Retired {
		t.Fatalf("withdrawing task 8 undid task 9's retirement: %+v", got)
	}
}

func TestReevaluatingTwoRewritingTasksRestoresTheOriginalText(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	rewrite := func(task string, day int, text string) Evaluation {
		ev := eval(task, day)
		ev.Revisions = []Revision{{ID: l.ID, Action: Revise, Text: text, Reason: "changed", Source: Maintainer, Cites: []string{"C1"}}}
		return ev
	}
	mustApply(t, s, repo, rewrite("8", 10, "Run the e2e script nightly."))
	mustApply(t, s, repo, rewrite("9", 11, "Run the e2e script weekly."))

	mustApply(t, s, repo, eval("8", 10))
	if got := learnings(t, s, repo)[0].Text; got != "Run the e2e script weekly." {
		t.Fatalf("after withdrawing task 8's rewrite, text = %q", got)
	}
	mustApply(t, s, repo, eval("9", 11))
	if got := learnings(t, s, repo)[0].Text; got != l.Text {
		t.Fatalf("after withdrawing both rewrites, text = %q, want %q", got, l.Text)
	}
}

func TestWithdrawingTheLaterRewriteRestoresTheEarlierOne(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	for _, r := range []struct{ task, text string }{{"8", "Run the e2e script nightly."}, {"9", "Run the e2e script weekly."}} {
		ev := eval(r.task, 10)
		ev.Revisions = []Revision{{ID: l.ID, Action: Revise, Text: r.text, Reason: "changed", Source: Maintainer, Cites: []string{"C1"}}}
		mustApply(t, s, repo, ev)
	}
	mustApply(t, s, repo, eval("9", 10))
	if got := learnings(t, s, repo)[0].Text; got != "Run the e2e script nightly." {
		t.Fatalf("text = %q, want task 8's rewrite", got)
	}
}

func TestReevaluatingTwoRetiringTasksRestoresTheLearning(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	for _, id := range []string{"8", "9"} {
		ev := eval(id, 10)
		ev.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "removed", Source: Maintainer, Cites: []string{"C1"}}}
		mustApply(t, s, repo, ev)
	}
	mustApply(t, s, repo, eval("8", 10))
	if got := learnings(t, s, repo)[0]; got.Status != Retired {
		t.Fatalf("task 9's retirement no longer holds: %+v", got)
	}
	mustApply(t, s, repo, eval("9", 10))
	if got := learnings(t, s, repo)[0]; got.Status != Active || !strings.Contains(knowledgeMD(t, s, repo), "e2e script") {
		t.Fatalf("both retirements withdrawn, but the learning is %+v", got)
	}
}

func TestWithdrawingARestorationReinstatesTheRetirement(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	retire := eval("8", 10)
	retire.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "removed", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, retire)
	p := observed(Practice, l.Text)
	p.Source, p.Cites = Maintainer, []string{"D1"}
	mustApply(t, s, repo, eval("9", 11, p))
	if got := learnings(t, s, repo)[0]; got.Status != Active {
		t.Fatalf("setup: the maintainer proposal did not restore it: %+v", got)
	}
	mustApply(t, s, repo, eval("9", 11))
	got := learnings(t, s, repo)[0]
	if got.Status != Retired || strings.Contains(knowledgeMD(t, s, repo), "e2e script") {
		t.Fatalf("task 8's retirement is in force again, but the learning is %+v", got)
	}
	if last := got.History[len(got.History)-1]; last.Action != "revert-restore" || last.Task != "9" {
		t.Errorf("history = %+v", got.History)
	}
}

func TestWithdrawingARestorationKeepsALaterMaintainerSupport(t *testing.T) {
	s, _ := newStore(t)
	l := activeLearning(t, s)
	retire := eval("8", 10)
	retire.Revisions = []Revision{{ID: l.ID, Action: Retire, Reason: "removed", Source: Maintainer, Cites: []string{"C1"}}}
	mustApply(t, s, repo, retire)
	support := func(task string, day int) Evaluation {
		p := observed(Practice, l.Text)
		p.Source, p.Cites = Maintainer, []string{"D1"}
		return eval(task, day, p)
	}
	mustApply(t, s, repo, support("9", 11))
	mustApply(t, s, repo, support("10", 12))
	mustApply(t, s, repo, eval("9", 11))
	if got := learnings(t, s, repo)[0]; got.Status != Active || !strings.Contains(knowledgeMD(t, s, repo), "e2e script") {
		t.Fatalf("task 10's maintainer support no longer restores it: %+v", got)
	}
	mustApply(t, s, repo, eval("10", 12))
	if got := learnings(t, s, repo)[0]; got.Status != Retired {
		t.Fatalf("with both supports withdrawn, task 8's retirement holds, but the learning is %+v", got)
	}
}
