package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/brymartinez/foreman/internal/config"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/task"
)

// Move copies a SQLite workspace into the configured store. It starts no agent,
// runner, or learner and sets no Queue box.
type Move struct {
	Sync    *Sync
	Storage config.Storage
	// Report receives one line per step.
	Report func(format string, args ...any)
}

// MoveResult counts what a move copied.
type MoveResult struct {
	AlreadyMoved bool
	Tasks        int
	Issues       int // local issues that have no task yet
	Repositories int
}

func (m Move) say(format string, args ...any) {
	if m.Report != nil {
		m.Report(format, args...)
	}
}

// Run moves the workspace: refuse while work is live, take the move lock, run the
// check, copy tasks and then knowledge, verify every entry, and only then write
// the cutover marker. Entries are upserted by Foreman ID, so a retry makes no
// duplicates.
func (m Move) Run(ctx context.Context) (MoveResult, error) {
	s := m.Sync
	marker, err := ReadMarker(s.DB)
	if err != nil {
		return MoveResult{}, err
	}
	if marker != nil {
		if marker.Backend == m.Storage.Backend && marker.Target == Target(m.Storage) {
			m.say("The workspace already moved to %s on %s. Nothing to do.", marker.Backend, marker.MovedAt)
			return MoveResult{AlreadyMoved: true, Tasks: marker.Tasks, Issues: marker.Issues, Repositories: marker.Repositories}, nil
		}
		return MoveResult{}, fmt.Errorf("this workspace already moved to %s (%s); moving to another store is not supported", marker.Backend, marker.Target)
	}
	unlock, err := s.lock(ctx, MoveLock)
	if err != nil {
		return MoveResult{}, errors.New("another foreman storage move is running")
	}
	defer unlock()

	recs, err := s.Tasks.List(ctx)
	if err != nil {
		return MoveResult{}, err
	}
	if err := m.refuseWhileLive(recs); err != nil {
		return MoveResult{}, err
	}

	m.say("Checking the store.")
	if err := s.Backend.Check(ctx, func(st Step) {
		if st.Err != nil {
			m.say("failed  %s: %v", st.Name, st.Err)
			return
		}
		m.say("passed  %s", st.Name)
	}); err != nil {
		return MoveResult{}, fmt.Errorf("the storage check failed, so nothing was moved: %w", err)
	}
	if err := s.Backend.Ensure(ctx); err != nil {
		return MoveResult{}, err
	}

	// Rows left by an earlier attempt describe another destination, or entries since
	// removed. Clearing them makes every entry copy again to this one.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM workspace_entries`); err != nil {
		return MoveResult{}, err
	}
	keys, res, err := m.sourceKeys(ctx, recs)
	if err != nil {
		return MoveResult{}, err
	}
	m.say("Copying %d tasks and %d issues without a task.", res.Tasks, res.Issues)
	for _, k := range keys {
		if err := s.Publish(ctx, k); err != nil {
			return MoveResult{}, fmt.Errorf("copy %s: %w", ForemanID(k), err)
		}
	}
	repos, err := s.Knowledge.Repos()
	if err != nil {
		return MoveResult{}, err
	}
	res.Repositories = len(repos)
	m.say("Copying the knowledge of %d repositories.", len(repos))
	for _, repo := range repos {
		if err := s.PublishKnowledge(ctx, repo); err != nil {
			return MoveResult{}, fmt.Errorf("copy the knowledge of %s: %w", repo, err)
		}
	}

	if err := s.trashDuplicates(ctx); err != nil {
		return MoveResult{}, err
	}
	m.say("Verifying every entry.")
	if diffs := m.verify(ctx, keys, repos); len(diffs) > 0 {
		return MoveResult{}, fmt.Errorf("verification failed, so the workspace stays on sqlite; these differ:\n  %s", strings.Join(diffs, "\n  "))
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return MoveResult{}, err
	}
	defer tx.Rollback()
	if err := WriteMarker(tx, Marker{Backend: m.Storage.Backend, Target: Target(m.Storage), Tasks: res.Tasks, Issues: res.Issues, Repositories: res.Repositories}); err != nil {
		return MoveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MoveResult{}, err
	}
	m.say("Moved %d tasks, %d issues without a task, and the knowledge of %d repositories to %s.", res.Tasks, res.Issues, res.Repositories, m.Storage.Backend)
	return res, nil
}

