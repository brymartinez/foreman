#!/usr/bin/env bash
# Re-verifies the Notion API behavior foreman's Notion backend depends on, against a
# real page. It is not part of the e2e suite and needs a token:
#
#   FOREMAN_NOTION_TOKEN=secret_... scripts/probe-notion.sh <parent page id or URL>
#
# The page must be shared with the integration. The probe creates the Tasks and
# Repository Knowledge databases under it if they are missing, runs
# `foreman storage check`, then adds one probe issue and confirms its scope from a
# throwaway workspace, which publishes the entry and then updates it. It does not run
# `storage move`: a move expects the store to hold only the workspace it copies, so
# it would reject the entries a populated Tasks database already has. It never reads
# or changes another page. The probe task stays in Tasks for you to trash.
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
  backend: notion
  notion:
    parent_page_id: $parent
YAML
export FOREMAN_CONFIG="$work/config.yaml"
printf 'Probe scope.\n' >"$work/triage.md"

echo "# Notion-Version $(sed -n "s/^const Version = //p" "$root/internal/notion/client.go")"
"$work/foreman" storage check
id="$(printf 'Created by scripts/probe-notion.sh.' | "$work/foreman" issue add --title "Foreman probe $(date -u +%Y-%m-%dT%H:%M:%SZ)" --description-file -)"
echo "ok   created $id"
"$work/foreman" issue triage "$id" --file "$work/triage.md"
echo "ok   confirmed its scope, which updates the entry"
echo "The probe task $id stays in the Tasks database. Trash it in Notion when done."
