package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
)

func TestRunnerStartsOneLearnerAfterACleanReview(t *testing.T) {
	f := newFixture()
	f.app.Knowledge, f.app.Learning = knowledge.Store{Dir: t.TempDir()}, true
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.runners.learned, []task.Key{key}) {
		t.Fatalf("learners started = %v; want one for %v", f.runners.learned, key)
	}
}

func TestRunnerStartsNoLearnerWhenLearningIsOff(t *testing.T) {
	f := newFixture()
	f.app.Knowledge = knowledge.Store{Dir: t.TempDir()}
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	if err := f.run(context.Background(), key, 4242, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.runners.learned) != 0 {
		t.Fatalf("learners started = %v; want none", f.runners.learned)
	}
}

func TestRunnerStartsNoLearnerWhenTheReviewFails(t *testing.T) {
	f := newFixture()
	f.app.Knowledge, f.app.Learning = knowledge.Store{Dir: t.TempDir()}, true
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	f.codex.reviewErr = errors.New("reviewer crashed")
	_ = f.run(context.Background(), key, 4242, io.Discard)
	if len(f.runners.learned) != 0 {
		t.Fatalf("learners started = %v; want none", f.runners.learned)
	}
}

func TestALearnerThatFailsToStartLeavesTheRunAndTaskAlone(t *testing.T) {
	f := newFixture()
	f.app.Knowledge, f.app.Learning = knowledge.Store{Dir: t.TempDir()}, true
	f.runners.learnErr = errors.New("no executable")
	key := started(t, f, startReq(t))
	f.claude.turn = prTurn("sid-1")
	var log strings.Builder
	if err := f.run(context.Background(), key, 4242, &log); err != nil {
		t.Fatalf("RunTask = %v; a learner error must not fail the run", err)
	}
	if got := f.store.recs[key]; got.State != task.WaitingForCI {
		t.Fatalf("state = %s; want REVIEWING", got.State)
	}
	if !strings.Contains(log.String(), "learning not started: no executable") {
		t.Errorf("log does not say the learner failed to start:\n%s", log.String())
	}
}

func TestRefreshStartsALearnerOnlyWhenTheEvidenceChanged(t *testing.T) {
	f, _, _ := learnFixture(t)
	f.app.Learning = true
	recs := func() []*task.Record { return []*task.Record{f.store.recs[learnKey]} }

	f.app.startLearners(recs())
	if !slices.Equal(f.runners.learned, []task.Key{learnKey}) {
		t.Fatalf("first refresh started %v; want %v", f.runners.learned, learnKey)
	}
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.runners.learned = nil
	f.app.startLearners(recs())
	if len(f.runners.learned) != 0 {
		t.Fatalf("unchanged evidence started %v; want none", f.runners.learned)
	}
	rec := f.store.recs[learnKey]
	rec.Feedback = append(rec.Feedback, task.Feedback{Kind: task.IssueComment, ID: 9, Digest: task.Digest("rename the flag")})
	f.app.startLearners(recs())
	if !slices.Equal(f.runners.learned, []task.Key{learnKey}) {
		t.Fatalf("new PR comment started %v; want %v", f.runners.learned, learnKey)
	}
}

func TestRefreshStartsNoLearnerWhileTheRepositoryIsLocked(t *testing.T) {
	f, _, ks := learnFixture(t)
	f.app.Learning = true
	unlock, err := ks.Lock(string(learnKey.Repo))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	f.app.startLearners([]*task.Record{f.store.recs[learnKey]})
	if len(f.runners.learned) != 0 {
		t.Fatalf("locked repository started %v; want none", f.runners.learned)
	}
}

func TestRefreshStartsNoLearnerForARunningTask(t *testing.T) {
	for _, state := range []task.State{task.Implementing, task.WaitingForCI} {
		f, _, _ := learnFixture(t)
		f.app.Learning = true
		f.store.recs[learnKey].State = state
		f.app.startLearners([]*task.Record{f.store.recs[learnKey]})
		if len(f.runners.learned) != 0 {
			t.Errorf("%s task started %v; want none", state, f.runners.learned)
		}
	}
}

