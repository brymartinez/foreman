#!/usr/bin/env bash
# End-to-end checks for the built binary. Everything runs in a temporary directory
# against a local bare git origin, a stand-in gh, and stub agents, so no real
# repository, GitHub account, or model is touched.
#
#   scripts/e2e.sh            run every scenario
#   scripts/e2e.sh runner     run the named scenarios
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/foreman-e2e.XXXXXX")
trap '[ -n "${E2E_KEEP:-}" ] && echo "kept $work" || rm -rf "$work"' EXIT
failures=0

go build -o "$work/foreman" "$root/cmd/foreman"

pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; failures=$((failures + 1)); }
check() { # check <description> <command...>
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then pass "$what"; else fail "$what"; fi
}
contains() { grep -qF -- "$2" "$1"; }

# wait_status <text> [seconds] polls `foreman status` for text.
wait_status() {
	local want=$1 limit=${2:-20} i=0
	while [ $i -lt $((limit * 10)) ]; do
		if foreman status 2>/dev/null | grep -qF -- "$want"; then return 0; fi
		sleep 0.1
		i=$((i + 1))
	done
	return 1
}

foreman() { (cd "${FOREMAN_CWD:-$sandbox}" && "$work/foreman" "$@"); }

# setup creates a fresh sandbox: origin, a checkout, stub tools, and config.
setup() {
	sandbox="$work/$1"
	state="$sandbox/state"
	mkdir -p "$sandbox/bin" "$state"
	git init -q --bare -b main "$sandbox/origin.git"
	git clone -q "$sandbox/origin.git" "$sandbox/checkout" 2>/dev/null
	(
		cd "$sandbox/checkout"
		git config user.email e2e@example.com
		git config user.name e2e
		echo "package widgets" >widgets.go
		git add . && git commit -qm init && git push -q origin main
	)
	FOREMAN_CWD="$sandbox/checkout"

	cat >"$sandbox/config.yaml" <<EOF
tasks_dir: $sandbox/tasks
worktrees_dir: $sandbox/worktrees
repositories_dir: $sandbox/repositories
log_dir: $sandbox/logs
claude:
  command: $sandbox/bin/claude
codex:
  command: $sandbox/bin/codex
EOF
	mkdir -p "$sandbox/tasks" "$sandbox/stages"
	cp "$root"/defaults/stages/*.md "$sandbox/stages/"
	export FOREMAN_CONFIG="$sandbox/config.yaml" E2E_STATE="$state" E2E_ORIGIN="$sandbox/origin.git"
	export PATH="$sandbox/bin:$PATH"
	unset FOREMAN_TASKS_DIR

	issue 42 "Add retries"
	echo '[]' >"$state/search-issues-assignee.json"
	echo '[]' >"$state/search-issues-mentions.json"
	echo '[]' >"$state/search-prs-review-requested.json"
	echo '[]' >"$state/search-prs-mentions.json"

	cat >"$sandbox/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Stand-in gh: serves JSON from $E2E_STATE and records every call.
echo "gh $*" >>"$E2E_STATE/gh.log"
s=$E2E_STATE
case "$1 $2" in
"repo view") echo '{"nameWithOwner":"acme/widgets","defaultBranchRef":{"name":"main"}}' ;;
"repo clone") git clone -q "$E2E_ORIGIN" "$4" ;;
"issue view") cat "$s/issue-$3.json" ;;
"pr view") cat "$s/pr-$3.json" ;;
"pr list") cat "$s/pr-list.json" 2>/dev/null || echo '[]' ;;
"pr checks")
	if [ -f "$s/checks.json" ]; then cat "$s/checks.json"; else echo "no checks reported on the 'x' branch" >&2; exit 1; fi ;;
"search issues" | "search prs")
	kind=$2
	for a in "$@"; do case $a in --assignee=*) f=assignee ;; --mentions=*) f=mentions ;; --review-requested=*) f=review-requested ;; esac; done
	cat "$s/search-$kind-$f.json" ;;
"api --method")
	# Issue comments: POST creates, PATCH edits. The body is stored for later reads.
	if [ "$3" = PATCH ]; then id=${4##*/}; else id=$(($(cat "$s/comment-seq" 2>/dev/null || echo 3000) + 1)); echo "$id" >"$s/comment-seq"; fi
	n=$(echo "$4" | sed -n 's#.*/issues/\([0-9]*\)/comments#\1#p')
	printf '%s' "${6#body=}" >"$s/comment-$id.md"
	body=$(sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' "$s/comment-$id.md" | awk '{printf "%s\\n", $0}')
	printf '{"id":%s,"html_url":"https://github.com/acme/widgets/issues/%s#issuecomment-%s","body":"%s"}\n' "$id" "${n:-0}" "$id" "$body" | tee "$s/api-$id.json" ;;
"api repos/"*)
	n=${2##*/}
	if [ -f "$s/gone-$n" ]; then
		echo '{"message":"Not Found","status":"404"}'
		echo "gh: Not Found (HTTP 404)" >&2
		exit 1
	fi
	cat "$s/api-$n.json" ;;
*) echo "stand-in gh: unhandled $*" >&2; exit 2 ;;
esac
EOF

	# Implementer: commits a change, records an open PR for its branch, and reports it.
	cat >"$sandbox/bin/claude" <<'EOF'
