package notion

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/brymartinez/foreman/internal/knowledge"
	"github.com/brymartinez/foreman/internal/workspace"
)

const (
	maxRichText      = 2000 // characters per rich-text item
	maxAppend        = 100  // blocks per append request
	maxCodeItems     = 100  // rich-text items per block
	sectionTitle     = "Foreman record"
	descriptionTitle = "Issue description"
	descriptionEnd   = "End of imported issue description"
	// endMarker is the last block of a complete section. A heading with children
	// but no marker is a publish that failed midway, and readers skip it.
	endMarker = "End of Foreman record"
	// blockRunes caps the text of one block. At the worst case of 6 JSON bytes per
	// character it stays well under the request limit, so one block always fits.
	blockRunes    = 60_000
	codeBlockSize = blockRunes
	// maxRequestBytes keeps a request under Notion's 500 KB payload limit with room
	// for the envelope.
	maxRequestBytes = 400_000
)

// takeBlocks says how many of blocks go into one request: at most 100, and at most
// maxRequestBytes of JSON, but always at least one.
func takeBlocks(blocks []any) int {
	size, n := 0, 0
	for _, b := range blocks {
		raw, err := json.Marshal(b)
		if err != nil {
			return max(n, 1)
		}
		if n > 0 && (n >= maxAppend || size+len(raw) > maxRequestBytes) {
			break
		}
		size += len(raw)
		n++
	}
	return n
}

// richText splits s into rich-text items of at most 2000 characters.
func richText(s string) []any {
	items := []any{}
	r := []rune(s)
	for len(r) > 0 {
		n := min(len(r), maxRichText)
		items = append(items, map[string]any{"type": "text", "text": map[string]any{"content": string(r[:n])}})
		r = r[n:]
	}
	return items
}

func block(typ string, content map[string]any) map[string]any {
	return map[string]any{"object": "block", "type": typ, typ: content}
}

// continuationMark starts a paragraph that continues the line before it. A line
// longer than one block can hold (a block of 60,000 characters stays under the
// request limit even when every character escapes to six bytes) is written as
// several blocks. The mark is an invisible word joiner that only Foreman writes, so
// the reader can join such a block back onto its line without mistaking a paragraph
// a person styled or typed for a continuation.
const continuationMark = "\u2060"

// escapeMark guards a chunk that the reader would otherwise take for something it
// is not. Only two places read a mark: the start of a block that follows a block of
// exactly blockRunes characters, and the start of a continuation. A chunk written
// there that starts with zero or more escape marks and then the continuation mark
// gets one more escape mark in front, so typed text is not read as a continuation
// and the reader can tell the escape from the text it guards. Text anywhere else is
// written and read as is, so an existing paragraph that happens to start with these
// characters is never altered.
const escapeMark = "\u200b"

func needsEscape(c string) bool {
	return strings.HasPrefix(strings.TrimLeft(c, escapeMark), continuationMark)
}

// unescapeChunk undoes the escape on a chunk read from a place the writer escapes.
func unescapeChunk(t string) string {
	if needsEscape(t) && strings.HasPrefix(t, escapeMark) {
		return strings.TrimPrefix(t, escapeMark)
	}
	return t
}

// paragraphs makes one paragraph block per line of s.
func paragraphs(s string) []any {
	var out []any
	prev := 0 // characters in the block before, 0 at the start
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		r := []rune(line)
		for first := true; first || len(r) > 0; first = false {
			n := min(len(r), blockRunes)
			chunk := string(r[:n])
			if (!first || prev == blockRunes) && needsEscape(chunk) {
				chunk = escapeMark + chunk
			}
			if !first {
				chunk = continuationMark + chunk
			}
			out = append(out, block("paragraph", map[string]any{"rich_text": richText(chunk)}))
			r = r[n:]
			prev = n
		}
	}
	return out
}

// codeBlocks holds payload in code blocks, each within the rich-text item limit.
func codeBlocks(payload string) []any {
	var out []any
	r := []rune(payload)
	for len(r) > 0 {
		n := min(len(r), codeBlockSize)
		out = append(out, block("code", map[string]any{"rich_text": richText(string(r[:n])), "language": "yaml"}))
		r = r[n:]
	}
	return out
}

type richItem struct {
	PlainText string `json:"plain_text"`
	Text      struct {
		Content string `json:"content"`
		Link    *struct {
			URL string `json:"url"`
		} `json:"link"`
	} `json:"text"`
}

