package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/localissues"
	"github.com/brymartinez/foreman/internal/task"
)

var pidSelf = os.Getpid

// Pull syncs the store into the host database and returns the answers and queued
// tasks that need the app. It writes to the store only to assign IDs, trash
// duplicates, and publish what is stale.
func (s *Sync) Pull(ctx context.Context) (app.WorkspacePull, error) {
	var out app.WorkspacePull
	if err := s.trashDuplicates(ctx); err != nil {
		return out, err
	}
	entries, err := s.Backend.ListTasks(ctx)
	if err != nil {
		return out, err
	}
	recs, err := s.Tasks.List(ctx)
	if err != nil {
		return out, err
	}
	byID := map[string]*task.Record{}
	for _, r := range recs {
		byID[TaskID(r)] = r
	}
	s.pullAt = s.now()
	edited := map[string]time.Time{}
	for _, e := range entries {
		at := e.EditedAt
		if at.IsZero() {
			at = e.CreatedAt
		}
		edited[e.Ref] = at
		s.pullEntry(ctx, e, byID, &out)
	}
	// Oldest request first. Requests first seen in one pull share a time, and the
	// store's edit time is the best order left among them.
	sort.SliceStable(out.Queued, func(i, j int) bool {
		a, b := out.Queued[i], out.Queued[j]
		if !a.QueuedAt.Equal(b.QueuedAt) {
			return a.QueuedAt.Before(b.QueuedAt)
		}
		return edited[a.Token].Before(edited[b.Token])
	})
	if err := s.pullKnowledge(ctx); err != nil {
		s.logf("knowledge sync: %v", err)
	}
	// The app publishes a queued task itself, with the reason it waits. Publishing
	// it here first would write the record without the reason and then again with it.
	skip := map[string]bool{}
	for _, q := range out.Queued {
		skip[ForemanID(q.Key)] = true
	}
	s.republish(ctx, skip)
	return out, nil
}

// trashDuplicates keeps the oldest entry of each Foreman ID and trashes the rest.
func (s *Sync) trashDuplicates(ctx context.Context) error {
	dups, err := s.Backend.Duplicates(ctx)
	if err != nil {
		return err
	}
	for _, d := range dups {
		for _, ref := range d.Refs[1:] {
			if err := s.Backend.Trash(ctx, ref); err != nil {
				s.logf("trash duplicate of %s: %v", d.ID, err)
			}
		}
	}
	return nil
}

