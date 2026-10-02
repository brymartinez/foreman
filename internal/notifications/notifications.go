// Package notifications delivers foreman's alerts to a Discord webhook.
package notifications

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brymartinez/foreman/internal/app"
)

// Discord posts each alert to a webhook. The webhook URL is a secret, so it never
// appears in the returned error or in the log at LogPath.
type Discord struct {
	WebhookURL string
	LogPath    string       // delivery errors are appended here
	Client     *http.Client // nil uses a 10 second timeout
	Now        func() time.Time
}

func (d Discord) Notify(a app.Alert) error {
	err := d.post(a)
	if err != nil {
		d.record(a, err)
	}
	return err
}

func (d Discord) post(a app.Alert) error {
	content := fmt.Sprintf("**%s** · %s#%d (%s)", a.Label(), a.Key.Repo, a.Key.Number, a.State)
	if a.URL != "" {
		content += "\n" + a.URL
	}
	// An empty parse list stops task text from pinging anyone.
	body, err := json.Marshal(map[string]any{
		"content":          content,
		"allowed_mentions": map[string][]string{"parse": {}},
	})
	if err != nil {
		return err
	}
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Post(d.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return d.redact(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Discord returned %s", resp.Status)
	}
	return nil
}

// redact drops the webhook URL from a client error: *url.Error names it, so use
// the error it wraps, then mask any copy left in that text.
func (d Discord) redact(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	msg := err.Error()
	if d.WebhookURL != "" {
		msg = strings.ReplaceAll(msg, d.WebhookURL, "[webhook]")
	}
	return errors.New("Discord webhook: " + msg)
}

// record appends a delivery error to the log. A log that can't be written is
// dropped too: delivery is best effort.
func (d Discord) record(a app.Alert, err error) {
	if d.LogPath == "" {
		return
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	if os.MkdirAll(filepath.Dir(d.LogPath), 0o755) != nil {
		return
	}
	f, ferr := os.OpenFile(d.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s#%d %s: %v\n", now().UTC().Format(time.RFC3339), a.Key.Repo, a.Key.Number, a.Label(), err)
}
