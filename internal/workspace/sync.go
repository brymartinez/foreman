package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/store"
	"github.com/brymartinez/foreman/internal/task"
)

// KnowledgeStore is the host's knowledge, as the sync needs it.
type KnowledgeStore interface {
	Snapshot(repo string) (knowledge.Snapshot, error)
	Replace(repo string, x knowledge.Snapshot) error
	Repos() ([]string, error)
	Busy(repo string) bool
	// Lock is the learner's lock: it fails with knowledge.ErrLocked while a learner
	// holds it.
	Lock(repo string) (func(), error)
}

// Sync keeps the host database and the workspace store in step. It implements
// app.Workspace. It reads saved state only: it has no agent, runner, or network
// dependency besides the Backend.
type Sync struct {
	DB        *sql.DB
	Backend   Backend
	Tasks     store.SQLite
	Issues    localissues.Store
	Knowledge KnowledgeStore
	Now       func() time.Time
	// Log receives problems that do not stop a refresh: a failed publish, an entry
	// that does not parse.
	Log func(format string, args ...any)
	// LockWait bounds how long a publish waits for another publish of the same entry.
	LockWait time.Duration
	// Reseed makes publishes carry the host's confirmed triage into entries that
	// already exist. `storage move` sets it while it owns the workspace.
	Reseed bool

	pullAt   time.Time // when the running pull began
	mu       sync.Mutex
	problems []string
}

var _ app.Workspace = (*Sync)(nil)

func (s *Sync) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Sync) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.mu.Lock()
	s.problems = append(s.problems, msg)
	if len(s.problems) > 50 {
		s.problems = s.problems[1:]
	}
	s.mu.Unlock()
	if s.Log != nil {
		s.Log("%s", msg)
	}
}

// Problems lists what the last refreshes could not do, newest last.
func (s *Sync) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.problems...)
}

// ForemanID is the stable ID of a task: L-xxxxxxxx or owner/repo#N.
func ForemanID(key task.Key) string {
	if key.Source == task.Local {
		return key.ExternalID
	}
	return fmt.Sprintf("%s#%d", key.Repo, key.Number)
}

// KeyOf is the task key a Foreman ID names. A local issue's key has no repository
// until work binds one.
func KeyOf(id string) (task.Key, error) {
	if localissues.IDPattern.MatchString(id) {
		return localissues.Key(id, ""), nil
	}
	repo, n, err := splitGitHubID(id)
	if err != nil || !githubID.MatchString(id) {
		return task.Key{}, fmt.Errorf("%q is not a Foreman ID", id)
	}
	// The store keeps a GitHub task's source empty, and Get compares keys exactly.
	return task.Key{Repo: repo, Number: n}, nil
}

type row struct {
	Ref, Digest, AnswerDigest, Stage, StageAt string
	// Question identifies the question the store was last told is pending, so an
	// answer left in the store before that question was published is never applied.
	Question string
	// FieldsSeen and TriageSeen hash what the store held when the host last
	// reconciled it. A pull applies the store's copy only when it differs, so a
	// change made on the host is not undone by the stale copy the store still has.
	FieldsSeen, TriageSeen string
}

func (s *Sync) row(kind, key string) (row, error) {
	var r row
	err := s.DB.QueryRow(`SELECT ref, published_digest, answer_digest, stage, stage_at, question, fields_seen, triage_seen FROM workspace_entries WHERE kind = ? AND key = ?`, kind, key).
		Scan(&r.Ref, &r.Digest, &r.AnswerDigest, &r.Stage, &r.StageAt, &r.Question, &r.FieldsSeen, &r.TriageSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, nil
	}
	return r, err
}

// saveRow writes the columns publishing and acks own. FieldsSeen and TriageSeen
// are written only by setSeen, so a publish never overwrites what a pull recorded.
func (s *Sync) saveRow(kind, key string, r row) error {
	_, err := s.DB.Exec(`INSERT INTO workspace_entries (kind, key, ref, published_digest, answer_digest, stage, stage_at, question) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, key) DO UPDATE SET ref = excluded.ref, published_digest = excluded.published_digest, answer_digest = excluded.answer_digest,
	stage = excluded.stage, stage_at = excluded.stage_at, question = excluded.question`, kind, key, r.Ref, r.Digest, r.AnswerDigest, r.Stage, r.StageAt, r.Question)
	return err
}

// setSeen records the hash of the store's copy of an entry's issue fields or
// confirmed triage that the host has reconciled.
func (s *Sync) setSeen(key, col, value string) error {
	if col != "fields_seen" && col != "triage_seen" && col != "queued_at" {
		return fmt.Errorf("unknown column %q", col)
	}
	_, err := s.DB.Exec(`INSERT INTO workspace_entries (kind, key, `+col+`) VALUES ('task', ?, ?)
ON CONFLICT(kind, key) DO UPDATE SET `+col+` = excluded.`+col, key, value)
	return err
}

