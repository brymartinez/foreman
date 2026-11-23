// Package notion is the Notion implementation of workspace.Backend. It uses only
// the standard library: a small REST client, the Tasks and Repository Knowledge
// databases, and the Foreman record section of each page.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Version is the Notion API version Foreman speaks.
const Version = "2025-09-03"

const (
	// DefaultBase is the public API. FOREMAN_NOTION_API overrides it for tests only.
	DefaultBase = "https://api.notion.com"
	// BaseEnv names the test-only base URL override.
	BaseEnv = "FOREMAN_NOTION_API"

	requestTimeout = 30 * time.Second
	maxRetries     = 5
)

// Client talks to the Notion API.
type Client struct {
	Token   string
	Base    string
	HTTP    *http.Client
	Sleep   func(time.Duration) // swapped in tests
	Retries int
}

// New builds a client for token. The base URL comes from FOREMAN_NOTION_API when
// set, which only tests do.
func New(token string) *Client {
	base := os.Getenv(BaseEnv)
	if base == "" {
		base = DefaultBase
	}
	return &Client{Token: token, Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: requestTimeout}, Sleep: time.Sleep, Retries: maxRetries}
}

// Error is a Notion API error. Its text never contains the token.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("notion: %d %s: %s", e.Status, e.Code, e.Message)
}

// IsNotFound reports whether err is a 404 from Notion.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// redact removes the token from text that is about to leave the client.
func (c *Client) redact(s string) string {
	if c.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Token, "[redacted]")
}

// do sends one request and decodes the JSON answer into out. It waits as long as
// Retry-After says after a 429 and retries 429 and 5xx up to Retries times.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(payload))
		if err != nil {
			return errors.New(c.redact(err.Error()))
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Notion-Version", Version)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			// A url.Error carries the URL, never the header, but scrub anyway.
			return errors.New(c.redact(err.Error()))
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && retriesAfterServerError(method, path) {
			if attempt < c.Retries {
				if err := c.sleep(ctx, retryDelay(resp, attempt)); err != nil {
					return err
				}
				continue
			}
		}
		if resp.StatusCode >= 300 {
			var e struct{ Code, Message string }
			_ = json.Unmarshal(raw, &e)
			return &Error{Status: resp.StatusCode, Code: e.Code, Message: c.redact(e.Message)}
		}
		if out == nil || len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
}

// retriesAfterServerError says whether a 5xx answer to this request is safe to
// retry. Notion may already have applied a write that answered 503, so repeating a
// create or an append could duplicate a database, a page, or a block of the
// payload. Those callers look the result up on their next attempt instead. A 429
// is always safe to retry: the request was refused.
func retriesAfterServerError(method, path string) bool {
	switch {
	case method == http.MethodPost && (path == "/v1/databases" || path == "/v1/pages"):
		return false
	case method == http.MethodPatch && strings.HasSuffix(path, "/children"):
		return false
	}
	return true
}

// sleep waits d before a retry. It gives up at once when the caller's deadline
// falls before the wait ends, and returns early when the caller cancels, so a long
// Retry-After cannot hold a publish, its task lock, or an interrupted command.
func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return fmt.Errorf("notion asked to wait %s, which is longer than the time left: %w", d.Round(time.Second), context.DeadlineExceeded)
	}
	if c.Sleep != nil {
		c.Sleep(d)
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryDelay is Retry-After when the answer has it, else exponential backoff.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.ParseFloat(s, 64); err == nil && n >= 0 {
			return time.Duration(n * float64(time.Second))
		}
	}
	return time.Duration(500<<attempt) * time.Millisecond
}
