package notifications

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brymartinez/foreman/internal/app"
	"github.com/brymartinez/foreman/internal/task"
)

type post struct {
	Content         string `json:"content"`
	AllowedMentions struct {
		Parse []string `json:"parse"`
	} `json:"allowed_mentions"`
}

// listener records each webhook body and answers with status.
func listener(t *testing.T, status int) (*httptest.Server, *[]post) {
	t.Helper()
	var posts []post
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var p post
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || json.Unmarshal(raw, &p) != nil {
			t.Errorf("request %s %q %q", r.Method, r.Header.Get("Content-Type"), raw)
		}
		posts = append(posts, p)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &posts
}

func discord(t *testing.T, url string) Discord {
	return Discord{
		WebhookURL: url,
		LogPath:    filepath.Join(t.TempDir(), "logs", "notifications.log"),
		Now:        func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) },
	}
}

var key = task.Key{Repo: "acme/widgets", Number: 42}

func TestDiscordPostsStateRepoAndLink(t *testing.T) {
	srv, posts := listener(t, http.StatusNoContent)
	d := discord(t, srv.URL+"/hook")
	if err := d.Notify(app.Alert{State: task.ReadyForHuman, Key: key, URL: "https://github.com/acme/widgets/pull/7"}); err != nil {
		t.Fatal(err)
	}
	want := "**Ready for human review** · acme/widgets#42 (READY_FOR_HUMAN)\nhttps://github.com/acme/widgets/pull/7"
	if len(*posts) != 1 || (*posts)[0].Content != want {
		t.Errorf("posts = %+v, want one with %q", *posts, want)
	}
	if _, err := os.Stat(d.LogPath); err == nil {
		t.Error("a delivered alert wrote the error log")
	}
}

func TestDiscordOmitsLinkWhenNone(t *testing.T) {
	srv, posts := listener(t, http.StatusNoContent)
	if err := discord(t, srv.URL).Notify(app.Alert{State: task.Failed, Cancelled: true, Key: key}); err != nil {
		t.Fatal(err)
	}
	want := "**Run cancelled** · acme/widgets#42 (FAILED)"
	if len(*posts) != 1 || (*posts)[0].Content != want {
		t.Errorf("posts = %+v, want one with %q", *posts, want)
	}
}

func TestDiscordDisablesMentions(t *testing.T) {
	srv, posts := listener(t, http.StatusNoContent)
	if err := discord(t, srv.URL).Notify(app.Alert{State: task.NeedsDecision, Key: key}); err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 1 || (*posts)[0].AllowedMentions.Parse == nil || len((*posts)[0].AllowedMentions.Parse) != 0 {
		t.Errorf("posts = %+v, want allowed_mentions.parse = []", *posts)
	}
}

func TestDiscordNon2xxIsLoggedWithoutURL(t *testing.T) {
	srv, _ := listener(t, http.StatusInternalServerError)
	d := discord(t, srv.URL+"/api/webhooks/1/secret-token")
	err := d.Notify(app.Alert{State: task.Reviewed, Key: key})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v, want an error without the URL", err)
	}
	logged, _ := os.ReadFile(d.LogPath)
	want := "2026-10-03T09:00:00Z acme/widgets#42 PR feedback to answer: Discord returned 500 Internal Server Error\n"
	if string(logged) != want {
		t.Errorf("log = %q, want %q", logged, want)
	}
}

func TestDiscordNetworkErrorIsLoggedWithoutURL(t *testing.T) {
	srv, _ := listener(t, http.StatusNoContent)
	url := srv.URL + "/api/webhooks/1/secret-token"
	srv.Close()
	d := discord(t, url)
	err := d.Notify(app.Alert{State: task.Failed, Key: key})
	if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err = %v, want an error without the URL", err)
	}
	logged, _ := os.ReadFile(d.LogPath)
	if !strings.HasPrefix(string(logged), "2026-10-03T09:00:00Z acme/widgets#42 Run failed: ") ||
		strings.Contains(string(logged), "secret-token") || strings.Contains(string(logged), srv.URL) {
		t.Errorf("log = %q, want the failure without the URL", logged)
	}
}
