// Package obsidian is the Obsidian vault implementation of workspace.Backend. Tasks
// are Markdown notes under <vault>/<folder>/Tasks, and repository knowledge is one
// note per repository. Foreman writes only the keys and the region it owns.
package obsidian

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/workspace"
)

const (
	tasksDir     = "Tasks"
	knowledgeDir = "Repository Knowledge"
	checkID      = "FOREMAN-CHECK"
	maxAttempts  = 5
)

// Backend keeps the workspace in a vault folder.
type Backend struct {
	Vault  string
	Folder string
	Now    func() time.Time

	mu sync.Mutex
	// beforeCommit runs after Foreman computed a note and before it checks the file
	// is unchanged; tests use it to edit the note at the worst moment.
	beforeCommit func(path string)
}

var _ workspace.Backend = (*Backend)(nil)

func (b *Backend) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *Backend) root() string  { return filepath.Join(b.Vault, b.Folder) }
func (b *Backend) tasks() string { return filepath.Join(b.root(), tasksDir) }

func (b *Backend) Ensure(context.Context) error {
	if info, err := os.Stat(b.Vault); err != nil || !info.IsDir() {
		return fmt.Errorf("vault %s is not an existing directory", b.Vault)
	}
	for _, d := range []string{b.tasks(), filepath.Join(b.root(), knowledgeDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

const maxNameBytes = 120

// sanitizeName replaces the characters Obsidian cannot use in a file name. Notes
// written before names were capped carry its uncapped result.
func sanitizeName(s string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-", "#", "-", "^", "-", "[", "-", "]", "-", "\n", " ")
	return strings.Trim(strings.TrimSpace(r.Replace(s)), ". ")
}

// safeName makes a note file name from a title.
func safeName(s string) string {
	s = sanitizeName(s)
	if s == "" {
		return "Untitled"
	}
	// File systems cap a name in bytes, not characters: 255 on most. A title and an
	// ID suffix at 120 bytes each, plus the extension, stay under it.
	if len(s) > maxNameBytes {
		cut := maxNameBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimRight(s[:cut], ". ")
		if s == "" {
			return "Untitled"
		}
	}
	return s
}

// edit reads a note, applies fn, and writes the result through a temporary file and
// a rename. If the note changed since it was read, edit reads it again and
// retries, so a concurrent edit is merged instead of overwritten.
func (b *Backend) edit(path string, fn func(n *note) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		orig, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n, err := parseNote(orig)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if err := fn(n); err != nil {
			return err
		}
		out, err := n.bytes()
		if err != nil {
			return err
		}
		if b.beforeCommit != nil {
			b.beforeCommit(path)
		}
		tmp, err := writeTemp(filepath.Dir(path), out)
		if err != nil {
			return err
		}
		cur, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(cur, orig) {
			os.Remove(tmp)
			if err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
	return fmt.Errorf("%s kept changing while Foreman wrote it", filepath.Base(path))
}

func writeTemp(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".foreman-*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// create writes a new note without replacing an existing one.
func create(path string, data []byte) error {
	tmp, err := writeTemp(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Link(tmp, path)
}

type found struct {
	path    string
	n       *note
	created time.Time
	edited  time.Time
	err     error
}

func (b *Backend) scan() ([]found, error) {
	if err := b.Ensure(context.Background()); err != nil {
		return nil, err
	}
	files, err := os.ReadDir(b.tasks())
	if err != nil {
		return nil, err
	}
	var out []found
	for _, f := range files {
		if f.IsDir() || strings.HasPrefix(f.Name(), ".") || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		path := filepath.Join(b.tasks(), f.Name())
		info, err := f.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		n, perr := parseNote(raw)
		fd := found{path: path, n: n, edited: info.ModTime().UTC(), created: info.ModTime().UTC(), err: perr}
		if perr == nil {
			if t, err := time.Parse(time.RFC3339, n.str("foreman_created")); err == nil {
				fd.created = t
			}
		}
		out = append(out, fd)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].created.Before(out[j].created) })
	return out, nil
}

func entryOf(f found) workspace.TaskEntry {
	e := workspace.TaskEntry{Ref: f.path, CreatedAt: f.created, EditedAt: f.edited, Name: strings.TrimSuffix(filepath.Base(f.path), ".md")}
	if f.err != nil {
		e.ParseErr = f.err
		return e
	}
	n := f.n
	region, err := n.splitRegion()
	if err != nil {
		e.ParseErr = err
		return e
	}
	e.ID = n.str("foreman_id")
	// The file name is the title with characters Obsidian cannot use replaced, so a
	// note Foreman named reads back under its real title unless someone renamed it.
	if t := n.str("foreman_title"); t != "" {
		base := strings.TrimSuffix(filepath.Base(f.path), ".md")
		if st := sanitizeName(t); base == safeName(t) || e.ID != "" && base == safeName(t)+" "+safeName(e.ID) ||
			st != "" && (base == st || e.ID != "" && base == st+" "+sanitizeName(e.ID)) {
			e.Name = t
		}
	}
	e.Repository, e.SourceURL, e.Tags = n.str("repository"), n.str("source_url"), n.strs("tags")
	e.ScopeConfirmed, e.Queue, e.Answer = n.boolean("scope_confirmed"), n.boolean("queue"), n.str("answer")
	e.Description, e.ConfirmedTriage = userText(region.userBody())
	if region.present {
		if r, err := region.parseRecord(); err == nil {
			e.Record = &r
		} else {
			e.ParseErr = err
		}
	}
	return e
}

func (b *Backend) ListTasks(context.Context) ([]workspace.TaskEntry, error) {
	fs, err := b.scan()
	if err != nil {
		return nil, err
	}
	out := make([]workspace.TaskEntry, 0, len(fs))
	for _, f := range fs {
		out = append(out, entryOf(f))
	}
	return out, nil
}

func (b *Backend) setForeman(n *note, r workspace.Record) error {
	var branch, pr string
	if r.Work != nil {
		branch = r.Work.Branch
		if r.Work.PR != nil {
			pr = r.Work.PR.URL
		}
	}
	for _, kv := range []struct {
		k string
		v any
	}{{"foreman_id", r.ID}, {"foreman_status", r.StatusLabel()}, {"foreman_branch", branch}, {"foreman_pr", pr},
		{"foreman_question", r.QuestionText()}, {"foreman_updated", b.now().Format(time.RFC3339)}} {
		if err := n.set(kv.k, kv.v); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) UpsertTask(ctx context.Context, p workspace.TaskPublish) (string, error) {
	fs, err := b.scan()
	if err != nil {
		return "", err
	}
	content := p.Record.Render()
	if p.Reason != "" {
		content = "Queued but not started: " + p.Reason + "\n\n" + content
	}
	for _, f := range fs { // oldest first
		if f.err == nil && f.n.str("foreman_id") == p.Record.ID {
			return f.path, b.edit(f.path, func(n *note) error {
				region, err := n.splitRegion()
				if err != nil {
					return err
				}
				if p.Reseed {
					region, err = reseedTriage(n, region, p.Create)
					if err != nil {
						return err
					}
				}
				if err := n.setRegion(region, content); err != nil {
					return err
				}
				return b.setForeman(n, p.Record)
			})
		}
	}
	s := p.Create
	n := newNote()
	if err := b.setForeman(n, p.Record); err != nil {
		return "", err
	}
	_ = n.set("foreman_created", b.now().Format(time.RFC3339))
	title := s.Name
	if title == "" {
		title = p.Record.Title
	}
	_ = n.set("foreman_title", title) // the file name is a sanitized title
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	for _, kv := range []struct {
		k string
		v any
	}{{"repository", s.Repository}, {"source_url", s.SourceURL}, {"tags", tags}, {"scope_confirmed", s.ScopeConfirmed}, {"queue", false}, {"answer", ""}} {
		if err := n.set(kv.k, kv.v); err != nil {
			return "", err
		}
	}
	var body strings.Builder
	if strings.TrimSpace(s.Description) != "" {
		body.WriteString(strings.TrimSpace(s.Description) + "\n\n")
	}
	body.WriteString(triageHeading + "\n\n")
	if strings.TrimSpace(s.ConfirmedTriage) != "" {
		body.WriteString(strings.TrimSpace(s.ConfirmedTriage) + "\n\n")
	}
	if err := n.setRegion(noteRegion{before: body.String()}, content); err != nil {
		return "", err
	}
	out, err := n.bytes()
	if err != nil {
		return "", err
	}
	name := safeName(s.Name)
	if s.Name == "" {
		name = safeName(p.Record.Title)
	}
	path := filepath.Join(b.tasks(), name+".md")
	if err := create(path, out); errors.Is(err, os.ErrExist) {
		path = filepath.Join(b.tasks(), name+" "+safeName(p.Record.ID)+".md")
		err = create(path, out)
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return path, nil
}

func (b *Backend) AssignID(ctx context.Context, ref, id string) error {
	return b.edit(ref, func(n *note) error {
		if n.str("foreman_created") == "" {
			if info, err := os.Stat(ref); err == nil {
				_ = n.set("foreman_created", info.ModTime().UTC().Format(time.RFC3339))
			}
		}
		return n.set("foreman_id", id)
	})
}

func (b *Backend) ClearQueue(ctx context.Context, ref string) error {
	return b.edit(ref, func(n *note) error { return n.set("queue", false) })
}

func (b *Backend) ClearAnswer(ctx context.Context, ref string) error {
	return b.edit(ref, func(n *note) error { return n.set("answer", "") })
}

func (b *Backend) Duplicates(context.Context) ([]workspace.Duplicate, error) {
	fs, err := b.scan()
	if err != nil {
		return nil, err
	}
	by := map[string][]string{}
	var order []string
	for _, f := range fs {
		if f.err != nil || f.n.str("foreman_id") == "" {
			continue
		}
		id := f.n.str("foreman_id")
		if _, ok := by[id]; !ok {
			order = append(order, id)
		}
		by[id] = append(by[id], f.path)
	}
	var out []workspace.Duplicate
	for _, id := range order {
		if len(by[id]) > 1 {
			out = append(out, workspace.Duplicate{ID: id, Refs: by[id]})
		}
	}
	return out, nil
}

// Trash deletes a note. Foreman only trashes the copies of an entry it keeps, and
// its own test note.
func (b *Backend) Trash(ctx context.Context, ref string) error { return os.Remove(ref) }

func (b *Backend) knowledgePath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return filepath.Join(b.root(), knowledgeDir, knowledgeName(owner), knowledgeName(name)+".md")
}

// knowledgeName is a repository's owner or name as a file name. Unlike safeName it
// never maps two names to one file: a dot at either end (which a file system hides
// or drops) and the percent sign itself are written as %XX, and unknowledgeName
// reverses that.
func knowledgeName(s string) string {
	var out strings.Builder
	last := len(s) - 1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c == '/' || c == '\\' || ((c == '.' || c == ' ') && (i == 0 || i == last)) {
			fmt.Fprintf(&out, "%%%02X", c)
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func unknowledgeName(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func (b *Backend) WriteKnowledge(ctx context.Context, p workspace.KnowledgePublish) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	raw, err := knowledge.Format(p.Snapshot)
	if err != nil {
		return err
	}
	path := b.knowledgePath(p.Repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := writeTemp(filepath.Dir(path), raw)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (b *Backend) ListKnowledge(ctx context.Context) ([]workspace.KnowledgeEntry, error) {
	if err := b.Ensure(ctx); err != nil {
		return nil, err
	}
	var out []workspace.KnowledgeEntry
	err := filepath.WalkDir(filepath.Join(b.root(), knowledgeDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") || !strings.HasSuffix(d.Name(), ".md") {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(b.root(), knowledgeDir), path)
		owner, name, _ := strings.Cut(strings.TrimSuffix(filepath.ToSlash(rel), ".md"), "/")
		repo := unknowledgeName(owner) + "/" + unknowledgeName(name)
		info, _ := d.Info()
		e := workspace.KnowledgeEntry{Repo: repo, Ref: path}
		if info != nil {
			e.EditedAt = info.ModTime().UTC()
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if snap, perr := knowledge.Parse(raw); perr != nil {
			e.ParseErr = perr
		} else {
			e.Snapshot, e.Digest = &snap, workspace.SnapshotDigest(snap)
		}
		out = append(out, e)
		return nil
	})
	return out, err
}

// Check creates, updates, reads back, and deletes a test note.
func (b *Backend) Check(ctx context.Context, report func(workspace.Step)) error {
	var path string
	const marker = "check-updated"
	step := func(name string, fn func() error) error {
		err := fn()
		report(workspace.Step{Name: name, Err: err})
		if err != nil {
			if path != "" {
				os.Remove(path)
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"vault folders", func() error { return b.Ensure(ctx) }},
		{"create test note", func() error {
			p := filepath.Join(b.tasks(), "Foreman connectivity check "+b.now().Format("2006-01-02T15-04-05Z")+".md")
			n := newNote()
			_ = n.set("foreman_id", checkID)
			out, err := n.bytes()
			if err != nil {
				return err
			}
			if err := create(p, out); err != nil {
				return err
			}
			path = p
			return nil
		}},
		{"update test note", func() error { return b.edit(path, func(n *note) error { return n.set("foreman_branch", marker) }) }},
		{"read test note back", func() error { _, err := os.ReadFile(path); return err }},
		{"confirm update is visible", func() error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			n, err := parseNote(raw)
			if err != nil {
				return err
			}
			if n.str("foreman_branch") != marker {
				return errors.New("the update is not visible")
			}
			return nil
		}},
		{"delete test note", func() error {
			err := os.Remove(path)
			if err == nil {
				path = ""
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

// reseedTriage replaces the user triage and scope flag while keeping the description.
func reseedTriage(n *note, region noteRegion, s workspace.TaskSeed) (noteRegion, error) {
	if err := n.set("scope_confirmed", s.ScopeConfirmed); err != nil {
		return noteRegion{}, err
	}
	desc, _ := userText(region.userBody())
	var user strings.Builder
	if desc != "" {
		user.WriteString(desc + "\n\n")
	}
	user.WriteString(triageHeading + "\n\n")
	if t := strings.TrimSpace(s.ConfirmedTriage); t != "" {
		user.WriteString(t + "\n\n")
	}
	return noteRegion{before: user.String()}, nil
}
