package obsidian

import (
	"context"
	"os"
	"path/filepath"

	"github.com/brymartinez/foreman/internal/portable"
	"github.com/brymartinez/foreman/internal/workspace"
)

func workspaceWorkValue() portable.Work {
	return portable.Work{Repo: "o/r", State: "open", Branch: "feat/x"}
}

// writeUserFields acts as a person editing a note in Obsidian: it changes the
// user-owned keys and text, and renames the file when the name changed.
func (b *Backend) writeUserFields(ctx context.Context, ref string, e workspace.TaskEntry) error {
	err := b.edit(ref, func(n *note) error {
		tags := e.Tags
		if tags == nil {
			tags = []string{}
		}
		for _, kv := range []struct {
			k string
			v any
		}{{"repository", e.Repository}, {"source_url", e.SourceURL}, {"tags", tags}, {"scope_confirmed", e.ScopeConfirmed}, {"queue", e.Queue}, {"answer", e.Answer}} {
			if err := n.set(kv.k, kv.v); err != nil {
				return err
			}
		}
		if e.ConfirmedTriage != "" {
			before, _, after, ok := splitRegion(n.body)
			desc, _ := userText(before + after)
			user := desc + "\n\n" + triageHeading + "\n\n" + e.ConfirmedTriage + "\n"
			if ok {
				_, region, _, _ := splitRegion(n.body)
				n.body = user + "\n" + beginMarker + region + endMarker + "\n"
			} else {
				n.body = user
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if want := e.Name + ".md"; filepath.Base(ref) != want {
		return os.Rename(ref, filepath.Join(filepath.Dir(ref), want))
	}
	return nil
}