func TestRefreshStartsNoLearnerWhenLearningIsOff(t *testing.T) {
	f, _, _ := learnFixture(t)
	f.app.Learning = false
	f.app.startLearners([]*task.Record{f.store.recs[learnKey]})
	if len(f.runners.learned) != 0 {
		t.Fatalf("learning off started %v; want none", f.runners.learned)
	}
}

func TestRefreshStartsOneLearnerPerRepository(t *testing.T) {
	f, _, _ := learnFixture(t)
	f.app.Learning = true
	first := f.store.recs[learnKey]
	second := *first
	second.Key = task.Key{Repo: learnKey.Repo, Number: 42}
	other := *first
	other.Key = task.Key{Repo: "acme/web", Number: 7}
	f.app.startLearners([]*task.Record{first, &second, &other})
	if !slices.Equal(f.runners.learned, []task.Key{learnKey, other.Key}) {
		t.Fatalf("started %v; want one per repository", f.runners.learned)
	}
}

func TestRefreshStartsDueLearners(t *testing.T) {
	f, _, _ := learnFixture(t)
	f.app.Learning = true
	if _, err := f.app.Inbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.runners.learned, []task.Key{learnKey}) {
		t.Fatalf("refresh started %v; want %v", f.runners.learned, learnKey)
	}
}

func TestRefreshWaitsBeforeRetryingAFailedEvaluation(t *testing.T) {
	f, ev, _ := learnFixture(t)
	f.app.Learning = true
	ev.err = errors.New("turn failed")
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err == nil {
		t.Fatal("Learn succeeded; want the evaluator's error")
	}
	recs := []*task.Record{f.store.recs[learnKey]}
	f.app.startLearners(recs)
	if len(f.runners.learned) != 0 {
		t.Fatalf("retried at once: %v", f.runners.learned)
	}
	later := now.Add(learnRetryAfter)
	f.app.Now = func() time.Time { return later }
	f.app.startLearners(recs)
	if !slices.Equal(f.runners.learned, []task.Key{learnKey}) {
		t.Fatalf("after %s started %v; want a retry", learnRetryAfter, f.runners.learned)
	}
}

func TestRefreshRetriesAFailedEvaluationAtOnceWhenTheEvidenceChanged(t *testing.T) {
	f, ev, _ := learnFixture(t)
	f.app.Learning = true
	ev.err = errors.New("turn failed")
	_ = f.app.Learn(context.Background(), learnKey, io.Discard)
	rec := f.store.recs[learnKey]
	rec.Feedback = append(rec.Feedback, task.Feedback{Kind: task.IssueComment, ID: 9, Digest: task.Digest("rename the flag")})
	f.app.startLearners([]*task.Record{rec})
	if !slices.Equal(f.runners.learned, []task.Key{learnKey}) {
		t.Fatalf("new evidence started %v; want %v", f.runners.learned, learnKey)
	}
}

func TestRefreshDoesNotStartAFirstEvaluationOfAFinishedTask(t *testing.T) {
	for _, state := range []task.State{task.Done, task.PRClosed} {
		f, _, _ := learnFixture(t)
		f.app.Learning = true
		f.store.recs[learnKey].State = state
		f.app.startLearners([]*task.Record{f.store.recs[learnKey]})
		if len(f.runners.learned) != 0 {
			t.Errorf("never-evaluated %s task started %v; want none", state, f.runners.learned)
		}
	}
}

func TestRefreshReevaluatesAnEvaluatedTaskOnceItsPRMerges(t *testing.T) {
	f, _, _ := learnFixture(t)
	f.app.Learning = true
	if err := f.app.Learn(context.Background(), learnKey, io.Discard); err != nil {
		t.Fatal(err)
	}
	f.store.recs[learnKey].State = task.Done
	f.app.startLearners([]*task.Record{f.store.recs[learnKey]})
	if !slices.Equal(f.runners.learned, []task.Key{learnKey}) {
		t.Fatalf("merged task started %v; want %v", f.runners.learned, learnKey)
	}
}
