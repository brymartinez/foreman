package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/task"
)

// Timing says how much of a history row's durations is known.
type Timing int

const (
	// TimingMeasured is first turn to first handoff, from recorded phase marks.
	TimingMeasured Timing = iota
	// TimingNoHandoff is a PR that merged or closed before any handoff. Its phases run
	// up to the close.
	TimingNoHandoff
	// TimingUnavailable is an attempt from before phase marks, or one whose start or
	// end was never recorded.
	TimingUnavailable
)

const (
	StatusMerged  = "merged"
	StatusClosed  = "closed"
	StatusUnknown = "unknown"
)

// LogRange is the part of a task log that belongs to one attempt. End 0 reads to the
// end of the file. Whole means the attempt's boundaries are unknown, so the range is
// the issue's whole log.
type LogRange struct {
	Path       string
	Start, End int64
	Whole      bool
}

// HistoryRow is one finished Foreman PR: one attempt at an issue.
type HistoryRow struct {
	Key      task.Key // the issue
	PR       int
	URL      string
	Title    string
	Status   string
	ClosedAt time.Time // when it merged or closed
	Timing   Timing
	// Total is first turn to first handoff for TimingMeasured, and first turn to the
	// close for TimingNoHandoff.
	Total  time.Duration
	Phases map[task.Phase]time.Duration
	Log    LogRange
}

// historyWorkers bounds the parallel gh reads when the history view opens.
const historyWorkers = 4

type historyCandidate struct {
	row     HistoryRow
	attempt *task.Attempt // nil for an attempt from before phase marks
	title   string        // the issue title, for a PR GitHub could not read
}

type prRef struct {
	repo task.Repo
	pr   int
}

// History lists the finished PRs foreman opened, newest close first, with their phase
// times. It reads recorded attempts, then finds older PRs in task files and task logs.
// It writes nothing. A PR whose status cannot be read stays in the list, last.
func (a App) History(ctx context.Context, allRepos bool) ([]HistoryRow, []error, error) {
	recs, err := a.Store.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	var cands []historyCandidate
	seen := map[prRef]bool{}
	byKey := map[task.Key]*task.Record{}
	logOf := map[string]task.Key{}
	for _, rec := range recs {
		if !a.repoVisible(rec.Repo, allRepos) {
			continue
		}
		byKey[rec.Key] = rec
		path := a.taskLog(rec)
		logOf[path] = rec.Key
		for i := range rec.Attempts {
			att := &rec.Attempts[i]
			if att.PR == 0 {
				continue
			}
			seen[prRef{rec.Repo, att.PR}] = true
			r := LogRange{Path: path, Start: att.LogStart}
			if i+1 < len(rec.Attempts) && rec.Attempts[i+1].LogStart > att.LogStart {
				r.End = rec.Attempts[i+1].LogStart
			}
			cands = append(cands, historyCandidate{row: HistoryRow{Key: rec.Key, PR: att.PR, URL: att.PRURL, Log: r}, attempt: att, title: rec.Title})
		}
	}
	var warnings []error
	for _, path := range a.historyLogs(logOf) {
		found, err := legacyAttempts(path, logOf[path], byKey, seen, func(repo task.Repo) bool { return a.repoVisible(repo, allRepos) })
		if err != nil {
			warnings = append(warnings, err)
			continue
		}
		for _, c := range found {
			seen[prRef{c.row.Key.Repo, c.row.PR}] = true
			if rec := byKey[c.row.Key]; rec != nil {
				c.title = rec.Title
			}
			cands = append(cands, c)
		}
	}
	for _, rec := range byKey {
		if d := rec.Delegation; d != nil && d.PR != 0 && !seen[prRef{rec.Repo, d.PR}] {
			seen[prRef{rec.Repo, d.PR}] = true
			cands = append(cands, historyCandidate{row: HistoryRow{Key: rec.Key, PR: d.PR, URL: d.PRURL, Log: LogRange{Path: a.taskLog(rec)}}, title: rec.Title})
		}
	}
	rows, readErrs := a.historyStatus(ctx, cands)
	warnings = append(warnings, readErrs...)
	slices.SortStableFunc(rows, func(x, y HistoryRow) int {
		if (x.Status == StatusUnknown) != (y.Status == StatusUnknown) {
			if x.Status == StatusUnknown {
				return 1
			}
			return -1
		}
		if c := y.ClosedAt.Compare(x.ClosedAt); c != 0 {
			return c
		}
		if c := strings.Compare(string(x.Key.Repo), string(y.Key.Repo)); c != 0 {
			return c
		}
		return y.PR - x.PR
	})
	return rows, warnings, nil
}

