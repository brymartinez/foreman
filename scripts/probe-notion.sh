#!/usr/bin/env bash
# Probe a designated disposable Notion page shared with FOREMAN_NOTION_TOKEN.
# The default mode reads back a CLI-created task, its payload, and its confirmation.
# --workflow drives a local sandbox against live Notion with stub agents and GitHub.
# Both modes leave the created databases and probe task on the disposable page.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
[ -n "${FOREMAN_NOTION_TOKEN:-}" ] || { echo "set FOREMAN_NOTION_TOKEN" >&2; exit 2; }
mode=readback
if [ "${1:-}" = "--workflow" ]; then
	mode=workflow
	shift
fi
[ $# -eq 1 ] || { echo "usage: $0 [--workflow] <disposable parent page ID or URL>" >&2; exit 2; }
export FOREMAN_PROBE_NOTION_VERSION="$(sed -n 's/^const Version = "\([^"]*\)".*/\1/p' "$root/internal/notion/client.go")"
[ -n "$FOREMAN_PROBE_NOTION_VERSION" ] || { echo "could not read the Notion API version" >&2; exit 1; }
unset FOREMAN_NOTION_API
probe() { python3 -I "$root/scripts/notion-probe.py" "$@"; }
parent="$(probe page-id "$1")"
cd "$root"

if [ "$mode" = "workflow" ]; then
	export FOREMAN_PROBE_NOTION_PARENT="$parent"
	exec "$root/scripts/e2e.sh" notion_live
fi

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

echo "# Notion-Version $FOREMAN_PROBE_NOTION_VERSION"
"$work/foreman" storage check
source="$(probe source "$parent")"
title="Foreman probe $(date -u +%Y-%m-%dT%H:%M:%SZ)"
id="$(printf 'Created by scripts/probe-notion.sh.' | "$work/foreman" issue add --title "$title" --description-file -)"
probe check "$source" "$id" "$title"
echo "ok   read back the published task $id and its record"
"$work/foreman" issue triage "$id" --file "$work/triage.md"
probe check "$source" "$id" "$title" "Probe scope."
echo "ok   read back the confirmed scope"
echo "The probe task $id stays in the disposable Tasks database."
