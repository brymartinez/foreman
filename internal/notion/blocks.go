package notion

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

const (
	maxRichText   = 2000 // characters per rich-text item
	maxAppend     = 100  // blocks per append request
	maxCodeItems  = 100  // rich-text items per block
	sectionTitle  = "Foreman record"
	codeBlockSize = maxRichText * maxCodeItems
)

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

// paragraphs makes one paragraph block per line of s.
func paragraphs(s string) []any {
	var out []any
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, block("paragraph", map[string]any{"rich_text": richText(line)}))
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

func (b blockObj) text() string {
	var c struct {
		RichText []richItem `json:"rich_text"`
		Title    string     `json:"title"`
	}
	_ = json.Unmarshal(b.raw[b.Type], &c)
	if b.Type == "child_database" {
		return c.Title
	}
	return plainOf(c.RichText)
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
		n := min(len(blocks), maxAppend)
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

// pageBody is what a page holds below its properties.
type pageBody struct {
	Description string
	// Payloads is the text of each Foreman record section's code blocks, in page order.
	Payloads []string
}

// readBody reads a page's blocks: the user's text outside Foreman's sections and
// each section's payload.
func (c *Client) readBody(ctx context.Context, page string) (pageBody, error) {
	blocks, err := c.blocksOf(ctx, page)
	if err != nil {
		return pageBody{}, err
	}
	var body pageBody
	var lines []string
	for _, b := range blocks {
		if b.Type == "heading_2" && b.text() == sectionTitle {
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
			body.Payloads = append(body.Payloads, payload.String())
			continue
		}
		switch b.Type {
		case "paragraph", "heading_1", "heading_2", "heading_3", "bulleted_list_item", "numbered_list_item", "quote", "to_do", "code":
			lines = append(lines, b.text())
		}
	}
	body.Description = strings.Join(lines, "\n")
	return body, nil
}

// sectionIDs lists the IDs of a page's Foreman record sections.
func (c *Client) sectionIDs(ctx context.Context, page string) ([]string, error) {
	blocks, err := c.blocksOf(ctx, page)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, b := range blocks {
		if b.Type == "heading_2" && b.text() == sectionTitle {
			ids = append(ids, b.ID)
		}
	}
	return ids, nil
}

// replaceSection appends a new Foreman record section, with the payload in its last
// block, and only then deletes the old sections. A reader uses the last section
// whose payload parses, so a crash at any point leaves a readable page.
func (c *Client) replaceSection(ctx context.Context, page string, text, payload string) error {
	old, err := c.sectionIDs(ctx, page)
	if err != nil {
		return err
	}
	kids := append(paragraphs(text), codeBlocks(payload)...)
	first := kids[:min(len(kids), maxAppend)]
	heading := block("heading_2", map[string]any{"rich_text": richText(sectionTitle), "is_toggleable": true, "children": first})
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
	for _, id := range old {
		if err := c.do(ctx, "DELETE", "/v1/blocks/"+id, nil, nil); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}