// taskLog is the task's runner log: the recorded path, or where the runner writes it.
func (a App) taskLog(rec *task.Record) string {
	if d := rec.Delegation; d != nil && d.RunnerLog != "" {
		return d.RunnerLog
	}
	if a.LogDir == "" {
		return ""
	}
	return filepath.Join(a.LogDir, fmt.Sprintf("%s-%d.log", strings.ReplaceAll(string(rec.Repo), "/", "-"), rec.Number))
}

// historyLogs lists the task logs to scan: every visible task's log, and every log in
// LogDir, which still holds the logs of tasks whose files are gone.
func (a App) historyLogs(logOf map[string]task.Key) []string {
	paths := map[string]bool{}
	for p := range logOf {
		if p != "" {
			paths[p] = true
		}
	}
	if a.LogDir != "" {
		matches, _ := filepath.Glob(filepath.Join(a.LogDir, "*.log"))
		for _, p := range matches {
			paths[p] = true
		}
	}
	out := make([]string, 0, len(paths))
	for p := range paths {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

var (
	verifiedLine = regexp.MustCompile(`^\S+ foreman: verified PR #(\d+) (\S+)`)
	startLine    = regexp.MustCompile(`^\S+ foreman: (runner \d+ started|starting )`)
	runnerLine   = regexp.MustCompile(`^\S+ foreman: runner \d+ started`)
	prURLRepo    = regexp.MustCompile(`/([^/]+/[^/]+)/pull/\d+$`)
	logNumber    = regexp.MustCompile(`-(\d+)\.log$`)
)

type loggedPR struct {
	repo        task.Repo
	pr          int
	url         string
	first, last int64 // the offsets of its first and last verified lines
}

// legacyAttempts finds the PRs a task log verified that no recorded attempt covers.
// key is the log's task, or zero for a log whose task file is gone; the issue then
// comes from the file name and the PR's repository. A PR's range starts at the last
// runner start before it verified the PR, so a runner that failed in between is left
// out, and the previous PR's range ends at the first start after it. When no start
// separates two PRs, each gets the whole log.
func legacyAttempts(path string, key task.Key, byKey map[task.Key]*task.Record, seen map[prRef]bool, visible func(task.Repo) bool) ([]historyCandidate, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var prs []loggedPR
	var starts, runners []int64
	r := bufio.NewReader(f)
	for off := int64(0); ; {
		line, err := r.ReadString('\n')
		if m := verifiedLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			if i := slices.IndexFunc(prs, func(p loggedPR) bool { return p.pr == n }); i >= 0 {
				prs[i].last = off
			} else if rm := prURLRepo.FindStringSubmatch(m[2]); rm != nil {
				prs = append(prs, loggedPR{repo: task.Repo(rm[1]), pr: n, url: m[2], first: off, last: off})
			}
		} else if startLine.MatchString(line) {
			starts = append(starts, off)
			if runnerLine.MatchString(line) {
				runners = append(runners, off)
			}
		}
		off += int64(len(line))
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	}
	if key == (task.Key{}) {
		m := logNumber.FindStringSubmatch(path)
		if m == nil || len(prs) == 0 {
			return nil, nil
		}
		n, _ := strconv.Atoi(m[1])
		key = task.Key{Repo: prs[0].repo, Number: n}
		if filepath.Base(path) != fmt.Sprintf("%s-%d.log", strings.ReplaceAll(string(key.Repo), "/", "-"), n) {
			return nil, nil
		}
	}
	if !visible(key.Repo) {
		return nil, nil
	}
	// Lines from the first recorded attempt on belong to recorded attempts.
	var recorded int64
	if rec := byKey[key]; rec != nil && len(rec.Attempts) > 0 {
		recorded = rec.Attempts[0].LogStart
	}
	prs = slices.DeleteFunc(prs, func(p loggedPR) bool { return p.repo != key.Repo || seen[prRef{p.repo, p.pr}] })
	ranges := make([]LogRange, len(prs))
	whole := false
	for i, p := range prs {
		ranges[i].Path = path
		if i == 0 {
			continue
		}
		prev := prs[i-1]
		between := func(s int64) bool { return s > prev.last && s <= p.first }
		j := slices.IndexFunc(starts, between)
		if j < 0 {
			whole = true
			break
		}
		ranges[i].Start, ranges[i-1].End = starts[j], starts[j]
		if k := lastIndex(runners, between); k >= 0 {
			ranges[i].Start = runners[k]
		}
	}
	out := make([]historyCandidate, 0, len(prs))
	for i, p := range prs {
		r := ranges[i]
		if whole {
			r = LogRange{Path: path, Whole: true}
		} else if recorded > r.Start && (r.End == 0 || r.End > recorded) {
			r.End = recorded
		}
		out = append(out, historyCandidate{row: HistoryRow{Key: key, PR: p.pr, URL: p.url, Log: r}})
	}
	return out, nil
}

func lastIndex(s []int64, f func(int64) bool) int {
	for i := len(s) - 1; i >= 0; i-- {
		if f(s[i]) {
			return i
		}
	}
	return -1
}

// historyStatus reads each PR from GitHub, drops the open ones, and works out timing.
func (a App) historyStatus(ctx context.Context, cands []historyCandidate) ([]HistoryRow, []error) {
	prs := make([]PullRequest, len(cands))
	errs := make([]error, len(cands))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(historyWorkers, len(cands)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				prs[i], errs[i] = a.GitHub.GetPullRequest(ctx, task.Key{Repo: cands[i].row.Key.Repo, Number: cands[i].row.PR})
			}
		}()
	}
	for i := range cands {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	var rows []HistoryRow
	var warnings []error
	for i, c := range cands {
		row, pr := c.row, prs[i]
		switch {
		case errs[i] != nil:
			row.Status, row.Title = StatusUnknown, c.title
			warnings = append(warnings, fmt.Errorf("%s PR #%d: %w", row.Key.Repo, row.PR, errs[i]))
		case pr.State == StatusMerged:
			row.Status, row.ClosedAt = StatusMerged, pr.MergedAt
		case pr.State == StatusClosed:
			row.Status, row.ClosedAt = StatusClosed, pr.ClosedAt
		default:
			continue
		}
		if errs[i] == nil {
			row.Title = pr.Title
			if row.URL == "" {
				row.URL = pr.URL
			}
		}
		timeRow(&row, c.attempt)
		rows = append(rows, row)
	}
	return rows, warnings
}

// timeRow fills in a row's timing from its recorded attempt. A PR that closed before
// any handoff is measured up to GitHub's close time, or else the close foreman saw.
func timeRow(row *HistoryRow, att *task.Attempt) {
	row.Timing = TimingUnavailable
	if att == nil || att.StartedAt.IsZero() {
		return
	}
	end := att.HandoffAt
	if !end.IsZero() {
		row.Timing = TimingMeasured
	} else {
		// GitHub's close time is when the PR closed. EndedAt is when a refresh saw it,
		// which can be hours later, so it is used only when GitHub's is unknown.
		end = row.ClosedAt
		if end.IsZero() {
			end = att.EndedAt
		}
		if end.IsZero() {
			return
		}
		row.Timing = TimingNoHandoff
	}
	row.Total, row.Phases = end.Sub(att.StartedAt), att.Phases(end)
}