#!/usr/bin/env bash
if [[ " $* " == *" --no-session-persistence "* ]]; then
	echo "claude review" >>"$E2E_STATE/agents.log"
	echo '{"type":"result","session_id":"r1","subtype":"success","total_cost_usd":0.05,"structured_output":{"verdict":"clean","summary":"looks right","findings":[]}}'
	exit 0
fi
# A planning turn starts with "## Plan first", or resumes one with its answers.
planning=
if [[ " $* " == *" --resume "* ]]; then
	if [[ "$2" == *"foreman posted your documents"* ]]; then
		echo "claude implement" >>"$E2E_STATE/agents.log"
		printf '%s' "$2" >"$E2E_STATE/plan-posted-prompt.txt"
	else
		echo "claude resume" >>"$E2E_STATE/agents.log"
		printf '%s' "$2" >"$E2E_STATE/resume-prompt.txt"
		[[ "$2" == *"planned, blocked, or failed"* ]] && planning=1
	fi
elif [[ "$*" == *"## Plan first"* ]]; then
	echo "claude plan" >>"$E2E_STATE/agents.log"
	printf '%s' "$*" >"$E2E_STATE/prompt.txt"
	planning=1
else
	echo "claude implement" >>"$E2E_STATE/agents.log"
	printf '%s' "$*" >"$E2E_STATE/prompt.txt"
fi
if [ -f "$E2E_STATE/block-once" ]; then
	rm "$E2E_STATE/block-once"
	q() { printf '{"issue":"%s","context":"c","options":[{"label":"A","description":"%s"},{"label":"B","description":"%s"}],"recommendation":"A","impactIfWrong":"i"}' "$1" "$2" "$3"; }
	printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"blocked","summary":"two decisions","prNumber":null,"prUrl":null,"validation":[],"questions":[%s,%s]}}\n' \
		"$(q 'Retry 4xx?' '5xx only' '4xx and 5xx')" "$(q 'Cap retries?' '3 tries' 'no cap')"
	exit 0
fi
if [ -n "$planning" ]; then
	echo '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.02,"structured_output":{"status":"planned","summary":"four documents","prNumber":null,"prUrl":null,"validation":[],"questions":[],"documents":{"analysis":"Retries are missing.","prd":"R1. Retry 5xx responses.","design":"Wrap the client.","plan":"### Task 1: retries"}}}'
	exit 0