func plainOf(items []richItem) string {
	var b strings.Builder
	for _, it := range items {
		if it.PlainText != "" {
			b.WriteString(it.PlainText)
		} else {
			b.WriteString(it.Text.Content)
		}
	}
	return b.String()
}

type blockObj struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	HasChildren bool   `json:"has_children"`
	raw         map[string]json.RawMessage
}

func (b *blockObj) UnmarshalJSON(data []byte) error {
	type plain blockObj
	if err := json.Unmarshal(data, (*plain)(b)); err != nil {
		return err
	}
	return json.Unmarshal(data, &b.raw)
}

func (b blockObj) richText() []richItem {
	var value struct {
		RichText []richItem `json:"rich_text"`
	}
	_ = json.Unmarshal(b.raw[b.Type], &value)
	return value.RichText
}

func (b blockObj) text() string {
	if b.Type == "child_database" {
		var value struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(b.raw[b.Type], &value)
		return value.Title
	}
	return plainOf(b.richText())
}

func sectionLink(page, title string) string {
	kind := "record"
	if title == descriptionTitle {
		kind = "description"
	}
	return "https://github.com/brymartinez/foreman#notion-" + kind + "-" + strings.ReplaceAll(page, "-", "")
}

func (b blockObj) ownsSection(page, title string) bool {
	if b.Type != "heading_2" {
		return false
	}
	items := b.richText()
	return len(items) == 1 && plainOf(items) == title && items[0].Text.Link != nil &&
		items[0].Text.Link.URL == sectionLink(page, title)
}

type listResp struct {
	Results    []json.RawMessage `json:"results"`
	HasMore    bool              `json:"has_more"`
	NextCursor string            `json:"next_cursor"`
}