// queuedAt is when a pull first saw the entry's Queue box ticked, or "" while it is
// not. The store's edit time moves with every unrelated edit and with Foreman's own
// writes, so it cannot say who asked first.
func (s *Sync) queuedAt(id string) (string, error) {
	var at string
	err := s.DB.QueryRow(`SELECT queued_at FROM workspace_entries WHERE kind = 'task' AND key = ?`, id).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return at, err
}

// fieldsHash holds one hash per shared issue field, so a pull can tell which
// field a person edited and leave the others as the host has them.
func fieldsHash(e TaskEntry) string {
	parts := fieldParts(e)
	return strings.Join(parts[:], ",")
}

// legacyFieldsHash is the single aggregate hash older databases recorded.
func legacyFieldsHash(e TaskEntry) string {
	return hash(strings.TrimSpace(e.Name), strings.TrimSpace(e.Description), e.SourceURL, strings.TrimSpace(e.Repository), strings.Join(e.Tags, "\x00"))
}

func fieldParts(e TaskEntry) [5]string {
	return [5]string{hash(strings.TrimSpace(e.Name)), hash(strings.TrimSpace(e.Description)), hash(e.SourceURL), hash(strings.TrimSpace(e.Repository)), hash(strings.Join(e.Tags, "\x00"))}
}

func triageHash(text string) string { return hash(strings.TrimSpace(text)) }