func (s *Sync) pullEntry(ctx context.Context, e TaskEntry, byID map[string]*task.Record, out *app.WorkspacePull) {
	if e.ParseErr != nil && e.Record == nil && e.ID == "" {
		s.logf("%s cannot be read: %v", e.Name, e.ParseErr)
		return
	}
	id := e.ID
	if id == "" {
		var err error
		if id, err = s.adopt(ctx, e); err != nil {
			s.logf("create an issue from %q: %v", e.Name, err)
			return
		}
		e.ID = id
	}
	key, err := KeyOf(id)
	if err != nil {
		return // the connectivity check's test entry, or an ID Foreman did not write
	}
	if e.Record != nil && e.Record.ID != id {
		// A copied note: its payload names another task. Restoring it would save that
		// task's record over the live one, so the copy is just a new entry. The next
		// publish writes its own record.
		s.logf("%s holds the record of %s, so it is not restored from it", e.Name, e.Record.ID)
		e.Record = nil
	}
	rec := byID[id]
	if rec == nil {
		if rec, err = s.hydrate(ctx, e, key); err != nil {
			s.logf("restore %s: %v", id, err)
		}
		if rec != nil {
			byID[id] = rec
		}
	}
	prev, rerr := s.row("task", id)
	if rerr != nil {
		s.logf("read entry map of %s: %v", id, rerr)
		return
	}
	if prev.Ref != "" && prev.Digest == "" {
		// A publish created this entry and has not finished writing it. Its fields
		// are partial, so they are not an edit. The republish at the end completes it.
		s.collect(ctx, e, id, taskKey(key, rec), rec, out)
		return
	}
	// The store's copy is applied only when it changed since the host last
	// reconciled it. Otherwise a change made on the host (issue triage, a replacing
	// import) would be undone by the older text the store still holds.
	if fh := fieldsHash(e); key.Source == task.Local && fh != prev.FieldsSeen {
		if prev.FieldsSeen != "" && prev.FieldsSeen == legacyFieldsHash(e) {
			// Recorded as one aggregate hash before per-field hashes: the store is
			// unchanged, so only the format is upgraded and host edits stay.
			if err := s.setSeen(id, "fields_seen", fh); err != nil {
				s.logf("record issue fields of %s: %v", id, err)
			}
		} else if err := s.syncIssue(ctx, e, key, prev.FieldsSeen); err != nil {
			if !errors.Is(err, errHeld) {
				s.logf("sync issue %s: %v", id, err)
			}
		} else if err := s.setSeen(id, "fields_seen", fh); err != nil {
			s.logf("record issue fields of %s: %v", id, err)
		}
	}
	if text := strings.TrimSpace(e.ConfirmedTriage); e.ScopeConfirmed && text != "" && triageHash(text) != prev.TriageSeen {
		if cur, err := s.triage(key); err == nil && cur != text {
			if err := s.saveTriage(taskKey(key, rec), e.ConfirmedTriage); err != nil {
				s.logf("save triage of %s: %v", id, err)
				text = ""
			}
		}
		if text != "" {
			if err := s.setSeen(id, "triage_seen", triageHash(text)); err != nil {
				s.logf("record triage of %s: %v", id, err)
			}
		}
	}
	if rec != nil {
		key = rec.Key
	}
	s.collect(ctx, e, id, key, rec, out)
}

// errHeld means a store edit was not applied because a field is invalid.
var errHeld = errors.New("issue fields are invalid")

func taskKey(key task.Key, rec *task.Record) task.Key {
	if rec != nil {
		return rec.Key
	}
	return key
}

// adopt gives an entry a person created a new L- ID. The ID is written first, so a
// crash leaves an entry whose issue the next refresh creates.
func (s *Sync) adopt(ctx context.Context, e TaskEntry) (string, error) {
	if strings.TrimSpace(e.Name) == "" {
		return "", errors.New("it has no title")
	}
	if err := (localissues.Input{Title: e.Name, SourceURL: e.SourceURL, Repo: e.Repository}).Validate(); err != nil {
		return "", err
	}
	id, err := localissues.NewID()
	if err != nil {
		return "", err
	}
	if err := s.Backend.AssignID(ctx, e.Ref, id); err != nil {
		return "", err
	}
	return id, nil
}

// hydrate rebuilds a task a host has no copy of from its shared record, and the
// local issue behind it when that is missing too.
func (s *Sync) hydrate(ctx context.Context, e TaskEntry, key task.Key) (*task.Record, error) {
	if key.Source == task.Local {
		if li, err := s.issueFor(key); err != nil || li == nil {
			if err != nil {
				return nil, err
			}
			if err := s.createIssue(e, key.ExternalID); err != nil {
				return nil, err
			}
		}
	}
	if e.Record == nil || e.Record.Work == nil {
		return nil, nil
	}
	rec, err := e.Record.Hydrate(s.now())
	if err != nil || rec == nil {
		return nil, err
	}
	if triage := strings.TrimSpace(e.Record.Triage); triage != "" {
		if cur, _ := s.triage(rec.Key); cur == "" {
			if err := s.saveTriage(rec.Key, triage); err != nil {
				return nil, err
			}
		}
	}
	return rec, s.Tasks.Save(ctx, rec)
}