// listAll walks every page of a list endpoint.
func (c *Client) listAll(ctx context.Context, method, path string, body map[string]any, each func(json.RawMessage) error) error {
	cursor := ""
	for {
		var resp listResp
		p, b := path, body
		if cursor != "" {
			if method == "GET" {
				p += "?start_cursor=" + url.QueryEscape(cursor)
			} else {
				b = map[string]any{}
				for k, v := range body {
					b[k] = v
				}
				b["start_cursor"] = cursor
			}
		}
		if err := c.do(ctx, method, p, b, &resp); err != nil {
			return err
		}
		for _, r := range resp.Results {
			if err := each(r); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.NextCursor == "" {
			return nil
		}
		cursor = resp.NextCursor
	}
}

func (c *Client) blocksOf(ctx context.Context, parent string) ([]blockObj, error) {
	var out []blockObj
	err := c.listAll(ctx, "GET", "/v1/blocks/"+parent+"/children", nil, func(r json.RawMessage) error {
		var b blockObj
		if err := json.Unmarshal(r, &b); err != nil {
			return err
		}
		out = append(out, b)
		return nil
	})
	return out, err
}

// appendBlocks appends blocks to parent in requests of at most 100 and returns the
// IDs of the created blocks.
func (c *Client) appendBlocks(ctx context.Context, parent string, blocks []any) ([]string, error) {
	var ids []string
	for len(blocks) > 0 {
		n := takeBlocks(blocks)
		var resp struct {
			Results []struct{ ID string } `json:"results"`
		}
		if err := c.do(ctx, "PATCH", "/v1/blocks/"+parent+"/children", map[string]any{"children": blocks[:n]}, &resp); err != nil {
			return ids, err
		}
		for _, r := range resp.Results {
			ids = append(ids, r.ID)
		}
		blocks = blocks[n:]
	}
	return ids, nil
}

// sectionPayload is one section's payload text. Complete says the section ends
// with the end marker. A section without it was cut short, or was written before
// the marker existed; a reader uses it only when its payload parses.
type sectionPayload struct {
	Text     string
	Complete bool
	Owned    bool
}

// pageBody is what a page holds below its properties.
type pageBody struct {
	Description       string
	LegacyDescription string
	LegacyParagraphs  []string
	// DescBlocks is how many top-level blocks make up Description.
	DescBlocks int
	// Payloads is each Foreman record section's code blocks, in page order.
	Payloads []sectionPayload
}

// readBody reads a page's blocks: the user's text outside Foreman's sections and
// each section's payload.
func (c *Client) readBody(ctx context.Context, page string) (pageBody, error) {
	blocks, err := c.blocksOf(ctx, page)
	if err != nil {
		return pageBody{}, err
	}
	var body pageBody
	var userBlocks, imported []blockObj
	for _, b := range blocks {
		if b.ownsSection(page, descriptionTitle) {
			kids, err := c.blocksOf(ctx, b.ID)
			if err != nil {
				return pageBody{}, err
			}
			if endsWith(kids, descriptionEnd) {
				imported = kids[:len(kids)-1]
			}
			continue
		}
		if b.Type == "heading_2" && b.text() == sectionTitle && (b.HasChildren || b.ownsSection(page, sectionTitle)) {
			kids, err := c.blocksOf(ctx, b.ID)
			if err != nil {
				return pageBody{}, err
			}
			var payload strings.Builder
			for _, k := range kids {
				if k.Type == "code" {
					payload.WriteString(k.text())
				}
			}
			owned := b.ownsSection(page, sectionTitle)
			complete := endsWith(kids, endMarker)
			if owned || complete && payload.Len() > 0 || legacyPayload(payload.String()) {
				body.Payloads = append(body.Payloads, sectionPayload{Text: payload.String(), Complete: complete, Owned: owned})
				continue
			}
		}
		userBlocks = append(userBlocks, b)
	}
	lines, count := descriptionLines(userBlocks)
	body.DescBlocks = count
	body.LegacyDescription = strings.Join(lines, "\n")
	for _, b := range userBlocks {
		if b.Type != "paragraph" || b.HasChildren {
			body.LegacyParagraphs = nil
			break
		}
		body.LegacyParagraphs = append(body.LegacyParagraphs, b.ID)
	}
	importedLines, _ := descriptionLines(imported)
	body.Description = strings.Join(append(importedLines, lines...), "\n")
	return body, nil
}

func legacyPayload(payload string) bool {
	if payload == "" {
		return false
	}
	if _, err := workspace.Parse([]byte(payload)); err == nil {
		return true
	}
	_, err := knowledge.Parse([]byte(payload))
	return err == nil
}

func endsWith(kids []blockObj, text string) bool {
	return len(kids) > 0 && kids[len(kids)-1].Type == "paragraph" && kids[len(kids)-1].text() == text
}

func descriptionLines(blocks []blockObj) ([]string, int) {
	var lines []string
	count, prevRunes := 0, 0
	for _, b := range blocks {
		if t := b.text(); b.Type == "paragraph" && len(lines) > 0 && prevRunes == blockRunes && strings.HasPrefix(t, continuationMark) {
			t = unescapeChunk(strings.TrimPrefix(t, continuationMark))
			lines[len(lines)-1] += t
			prevRunes = len([]rune(t))
			count++
			continue
		}
		switch b.Type {
		case "paragraph", "heading_1", "heading_2", "heading_3", "bulleted_list_item", "numbered_list_item", "quote", "to_do", "code":
			t := b.text()
			if b.Type == "paragraph" && len(lines) > 0 && prevRunes == blockRunes {
				t = unescapeChunk(t)
			}
			lines = append(lines, t)
			prevRunes = len([]rune(t))
			count++
		}
	}
	return lines, count
}

func (c *Client) replaceSection(ctx context.Context, page string, text, payload string) error {
	kids := append(paragraphs(text), codeBlocks(payload)...)
	return c.replaceOwnedSection(ctx, page, sectionTitle, endMarker, kids)
}

func (c *Client) replaceOwnedSection(ctx context.Context, page, title, end string, kids []any) error {
	blocks, err := c.blocksOf(ctx, page)
	if err != nil {
		return err
	}
	kids = append(kids, block("paragraph", map[string]any{"rich_text": richText(end)}))
	first := kids[:takeBlocks(kids)]
	label := []any{map[string]any{"type": "text", "text": map[string]any{
		"content": title, "link": map[string]string{"url": sectionLink(page, title)},
	}}}
	heading := block("heading_2", map[string]any{"rich_text": label, "is_toggleable": true, "children": first})
	ids, err := c.appendBlocks(ctx, page, []any{heading})
	if err != nil {
		return err
	}
	if len(kids) > len(first) {
		if len(ids) == 0 {
			return &Error{Status: 500, Code: "no_block", Message: "the section heading was not created"}
		}
		if _, err := c.appendBlocks(ctx, ids[0], kids[len(first):]); err != nil {
			return err
		}
	}
	for _, old := range blocks {
		if old.ownsSection(page, title) {
			if err := c.do(ctx, "DELETE", "/v1/blocks/"+old.ID, nil, nil); err != nil && !IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
