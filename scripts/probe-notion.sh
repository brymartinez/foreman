#!/usr/bin/env bash
# Re-verifies the Notion API behavior foreman's Notion backend depends on, against a
# real page. It is not part of the e2e suite and needs a token:
#
#   FOREMAN_NOTION_TOKEN=secret_... scripts/probe-notion.sh <parent page id or URL>
#
# The page must be shared with the integration. The probe creates the Tasks and
# Repository Knowledge databases under it if they are missing, creates one probe task,
# runs `foreman storage check` and `foreman storage move` against a throwaway SQLite
# workspace with one probe issue. It never reads or changes another page. The probe
# task stays in Tasks for you to trash.
# Rerun it after Notion changes its API version.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
[ -n "${FOREMAN_NOTION_TOKEN:-}" ] || { echo "set FOREMAN_NOTION_TOKEN" >&2; exit 2; }
[ $# -eq 1 ] || { echo "usage: $0 <parent page id or URL>" >&2; exit 2; }
parent="$1"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
go build -o "$work/foreman" "$root/cmd/foreman"

cat >"$work/config.yaml" <<YAML
database: $work/foreman.db
triage_dir: $work/triage
tasks_dir: $work/tasks
knowledge_dir: $work/knowledge
log_dir: $work/logs
notifications:
  enabled: false
storage:
  backend: sqlite
YAML
export FOREMAN_CONFIG="$work/config.yaml"
printf 'Probe scope.\n' >"$work/triage.md"
id="$(printf 'Created by scripts/probe-notion.sh.' | "$work/foreman" issue add --title "Foreman probe $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --description-file - --triage-file "$work/triage.md")"

sed -i.bak '/^storage:$/,/^  backend: sqlite$/d' "$work/config.yaml"
printf 'storage:\n  backend: notion\n  notion:\n    parent_page_id: %s\n' "$parent" >>"$work/config.yaml"

echo "# Notion-Version $(sed -n "s/^const Version = //p" "$root/internal/notion/client.go")"
"$work/foreman" storage check
"$work/foreman" storage move
"$work/foreman" storage move | grep -q 'already moved' && echo "ok   a second move changes nothing"
echo "The probe task $id stays in the Tasks database. Trash it in Notion when done."