fi
sleep "${E2E_AGENT_SLEEP:-1}"
branch=$(git branch --show-current)
echo "// retries" >>widgets.go
git -c user.email=e2e@example.com -c user.name=e2e commit -qam "feat: retries"
issue=${branch#*/}
pr=$((900 + ${issue%%-*}))
printf '{"number":%s,"url":"https://github.com/acme/widgets/pull/%s","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main","isCrossRepository":false}\n' "$pr" "$pr" "$branch" "$(git rev-parse HEAD)" >"$E2E_STATE/pr-$pr.json"
echo "claude stderr progress" >&2
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"Run the tests"}}]}}'
printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.25,"structured_output":{"status":"pr_opened","summary":"added retries","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
EOF

	# Reviewer on the codex JSONL protocol.
	cat >"$sandbox/bin/codex" <<'EOF'
#!/usr/bin/env bash
if [[ " $* " == *" --ephemeral "* ]]; then echo "codex review"; else echo "codex implement"; fi >>"$E2E_STATE/agents.log"
last=
while [ $# -gt 0 ]; do [ "$1" = -o ] && last=$2; shift; done
echo '{"type":"thread.started","thread_id":"t1"}'
sleep "${E2E_REVIEW_SLEEP:-0}"
echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5}}'
echo '{"verdict":"clean","summary":"looks right","findings":[]}' >"$last"
EOF

	# Notifications go through osascript on macOS and notify-send on Linux.
	for n in osascript notify-send; do
		printf '#!/usr/bin/env bash\necho "%s $*" >>"$E2E_STATE/notifications.log"\n' "$n" >"$sandbox/bin/$n"
	done
	chmod +x "$sandbox/bin/"*
}

issue() { # issue <number> <title> [labels-json]
	printf '{"number":%s,"title":"%s","body":"do it","labels":%s,"url":"https://github.com/acme/widgets/issues/%s","state":"OPEN","updatedAt":"2026-09-25T10:00:00Z"}\n' \
		"$1" "$2" "${3:-[]}" "$1" >"$state/issue-$1.json"
	printf '{"number":%s,"title":"%s","html_url":"https://github.com/acme/widgets/issues/%s","state":"open","updated_at":"2026-09-25T10:00:00Z"}\n' \
		"$1" "$2" "$1" >"$state/api-$1.json"
}

