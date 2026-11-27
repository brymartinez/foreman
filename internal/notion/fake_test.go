package notion

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a small in-memory Notion API: enough of 2025-09-03 for the backend.
type fake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	token    string
	seq      int
	clock    time.Time
	dbs      map[string]*fdb
	sources  map[string]*fsource
	pages    map[string]*fpage
	blocks   map[string]*fblock
	order    map[string][]string
	PageSize int
	Requests []string
	// Fail returns a status (and Retry-After) to answer with instead of serving.
	Fail func(method, path string) (status int, retryAfter string)
	// Versions records the Notion-Version header of every request.
	Versions []string
}

type fdb struct {
	id, title, parent, source string
}
type fsource struct {
	id    string
	props map[string]string // name -> type
}
type fpage struct {
	id, source      string
	props           map[string]any // name -> stored value in request form
	created, edited time.Time
	trashed         bool
}
type fblock struct {
	id, parent, typ string
	content         map[string]any
	children        []string
	deleted         bool
}

func newFake(t *testing.T) *fake {
	f := &fake{token: "secret_test_token", clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PageSize: 100,
		dbs: map[string]*fdb{}, sources: map[string]*fsource{}, pages: map[string]*fpage{}, blocks: map[string]*fblock{}, order: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) client() *Client {
	c := New(f.token)
	c.Base = f.srv.URL
	c.Sleep = func(time.Duration) {}
	return c
}

func (f *fake) id(prefix string) string {
	f.seq++
	return prefix + fmt.Sprintf("%031x", f.seq)
}

func (f *fake) tick() time.Time { f.clock = f.clock.Add(time.Second); return f.clock }

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	f.Versions = append(f.Versions, r.Header.Get("Notion-Version"))
	if f.Fail != nil {
		if st, ra := f.Fail(r.Method, r.URL.Path); st != 0 {
			if ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			f.reply(w, st, map[string]any{"code": "injected", "message": "injected failure " + f.token})
			return
		}
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.reply(w, 401, map[string]any{"code": "unauthorized", "message": "API token is invalid."})
		return
	}
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 500_000 { // Notion's payload limit
			f.reply(w, 413, map[string]any{"code": "validation_error", "message": "payload too large"})
			return
		}
		_ = json.Unmarshal(raw, &body)
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "v1" {
		f.reply(w, 404, map[string]any{"code": "not_found", "message": "no route"})
		return
	}
	route := parts[1:]
	switch {
	case r.Method == "GET" && route[0] == "users" && len(route) == 2:
		f.reply(w, 200, map[string]any{"object": "user", "id": "bot", "type": "bot"})
	case r.Method == "POST" && route[0] == "databases" && len(route) == 1:
		f.createDB(w, body)
	case r.Method == "GET" && route[0] == "databases" && len(route) == 2:
		db := f.dbs[route[1]]
		if db == nil {
			f.notFound(w)
			return
		}
		f.reply(w, 200, map[string]any{"object": "database", "id": db.id, "data_sources": []any{map[string]any{"id": db.source, "name": db.title}}})
	case route[0] == "data_sources" && len(route) == 2:
		src := f.sources[route[1]]
		if src == nil {
			f.notFound(w)
			return
		}
		if r.Method == "PATCH" {
			for name, spec := range body["properties"].(map[string]any) {
				if spec == nil {
					continue
				}
				for typ := range spec.(map[string]any) {
					src.props[name] = typ
				}
			}
		}
		f.reply(w, 200, f.sourceJSON(src))
	case r.Method == "POST" && route[0] == "data_sources" && len(route) == 3 && route[2] == "query":
		f.query(w, route[1], body)
	case r.Method == "POST" && route[0] == "pages" && len(route) == 1:
		f.createPage(w, body)
	case route[0] == "pages" && len(route) == 2:
		pg := f.pages[route[1]]
		if pg == nil || pg.trashed && r.Method == "GET" && false {
			f.notFound(w)
			return
		}
		if r.Method == "PATCH" {
			if props, ok := body["properties"].(map[string]any); ok {
				for k, v := range props {
					pg.props[k] = v
				}
			}
			if v, ok := body["in_trash"].(bool); ok {
				pg.trashed = v
			}
			pg.edited = f.tick()
		}
		f.reply(w, 200, f.pageJSON(pg))
	case route[0] == "blocks" && len(route) == 3 && route[2] == "children":
		f.children(w, r, route[1], body)
	case r.Method == "PATCH" && route[0] == "blocks" && len(route) == 2:
		b := f.blocks[route[1]]
		if b == nil {
			f.notFound(w)
			return
		}
		if upd, ok := body[b.typ].(map[string]any); ok {
			for k, v := range upd {
				b.content[k] = v
			}
		}
		f.reply(w, 200, f.blockJSON(b))
	case r.Method == "DELETE" && route[0] == "blocks" && len(route) == 2:
		b := f.blocks[route[1]]
		if b == nil {
			f.notFound(w)
			return
		}
		b.deleted = true
		f.reply(w, 200, map[string]any{"id": b.id, "archived": true})
	default:
		f.reply(w, 404, map[string]any{"code": "not_found", "message": "no route " + r.URL.Path})
	}
}