func (s *Sync) createIssue(e TaskEntry, id string) error {
	li := localissues.Issue{ID: id, Title: strings.TrimSpace(e.Name), Description: strings.TrimSpace(e.Description), SourceURL: e.SourceURL,
		Status: localissues.Open, Repo: e.Repository, Tags: e.Tags, CreatedAt: s.now(), UpdatedAt: s.now()}
	if r := e.Record; r != nil {
		if rli := r.LocalIssue(); rli != nil {
			li = *rli
		}
	}
	if li.Title == "" {
		li.Title = id
	}
	return localissues.PutIn(s.DB, li)
}

// syncIssue copies edits a person made to the shared fields into the host's issue.
// Only a field whose store value differs from what the host last saw is copied.
func (s *Sync) syncIssue(ctx context.Context, e TaskEntry, key task.Key, seen string) error {
	li, err := s.issueFor(key)
	if err != nil || li == nil {
		return err
	}
	next := *li
	parts := fieldParts(e)
	was := strings.Split(seen, ",")
	changed := func(i int) bool { return len(was) != len(parts) || was[i] != parts[i] }
	if t := strings.TrimSpace(e.Name); changed(0) && t != "" {
		next.Title = t
	}
	if changed(1) {
		next.Description = strings.TrimSpace(e.Description)
	}
	if changed(2) && (e.SourceURL != "" || li.SourceURL == "") {
		next.SourceURL = e.SourceURL
	}
	if changed(3) {
		next.Repo = strings.TrimSpace(e.Repository)
	}
	if changed(4) {
		next.Tags = e.Tags
		if next.Tags == nil {
			next.Tags = []string{}
		}
	}
	if err := (localissues.Input{Title: next.Title, SourceURL: next.SourceURL, Repo: next.Repo}).Validate(); err != nil {
		// Keep the host's copy until the field is fixed. Not recording the fields as
		// seen makes the next pull retry every edit together.
		return errHeld
	}
	have := *li
	if have.Tags == nil {
		have.Tags = []string{}
	}
	if next.Title == have.Title && strings.TrimSpace(next.Description) == strings.TrimSpace(have.Description) && next.SourceURL == have.SourceURL &&
		next.Repo == have.Repo && slices.Equal(next.Tags, have.Tags) {
		return nil
	}
	next.UpdatedAt = s.now()
	return localissues.PutIn(s.DB, next)
}

// collect adds the entry's pending answer and queue request to out.
func (s *Sync) collect(ctx context.Context, e TaskEntry, id string, key task.Key, rec *task.Record, out *app.WorkspacePull) {
	if ans := s.pendingAnswer(ctx, e, id, key); ans != nil {
		out.Answers = append(out.Answers, *ans)
	}
	seen, err := s.queuedAt(id)
	if err != nil {
		s.logf("read the queue request of %s: %v", id, err)
	}
	if !e.Queue && seen != "" {
		if err := s.setSeen(id, "queued_at", ""); err != nil {
			s.logf("%v", err)
		}
	}
	if e.Queue && (key.Source != task.Local || localIssueOpen(s, key)) {
		at, perr := time.Parse(time.RFC3339Nano, seen)
		if perr != nil {
			at = s.pullAt
			if err := s.setSeen(id, "queued_at", at.UTC().Format(time.RFC3339Nano)); err != nil {
				s.logf("%v", err)
			}
		}
		out.Queued = append(out.Queued, app.WorkspaceQueued{Key: key, QueuedAt: at, Token: e.Ref})
	}
}

