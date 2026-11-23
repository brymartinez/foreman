package obsidian

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	beginMarker   = "<!-- foreman:begin -->"
	endMarker     = "<!-- foreman:end -->"
	triageHeading = "## Confirmed triage"
)

// note is a Markdown note split into its frontmatter and body. The frontmatter
// stays a yaml.Node so keys, order, and comments that Foreman does not own
// survive a rewrite.
type note struct {
	doc  *yaml.Node
	body string
	// crlf is set when the file used Windows line endings. Parsing works on LF text
	// and bytes writes the endings back.
	crlf bool
}

func newNote() *note {
	return &note{doc: &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}}
}

func (n *note) mapping() *yaml.Node { return n.doc.Content[0] }

// parseNote reads a note. A note with no frontmatter gets an empty one. Frontmatter
// that does not parse is an error, so Foreman never rewrites what it cannot read.
func parseNote(raw []byte) (*note, error) {
	s := string(raw)
	n := newNote()
	if strings.Contains(s, "\r\n") {
		s, n.crlf = strings.ReplaceAll(s, "\r\n", "\n"), true
	}
	if !strings.HasPrefix(s, "---\n") {
		n.body = s
		return n, nil
	}
	rest := s[4:]
	var fm string
	switch {
	case strings.HasPrefix(rest, "---\n"):
		n.body = rest[4:]
	case rest == "---":
	default:
		i := strings.Index(rest, "\n---\n")
		switch {
		case i >= 0:
			fm, n.body = rest[:i+1], rest[i+5:]
		case strings.HasSuffix(rest, "\n---"):
			fm = rest[:len(rest)-3]
		default:
			n.body = s // an opening --- with no end is text, not frontmatter
			return n, nil
		}
	}
	if strings.TrimSpace(fm) == "" {
		return n, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		return nil, fmt.Errorf("frontmatter does not parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("frontmatter is not a mapping")
	}
	n.doc = &doc
	return n, nil
}

func (n *note) bytes() ([]byte, error) {
	out, err := n.lfBytes()
	if err == nil && n.crlf {
		out = []byte(strings.ReplaceAll(string(out), "\n", "\r\n"))
	}
	return out, err
}

func (n *note) lfBytes() ([]byte, error) {
	var buf bytes.Buffer
	if len(n.mapping().Content) > 0 {
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(n.doc); err != nil {
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
		return append(append([]byte("---\n"), buf.Bytes()...), append([]byte("---\n"), n.body...)...), nil
	}
	if strings.HasPrefix(n.body, "---\n") { // keep a body that merely looks like frontmatter
		return []byte("---\n---\n" + n.body), nil
	}
	return []byte(n.body), nil
}

func (n *note) index(key string) int {
	m := n.mapping()
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// set writes a key. An existing key keeps its position and comments.
func (n *note) set(key string, v any) error {
	var vn yaml.Node
	if err := vn.Encode(v); err != nil {
		return err
	}
	if i := n.index(key); i >= 0 {
		old := n.mapping().Content[i+1]
		vn.HeadComment, vn.LineComment, vn.FootComment = old.HeadComment, old.LineComment, old.FootComment
		*old = vn
		return nil
	}
	m := n.mapping()
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &vn)
	return nil
}

func (n *note) value(key string) *yaml.Node {
	if i := n.index(key); i >= 0 {
		return n.mapping().Content[i+1]
	}
	return nil
}

func (n *note) str(key string) string {
	if v := n.value(key); v != nil && v.Kind == yaml.ScalarNode && v.Tag != "!!null" {
		return v.Value
	}
	return ""
}

func (n *note) boolean(key string) bool {
	if v := n.value(key); v != nil {
		var b bool
		if v.Decode(&b) == nil {
			return b
		}
	}
	return false
}

func (n *note) strs(key string) []string {
	v := n.value(key)
	if v == nil {
		return nil
	}
	var out []string
	if v.Kind == yaml.SequenceNode {
		for _, c := range v.Content {
			out = append(out, c.Value)
		}
	} else if v.Kind == yaml.ScalarNode && v.Value != "" {
		out = []string{v.Value}
	}
	return out
}

// splitRegion cuts the body at Foreman's markers.
func splitRegion(body string) (before, region, after string, ok bool) {
	b := strings.Index(body, beginMarker)
	if b < 0 {
		return body, "", "", false
	}
	e := strings.Index(body[b:], endMarker)
	if e < 0 {
		return body, "", "", false
	}
	e += b
	return body[:b], body[b+len(beginMarker) : e], body[e+len(endMarker):], true
}

// userText splits the user-owned body into the description and the confirmed triage.
func userText(s string) (desc, triage string) {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == triageHeading {
			return strings.TrimSpace(strings.Join(lines[:i], "\n")), strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return strings.TrimSpace(s), ""
}

func regionText(content string) string {
	return beginMarker + "\n" + strings.TrimRight(content, "\n") + "\n" + endMarker
}

// setRegion replaces Foreman's region, or appends it when the note has none.
func (n *note) setRegion(content string) {
	region := regionText(content)
	before, _, after, ok := splitRegion(n.body)
	if ok {
		n.body = before + region + after
		return
	}
	if n.body != "" && !strings.HasSuffix(n.body, "\n") {
		n.body += "\n"
	}
	if n.body != "" {
		n.body += "\n"
	}
	n.body += region + "\n"
}

// regionPayload is the section text between the markers, or "".
func (n *note) regionPayload() string {
	_, region, _, ok := splitRegion(n.body)
	if !ok {
		return ""
	}
	return region
}

// userBody is the body outside Foreman's region.
func (n *note) userBody() string {
	before, _, after, _ := splitRegion(n.body)
	return before + after
}
