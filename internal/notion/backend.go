package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/task"
	"github.com/brymartinez/foreman/internal/workspace"
)

const (
	tasksTitle     = "Tasks"
	knowledgeTitle = "Repository Knowledge"
	checkForemanID = "FOREMAN-CHECK"
	// cacheSettle is how long after a page's last edit a cached body is trusted.
	// Notion rounds edit times to the minute, so a fresher edit could hide behind
	// an equal timestamp.
	cacheSettle = 2 * time.Minute
)

type dbRef struct{ id, source string }

type cached struct {
	edited string
	at     time.Time
	body   pageBody
}

// Backend stores the workspace in two Notion databases under one parent page.
type Backend struct {
	C        *Client
	ParentID string
	Now      func() time.Time

	mu        sync.Mutex
	ensured   bool
	tasks     dbRef
	knowledge dbRef
	cache     map[string]cached
}

var _ workspace.Backend = (*Backend)(nil)

func (b *Backend) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

// forget drops what the backend cached, as a new process would have.
func (b *Backend) forget() {
	b.mu.Lock()
	b.cache = nil
	b.mu.Unlock()
}

func spec(typ string) map[string]any { return map[string]any{typ: map[string]any{}} }

func tasksSchema() map[string]map[string]any {
	return map[string]map[string]any{
		"Name": spec("title"), "Foreman ID": spec("rich_text"), "Status": spec("select"),
		"Source":     {"select": map[string]any{"options": []any{map[string]any{"name": "GitHub"}, map[string]any{"name": "Local"}}}},
		"Repository": spec("rich_text"), "Source URL": spec("url"), "Tags": spec("multi_select"),
		"Confirmed triage": spec("rich_text"), "Scope confirmed": spec("checkbox"), "Queue": spec("checkbox"),
		"Question": spec("rich_text"), "Answer": spec("rich_text"), "Branch": spec("rich_text"),
		"PR": spec("url"), "Commit": spec("rich_text"), "Updated by Foreman": spec("date"),
	}
}

func knowledgeSchema() map[string]map[string]any {
	return map[string]map[string]any{"Repository": spec("title"), "Active learnings": spec("number"), "Updated by Foreman": spec("date")}
}

// Ensure finds the two databases by title, creates the missing ones, and adds
// missing fields. It never deletes a field, a page, or a database.
func (b *Backend) Ensure(ctx context.Context) error {
	kids, err := b.C.blocksOf(ctx, b.ParentID)
	if err != nil {
		return err
	}
	found := map[string]string{}
	for _, k := range kids {
		if k.Type == "child_database" {
			if _, dup := found[k.text()]; !dup {
				found[k.text()] = k.ID
			}
		}
	}
	ensureOne := func(title string, schema map[string]map[string]any) (dbRef, error) {
		id := found[title]
		var db struct {
			ID          string `json:"id"`
			DataSources []struct {
				ID string `json:"id"`
			} `json:"data_sources"`
		}
		if id == "" {
			props := map[string]any{}
			for k, v := range schema {
				props[k] = v
			}
			err := b.C.do(ctx, "POST", "/v1/databases", map[string]any{
				"parent":              map[string]any{"type": "page_id", "page_id": b.ParentID},
				"title":               richText(title),
				"initial_data_source": map[string]any{"properties": props},
			}, &db)
			if err != nil {
				return dbRef{}, err
			}
		} else if err := b.C.do(ctx, "GET", "/v1/databases/"+id, nil, &db); err != nil {
			return dbRef{}, err
		}
		if len(db.DataSources) == 0 {
			return dbRef{}, fmt.Errorf("notion: database %s has no data source", title)
		}
		ref := dbRef{id: db.ID, source: db.DataSources[0].ID}
		var src struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := b.C.do(ctx, "GET", "/v1/data_sources/"+ref.source, nil, &src); err != nil {
			return dbRef{}, err
		}
		missing := map[string]any{}
		for name, s := range schema {
			if _, ok := src.Properties[name]; !ok {
				missing[name] = s
			}
		}
		if len(missing) > 0 {
			if err := b.C.do(ctx, "PATCH", "/v1/data_sources/"+ref.source, map[string]any{"properties": missing}, nil); err != nil {
				return dbRef{}, err
			}
		}
		return ref, nil
	}
	tasks, err := ensureOne(tasksTitle, tasksSchema())
	if err != nil {
		return err
	}
	know, err := ensureOne(knowledgeTitle, knowledgeSchema())
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.tasks, b.knowledge, b.ensured = tasks, know, true
	b.mu.Unlock()
	return nil
}