// pendingAnswer returns the answer typed for the task's pending question, or nil.
// It counts only for the question the store was last told is pending: publishing
// clears an answer typed before a question appeared, so what is in the field now
// came after it. It re-reads the entry and the task under the publish lock, so a
// snapshot taken before another process published a new question cannot hand an
// old answer to that question.
func (s *Sync) pendingAnswer(ctx context.Context, e TaskEntry, id string, key task.Key) *app.WorkspaceAnswer {
	if strings.TrimSpace(e.Answer) == "" {
		return nil
	}
	unlock, err := s.lock(ctx, "publish:task:"+id)
	if err != nil {
		s.logf("%v", err)
		return nil
	}
	defer unlock()
	rec, err := s.taskFor(ctx, key)
	if err != nil {
		s.logf("read %s: %v", id, err)
		return nil
	}
	qk := app.QuestionKey(rec)
	if qk == "" {
		return nil
	}
	prev, err := s.row("task", id)
	if err != nil {
		s.logf("read entry map of %s: %v", id, err)
		return nil
	}
	adopt := prev.Question == legacyQuestionKey(rec)
	if adopt {
		// Published under an earlier revision's key for this same question.
		prev.Question = qk
		if err := s.saveRow("task", id, prev); err != nil {
			s.logf("%v", err)
			return nil
		}
	}
	if qk != prev.Question {
		return nil
	}
	entries, err := s.Backend.ListTasks(ctx)
	if err != nil {
		s.logf("read the answer of %s: %v", id, err)
		return nil
	}
	var ans string
	for _, f := range entries {
		if f.Ref == e.Ref {
			ans = strings.TrimSpace(f.Answer)
		}
	}
	if ans == "" {
		return nil
	}
	token := hash(id, qk, ans)
	if token == prev.AnswerDigest {
		// Already applied; the clear did not land. Try again.
		if err := s.Backend.ClearAnswer(ctx, e.Ref); err != nil {
			s.logf("clear the answer of %s: %v", id, err)
		}
		return nil
	}
	return &app.WorkspaceAnswer{Key: key, Text: ans, Token: token, Question: qk}
}

func localIssueOpen(s *Sync, key task.Key) bool {
	li, err := s.issueFor(key)
	return err == nil && li != nil && li.Status == localissues.Open
}

// Ack records that a pulled answer was applied or a queued task started, then
// clears the field in the store. The record is written first, so an answer
// applies at most once even when the clear fails.
func (s *Sync) Ack(ctx context.Context, a app.WorkspaceAck) error {
	id := ForemanID(a.Key)
	unlock, err := s.lock(ctx, "publish:task:"+id)
	if err != nil {
		return err
	}
	prev, err := s.row("task", id)
	moved := false
	if err == nil && a.Kind == app.AckAnswer {
		prev.AnswerDigest = a.Token
		err = s.saveRow("task", id, prev)
		if cur, gerr := s.taskFor(ctx, a.Key); err == nil && gerr == nil && a.Question != "" && app.QuestionKey(cur) != a.Question {
			moved = true
		}
	}
	unlock()
	if err != nil {
		return err
	}
	if moved {
		// The task is on another question. Publishing it cleared this answer, and the
		// field may already hold the answer to the new question.
		return nil
	}
	ref, err := s.refFor(ctx, id)
	if err != nil {
		return err
	}
	switch a.Kind {
	case app.AckAnswer:
		err = s.Backend.ClearAnswer(ctx, ref)
	case app.AckQueue:
		err = s.Backend.ClearQueue(ctx, ref)
	default:
		return fmt.Errorf("unknown ack kind %q", a.Kind)
	}
	return err
}

func (s *Sync) refFor(ctx context.Context, id string) (string, error) {
	entries, err := s.Backend.ListTasks(ctx)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.ID == id {
			return e.Ref, nil
		}
	}
	return "", fmt.Errorf("the store has no entry for %s", id)
}

// republish retries every task whose store copy is stale: after a failed publish,
// or a change made by a process that could not reach the store.
func (s *Sync) republish(ctx context.Context, skip map[string]bool) {
	issues, err := s.Issues.List(ctx)
	if err != nil {
		s.logf("list issues: %v", err)
		return
	}
	seen := map[string]bool{}
	recs, err := s.Tasks.List(ctx)
	if err != nil {
		s.logf("list tasks: %v", err)
		return
	}
	for _, r := range recs {
		seen[TaskID(r)] = true
		if skip[ForemanID(r.Key)] {
			continue
		}
		if err := s.Publish(ctx, r.Key); err != nil {
			s.logf("%v", err)
		}
	}
	for _, i := range issues {
		if !seen[i.ID] && !skip[ForemanID(localissues.Key(i.ID, ""))] {
			if err := s.Publish(ctx, localissues.Key(i.ID, "")); err != nil {
				s.logf("%v", err)
			}
		}
	}
}

