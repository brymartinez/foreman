package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

func historyFixture(t *testing.T) (App, *fakeStore, *fakeGitHub) {
	store := &fakeStore{recs: map[task.Key]*task.Record{}}
	gh := &fakeGitHub{prs: map[int]PullRequest{}}
	return App{Store: store, GitHub: gh, LogDir: t.TempDir(), Now: func() time.Time { return now }}, store, gh
}

func closedPR(repo task.Repo, n int, state string, at time.Time) PullRequest {
	pr := PullRequest{Key: task.Key{Repo: repo, Number: n}, State: state, Title: "PR " + string(rune('A'+n%26)), ClosedAt: at}
	if state == "merged" {
		pr.MergedAt = at
	}
	return pr
}

func mins(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func measured(pr int, start, handoff int) task.Attempt {
	return task.Attempt{StartedAt: mins(start), PR: pr, HandoffAt: mins(handoff), Marks: []task.Mark{
		{At: mins(start), Phase: task.PhasePlanning}, {At: mins(start + 10), Phase: task.PhaseReview}, {At: mins(handoff), Phase: task.PhaseEnded},
	}}
}

func addTask(store *fakeStore, repo task.Repo, n int, attempts ...task.Attempt) *task.Record {
	rec := &task.Record{Key: task.Key{Repo: repo, Number: n}, Kind: task.Issue, Title: "issue", State: task.Done,
		Delegation: &task.Delegation{Implementer: task.Session{Agent: task.Claude}, Branch: "feat/x"}, Attempts: attempts}
	store.recs[rec.Key] = rec
	return rec
}

func TestHistoryListsFinishedPRsNewestFirstWithPhaseTotals(t *testing.T) {
	a, store, gh := historyFixture(t)
	addTask(store, "a/b", 1, measured(11, 0, 60))
	addTask(store, "a/b", 2, measured(12, 0, 30))
	addTask(store, "a/b", 3, measured(13, 0, 30))
	gh.prs[11] = closedPR("a/b", 11, "merged", mins(200))
	gh.prs[12] = closedPR("a/b", 12, "closed", mins(300))
	gh.prs[13] = closedPR("a/b", 13, "open", time.Time{})
	rows, warnings, err := a.History(context.Background(), false)
	if err != nil || len(warnings) != 0 {
		t.Fatal(err, warnings)
	}
	if len(rows) != 2 || rows[0].PR != 12 || rows[1].PR != 11 {
		t.Fatalf("rows = %+v, want PR 12 then PR 11 and no open PR", rows)
	}
	r := rows[1]
	if r.Status != "merged" || r.Timing != TimingMeasured || r.Total != time.Hour || r.Title != gh.prs[11].Title {
		t.Fatalf("row = %+v", r)
	}
	if r.Phases[task.PhasePlanning] != 10*time.Minute || r.Phases[task.PhaseReview] != 50*time.Minute {
		t.Fatalf("phases = %v", r.Phases)
	}
}

func TestHistoryShowsNoHandoffForAPRClosedBeforeHandoff(t *testing.T) {
	a, store, gh := historyFixture(t)
	att := measured(11, 0, 0)
	att.HandoffAt, att.EndedAt = time.Time{}, mins(40)
	att.Marks = att.Marks[:2]
	addTask(store, "a/b", 1, att)
	gh.prs[11] = closedPR("a/b", 11, "closed", mins(45))
	rows, _, err := a.History(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if r := rows[0]; r.Timing != TimingNoHandoff || r.Phases[task.PhasePlanning] != 10*time.Minute || r.Phases[task.PhaseReview] != 30*time.Minute {
		t.Fatalf("row = %+v, want no handoff and the phases known up to the close", r)
	}
}

func TestHistoryFollowsTheRepositoryFilterUnlessAllRepositoriesIsOn(t *testing.T) {
	a, store, gh := historyFixture(t)
	a.IncludedRepositories = []task.Repo{"a/b"}
	addTask(store, "a/b", 1, measured(11, 0, 60))
	addTask(store, "c/d", 1, measured(21, 0, 60))
	gh.prs[11] = closedPR("a/b", 11, "merged", mins(100))
	gh.prs[21] = closedPR("c/d", 21, "merged", mins(100))
	rows, _, _ := a.History(context.Background(), false)
	if len(rows) != 1 || rows[0].Key.Repo != "a/b" {
		t.Fatalf("filtered rows = %+v", rows)
	}
	rows, _, _ = a.History(context.Background(), true)
	if len(rows) != 2 {
		t.Fatalf("all-repositories rows = %+v", rows)
	}
}

func TestHistoryKeepsAPRWhoseStatusCannotBeReadAndSortsItLast(t *testing.T) {
	a, store, gh := historyFixture(t)
	addTask(store, "a/b", 1, measured(11, 0, 60))
	addTask(store, "a/b", 2, measured(99, 0, 60))
	gh.prs[11] = closedPR("a/b", 11, "merged", mins(100))
	rows, warnings, err := a.History(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].PR != 99 || rows[1].Status != StatusUnknown || len(warnings) != 1 {
		t.Fatalf("rows = %+v warnings = %v", rows, warnings)
	}
}

func TestHistoryGivesEachRestartedAttemptItsOwnRowAndLogRange(t *testing.T) {
	a, store, gh := historyFixture(t)
	first, second := measured(11, 0, 60), measured(12, 100, 130)
	second.LogStart = 500
	rec := addTask(store, "a/b", 1, first, second)
	rec.Delegation.RunnerLog = filepath.Join(a.LogDir, "a-b-1.log")
	gh.prs[11] = closedPR("a/b", 11, "closed", mins(90))
	gh.prs[12] = closedPR("a/b", 12, "merged", mins(200))
	rows, _, _ := a.History(context.Background(), false)
	if len(rows) != 2 || rows[0].Total != 30*time.Minute || rows[1].Total != time.Hour {
		t.Fatalf("rows = %+v", rows)
	}
	if l := rows[0].Log; l.Start != 500 || l.End != 0 || l.Whole {
		t.Fatalf("second attempt log = %+v, want from byte 500 to the end", l)
	}
	if l := rows[1].Log; l.Start != 0 || l.End != 500 || l.Whole {
		t.Fatalf("first attempt log = %+v, want bytes 0 to 500", l)
	}
}

func writeLog(t *testing.T, path string, lines ...string) []int64 {
	t.Helper()
	var offsets []int64
	var b strings.Builder
	for _, l := range lines {
		offsets = append(offsets, int64(b.Len()))
		b.WriteString(l + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return offsets
}

func TestHistoryFindsOlderPRsInIssueLogsIncludingReplacedAttempts(t *testing.T) {
	a, store, gh := historyFixture(t)
	rec := addTask(store, "a/b", 1)
	rec.Delegation.PR = 12
	rec.Delegation.RunnerLog = filepath.Join(a.LogDir, "a-b-1.log")
	off := writeLog(t, rec.Delegation.RunnerLog,
		"2026-09-01T09:00:00Z foreman: runner 1 started",
		"2026-09-01T09:00:01Z foreman: starting claude in /wt",
		`2026-09-01T09:10:00Z {"text":"foreman: verified PR #77 https://github.com/a/b/pull/77 at x"}`,
		"2026-09-01T09:20:00Z foreman: verified PR #11 https://github.com/a/b/pull/11 at abc1234",
		"2026-09-02T09:00:00Z foreman: runner 2 started",
		"2026-09-02T09:20:00Z foreman: verified PR #12 https://github.com/a/b/pull/12 at def5678",
	)
	gh.prs[11] = closedPR("a/b", 11, "closed", mins(100))
	gh.prs[12] = closedPR("a/b", 12, "merged", mins(200))
	rows, _, err := a.History(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].PR != 12 || rows[1].PR != 11 {
		t.Fatalf("rows = %+v, want the current PR 12 and the replaced PR 11, and nothing from agent output", rows)
	}
	for _, r := range rows {
		if r.Timing != TimingUnavailable || r.Phases != nil || r.Key != rec.Key {
			t.Errorf("row %d = %+v, want unavailable timing on issue 1", r.PR, r)
		}
	}
	if l := rows[1].Log; l.Start != 0 || l.End != off[4] || l.Whole {
		t.Errorf("PR 11 log = %+v, want up to runner 2", l)
	}
	if l := rows[0].Log; l.Start != off[4] || l.End != 0 || l.Whole {
		t.Errorf("PR 12 log = %+v, want from runner 2 on", l)
	}
}

func TestHistoryLabelsTheWholeLogWhenAttemptsCannotBeSeparated(t *testing.T) {
	a, _, gh := historyFixture(t)
	writeLog(t, filepath.Join(a.LogDir, "a-b-3.log"),
		"2026-09-01T09:00:00Z foreman: runner 1 started",
		"2026-09-01T09:20:00Z foreman: verified PR #11 https://github.com/a/b/pull/11 at abc1234",
		"2026-09-01T09:40:00Z foreman: verified PR #12 https://github.com/a/b/pull/12 at def5678",
	)
	gh.prs[11] = closedPR("a/b", 11, "closed", mins(100))
	gh.prs[12] = closedPR("a/b", 12, "merged", mins(200))
	rows, _, err := a.History(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both PRs from a log with no task file", rows)
	}
	for _, r := range rows {
		if !r.Log.Whole || r.Key != (task.Key{Repo: "a/b", Number: 3}) {
			t.Errorf("row %d = %+v, want the whole log of issue 3", r.PR, r)
		}
	}
}

func TestHistorySynthesizesAnAttemptForATaskFilePRMissingFromTheLog(t *testing.T) {
	a, store, gh := historyFixture(t)
	rec := addTask(store, "a/b", 1)
	rec.Delegation.PR = 11
	gh.prs[11] = closedPR("a/b", 11, "merged", mins(100))
	rows, _, _ := a.History(context.Background(), false)
	if len(rows) != 1 || rows[0].PR != 11 || rows[0].Timing != TimingUnavailable {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestHistoryStartsAnOlderPRsLogAtTheRunnerThatOpenedIt(t *testing.T) {
	a, store, gh := historyFixture(t)
	rec := addTask(store, "a/b", 1)
	rec.Delegation.RunnerLog = filepath.Join(a.LogDir, "a-b-1.log")
	off := writeLog(t, rec.Delegation.RunnerLog,
		"2026-09-01T09:00:00Z foreman: runner 1 started",
		"2026-09-01T09:20:00Z foreman: verified PR #11 https://github.com/a/b/pull/11 at abc1234",
		"2026-09-02T09:00:00Z foreman: runner 2 started",
		"2026-09-02T09:01:00Z foreman: run failed",
		"2026-09-03T09:00:00Z foreman: runner 3 started",
		"2026-09-03T09:00:01Z foreman: starting claude in /wt",
		"2026-09-03T09:20:00Z foreman: verified PR #12 https://github.com/a/b/pull/12 at def5678",
	)
	gh.prs[11] = closedPR("a/b", 11, "closed", mins(100))
	gh.prs[12] = closedPR("a/b", 12, "merged", mins(200))
	rows, _, err := a.History(context.Background(), false)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if l := rows[0].Log; l.Start != off[4] || l.Whole {
		t.Errorf("PR 12 log = %+v, want it to start at runner 3, after the failed runner 2", l)
	}
	if l := rows[1].Log; l.End != off[2] {
		t.Errorf("PR 11 log = %+v, want it to end at runner 2", l)
	}
}
