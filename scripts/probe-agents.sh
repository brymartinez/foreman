#!/usr/bin/env bash
# Re-verifies the agent CLI capabilities foreman's adapters depend on.
# Rerun after upgrading claude or codex. Costs a few cents of model usage.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
schema="$root/internal/agent/outcome.schema.json"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q -b main && git commit -q --allow-empty -m init
git worktree add -q -b agent/1-probe "$work/wt"
cd "$work/wt"

pass=0 fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; pass=$((pass+1)); else echo "FAIL $1: got '$2' want '$3'"; fail=$((fail+1)); fi; }

blocked_prompt='You must not change any files. Report status "blocked" with one entry in questions asking whether retries should apply to 4xx responses, two options A and B, recommendation A.'
resume_prompt='Decision: A. Report status "failed" with summary "probe complete". Do not change files.'
planned_prompt='You must not change any files. Report status "planned" with summary "probe plan" and documents: analysis "a", prd "p", design "d", plan "t".'

echo "# claude $(claude --version)"
sid="$(uuidgen | tr 'A-Z' 'a-z')"
claude -p "$blocked_prompt" --session-id "$sid" --output-format stream-json --verbose --json-schema "$(cat "$schema")" \
  --permission-mode acceptEdits --model haiku </dev/null >c1.jsonl 2>/dev/null
jq -c 'select(.type=="result")' c1.jsonl >c1.json
check "claude stream-json has one result event" "$(wc -l <c1.json | tr -d ' ')" 1
check "claude honors --session-id" "$(jq -r .session_id c1.json)" "$sid"
check "claude structured_output status" "$(jq -r .structured_output.status c1.json)" blocked
check "claude structured_output has questions" "$(jq -r '.structured_output.questions | length' c1.json)" 1
claude -p "$resume_prompt" --resume "$sid" --output-format stream-json --verbose --json-schema "$(cat "$schema")" \
  --permission-mode acceptEdits --model haiku </dev/null 2>/dev/null | jq -c 'select(.type=="result")' >c2.json
check "claude resume keeps session" "$(jq -r .session_id c2.json)" "$sid"
check "claude resume structured status" "$(jq -r .structured_output.status c2.json)" failed
check "claude keeps the transcript under the cwd's project dir" \
  "$(test -f "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects/$(pwd -P | sed 's/[^a-zA-Z0-9]/-/g')/$sid.jsonl" && echo yes || echo no)" yes
claude -p hi --resume "$(uuidgen | tr 'A-Z' 'a-z')" --output-format stream-json --verbose --model haiku </dev/null 2>/dev/null \
  | jq -c 'select(.type=="result")' >c5.json || true
check "claude missing session: error_during_execution, no turns" "$(jq -r '"\(.subtype) \(.num_turns)"' c5.json)" "error_during_execution 0"
claude -p "$planned_prompt" --output-format stream-json --verbose --json-schema "$(cat "$schema")" \
  --permission-mode acceptEdits --no-session-persistence --model haiku </dev/null 2>/dev/null | jq -c 'select(.type=="result")' >c6.json
check "claude planned outcome carries documents" "$(jq -r '"\(.structured_output.status) \(.structured_output.documents.prd)"' c6.json)" "planned p"
claude -p 'Run the shell command: touch allowed.txt. Then reply DONE.' --permission-mode acceptEdits \
  --allowedTools Bash --output-format json --model haiku </dev/null >c3.json 2>/dev/null
check "claude headless Bash via --allowedTools" "$(test -f allowed.txt && echo yes || echo no)" yes

claude -p 'Run the shell command: touch reviewer-wrote.txt. Then reply DONE or DENIED.' --permission-mode dontAsk \
  --allowedTools 'Read,Grep,Glob,Bash(git diff:*)' --no-session-persistence --output-format json --model haiku </dev/null >c4.json 2>/dev/null
check "claude reviewer (dontAsk) cannot write" "$(test -f reviewer-wrote.txt && echo wrote || echo denied)" denied