// refuseWhileLive stops the move while a runner, a launch claim, or a learner is
// live, because it would change what the move copies.
func (m Move) refuseWhileLive(recs []*task.Record) error {
	s := m.Sync
	var live []string
	for _, r := range recs {
		d := r.Delegation
		if d == nil {
			continue
		}
		id := ForemanID(r.Key)
		if d.RunnerPID > 0 && pidAlive(d.RunnerPID) {
			live = append(live, "a runner works "+id)
		}
		if d.Launch != nil && d.Launch.PID > 0 && pidAlive(d.Launch.PID) {
			live = append(live, "a launch is starting "+id)
		}
	}
	rows, err := s.DB.Query(`SELECT 'a start is running for ' || id, pid FROM start_locks UNION ALL SELECT 'a learner holds ' || name, pid FROM locks WHERE name LIKE 'learning:%'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var what string
		var pid int
		if err := rows.Scan(&what, &pid); err != nil {
			return err
		}
		if pidAlive(pid) {
			live = append(live, what)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(live) > 0 {
		sort.Strings(live)
		return fmt.Errorf("cannot move while work is live (%s); stop the dashboard and runners, then run foreman storage move again", strings.Join(live, "; "))
	}
	return nil
}

// sourceKeys lists every task and every local issue that has no task.
func (m Move) sourceKeys(ctx context.Context, recs []*task.Record) ([]task.Key, MoveResult, error) {
	var keys []task.Key
	haveLocal := map[string]bool{}
	for _, r := range recs {
		keys = append(keys, r.Key)
		if r.Source == task.Local {
			haveLocal[r.ExternalID] = true
		}
	}
	res := MoveResult{Tasks: len(recs)}
	issues, err := m.Sync.Issues.List(ctx)
	if err != nil {
		return nil, res, err
	}
	for _, li := range issues {
		if !haveLocal[li.ID] {
			keys = append(keys, localissues.Key(li.ID, ""))
			res.Issues++
		}
	}
	return keys, res, nil
}

// verify reads every entry back and compares its parsed record to the one the host
// builds from its database, then compares each repository's knowledge digest. It
// names every entry that differs.
func (m Move) verify(ctx context.Context, keys []task.Key, repos []string) []string {
	s := m.Sync
	entries, err := s.Backend.ListTasks(ctx)
	if err != nil {
		return []string{"read back the store: " + err.Error()}
	}
	byID := map[string][]TaskEntry{}
	for _, e := range entries {
		if e.ID != "" {
			byID[e.ID] = append(byID[e.ID], e)
		}
	}
	var diffs []string
	want := map[string]bool{}
	for _, k := range keys {
		id := ForemanID(k)
		want[id] = true
		got := byID[id]
		if len(got) != 1 {
			diffs = append(diffs, fmt.Sprintf("%s: %d entries in the store, want 1", id, len(got)))
			continue
		}
		b, ok, err := s.build(ctx, k)
		if err != nil || !ok {
			diffs = append(diffs, fmt.Sprintf("%s: cannot rebuild from the database: %v", id, err))
			continue
		}
		if want := strings.TrimSpace(b.triage); want != strings.TrimSpace(got[0].ConfirmedTriage) {
			// The payload holds the full text; the user-facing field is what the first
			// refresh reads back as an edit, so it must hold the full text too.
			diffs = append(diffs, fmt.Sprintf("%s: the Confirmed triage field differs from the saved scope", id))
			continue
		}
		g := got[0].Record
		if g == nil {
			diffs = append(diffs, fmt.Sprintf("%s: the entry has no readable record: %v", id, got[0].ParseErr))
			continue
		}
		if g.Status != b.r.Status || branchPR(g) != branchPR(&b.r) || g.Digest() != b.r.Digest() {
			diffs = append(diffs, fmt.Sprintf("%s: the stored record differs from the database (status %q, want %q)", id, g.Status, b.r.Status))
		}
	}
	if len(byID) != len(want) {
		var extra []string
		for id := range byID {
			if !want[id] {
				extra = append(extra, id)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			diffs = append(diffs, "entries the database does not have: "+strings.Join(extra, ", "))
		}
	}
	kentries, err := s.Backend.ListKnowledge(ctx)
	if err != nil {
		return append(diffs, "read back the knowledge: "+err.Error())
	}
	kby := map[string]KnowledgeEntry{}
	for _, e := range kentries {
		kby[e.Repo] = e
	}
	for _, repo := range repos {
		snap, err := s.Knowledge.Snapshot(repo)
		if err != nil {
			diffs = append(diffs, fmt.Sprintf("knowledge of %s: %v", repo, err))
			continue
		}
		e, ok := kby[repo]
		if !ok || e.Snapshot == nil || e.Digest != SnapshotDigest(snap) {
			diffs = append(diffs, fmt.Sprintf("knowledge of %s differs from the database", repo))
		}
	}
	sort.Strings(diffs)
	return diffs
}

// branchPR is the branch and PR number of a record, the key fields verification
// reports besides the ID and status.
func branchPR(r *Record) string {
	if r.Work == nil {
		return ""
	}
	pr := 0
	if r.Work.PR != nil {
		pr = r.Work.PR.Number
	}
	return fmt.Sprintf("%s#%d", r.Work.Branch, pr)
}
