package workspace

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is an in-process Backend. Tests use it for the sync service, and it
// defines the behavior the real backends must match.
type Memory struct {
	mu      sync.Mutex
	Now     func() time.Time
	tasks   []*TaskEntry
	know    map[string]*KnowledgeEntry
	seq     int
	Fail    error // returned by every write while set
	Writes  int
	Trashed []string
	// FailOnWrite makes the nth write (1-based) fail once, to model a crash partway.
	FailOnWrite int
	// Mangle changes an entry right after it was written, to model a store that
	// does not keep what it was given.
	Mangle func(*TaskEntry)
	// PartialOnWrite makes the nth write (1-based) create its entry with only the
	// first half of the description and then fail, as an append that did not land.
	PartialOnWrite int
	// LoseRefOnWrite is PartialOnWrite for a create whose response never arrives, so
	// the caller learns no reference.
	LoseRefOnWrite int
}

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Memory) find(ref string) *TaskEntry {
	for _, e := range m.tasks {
		if e.Ref == ref {
			return e
		}
	}
	return nil
}

func (m *Memory) Ensure(context.Context) error { return m.Fail }

func (m *Memory) Check(_ context.Context, report func(Step)) error {
	if m.Fail != nil {
		report(Step{Name: "authenticate", Err: m.Fail})
		return m.Fail
	}
	report(Step{Name: "authenticate"})
	return nil
}

func (m *Memory) ListTasks(context.Context) ([]TaskEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TaskEntry, len(m.tasks))
	for i, e := range m.tasks {
		out[i] = *e
	}
	return out, nil
}

// UserCreate adds an entry the way a person would. A non-empty e.ID makes it a
// pre-existing Foreman entry.
func (m *Memory) UserCreate(e TaskEntry) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	e.Ref = fmt.Sprintf("mem-%d", m.seq)
	e.CreatedAt, e.EditedAt = m.now(), m.now()
	m.tasks = append(m.tasks, &e)
	return e.Ref
}

// UserEdit changes an entry the way a person would, bumping its edit time.
func (m *Memory) UserEdit(ref string, fn func(*TaskEntry)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.find(ref); e != nil {
		fn(e)
		e.EditedAt = m.now()
	}
}

func (m *Memory) UpsertTask(_ context.Context, p TaskPublish) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return "", m.Fail
	}
	m.Writes++
	if m.FailOnWrite == m.Writes {
		return "", errors.New("store dropped the connection")
	}
	var e *TaskEntry
	for _, c := range m.tasks {
		if c.ID == p.Record.ID && (e == nil || c.CreatedAt.Before(e.CreatedAt)) {
			e = c
		}
	}
	if e == nil {
		m.seq++
		s := p.Create
		e = &TaskEntry{Ref: fmt.Sprintf("mem-%d", m.seq), ID: p.Record.ID, CreatedAt: m.now(), Name: s.Name, Repository: s.Repository,
			SourceURL: s.SourceURL, Tags: s.Tags, Description: s.Description, ConfirmedTriage: s.ConfirmedTriage, ScopeConfirmed: s.ScopeConfirmed}
		m.tasks = append(m.tasks, e)
	}
	if p.Resume && e.Description != "" {
		if want := strings.TrimSpace(p.Create.Description); want != e.Description && strings.HasPrefix(want, e.Description) {
			e.Description = want
		}
	}
	if p.Reseed {
		e.ConfirmedTriage, e.ScopeConfirmed = p.Create.ConfirmedTriage, p.Create.ScopeConfirmed
	}
	if m.LoseRefOnWrite == m.Writes {
		e.Description = e.Description[:len(e.Description)/2]
		return "", errors.New("the response to the create was lost")
	}
	if m.PartialOnWrite == m.Writes {
		e.Description = e.Description[:len(e.Description)/2]
		return e.Ref, errors.New("store dropped the connection after creating the entry")
	}
	rec := p.Record
	e.Record, e.ParseErr, e.EditedAt = &rec, nil, m.now()
	if m.Mangle != nil {
		m.Mangle(e)
	}
	return e.Ref, nil
}

func (m *Memory) AssignID(_ context.Context, ref, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	e := m.find(ref)
	if e == nil {
		return errors.New("no such entry")
	}
	e.ID, e.EditedAt = id, m.now()
	return nil
}

func (m *Memory) clear(ref string, fn func(*TaskEntry)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	e := m.find(ref)
	if e == nil {
		return errors.New("no such entry")
	}
	fn(e)
	return nil
}

// Clearing a field is Foreman's write, so it does not count as a user edit time.
func (m *Memory) ClearQueue(_ context.Context, ref string) error {
	return m.clear(ref, func(e *TaskEntry) { e.Queue = false })
}

func (m *Memory) ClearAnswer(_ context.Context, ref string) error {
	return m.clear(ref, func(e *TaskEntry) { e.Answer = "" })
}

func (m *Memory) Duplicates(context.Context) ([]Duplicate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	by := map[string][]*TaskEntry{}
	for _, e := range m.tasks {
		if e.ID != "" {
			by[e.ID] = append(by[e.ID], e)
		}
	}
	var out []Duplicate
	for id, es := range by {
		if len(es) < 2 {
			continue
		}
		sort.SliceStable(es, func(i, j int) bool { return es[i].CreatedAt.Before(es[j].CreatedAt) })
		d := Duplicate{ID: id}
		for _, e := range es {
			d.Refs = append(d.Refs, e.Ref)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) Trash(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	for i, e := range m.tasks {
		if e.Ref == ref {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			m.Trashed = append(m.Trashed, ref)
			return nil
		}
	}
	return errors.New("no such entry")
}

func (m *Memory) ListKnowledge(context.Context) ([]KnowledgeEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []KnowledgeEntry
	for _, e := range m.know {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

func (m *Memory) WriteKnowledge(_ context.Context, p KnowledgePublish) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	m.Writes++
	if m.FailOnWrite == m.Writes {
		return errors.New("store dropped the connection")
	}
	if m.know == nil {
		m.know = map[string]*KnowledgeEntry{}
	}
	snap := p.Snapshot
	m.know[p.Repo] = &KnowledgeEntry{Repo: p.Repo, Ref: "know-" + p.Repo, EditedAt: m.now(), Snapshot: &snap, Digest: SnapshotDigest(snap)}
	return nil
}

// UserCorruptKnowledge replaces a repository's entry with one that does not parse.
func (m *Memory) UserCorruptKnowledge(repo string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.know == nil {
		m.know = map[string]*KnowledgeEntry{}
	}
	m.know[repo] = &KnowledgeEntry{Repo: repo, Ref: "know-" + repo, EditedAt: m.now(), ParseErr: err}
}
