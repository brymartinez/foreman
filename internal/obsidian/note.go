package obsidian

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/brymartinez/foreman/internal/workspace"
)

const (
	beginMarker   = "<!-- foreman:begin -->"
	endMarker     = "<!-- foreman:end -->"
	triageHeading = "## Confirmed triage"
	regionKey     = "foreman_region"
	regionTag     = " <!-- foreman:region "
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

// noteRegion records ownership independently of whether the record can be read.
type noteRegion struct {
	before, text, after string
	present             bool
}

func (r noteRegion) userBody() string { return r.before + r.after }

func regionMarker(marker, id string) string {
	return marker + regionTag + id + " -->"
}

func (n *note) splitRegion() (noteRegion, error) {
	begin, end := beginMarker, endMarker
	identified := n.value(regionKey) != nil
	legacy := !identified && n.str("foreman_status") != ""
	if identified {
		id := n.str(regionKey)
		if id == "" {
			return noteRegion{}, errors.New("empty foreman_region in frontmatter")
		}
		begin, end = regionMarker(begin, id), regionMarker(end, id)
	}

	var region noteRegion
	var offset, start, contentStart int
	open := false
	for line := range strings.SplitSeq(n.body, "\n") {
		next := offset + len(line) + 1
		switch {
		case !identified && (strings.HasPrefix(line, beginMarker+regionTag) || strings.HasPrefix(line, endMarker+regionTag)):
			return noteRegion{}, errors.New("Foreman region ID is missing from frontmatter")
		case !identified && !legacy:
			// An unpublished note can quote the pre-ID markers freely.
		case line == begin:
			if open || region.present {
				return noteRegion{}, errors.New("ambiguous Foreman regions in note")
			}
			if legacy && !startsLegacyRegion(n.body, next) {
				return noteRegion{}, errors.New("missing legacy Foreman region header in note")
			}
			start, contentStart, open = offset, next, true
		case line == end:
			if !open {
				return noteRegion{}, errors.New("unmatched Foreman region end in note")
			}
			region = noteRegion{
				before:  n.body[:start],
				text:    n.body[contentStart:offset],
				after:   n.body[next-1:],
				present: true,
			}
			open = false
		}
		offset = next
	}
	if open || (identified || legacy) && !region.present {
		return noteRegion{}, errors.New("missing Foreman region marker in note")
	}
	if !region.present {
		region.before = n.body
	}
	return region, nil
}

func startsLegacyRegion(body string, start int) bool {
	if start >= len(body) {
		return false
	}
	return strings.HasPrefix(body[start:], "Status: ") || strings.HasPrefix(body[start:], "Queued but not started: ")
}

// parseRecord reads the terminal payload directly. Fences in the preceding readable
// text do not determine where the payload starts.
func (r noteRegion) parseRecord() (workspace.Record, error) {
	const opening = "\n```yaml\n"
	text, closed := strings.CutSuffix(r.text, "```\n")
	i := strings.LastIndex(text, opening)
	if !closed || !strings.HasSuffix(text, "\n") || i < 0 {
		return workspace.Record{}, errors.New("no terminal Foreman task payload")
	}
	return workspace.Parse([]byte(unescapeMarkers(text[i+len(opening):])))
}

var markerPrefix = regexp.MustCompile(`<!-- foreman\x{2063}*`)

// escapeMarkers keeps marker text in Foreman's own content from ending its region
// early. It adds one invisible separator after "<!-- foreman", and unescapeMarkers
// takes one away, so any text round-trips.
func escapeMarkers(s string) string {
	return markerPrefix.ReplaceAllStringFunc(s, func(m string) string { return m + "\u2063" })
}

func unescapeMarkers(s string) string {
	return markerPrefix.ReplaceAllStringFunc(s, func(m string) string { return strings.TrimSuffix(m, "\u2063") })
}

// triageMarker is the line Foreman writes under the Confirmed triage heading it
// owns. It carries the note's region ID, so text that merely holds the heading,
// such as a description quoting another note, does not start the section.
func triageMarker(id string) string { return "<!-- foreman:triage " + id + " -->" }

// userSection is the user-owned text of a note Foreman writes: the description,
// then the triage heading and the confirmed triage. An empty id leaves the heading
// unmarked.
func userSection(id, desc, triage string) string {
	var b strings.Builder
	if desc = strings.TrimSpace(desc); desc != "" {
		b.WriteString(desc + "\n\n")
	}
	b.WriteString(triageHeading + "\n")
	if id != "" {
		b.WriteString(triageMarker(id) + "\n")
	}
	b.WriteString("\n")
	if triage = strings.TrimSpace(triage); triage != "" {
		b.WriteString(triage + "\n\n")
	}
	return b.String()
}

// userText splits the user-owned body into the description and the confirmed
// triage. With the marker, the heading nearest above it starts the triage, and text
// typed between the two is triage too. A note without the marker needs exactly one
// heading outside code blocks. Anything ambiguous is an error, because a wrong
// split would replace the confirmed scope.
func userText(s, id string) (desc, triage string, err error) {
	lines := strings.Split(s, "\n")
	split := func(heading int, rest []string) (string, string, error) {
		return strings.TrimSpace(strings.Join(lines[:heading], "\n")), strings.TrimSpace(strings.Join(rest, "\n")), nil
	}
	if id != "" {
		var marks []int
		for i, l := range lines {
			if strings.TrimSpace(l) == triageMarker(id) {
				marks = append(marks, i)
			}
		}
		if len(marks) > 1 {
			return "", "", errors.New("more than one foreman:triage line; keep the one under the real Confirmed triage heading")
		}
		if len(marks) == 1 {
			m := marks[0]
			h := m - 1
			for h >= 0 && !isTriageHeading(lines[h]) {
				h--
			}
			if h < 0 {
				return "", "", fmt.Errorf("the foreman:triage line must follow the %q heading", triageHeading)
			}
			return split(h, slices.Concat(lines[h+1:m], lines[m+1:]))
		}
	}
	var headings []int
	var fence string // the opening run of the code block the line is in
	hidden := false  // the open code block holds the heading
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case fence != "":
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence, hidden = "", false
			} else if isTriageHeading(l) {
				hidden = true
			}
		case openingFence(t) != "":
			fence = openingFence(t)
		case isTriageHeading(l):
			headings = append(headings, i)
		}
	}
	switch {
	case hidden:
		return "", "", fmt.Errorf("an unclosed code block holds the %q heading", triageHeading)
	case len(headings) > 1:
		return "", "", fmt.Errorf("more than one %q heading; keep one, or put the others in a code block", triageHeading)
	case len(headings) == 1:
		return split(headings[0], lines[headings[0]+1:])
	}
	return strings.TrimSpace(s), "", nil
}