echo "# $(codex --version)"
codex exec --json -s workspace-write --output-schema "$schema" -o x1.json "$blocked_prompt" </dev/null >x1.jsonl 2>/dev/null
tid="$(head -1 x1.jsonl | jq -r 'select(.type=="thread.started").thread_id')"
check "codex emits thread.started" "$([ -n "$tid" ] && echo yes || echo no)" yes
check "codex output-schema status" "$(jq -r .status x1.json)" blocked
check "codex output-schema has questions" "$(jq -r '.questions | length' x1.json)" 1
codex exec --json --ephemeral -s read-only --output-schema "$schema" -o x7.json "$planned_prompt" </dev/null >/dev/null 2>&1
check "codex planned outcome carries documents" "$(jq -r '"\(.status) \(.documents.prd)"' x7.json)" "planned p"
codex exec resume --json --output-schema "$schema" -o x2.json "$tid" "$resume_prompt" </dev/null >x2.jsonl 2>/dev/null
check "codex resume keeps thread" "$(head -1 x2.jsonl | jq -r .thread_id)" "$tid"
check "codex resume structured status" "$(jq -r .status x2.json)" failed
codex exec resume --json -c 'sandbox_mode="workspace-write"' -o x6.txt "$tid" "Run: touch resumed.txt ; reply OK." </dev/null >/dev/null 2>&1
check "codex resume can write via -c sandbox_mode" "$(test -f resumed.txt && echo yes || echo no)" yes
codex exec -s workspace-write -c sandbox_workspace_write.network_access=true -o x3.txt \
  "Run exactly: curl -s -o /dev/null -w '%{http_code}' https://api.github.com ; reply with only the printed code." </dev/null >/dev/null 2>&1
check "codex network with network_access=true" "$(tr -d '[:space:]' <x3.txt)" 200
codex exec -s workspace-write -c sandbox_workspace_write.network_access=true -o x5.txt \
  "Run exactly: gh api user --jq .login ; reply with only its stdout, or FAILED." </dev/null >/dev/null 2>&1
check "codex sandbox can use gh credentials" "$(tr -d '[:space:]' <x5.txt)" "$(gh api user --jq .login)"
# The probe lives under $TMPDIR, which workspace-write can write by default; real clones
# don't. Excluding the temp dirs makes these checks see what a real worktree sees.
notmp=(-c sandbox_workspace_write.exclude_slash_tmp=true -c sandbox_workspace_write.exclude_tmpdir_env_var=true)
roots="sandbox_workspace_write.writable_roots=[\"$(git rev-parse --path-format=absolute --git-common-dir)\"]"
commit_prompt() { echo "Create $1 containing hi, then run: git add $1 && git commit -q -m probe. Reply OK or the git error."; }
codex exec -s workspace-write "${notmp[@]}" -o x4.txt "$(commit_prompt f0.txt)" </dev/null >/dev/null 2>&1
check "codex can't commit in a worktree without the git dir as a root" "$(git -C "$work" log --oneline agent/1-probe | wc -l | tr -d ' ')" 1
git reset -q && rm -f f0.txt
codex exec -s workspace-write "${notmp[@]}" -c "$roots" -o x4.txt "$(commit_prompt f1.txt)" </dev/null >/dev/null 2>&1
check "codex commits in a worktree with the git dir as a writable root" "$(git -C "$work" log --oneline agent/1-probe | wc -l | tr -d ' ')" 2
codex exec resume -c 'sandbox_mode="workspace-write"' "${notmp[@]}" -c "$roots" -o x4.txt "$tid" "$(commit_prompt f2.txt)" </dev/null >/dev/null 2>&1
check "codex resume commits with the git dir as a writable root" "$(git -C "$work" log --oneline agent/1-probe | wc -l | tr -d ' ')" 3

codex exec --ephemeral -s read-only -o x7.txt "Run: touch reviewer-wrote-codex.txt ; reply OK or the error." </dev/null >/dev/null 2>&1
check "codex reviewer (read-only) cannot write" "$(test -f reviewer-wrote-codex.txt && echo wrote || echo denied)" denied

echo "# $pass passed, $fail failed"
[ "$fail" -eq 0 ]