func (b *Backend) ready(ctx context.Context) error {
	b.mu.Lock()
	ok := b.ensured
	b.mu.Unlock()
	if ok {
		return nil
	}
	return b.Ensure(ctx)
}

type propObj struct {
	Title       []richItem              `json:"title"`
	RichText    []richItem              `json:"rich_text"`
	Select      *struct{ Name string }  `json:"select"`
	MultiSelect []struct{ Name string } `json:"multi_select"`
	Checkbox    bool                    `json:"checkbox"`
	URL         *string                 `json:"url"`
	Number      *float64                `json:"number"`
}

type pageObj struct {
	ID         string             `json:"id"`
	Created    string             `json:"created_time"`
	Edited     string             `json:"last_edited_time"`
	InTrash    bool               `json:"in_trash"`
	Properties map[string]propObj `json:"properties"`
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (b *Backend) queryPages(ctx context.Context, source string, filter map[string]any) ([]pageObj, error) {
	body := map[string]any{"sorts": []any{map[string]any{"timestamp": "created_time", "direction": "ascending"}}}
	if filter != nil {
		body["filter"] = filter
	}
	var out []pageObj
	err := b.C.listAll(ctx, "POST", "/v1/data_sources/"+source+"/query", body, func(r json.RawMessage) error {
		var p pageObj
		if err := json.Unmarshal(r, &p); err != nil {
			return err
		}
		if !p.InTrash {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

func (b *Backend) body(ctx context.Context, p pageObj) (pageBody, error) {
	b.mu.Lock()
	c, ok := b.cache[p.ID]
	b.mu.Unlock()
	if ok && c.edited == p.Edited && c.at.After(parseTime(p.Edited).Add(cacheSettle)) {
		return c.body, nil
	}
	started := time.Now()
	body, err := b.C.readBody(ctx, p.ID)
	if err != nil {
		return pageBody{}, err
	}
	b.mu.Lock()
	if b.cache == nil {
		b.cache = map[string]cached{}
	}
	b.cache[p.ID] = cached{edited: p.Edited, at: started, body: body}
	b.mu.Unlock()
	return body, nil
}

func (b *Backend) invalidate(page string) {
	b.mu.Lock()
	delete(b.cache, page)
	b.mu.Unlock()
}

func taskFields(p pageObj) workspace.TaskEntry {
	pr := p.Properties
	e := workspace.TaskEntry{
		Ref: p.ID, ID: strings.TrimSpace(plainOf(pr["Foreman ID"].RichText)),
		CreatedAt: parseTime(p.Created), EditedAt: parseTime(p.Edited),
		Name: plainOf(pr["Name"].Title), Repository: plainOf(pr["Repository"].RichText),
		ConfirmedTriage: plainOf(pr["Confirmed triage"].RichText), ScopeConfirmed: pr["Scope confirmed"].Checkbox,
		Queue: pr["Queue"].Checkbox, Answer: plainOf(pr["Answer"].RichText),
	}
	if u := pr["Source URL"].URL; u != nil {
		e.SourceURL = *u
	}
	for _, t := range pr["Tags"].MultiSelect {
		e.Tags = append(e.Tags, t.Name)
	}
	if question, ok := strings.CutPrefix(plainOf(pr["Question"].RichText), "["); ok {
		e.ReplyCode, _, _ = strings.Cut(question, "] ")
	}
	return e
}

func (b *Backend) entry(ctx context.Context, p pageObj) (workspace.TaskEntry, error) {
	e := taskFields(p)
	body, err := b.body(ctx, p)
	if err != nil {
		return e, err
	}
	e.Description = body.Description
	var rec workspace.Record
	if i, perr := selectPayload(body.Payloads, func(text string) error {
		r, err := workspace.Parse([]byte(text))
		rec = r
		return err
	}); i >= 0 {
		e.Record, e.ParseErr = &rec, nil
	} else {
		e.ParseErr = perr
	}
	return e, nil
}

func (b *Backend) CodedAnswers() bool { return true }

func (b *Backend) TaskFields(ctx context.Context, id string) (*workspace.TaskEntry, error) {
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	page, err := b.findByID(ctx, b.tasks.source, "Foreman ID", id, "rich_text")
	if err != nil || page == nil {
		return nil, err
	}
	fields := taskFields(*page)
	return &fields, nil
}

func (b *Backend) ListTasks(ctx context.Context) ([]workspace.TaskEntry, error) {
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	pages, err := b.queryPages(ctx, b.tasks.source, nil)
	if err != nil {
		return nil, err
	}
	out := make([]workspace.TaskEntry, 0, len(pages))
	for _, p := range pages {
		e, err := b.entry(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func statusLabelOf(r workspace.Record) string { return r.StatusLabel() }

func sourceLabel(s task.Source) string {
	if s == task.GitHub {
		return "GitHub"
	}
	return "Local"
}

func rt(s string) map[string]any {
	if len([]rune(s)) > maxRichText {
		s = string([]rune(s)[:maxRichText])
	}
	return map[string]any{"rich_text": richText(s)}
}

// rtLong holds text of any length a property can take: items of 2000 characters,
// at most 100 of them. Confirmed triage is read back as the user's edit, so cutting
// it to one item would turn the cut text into a "change".
func rtLong(s string) map[string]any {
	items := richText(s)
	if len(items) > maxCodeItems {
		items = items[:maxCodeItems]
	}
	return map[string]any{"rich_text": items}
}

func foremanProps(r workspace.Record, replyCode string, at time.Time) map[string]any {
	question := r.QuestionText()
	if replyCode != "" && question != "" {
		question = "[" + replyCode + "] " + question
	}
	props := map[string]any{
		"Foreman ID": rt(r.ID), "Status": map[string]any{"select": map[string]any{"name": statusLabelOf(r)}},
		"Source":   map[string]any{"select": map[string]any{"name": sourceLabel(r.Source)}},
		"Question": rt(question), "Commit": rt(r.Commit()), "Branch": rt(""), "PR": map[string]any{"url": nil},
		"Updated by Foreman": map[string]any{"date": map[string]any{"start": at.Format(time.RFC3339)}},
	}
	if w := r.Work; w != nil {
		props["Branch"] = rt(w.Branch)
		if w.PR != nil && w.PR.URL != "" {
			props["PR"] = map[string]any{"url": w.PR.URL}
		}
	}
	return props
}

func (b *Backend) findByID(ctx context.Context, source, prop, id string, kind string) (*pageObj, error) {
	pages, err := b.queryPages(ctx, source, map[string]any{"property": prop, kind: map[string]any{"equals": id}})
	if err != nil || len(pages) == 0 {
		return nil, err
	}
	return &pages[0], nil // the query returns oldest first
}

func (b *Backend) UpsertTask(ctx context.Context, p workspace.TaskPublish) (string, error) {
	if err := b.ready(ctx); err != nil {
		return "", err
	}
	payload, err := p.Record.Encode()
	if err != nil {
		return "", err
	}
	text := p.Record.RenderText()
	if p.Reason != "" {
		text = "Queued but not started: " + p.Reason + "\n\n" + text
	}
	page, err := b.findByID(ctx, b.tasks.source, "Foreman ID", p.Record.ID, "rich_text")
	if err != nil {
		return "", err
	}
	var ref string
	if page == nil {
		s := p.Create
		props := foremanProps(p.Record, p.ReplyCode, b.now())
		props["Name"] = map[string]any{"title": richText(s.Name)}
		props["Repository"] = rt(s.Repository)
		props["Confirmed triage"] = rtLong(strings.TrimSpace(s.ConfirmedTriage))
		props["Scope confirmed"] = map[string]any{"checkbox": s.ScopeConfirmed}
		if s.SourceURL != "" {
			props["Source URL"] = map[string]any{"url": s.SourceURL}
		}
		tags := []any{}
		for _, t := range s.Tags {
			tags = append(tags, map[string]any{"name": t})
		}
		props["Tags"] = map[string]any{"multi_select": tags}
		var desc []any
		if strings.TrimSpace(s.Description) != "" {
			desc = paragraphs(s.Description)
		}
		first := desc[:takeBlocks(desc)]
		body := map[string]any{"parent": map[string]any{"type": "data_source_id", "data_source_id": b.tasks.source}, "properties": props}
		if len(first) > 0 {
			body["children"] = first
		}
		var created struct{ ID string }
		if err := b.C.do(ctx, "POST", "/v1/pages", body, &created); err != nil {
			return "", err
		}
		ref = created.ID
		if len(desc) > len(first) {
			if _, err := b.C.appendBlocks(ctx, ref, desc[len(first):]); err != nil {
				return ref, err
			}
		}
	} else {
		ref = page.ID
		props := foremanProps(p.Record, p.ReplyCode, b.now())
		if p.Reseed {
			props["Confirmed triage"] = rtLong(strings.TrimSpace(p.Create.ConfirmedTriage))
			props["Scope confirmed"] = map[string]any{"checkbox": p.Create.ScopeConfirmed}
		}
		if err := b.C.do(ctx, "PATCH", "/v1/pages/"+ref, map[string]any{"properties": props}, nil); err != nil {
			return ref, err
		}
		if p.Resume {
			if err := b.completeDescription(ctx, ref, p.Create.Description); err != nil {
				return ref, err
			}
		}
	}
	b.invalidate(ref)
	return ref, b.C.replaceSection(ctx, ref, text, string(payload))
}

// completeDescription appends the rest of a description whose first batch of
// blocks was all a failed publish managed to write. It touches the description only
// when what the page holds is the start of what it should hold.
func (b *Backend) completeDescription(ctx context.Context, ref, want string) error {
	if strings.TrimSpace(want) == "" {
		return nil
	}
	// Cut it the way creation did, so the blocks already on the page line up with
	// paragraphs(want), blank lines at the start included.
	want = strings.TrimRight(want, "\n")
	body, err := b.C.readBody(ctx, ref)
	if err != nil {
		return err
	}
	have := strings.TrimRight(body.Description, "\n")
	if have == want || !strings.HasPrefix(want, have) {
		return nil
	}
	// The blocks already on the page are the first ones paragraphs(want) makes, since
	// they came from a prefix of it, so the rest are the ones after them. That keeps
	// a line that was cut in the middle continuing as one line.
	all := paragraphs(want)
	if body.DescBlocks >= len(all) {
		return nil
	}
	b.invalidate(ref)
	_, err = b.C.appendBlocks(ctx, ref, all[body.DescBlocks:])
	return err
}

func (b *Backend) patch(ctx context.Context, ref string, props map[string]any) error {
	return b.C.do(ctx, "PATCH", "/v1/pages/"+ref, map[string]any{"properties": props}, nil)
}

func (b *Backend) AssignID(ctx context.Context, ref, id string) error {
	return b.patch(ctx, ref, map[string]any{"Foreman ID": rt(id)})
}

func (b *Backend) ClearQueue(ctx context.Context, ref string) error {
	return b.patch(ctx, ref, map[string]any{"Queue": map[string]any{"checkbox": false}})
}

func (b *Backend) ClearAnswer(ctx context.Context, ref string) error {
	return b.patch(ctx, ref, map[string]any{"Answer": map[string]any{"rich_text": []any{}}})
}

func (b *Backend) Trash(ctx context.Context, ref string) error {
	b.invalidate(ref)
	return b.C.do(ctx, "PATCH", "/v1/pages/"+ref, map[string]any{"in_trash": true}, nil)
}

func (b *Backend) Duplicates(ctx context.Context) ([]workspace.Duplicate, error) {
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	pages, err := b.queryPages(ctx, b.tasks.source, nil)
	if err != nil {
		return nil, err
	}
	by := map[string][]string{}
	var order []string
	for _, p := range pages {
		id := strings.TrimSpace(plainOf(p.Properties["Foreman ID"].RichText))
		if id == "" {
			continue
		}
		if _, ok := by[id]; !ok {
			order = append(order, id)
		}
		by[id] = append(by[id], p.ID)
	}
	var out []workspace.Duplicate
	for _, id := range order {
		if len(by[id]) > 1 {
			out = append(out, workspace.Duplicate{ID: id, Refs: by[id]})
		}
	}
	return out, nil
}

func (b *Backend) ListKnowledge(ctx context.Context) ([]workspace.KnowledgeEntry, error) {
	return b.listKnowledge(ctx, nil)
}

// KnowledgeEntriesFor reads one repository directly because approval must see
// the current payload even when the page's edit timestamp is unchanged.
func (b *Backend) KnowledgeEntriesFor(ctx context.Context, repo string) ([]workspace.KnowledgeEntry, error) {
	return b.listKnowledge(ctx, map[string]any{"property": "Repository", "title": map[string]any{"equals": repo}})
}

func (b *Backend) listKnowledge(ctx context.Context, filter map[string]any) ([]workspace.KnowledgeEntry, error) {
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	pages, err := b.queryPages(ctx, b.knowledge.source, filter)
	if err != nil {
		return nil, err
	}
	var out []workspace.KnowledgeEntry
	for _, p := range pages {
		e := workspace.KnowledgeEntry{Repo: strings.TrimSpace(plainOf(p.Properties["Repository"].Title)), Ref: p.ID, EditedAt: parseTime(p.Edited)}
		var body pageBody
		if filter != nil {
			body, err = b.C.readBody(ctx, p.ID)
		} else {
			body, err = b.body(ctx, p)
		}
		if err != nil {
			return nil, err
		}
		e.ParseErr = workspace.ErrNoPayload
		var snap knowledge.Snapshot
		if i, perr := selectPayload(body.Payloads, func(text string) error {
			x, err := knowledge.Parse([]byte(text))
			snap = x
			return err
		}); i >= 0 {
			e.Snapshot, e.ParseErr, e.Digest = &snap, nil, workspace.SnapshotDigest(snap)
		} else if perr != nil {
			e.ParseErr = perr
		}
		out = append(out, e)
	}
	return out, nil
}

func (b *Backend) WriteKnowledge(ctx context.Context, p workspace.KnowledgePublish) error {
	if err := b.ready(ctx); err != nil {
		return err
	}
	raw, err := knowledge.Format(p.Snapshot)
	if err != nil {
		return err
	}
	active := 0
	for _, l := range p.Snapshot.Learnings {
		if l.Status == knowledge.Active {
			active++
		}
	}
	props := map[string]any{
		"Active learnings":   map[string]any{"number": active},
		"Updated by Foreman": map[string]any{"date": map[string]any{"start": b.now().Format(time.RFC3339)}},
	}
	page, err := b.findByID(ctx, b.knowledge.source, "Repository", p.Repo, "title")
	if err != nil {
		return err
	}
	var ref string
	if page == nil {
		props["Repository"] = map[string]any{"title": richText(p.Repo)}
		var created struct{ ID string }
		err := b.C.do(ctx, "POST", "/v1/pages", map[string]any{"parent": map[string]any{"type": "data_source_id", "data_source_id": b.knowledge.source}, "properties": props}, &created)
		if err != nil {
			return err
		}
		ref = created.ID
	} else {
		ref = page.ID
		if err := b.patch(ctx, ref, props); err != nil {
			return err
		}
	}
	b.invalidate(ref)
	return b.C.replaceSection(ctx, ref, p.Guidance, string(raw))
}

// Check runs the connectivity check of R10 and stops at the first failure.
func (b *Backend) Check(ctx context.Context, report func(workspace.Step)) error {
	var testID string
	step := func(name string, fn func() error) error {
		err := fn()
		report(workspace.Step{Name: name, Err: err})
		if err != nil {
			if testID != "" { // best effort: leave no test task behind
				_ = b.C.do(ctx, "PATCH", "/v1/pages/"+testID, map[string]any{"in_trash": true}, nil)
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	const marker = "check-updated"
	steps := []struct {
		name string
		fn   func() error
	}{
		{"authenticate", func() error { return b.C.do(ctx, "GET", "/v1/users/me", nil, nil) }},
		{"ensure databases", func() error { return b.Ensure(ctx) }},
		{"read Tasks data source", func() error { return b.C.do(ctx, "GET", "/v1/data_sources/"+b.tasks.source, nil, nil) }},
		{"create test task", func() error {
			var created struct{ ID string }
			err := b.C.do(ctx, "POST", "/v1/pages", map[string]any{
				"parent": map[string]any{"type": "data_source_id", "data_source_id": b.tasks.source},
				"properties": map[string]any{
					"Name":       map[string]any{"title": richText("Foreman connectivity check " + b.now().Format("2006-01-02T15:04:05Z"))},
					"Foreman ID": rt(checkForemanID),
				}}, &created)
			testID = created.ID
			return err
		}},
		{"update test task", func() error { return b.patch(ctx, testID, map[string]any{"Branch": rt(marker)}) }},
		{"read test task back", func() error { return b.C.do(ctx, "GET", "/v1/pages/"+testID, nil, nil) }},
		{"confirm update is visible", func() error {
			var p pageObj
			if err := b.C.do(ctx, "GET", "/v1/pages/"+testID, nil, &p); err != nil {
				return err
			}
			if plainOf(p.Properties["Branch"].RichText) != marker {
				return errors.New("the update is not visible")
			}
			return nil
		}},
		{"trash test task", func() error {
			err := b.C.do(ctx, "PATCH", "/v1/pages/"+testID, map[string]any{"in_trash": true}, nil)
			if err == nil {
				testID = ""
			}
			return err
		}},
	}
	for _, s := range steps {
		if err := step(s.name, s.fn); err != nil {
			return err
		}
	}
	return nil
}

// selectPayload picks the section to read: the newest complete one whose payload
// parses. Only when none exists does it fall back to the newest unmarked one that
// parses, which is a section written before the end marker. A cut-short section
// can parse too, because the formats accept an early end, so it must never win over
// a complete one. It returns -1 and the newest complete section's error when nothing
// parses; a cut-short section's error is not reported, which keeps a half written
// page reading as "no payload".
func selectPayload(ps []sectionPayload, parse func(string) error) (int, error) {
	var first error
	for i := len(ps) - 1; i >= 0; i-- {
		if !ps[i].Complete {
			continue
		}
		if err := parse(ps[i].Text); err == nil {
			return i, nil
		} else if first == nil {
			first = err
		}
	}
	for i := len(ps) - 1; i >= 0; i-- {
		if !ps[i].Complete && parse(ps[i].Text) == nil {
			return i, nil
		}
	}
	return -1, first
}