// isTriageHeading reports the heading line. Four spaces would make it code.
func isTriageHeading(l string) bool {
	t := strings.TrimLeft(l, " ")
	return len(l)-len(t) <= 3 && strings.TrimRight(t, " \t") == triageHeading
}

// openingFence returns the run of backticks or tildes that opens a code block.
func openingFence(t string) string {
	for _, c := range []string{"`", "~"} {
		n := len(t) - len(strings.TrimLeft(t, c))
		if n >= 3 && (c == "~" || !strings.Contains(t[n:], c)) {
			return t[:n]
		}
	}
	return ""
}

// regionID is the note's region ID, assigned on first use.
func (n *note) regionID() (string, error) {
	if id := n.str(regionKey); id != "" {
		return id, nil
	}
	id := rand.Text()
	return id, n.set(regionKey, id)
}

// setRegion replaces the owned section and assigns an ID on the first write.
func (n *note) setRegion(r noteRegion, content string) error {
	id, err := n.regionID()
	if err != nil {
		return err
	}
	region := regionMarker(beginMarker, id) + "\n" + escapeMarkers(strings.TrimRight(content, "\n")) + "\n" + regionMarker(endMarker, id)
	if r.present {
		n.body = r.before + region + r.after
		return nil
	}
	n.body = r.before
	if n.body != "" && !strings.HasSuffix(n.body, "\n") {
		n.body += "\n"
	}
	if n.body != "" {
		n.body += "\n"
	}
	n.body += region + "\n"
	return nil
}
