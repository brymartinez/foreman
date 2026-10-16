package knowledge

import (
	"slices"
	"strings"
	"time"
	"unicode"
)

// Select returns up to limit active learnings for repo that total at most maxChars
// as Markdown bullets, each text plus "- " and a newline. Learnings sharing more words with query come first, then those backed by
// more tasks, then the most recently supported. Learnings that share no words still
// qualify, because a convention applies whatever the issue is about.
func (s Store) Select(repo, query string, limit, maxChars int) ([]Learning, error) {
	ls, err := s.Learnings(repo)
	if err != nil {
		return nil, err
	}
	want := words(query)
	type ranked struct {
		l       Learning
		overlap int
		recent  time.Time
	}
	var rs []ranked
	for _, l := range ls {
		if l.Status != Active {
			continue
		}
		r := ranked{l: l}
		for w := range words(l.Text) {
			if want[w] {
				r.overlap++
			}
		}
		for _, e := range l.Evidence {
			if e.At.After(r.recent) {
				r.recent = e.At
			}
		}
		rs = append(rs, r)
	}
	slices.SortStableFunc(rs, func(a, b ranked) int {
		if a.overlap != b.overlap {
			return b.overlap - a.overlap
		}
		if a.l.Tasks() != b.l.Tasks() {
			return b.l.Tasks() - a.l.Tasks()
		}
		return b.recent.Compare(a.recent)
	})
	var out []Learning
	total := 0
	for _, r := range rs {
		if len(out) == limit {
			break
		}
		n := len(r.l.Text) + len("- \n")
		if total+n > maxChars {
			continue
		}
		total += n
		out = append(out, r.l)
	}
	return out, nil
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true, "this": true,
	"from": true, "into": true, "are": true, "not": true, "use": true, "when": true,
	"before": true, "after": true, "should": true, "must": true, "add": true, "fix": true,
}

func words(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 3 && !stopWords[w] {
			out[w] = true
		}
	}
	return out
}
