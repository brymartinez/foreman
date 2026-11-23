package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Backend names the one place a workspace keeps its shared records.
type Backend string

const (
	BackendNotion   Backend = "notion"
	BackendObsidian Backend = "obsidian"
	BackendSQLite   Backend = "sqlite"
)

// NotionTokenEnv is the only source of the Notion integration token. The token
// is never a config key, so it cannot end up in a file that gets copied or shown.
const NotionTokenEnv = "FOREMAN_NOTION_TOKEN"

// DefaultObsidianFolder is the vault folder Foreman writes under.
const DefaultObsidianFolder = "Foreman"

// Storage selects the workspace store.
type Storage struct {
	// Backend is notion, obsidian, or sqlite. Unset means the store the config
	// names a target for (a Notion page, then an Obsidian vault), else sqlite when
	// the configured database file exists and notion otherwise.
	Backend  Backend  `yaml:"backend"`
	Notion   Notion   `yaml:"notion"`
	Obsidian Obsidian `yaml:"obsidian"`
}

type Notion struct {
	// ParentPageID is the ID or URL of an existing page; it is stored as 32 hex.
	ParentPageID string `yaml:"parent_page_id"`
}

type Obsidian struct {
	Vault  string `yaml:"vault"`
	Folder string `yaml:"folder"`
}

var pageIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{32}$`)

// NormalizePageID returns the 32-hex form of a Notion page ID, dashed ID, or URL.
func NormalizePageID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if strings.Contains(s, "/") { // a URL: the ID ends its last path segment
		s = strings.TrimRight(s, "/")
		s = s[strings.LastIndex(s, "/")+1:]
	}
	compact := strings.ReplaceAll(s, "-", "")
	if len(compact) >= 32 && pageIDPattern.MatchString(compact[len(compact)-32:]) &&
		(len(compact) == 32 || strings.Contains(s, "-")) {
		return strings.ToLower(compact[len(compact)-32:]), nil
	}
	return "", errors.New("not a Notion page ID or URL")
}

// resolveStorage applies the defaults and checks the values that are present.
// Whether the required ones are present is Validate's question, so commands that
// never open the store (config show, version) still load.
func resolveStorage(s *Storage, databaseExists bool) error {
	switch s.Backend {
	case "":
		// A configured target is a decision to switch. It stays the choice on every
		// run, so a first run that created the database file does not flip the
		// backend to sqlite on the next one.
		switch {
		case strings.TrimSpace(s.Notion.ParentPageID) != "":
			s.Backend = BackendNotion
		case strings.TrimSpace(s.Obsidian.Vault) != "":
			s.Backend = BackendObsidian
		case databaseExists:
			s.Backend = BackendSQLite
		default:
			s.Backend = BackendNotion
		}
	case BackendNotion, BackendObsidian, BackendSQLite:
	default:
		return fmt.Errorf("storage.backend is %q; want notion, obsidian, or sqlite", s.Backend)
	}
	if strings.TrimSpace(s.Notion.ParentPageID) != "" {
		id, err := NormalizePageID(s.Notion.ParentPageID)
		if err != nil {
			return fmt.Errorf("storage.notion.parent_page_id: %w", err)
		}
		s.Notion.ParentPageID = id
	}
	if strings.TrimSpace(s.Obsidian.Folder) == "" {
		s.Obsidian.Folder = DefaultObsidianFolder
	}
	return nil
}

// NotionToken reads the integration token from the environment.
func (s Storage) NotionToken() string { return strings.TrimSpace(os.Getenv(NotionTokenEnv)) }

// Validate reports a missing or invalid value the chosen backend needs. It never
// includes the token.
func (s Storage) Validate() error {
	hint := "; run foreman storage check to test the setup, or set storage.backend: sqlite to keep the workspace in the local database"
	switch s.Backend {
	case BackendNotion:
		if s.Notion.ParentPageID == "" {
			return errors.New("storage.notion.parent_page_id is not set: give the ID or URL of an existing Notion page" + hint)
		}
		if s.NotionToken() == "" {
			return fmt.Errorf("%s is not set: export the Notion integration token on this host%s", NotionTokenEnv, hint)
		}
	case BackendObsidian:
		if strings.TrimSpace(s.Obsidian.Vault) == "" {
			return errors.New("storage.obsidian.vault is not set: give the path of an existing vault directory" + hint)
		}
		info, err := os.Stat(s.Obsidian.Vault)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("storage.obsidian.vault %s is not an existing directory%s", s.Obsidian.Vault, hint)
		}
	}
	return nil
}