scenario_runner() {
	echo "runner: a detached runner outlives foreman start"
	setup runner
	check "start delegates #42" foreman start 42 --agent claude
	check "the runner reaches a clean review" wait_status "review ✓"
	local log
	log=$(ls "$sandbox"/logs/*.log)
	check "log has the runner's finish line" contains "$log" "finished"
	check "log lines are timestamped" grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' "$log"
	check "agent stderr reached the log" contains "$log" "claude stderr progress"
	check "no line is stamped twice" bash -c "! grep -qE '^[^ ]+ [0-9]{4}-[0-9]{2}-[0-9]{2}T' '$log'"
	check "the prompt carries the implement stage file" contains "$state/prompt.txt" "Add or update tests when behavior changes."
	check "the first turn plans" contains "$state/prompt.txt" "## Plan first"
	check "the four documents are posted on the issue" test "$(grep -c 'api --method POST repos/acme/widgets/issues/42/comments' "$state/gh.log")" = 4
	check "the PRD comment has its heading" contains "$state/comment-3002.md" "## PRD"
	check "the planning session implements the plan" contains "$state/plan-posted-prompt.txt" "- Implementation plan: https://github.com/acme/widgets/issues/42#issuecomment-3004"
	check "show lists the plan" bash -c "cd '$FOREMAN_CWD' && '$work/foreman' show 42 | grep -qF 'Plan          Analysis https://github.com/acme/widgets/issues/42#issuecomment-3001'"
	rm "$sandbox/stages/pr.md"
	check "a missing stage file stops foreman" bash -c "cd '$FOREMAN_CWD' && ! '$work/foreman' status 2>'$sandbox/err.txt' && grep -qF 'stages/pr.md' '$sandbox/err.txt'"
	check "config --stages exits nonzero" bash -c "cd '$FOREMAN_CWD' && ! '$work/foreman' config --stages >'$sandbox/stages.txt'"
	check "config --stages names it as missing" contains "$sandbox/stages.txt" "MISSING, required"
}

scenario_notify() {
	echo "notify: the developer hears about ready work and stopped runners"
	setup notify
	echo '[{"name":"test","bucket":"pending","link":"https://ci/1"}]' >"$state/checks.json"
	foreman start 42 --agent claude >/dev/null
	check "the runner reaches a clean review" wait_status "CI pending"
	check "a clean review with pending CI stays quiet" bash -c "[ ! -s '$state/notifications.log' ]"
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	foreman status >/dev/null
	foreman status >/dev/null
	check "ready work notifies once" bash -c "[ \$(grep -c 'Ready for human review' '$state/notifications.log') = 1 ]"

	issue 43 "Fix pagination"
	E2E_AGENT_SLEEP=30.017 foreman start 43 --agent claude >/dev/null
	sleep 0.5
	pkill -TERM -f "$work/foreman __run acme/widgets#43" || true
	check "a stopped runner records FAILED promptly" wait_status "FAILED" 5
	check "and notifies" grep -q 'Run cancelled.*acme/widgets#43' "$state/notifications.log"
	check "and leaves no agent behind" bash -c "! pgrep -f 'sleep 30.017' >/dev/null"

	issue 44 "Fix sorting"
	E2E_AGENT_SLEEP=30.017 foreman start 44 --agent claude >/dev/null
	sleep 0.5
	pkill -KILL -f "$work/foreman __run acme/widgets#44" || true
	foreman status >/dev/null
	check "a killed runner is reaped and notifies" grep -q 'Run failed.*acme/widgets#44' "$state/notifications.log"
	pkill -f "sleep 30.017" || true
}

scenario_logs() {
	echo "logs: clean, raw, and follow views of a run"
	setup logs
	E2E_AGENT_SLEEP=2 foreman start 42 --agent claude >/dev/null
	foreman logs 42 --follow >"$sandbox/follow.txt" 2>&1 &
	local follower=$!
	wait_status "review ✓" || true
	sleep 1
	kill $follower 2>/dev/null || true
	wait $follower 2>/dev/null || true
	check "follow shows the run through to the end" contains "$sandbox/follow.txt" "finished"
	check "follow never splits a line" bash -c "! grep -vE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' '$sandbox/follow.txt'"
	foreman logs 42 >"$sandbox/clean.txt"
	foreman logs 42 --raw >"$sandbox/raw.txt"
	check "clean shows Claude's result" contains "$sandbox/clean.txt" 'claude: subtype=success cost=$0.25'
	check "clean shows Claude's tool calls" contains "$sandbox/clean.txt" "claude Bash Run the tests"
	check "clean shows Claude's transcript" contains "$sandbox/clean.txt" "claude: transcript "
	check "raw keeps Claude JSON" contains "$sandbox/raw.txt" '"type":"assistant"'
	check "clean shows Codex usage without a price" contains "$sandbox/clean.txt" "(USD unavailable)"
	check "clean hides Codex JSON" bash -c "! grep -q '\"type\"' '$sandbox/clean.txt'"
	check "raw keeps Codex JSON" contains "$sandbox/raw.txt" '"type":"thread.started"'
	check "show reports usage" bash -c "cd '$FOREMAN_CWD' && '$work/foreman' show 42 | grep -F 'Claude \$0.27 (2 turns) · Codex 125 tokens (1 turn)'"
}

hit() { # hit <repo> <number> <title>
	printf '{"number":%s,"title":"%s","url":"https://github.com/%s/issues/%s","state":"OPEN","updatedAt":"2026-09-25T10:00:00Z","repository":{"nameWithOwner":"%s"}}' "$2" "$3" "$1" "$2" "$1"
}

scenario_inbox() {
	echo "inbox: first run, filters, and delegation from outside any checkout"
	setup inbox
	rm -rf "$sandbox/tasks"
	printf 'ignore_repositories: [acme/ignored]\n' >>"$sandbox/config.yaml"
	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	echo "[$(hit acme/widgets 950 'Review me')]" >"$state/search-prs-review-requested.json"
	FOREMAN_CWD=$sandbox

	check "non-interactive refuses a missing tasks directory" bash -c "! (cd '$sandbox' && '$work/foreman' inbox --non-interactive)"
	check "declining setup leaves no tasks directory" bash -c "echo n | (cd '$sandbox' && '$work/foreman' inbox) && [ ! -d '$sandbox/tasks' ]"

	printf 'y\n1\nclaude\n\n' | foreman inbox >"$sandbox/inbox.txt" 2>&1 || true
	check "setup creates the tasks directory" test -d "$sandbox/tasks"
	check "the picker shows repo-qualified items" contains "$sandbox/inbox.txt" "acme/widgets#42"
	check "ignored repositories are hidden" bash -c "! grep -q 'acme/ignored' '$sandbox/inbox.txt'"
	check "review requests aren't offered for delegation" bash -c "! grep -q 'acme/widgets#950' '$sandbox/inbox.txt'"
	check "the source checkout is cloned" test -d "$sandbox/repositories/acme-widgets/.git"
	check "the selection is delegated" wait_status "review ✓"

	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/widgets 43 'Fix pagination'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	foreman inbox --non-interactive </dev/null >"$sandbox/report.txt"
	check "non-interactive reports new work" contains "$sandbox/report.txt" "acme/widgets#43"
	check "non-interactive hides ignored repositories" bash -c "! grep -q 'acme/ignored' '$sandbox/report.txt'"

	echo "[$(hit acme/widgets 43 'Fix pagination')]" >"$state/search-issues-assignee.json"
	echo "[$(hit acme/widgets 951 'Mention me')]" >"$state/search-prs-mentions.json"
	foreman inbox --non-interactive </dev/null >"$sandbox/report2.txt"
	check "PR mentions are listed" contains "$sandbox/report2.txt" "acme/widgets#951"
	echo "[$(hit acme/widgets 43 'Fix pagination')]" >"$state/search-issues-mentions.json"
	foreman inbox --non-interactive </dev/null >/dev/null
	echo '[]' >"$state/search-issues-assignee.json"
	foreman inbox --non-interactive </dev/null >"$sandbox/report3.txt"
	check "a lost assignment is reported" contains "$sandbox/report3.txt" "no longer assigned to you"
	check "searches ask for gh's maximum" contains "$state/gh.log" "--limit=1000"

	foreman config --show >"$sandbox/show.txt"
	check "config --show lists the ignored repository" contains "$sandbox/show.txt" "Ignore repositories: acme/ignored"
	printf '#!/bin/sh\n[ "$1" = -w ] && echo "edited $2" >"%s/edited"\n' "$sandbox" >"$sandbox/bin/editor"
	chmod +x "$sandbox/bin/editor"
	check "config --edit runs an editor with arguments" bash -c "cd '$sandbox' && EDITOR='editor -w' VISUAL= '$work/foreman' config --edit && grep -qF '$sandbox/config.yaml' '$sandbox/edited'"
	cp "$sandbox/config.yaml" "$sandbox/config.good"
	printf 'ignore_repositories:\n  - "acme/typo\\""\n' >>"$sandbox/config.yaml"
	check "an invalid config stops other commands" bash -c "cd '$sandbox' && ! '$work/foreman' status 2>/dev/null"
	printf '#!/bin/sh\ncp "%s/config.good" "$1"\n' "$sandbox" >"$sandbox/bin/fixer"
	chmod +x "$sandbox/bin/fixer"
	check "config --edit still opens an invalid config" bash -c "cd '$sandbox' && EDITOR=fixer VISUAL= '$work/foreman' config --edit 2>'$sandbox/edit-err.txt' && grep -qF 'so you can fix it' '$sandbox/edit-err.txt'"
	check "and the fixed config loads" bash -c "cd '$sandbox' && '$work/foreman' status >/dev/null"
}

scenario_inbox_lifecycle() {
	echo "inbox lifecycle: departed items, inaccessible issues, and recovery"
	setup inbox_lifecycle
	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/widgets 43 'Unavailable')]" >"$state/search-issues-assignee.json"
	check "initial discovery succeeds" foreman inbox --non-interactive
	echo "[$(hit acme/widgets 44 'Current work')]" >"$state/search-issues-assignee.json"
	check "inaccessible historic item does not abort refresh" bash -c "'$work/foreman' inbox --non-interactive >'$sandbox/report.txt' 2>&1"
	check "lost involvement is reported" contains "$sandbox/report.txt" "no longer assigned to you"
	check "failed item is identified" contains "$sandbox/report.txt" "refresh acme/widgets#43"
	check "healthy new work survives failure" contains "$sandbox/report.txt" "acme/widgets#44"
	printf 'q\n' | foreman inbox >"$sandbox/picker.txt" 2>&1
	check "departed issue is not offered" bash -c "! grep -q 'acme/widgets#42' '$sandbox/picker.txt'"
	check "interactive warning is visible" contains "$sandbox/picker.txt" "refresh acme/widgets#43"
	check "departed issue is fetched only once" bash -c "[ \"\$(grep -c 'api repos/acme/widgets/issues/42' '$state/gh.log')\" = 1 ]"
	issue 43 'Available again'
	printf 'q\n' | foreman inbox >"$sandbox/recovered.txt" 2>&1
	check "interactive loss notice survives picker filtering" contains "$sandbox/recovered.txt" "no longer assigned to you"
	check "recovered item is not selectable" bash -c "! grep -qE '[0-9]+\. acme/widgets#43' '$sandbox/recovered.txt'"
	check "refresh after recovery succeeds" foreman inbox --non-interactive
	check "recovered departed issue stops being fetched" bash -c "[ \"\$(grep -c 'api repos/acme/widgets/issues/43' '$state/gh.log')\" = 3 ]"

	issue 45 'Deleted later'
	echo "[$(hit acme/widgets 44 'Current work'),$(hit acme/widgets 45 'Deleted later')]" >"$state/search-issues-assignee.json"
	foreman inbox --non-interactive >/dev/null
	echo "[$(hit acme/widgets 44 'Current work')]" >"$state/search-issues-assignee.json"
	touch "$state/gone-45"
	foreman inbox --non-interactive >"$sandbox/gone.txt" 2>&1
	check "a deleted issue is reported" contains "$sandbox/gone.txt" "foreman stopped checking it"
	check "without a warning" bash -c "! grep -q 'refresh acme/widgets#45' '$sandbox/gone.txt'"
	foreman inbox --non-interactive >"$sandbox/gone2.txt" 2>&1
	check "and only once" bash -c "! grep -q 'acme/widgets#45' '$sandbox/gone2.txt'"
	check "and is not looked up again" bash -c "[ \"\$(grep -c 'api repos/acme/widgets/issues/45' '$state/gh.log')\" = 1 ]"
}

turns() { grep -cx "$1" "$state/agents.log" || true; }

scenario_rereview() {
	echo "rereview: new commits and interrupted reviews are reviewed without reimplementing"
	setup rereview
	echo '[{"name":"test","bucket":"pending","link":"https://ci/1"}]' >"$state/checks.json"
	foreman start 42 --agent claude >/dev/null
	wait_status "CI pending" || true
	sed -i.bak 's/"headRefOid":"[0-9a-f]*"/"headRefOid":"feedfacefeedfacefeedfacefeedfacefeedface"/' "$state/pr-942.json"
	foreman status >/dev/null
	check "a push after the clean review is reviewed again" wait_status "CI pending"
	check "by the reviewer alone" test "$(turns 'claude implement') $(turns 'codex review')" = "1 2"
	check "the review covers the new head" grep -q 'head: feedface' "$sandbox/tasks/acme-widgets-42.md"

	issue 43 "Fix pagination"
	E2E_REVIEW_SLEEP=30.017 foreman start 43 --agent claude >/dev/null
	local i=0
	until [ "$(turns 'codex review')" = 3 ] || [ $i -gt 100 ]; do sleep 0.1; i=$((i + 1)); done
	pkill -KILL -f "$work/foreman __run acme/widgets#43" || true
	pkill -f "sleep 30.017" || true
	check "a runner killed mid-review is reaped" wait_status "FAILED" 5
	foreman start 43 --agent claude >/dev/null
	check "the restart finishes the review" bash -c "cd '$FOREMAN_CWD' && for i in \$(seq 50); do '$work/foreman' show 43 | grep -q 'review ✓\\|Latest review' && exit 0; sleep 0.1; done; exit 1"
	check "without implementing again" test "$(turns 'claude implement')" = 2
}

scenario_respond() {
	echo "respond: several questions from one turn, answered in one sitting"
	setup respond
	touch "$state/block-once"
	foreman start 42 --agent claude >/dev/null
	check "the task waits on both questions" wait_status "NEEDS_DECISION"
	foreman inbox --non-interactive </dev/null >"$sandbox/inbox.txt"
	check "inbox counts the second question" contains "$sandbox/inbox.txt" "Retry 4xx? (+1 more)"
	check "input ending early answers nothing more" bash -c "printf 'B\\n' | (cd '$FOREMAN_CWD' && '$work/foreman' respond 42) | grep -q '1 question is still waiting'"
	check "the task still waits" wait_status "NEEDS_DECISION" 1
	printf '\n' | foreman respond 42 >"$sandbox/respond.txt"
	check "respond relaunches once every question is answered" wait_status "review ✓"
	check "the agent resumed once, with both answers" test "$(turns 'claude resume')" = 1
	check "resume carries the first answer" contains "$state/resume-prompt.txt" "Decision: B. 4xx and 5xx"
	check "resume carries the second answer" contains "$state/resume-prompt.txt" "Decision: A. 3 tries"
}

scenario_cancel() {
	echo "cancel: stopping a run keeps its work and can be resumed"
	setup cancel
	E2E_AGENT_SLEEP=30.017 foreman start 42 --agent claude >/dev/null
	check "the runner is working" wait_status "WORKING" 5
	sleep 0.5
	foreman cancel 42 >"$sandbox/cancel.txt" 2>&1
	check "cancel reports the stop" contains "$sandbox/cancel.txt" "Cancelled acme/widgets#42: run cancelled during implementation (terminated signal received)"
	check "the task is FAILED" wait_status "FAILED" 1
	check "no runner or agent is left" bash -c "! pgrep -f 'sleep 30.017' >/dev/null && ! pgrep -f '$work/foreman __run acme/widgets#42' >/dev/null"
	local log
	log=$(ls "$sandbox"/logs/*.log)
	check "the log shows who stopped it" contains "$log" "foreman: cancel requested; stopping runner"
	check "a second cancel exits nonzero" bash -c "cd '$FOREMAN_CWD' && ! '$work/foreman' cancel 42 >'$sandbox/cancel2.txt' 2>&1"
	check "and says there is nothing to cancel" contains "$sandbox/cancel2.txt" "nothing to cancel"
	foreman start 42 --agent claude >/dev/null
	check "start resumes the cancelled task" wait_status "review ✓"
}

scenario_tui() {
	echo "tui: the dashboard needs a terminal, so a pipe still gets usage"
	setup tui
	for argv in "" "tui"; do
		foreman $argv </dev/null >"$sandbox/usage.txt" 2>&1
		check "foreman ${argv:-(no arguments)} exits 0 on a pipe" test $? -eq 0
		check "foreman ${argv:-(no arguments)} prints usage" contains "$sandbox/usage.txt" "foreman inbox [--non-interactive]"
	done
	check "the dashboard is in the usage text" contains "$sandbox/usage.txt" "Dashboard: inbox, delegated work, and logs"
	foreman status >"$sandbox/status.txt" 2>&1
	check "status is unchanged" contains "$sandbox/status.txt" "No delegated work."

	# The terminal check runs before the config is read, so nothing a piped `foreman`
	# never needed can make it fail where it used to print usage.
	mv "$sandbox/stages/implement.md" "$sandbox/implement.md"
	foreman </dev/null >"$sandbox/broken.txt" 2>&1
	check "a missing stage file still gets usage on a pipe" test $? -eq 0
	check "and prints usage, not the error" contains "$sandbox/broken.txt" "foreman inbox [--non-interactive]"
	mv "$sandbox/implement.md" "$sandbox/stages/implement.md"
}

scenarios=${*:-runner notify logs inbox inbox_lifecycle rereview respond cancel tui}
for s in $scenarios; do "scenario_$s"; done
if [ $failures -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"