func (f *fake) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fake) notFound(w http.ResponseWriter) {
	f.reply(w, 404, map[string]any{"code": "object_not_found", "message": "Could not find the object."})
}

func (f *fake) sourceJSON(s *fsource) map[string]any {
	props := map[string]any{}
	for n, t := range s.props {
		props[n] = map[string]any{"name": n, "type": t}
	}
	return map[string]any{"object": "data_source", "id": s.id, "properties": props}
}

func (f *fake) createDB(w http.ResponseWriter, body map[string]any) {
	parent := body["parent"].(map[string]any)["page_id"].(string)
	title := body["title"].([]any)[0].(map[string]any)["text"].(map[string]any)["content"].(string)
	db := &fdb{id: f.id("d"), title: title, parent: parent}
	src := &fsource{id: f.id("s"), props: map[string]string{}}
	db.source = src.id
	if ids, ok := body["initial_data_source"].(map[string]any); ok {
		for name, spec := range ids["properties"].(map[string]any) {
			for typ := range spec.(map[string]any) {
				src.props[name] = typ
			}
		}
	}
	f.dbs[db.id], f.sources[src.id] = db, src
	f.reply(w, 200, map[string]any{"object": "database", "id": db.id, "data_sources": []any{map[string]any{"id": src.id, "name": title}}})
}

func (f *fake) createPage(w http.ResponseWriter, body map[string]any) {
	parent := body["parent"].(map[string]any)
	pg := &fpage{id: f.id("p"), props: map[string]any{}, created: f.tick()}
	pg.edited = pg.created
	if ds, ok := parent["data_source_id"].(string); ok {
		if f.sources[ds] == nil {
			f.notFound(w)
			return
		}
		pg.source = ds
	}
	if props, ok := body["properties"].(map[string]any); ok {
		for k, v := range props {
			pg.props[k] = v
		}
	}
	f.pages[pg.id] = pg
	if kids, ok := body["children"].([]any); ok {
		f.addBlocks(pg.id, kids)
	}
	f.reply(w, 200, f.pageJSON(pg))
}

func (f *fake) addBlocks(parent string, kids []any) []map[string]any {
	var out []map[string]any
	for _, k := range kids {
		bm := k.(map[string]any)
		typ := bm["type"].(string)
		content, _ := bm[typ].(map[string]any)
		cp := map[string]any{}
		var nested []any
		for ck, cv := range content {
			if ck == "children" {
				nested, _ = cv.([]any)
				continue
			}
			cp[ck] = cv
		}
		if rts, ok := cp["rich_text"].([]any); ok {
			for _, rt := range rts {
				m := rt.(map[string]any)
				m["plain_text"] = m["text"].(map[string]any)["content"]
			}
		}
		b := &fblock{id: f.id("b"), parent: parent, typ: typ, content: cp}
		f.blocks[b.id] = b
		if pb := f.blocks[parent]; pb != nil {
			pb.children = append(pb.children, b.id)
		} else if pg := f.pages[parent]; pg != nil {
			pg.edited = f.tick()
		}
		f.pageChildren(parent, b.id)
		if len(nested) > 0 {
			f.addBlocks(b.id, nested)
		}
		out = append(out, f.blockJSON(b))
	}
	return out
}

// pageChildren records top-level blocks in order under their page.

func (f *fake) pageChildren(parent, id string) {
	if f.pages[parent] != nil {
		f.order[parent] = append(f.order[parent], id)
	}
}

func (f *fake) childIDs(parent string) []string {
	if pb := f.blocks[parent]; pb != nil {
		return pb.children
	}
	return f.order[parent]
}

func (f *fake) blockJSON(b *fblock) map[string]any {
	return map[string]any{"object": "block", "id": b.id, "type": b.typ, b.typ: b.content, "has_children": len(b.children) > 0}
}