// lock serializes publishes of one entry across the dashboard, runners, and
// learners on this host. It names its holder's pid, so a crashed holder's lock is
// taken over.
func (s *Sync) lock(ctx context.Context, name string) (func(), error) {
	wait := s.LockWait
	if wait == 0 {
		wait = 20 * time.Second
	}
	deadline := time.Now().Add(wait)
	token := fmt.Sprintf("%d-%d", pidSelf(), time.Now().UnixNano())
	for {
		got, err := s.tryLock(name, token)
		if err != nil {
			return nil, err
		}
		if got {
			return func() { _, _ = s.DB.Exec(`DELETE FROM locks WHERE name = ? AND token = ?`, name, token) }, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another publish of %s is still running", name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Sync) tryLock(name, token string) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var pid int
	err = tx.QueryRow(`SELECT pid FROM locks WHERE name = ?`, name).Scan(&pid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, err
	case pidAlive(pid):
		return false, nil
	}
	if _, err := tx.Exec(`INSERT INTO locks (name, pid, token, since) VALUES (?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET pid = excluded.pid, token = excluded.token, since = excluded.since`, name, pidSelf(), token, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func triageRowID(key task.Key) (string, error) {
	if key.Source == task.Local {
		return "local/" + strings.ToLower(key.ExternalID), nil
	}
	repo, err := task.ParseRepo(string(key.Repo))
	if err != nil || key.Number <= 0 {
		return "", errors.New("invalid GitHub issue identity")
	}
	return fmt.Sprintf("github/%s/%d", strings.ToLower(string(repo)), key.Number), nil
}

func (s *Sync) triage(key task.Key) (string, error) {
	id, err := triageRowID(key)
	if err != nil {
		return "", err
	}
	var text string
	err = s.DB.QueryRow(`SELECT decisions FROM triage WHERE id = ?`, id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(text), err
}

// saveTriage stores confirmed scope, replacing an earlier text. The plan check
// compares scope digests, so a changed scope invalidates the plan as before.
func (s *Sync) saveTriage(key task.Key, text string) error {
	id, err := triageRowID(key)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO triage (id, decisions, confirmed_at) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET decisions = excluded.decisions`, id, strings.TrimSpace(text), s.now().Format(time.RFC3339))
	return err
}

// taskFor finds the task record of a key. A local issue's task is stored under
// the repository it was bound to, so it is found by its ID.
func (s *Sync) taskFor(ctx context.Context, key task.Key) (*task.Record, error) {
	if key.Source != task.Local || key.Repo != "" {
		if rec, err := s.Tasks.Get(ctx, key); err != nil || rec != nil || key.Source != task.Local {
			return rec, err
		}
	}
	recs, err := s.Tasks.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.Source == task.Local && r.ExternalID == key.ExternalID {
			return r, nil
		}
	}
	return nil, nil
}

func (s *Sync) issueFor(key task.Key) (*localissues.Issue, error) {
	if key.Source != task.Local {
		return nil, nil
	}
	li, err := localissues.GetIn(s.DB, key.ExternalID)
	if errors.Is(err, localissues.ErrNotFound) {
		return nil, nil
	}
	return &li, err
}

// Publish writes one task's saved state to the store. It skips the write when the
// record is what the store last received, and a failure leaves that memory alone so
// the next refresh tries again.
func (s *Sync) Publish(ctx context.Context, key task.Key) error { return s.publish(ctx, key, "") }

// Blocked publishes why a queued task could not start.
func (s *Sync) Blocked(ctx context.Context, key task.Key, reason string) error {
	return s.publish(ctx, key, reason)
}

// built is a task's shared record as the host's saved state builds it.
type built struct {
	rec     *task.Record
	r       Record
	li      *localissues.Issue
	triage  string
	status  string
	stageAt time.Time
	prev    row
}

// build makes the record for key from saved state. It reports false when the host
// holds neither a task nor a local issue for key.
func (s *Sync) build(ctx context.Context, key task.Key) (built, bool, error) {
	id := ForemanID(key)
	rec, err := s.taskFor(ctx, key)
	if err != nil {
		return built{}, false, err
	}
	li, err := s.issueFor(key)
	if err != nil {
		return built{}, false, err
	}
	if rec == nil && li == nil {
		return built{}, false, nil
	}
	triage, err := s.triage(task.Key{Source: key.Source, ExternalID: key.ExternalID, Repo: key.Repo, Number: key.Number})
	if err != nil && key.Source == task.Local {
		return built{}, false, err
	}
	prev, err := s.row("task", id)
	if err != nil {
		return built{}, false, err
	}
	status := StatusInbox
	if rec != nil {
		status = string(rec.State)
	}
	stageAt := s.now()
	if t, perr := time.Parse(time.RFC3339, prev.StageAt); perr == nil && prev.Stage == status {
		stageAt = t
	}
	r, err := Build(Input{Task: rec, Issue: li, Triage: triage, StageAt: stageAt})
	if err != nil {
		return built{}, false, err
	}
	return built{rec: rec, r: r, li: li, triage: triage, status: status, stageAt: stageAt, prev: prev}, true, nil
}

func (s *Sync) publish(ctx context.Context, key task.Key, reason string) error {
	id := ForemanID(key)
	unlock, err := s.lock(ctx, "publish:task:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	b, ok, err := s.build(ctx, key)
	if err != nil || !ok {
		return err
	}
	r, li, triage, prev, status, stageAt := b.r, b.li, b.triage, b.prev, b.status, b.stageAt
	digest := hash(r.Digest(), reason)
	qk := app.QuestionKey(b.rec)
	stored := qk
	if stored == "" {
		stored = noQuestion
	}
	// A row written by an earlier revision names the question in its own format. If
	// that is this question, the store was told about it and its answer is valid.
	// Any other value that is not this question's key, including the empty one that
	// cannot say whether a question was pending, makes an answer in the store
	// untrustworthy.
	adopt := qk != "" && prev.Question == legacyQuestionKey(b.rec)
	if digest == prev.Digest {
		if prev.Question != stored {
			if !adopt {
				if err := s.clearStaleAnswer(ctx, id); err != nil {
					return fmt.Errorf("publish %s: %w", id, err)
				}
			}
			prev.Question = stored
			return s.saveRow("task", id, prev)
		}
		return nil
	}
	seed := TaskSeed{Name: r.Title, Repository: r.Repo, SourceURL: r.URL, ConfirmedTriage: triage, ScopeConfirmed: triage != ""}
	if li != nil {
		seed.Description, seed.Tags = li.Description, li.Tags
	}
	if stored != prev.Question && !adopt {
		// An answer already in the store was typed before this question existed.
		if err := s.clearStaleAnswer(ctx, id); err != nil {
			return fmt.Errorf("publish %s: %w", id, err)
		}
	}
	// An earlier attempt created the entry and did not finish writing it.
	unfinished := prev.Ref != "" && prev.Digest == ""
	// Scope confirmed on the host after the entry was created is copied to the
	// visible fields, unless someone changed them in the store since the host last
	// reconciled them.
	confirm := false
	if prev.Ref != "" && !unfinished && !s.Reseed && triageHash(triage) != seenOrEmpty(prev.TriageSeen) {
		confirm = s.storeTriageUnchanged(ctx, prev)
	}
	ref, err := s.Backend.UpsertTask(ctx, TaskPublish{Record: r, Reason: scrubHost(reason, b.rec), Create: seed, Resume: unfinished || s.Reseed, Reseed: s.Reseed || confirm})
	if err != nil {
		if prev.Ref == "" {
			// The entry may exist half written, and a create whose response was lost
			// returns no reference. Remember it either way so the next pull does not
			// read its partial fields as an edit and the next publish completes it.
			if ref == "" {
				ref = unknownRef
			}
			if serr := s.saveRow("task", id, row{Ref: ref, AnswerDigest: prev.AnswerDigest, Stage: prev.Stage, StageAt: prev.StageAt, Question: prev.Question}); serr != nil {
				s.logf("%v", serr)
			}
		}
		return fmt.Errorf("publish %s: %w", id, err)
	}
	if err := s.saveRow("task", id, row{Ref: ref, Digest: digest, AnswerDigest: prev.AnswerDigest, Stage: status, StageAt: stageAt.Format(time.RFC3339), Question: stored}); err != nil {
		return err
	}
	if confirm {
		if err := s.setSeen(id, "triage_seen", triageHash(triage)); err != nil {
			return err
		}
	}
	if prev.Ref == "" || unfinished {
		// A new entry was seeded from the host, so the store's copy is what the host has.
		if err := s.setSeen(id, "fields_seen", fieldsHash(TaskEntry{Name: seed.Name, Description: seed.Description, SourceURL: seed.SourceURL, Repository: seed.Repository, Tags: seed.Tags})); err != nil {
			return err
		}
		return s.setSeen(id, "triage_seen", triageHash(seed.ConfirmedTriage))
	}
	return nil
}

// unknownRef marks an entry a create may have made without telling the host where.
const unknownRef = "unconfirmed"

func seenOrEmpty(seen string) string {
	if seen == "" {
		return triageHash("")
	}
	return seen
}

// storeTriageUnchanged reports whether the store's confirmed-scope field is what the
// host last reconciled, so writing the host's scope over it loses no edit.
func (s *Sync) storeTriageUnchanged(ctx context.Context, prev row) bool {
	entries, err := s.Backend.ListTasks(ctx)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Ref == prev.Ref {
			return triageHash(e.ConfirmedTriage) == seenOrEmpty(prev.TriageSeen)
		}
	}
	return false
}

// legacyQuestionKey is the key an earlier revision stored for a task's pending
// question, kept so a question already published under it is recognized.
func legacyQuestionKey(rec *task.Record) string {
	if rec == nil {
		return ""
	}
	d := rec.Pending()
	if d == nil {
		return ""
	}
	answered := 0
	for _, x := range rec.Decisions {
		if x.Answer != "" {
			answered++
		}
	}
	return hash(d.AskedAt.UTC().Format(time.RFC3339Nano), fmt.Sprint(answered), d.Question.Issue)
}

// noQuestion is the stored question of a published record with nothing pending, so
// an empty value still means "never tracked".
const noQuestion = "-"

// clearStaleAnswer empties the Answer field of an entry that is about to show a new
// question.
func (s *Sync) clearStaleAnswer(ctx context.Context, id string) error {
	entries, err := s.Backend.ListTasks(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.ID == id && strings.TrimSpace(e.Answer) != "" {
			if err := s.Backend.ClearAnswer(ctx, e.Ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// PublishKnowledge writes one repository's knowledge to the store, unless the
// store's entry for it does not parse: that entry is neither adopted nor
// overwritten until someone fixes it.
func (s *Sync) PublishKnowledge(ctx context.Context, repo string) error {
	unlock, err := s.lock(ctx, "publish:knowledge:"+repo)
	if err != nil {
		return err
	}
	defer unlock()
	prev, err := s.row("knowledge", repo)
	if err != nil {
		return err
	}
	if prev.AnswerDigest == pausedMark {
		return nil
	}
	snap, err := s.Knowledge.Snapshot(repo)
	if err != nil {
		return err
	}
	if snap.Empty() {
		return nil
	}
	digest := SnapshotDigest(snap)
	if digest == prev.Digest {
		return nil
	}
	if !s.Reseed {
		// Never write over an entry someone edited, or broke, since the host last
		// saw it. The next pull adopts the edit or pauses on the broken entry.
		entries, err := s.Backend.ListKnowledge(ctx)
		if err != nil {
			return fmt.Errorf("publish knowledge of %s: %w", repo, err)
		}
		for _, e := range entries {
			if e.Repo != repo {
				continue
			}
			if e.ParseErr != nil && !errors.Is(e.ParseErr, ErrNoPayload) {
				return nil
			}
			if e.ParseErr == nil && e.Snapshot != nil && e.Digest != prev.Digest {
				return nil
			}
		}
	}
	if err := s.Backend.WriteKnowledge(ctx, KnowledgePublish{Repo: repo, Snapshot: snap, Guidance: snap.Guidance(), Active: 0}); err != nil {
		return fmt.Errorf("publish knowledge of %s: %w", repo, err)
	}
	return s.saveRow("knowledge", repo, row{Ref: prev.Ref, Digest: digest})
}

// pausedMark in a knowledge row's answer_digest means the store's entry is broken.
const pausedMark = "paused"