// pullKnowledge adopts store entries that changed, reports ones that do not parse,
// and publishes repositories the store lacks or holds an older copy of.
func (s *Sync) pullKnowledge(ctx context.Context) error {
	entries, err := s.Backend.ListKnowledge(ctx)
	if err != nil {
		return err
	}
	inStore := map[string]bool{}
	for _, e := range entries {
		inStore[e.Repo] = true
		prev, err := s.row("knowledge", e.Repo)
		if err != nil {
			return err
		}
		if errors.Is(e.ParseErr, ErrNoPayload) {
			// The page exists but a publish never finished writing it. Nobody edited
			// it, so publish again instead of pausing.
			if prev.AnswerDigest == pausedMark {
				prev.AnswerDigest = ""
				if err := s.saveRow("knowledge", e.Repo, prev); err != nil {
					return err
				}
			}
			if err := s.PublishKnowledge(ctx, e.Repo); err != nil {
				s.logf("%v", err)
			}
			continue
		}
		if e.ParseErr != nil || e.Snapshot == nil {
			s.logf("the knowledge entry of %s does not parse and is left alone until it is fixed: %v", e.Repo, e.ParseErr)
			prev.AnswerDigest = pausedMark
			if err := s.saveRow("knowledge", e.Repo, prev); err != nil {
				return err
			}
			continue
		}
		if prev.AnswerDigest == pausedMark {
			prev.AnswerDigest = ""
			if err := s.saveRow("knowledge", e.Repo, prev); err != nil {
				return err
			}
		}
		if e.Digest != prev.Digest {
			if s.Knowledge.Busy(e.Repo) {
				continue // a learner is writing; try again next refresh
			}
			if err := s.adoptKnowledge(ctx, e); err != nil {
				s.logf("adopt the knowledge of %s: %v", e.Repo, err)
			}
			continue
		}
		if err := s.PublishKnowledge(ctx, e.Repo); err != nil {
			s.logf("%v", err)
		}
	}
	repos, err := s.Knowledge.Repos()
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if !inStore[repo] {
			if err := s.PublishKnowledge(ctx, repo); err != nil {
				s.logf("%v", err)
			}
		}
	}
	return nil
}

// adoptKnowledge replaces the host's knowledge with the store's entry. The listing
// it came from may be older than a publish that has since finished, so under the
// publish lock it reads the entry and the host's row again and adopts only a
// snapshot that is still what the store holds and still differs from the host's.
func (s *Sync) adoptKnowledge(ctx context.Context, e KnowledgeEntry) error {
	unlock, err := s.lock(ctx, "publish:knowledge:"+e.Repo)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := s.Backend.ListKnowledge(ctx)
	if err != nil {
		return err
	}
	var cur *KnowledgeEntry
	for i := range entries {
		if entries[i].Repo == e.Repo {
			cur = &entries[i]
		}
	}
	if cur == nil || cur.ParseErr != nil || cur.Snapshot == nil || cur.Digest != e.Digest {
		return nil // the entry changed since the listing; the next pull decides
	}
	prev, err := s.row("knowledge", e.Repo)
	if err != nil {
		return err
	}
	if prev.Digest == cur.Digest || prev.AnswerDigest == pausedMark {
		return nil
	}
	// A learner that started after the pull's Busy check holds its lock until its
	// publish, which waits for the lock held here. Replacing now would drop what it
	// saved, so take its lock and leave the entry to the next pull if it is held.
	free, err := s.Knowledge.Lock(e.Repo)
	if errors.Is(err, knowledge.ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	defer free()
	if err := s.Knowledge.Replace(e.Repo, *cur.Snapshot); err != nil {
		return err
	}
	prev.Digest = cur.Digest
	return s.saveRow("knowledge", e.Repo, prev)
}

var _ = knowledge.FormatVersion