func (f *fake) children(w http.ResponseWriter, r *http.Request, parent string, body map[string]any) {
	if f.pages[parent] == nil && f.blocks[parent] == nil {
		f.notFound(w)
		return
	}
	if r.Method == "PATCH" {
		kids, _ := body["children"].([]any)
		if len(kids) > 100 {
			f.reply(w, 400, map[string]any{"code": "validation_error", "message": "children length should be <= 100"})
			return
		}
		f.reply(w, 200, map[string]any{"object": "list", "results": f.addBlocks(parent, kids)})
		return
	}
	var live []string
	for _, db := range f.dbs {
		if db.parent == parent {
			live = append(live, "db:"+db.id)
		}
	}
	sort.Strings(live)
	for _, id := range f.childIDs(parent) {
		if !f.blocks[id].deleted {
			live = append(live, id)
		}
	}
	start := 0
	if c := r.URL.Query().Get("start_cursor"); c != "" {
		start, _ = strconv.Atoi(c)
	}
	end := min(start+f.PageSize, len(live))
	var results []any
	for _, id := range live[start:end] {
		if strings.HasPrefix(id, "db:") {
			db := f.dbs[id[3:]]
			results = append(results, map[string]any{"object": "block", "id": db.id, "type": "child_database", "child_database": map[string]any{"title": db.title}})
			continue
		}
		results = append(results, f.blockJSON(f.blocks[id]))
	}
	resp := map[string]any{"object": "list", "results": results, "has_more": end < len(live)}
	if end < len(live) {
		resp["next_cursor"] = strconv.Itoa(end)
	}
	f.reply(w, 200, resp)
}

func (f *fake) pageJSON(pg *fpage) map[string]any {
	props := map[string]any{}
	src := f.sources[pg.source]
	for name, v := range pg.props {
		typ := ""
		if src != nil {
			typ = src.props[name]
		}
		entry := map[string]any{"type": typ}
		switch typ {
		case "title", "rich_text":
			rts, _ := v.([]any)
			if rts == nil {
				if m, ok := v.(map[string]any); ok {
					rts, _ = m[typ].([]any)
				}
			}
			for _, rt := range rts {
				m := rt.(map[string]any)
				m["plain_text"] = m["text"].(map[string]any)["content"]
			}
			entry[typ] = rts
		default:
			if m, ok := v.(map[string]any); ok {
				entry[typ] = m[typ]
			} else {
				entry[typ] = v
			}
		}
		props[name] = entry
	}
	return map[string]any{"object": "page", "id": pg.id, "in_trash": pg.trashed, "created_time": pg.created.Format(time.RFC3339), "last_edited_time": pg.edited.Format(time.RFC3339), "properties": props}
}

func (f *fake) query(w http.ResponseWriter, source string, body map[string]any) {
	if f.sources[source] == nil {
		f.notFound(w)
		return
	}
	var match []*fpage
	for _, pg := range f.pages {
		if pg.source != source || pg.trashed {
			continue
		}
		if flt, ok := body["filter"].(map[string]any); ok {
			prop := flt["property"].(string)
			cond, ok := flt["rich_text"].(map[string]any)
			if !ok {
				cond = flt["title"].(map[string]any)
			}
			if plain(pg.props[prop]) != cond["equals"].(string) {
				continue
			}
		}
		match = append(match, pg)
	}
	for i := range match {
		for j := i + 1; j < len(match); j++ {
			if match[j].created.Before(match[i].created) {
				match[i], match[j] = match[j], match[i]
			}
		}
	}
	start := 0
	if c, ok := body["start_cursor"].(string); ok {
		start, _ = strconv.Atoi(c)
	}
	end := min(start+f.PageSize, len(match))
	var results []any
	for _, pg := range match[start:end] {
		results = append(results, f.pageJSON(pg))
	}
	resp := map[string]any{"object": "list", "results": results, "has_more": end < len(match)}
	if end < len(match) {
		resp["next_cursor"] = strconv.Itoa(end)
	}
	f.reply(w, 200, resp)
}

func plain(v any) string {
	rts, _ := v.([]any)
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"rich_text", "title"} {
			if x, ok := m[k].([]any); ok {
				rts = x
			}
		}
	}
	var s string
	for _, rt := range rts {
		s += rt.(map[string]any)["text"].(map[string]any)["content"].(string)
	}
	return s
}

// count returns how many requests matched method and path prefix.
func (f *fake) count(method, prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.Requests {
		if strings.HasPrefix(r, method+" "+prefix) {
			n++
		}
	}
	return n
}

// addParentPage makes the blank page a user shares with the integration.
func (f *fake) addParentPage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	pg := &fpage{id: f.id("a"), props: map[string]any{}, created: f.tick()}
	pg.edited = pg.created
	f.pages[pg.id] = pg
	return pg.id
}
