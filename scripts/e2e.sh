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
listeners=
trap '[ -z "$listeners" ] || kill $listeners 2>/dev/null; [ -n "${E2E_KEEP:-}" ] && echo "kept $work" || rm -rf "$work"' EXIT
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
  model: opus
  plan_model: sonnet
  review_model: opus
codex:
  command: $sandbox/bin/codex
  model: gpt-6-sol
  plan_model: gpt-6-luna
  review_model: gpt-6-sol
  reasoning_effort: medium
  plan_reasoning_effort: xhigh
  review_reasoning_effort: medium
notifications:
  enabled: false
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
	# A reply in an inline thread answers that thread and nothing else.
	case "$4" in
	*/replies)
		printf '{"id":%s,"html_url":"https://github.com/acme/widgets/pull/0#discussion_r%s","body":"%s"}\n' "$id" "$id" "$body"
		exit 0 ;;
	esac
	# A comment on a pull request joins its conversation, so the next read sees it.
	conv="$s/conv-repos-acme-widgets-issues-$n-comments.json"
	if [ -f "$conv" ]; then
		entry=$(printf '{"id":%s,"html_url":"https://github.com/acme/widgets/pull/%s#issuecomment-%s","body":"%s","updated_at":"2026-10-01T10:00:00Z","user":{"login":"brymartinez"}}' "$id" "$n" "$id" "$body")
		cur=$(cat "$conv")
		if [ "$cur" = "[[]]" ]; then printf '[[%s]]\n' "$entry" >"$conv"; else printf '%s,%s]]\n' "${cur%]]}" "$entry" >"$conv"; fi
	fi
	printf '{"id":%s,"html_url":"https://github.com/acme/widgets/issues/%s#issuecomment-%s","body":"%s"}\n' "$id" "${n:-0}" "$id" "$body" | tee "$s/api-$id.json" ;;
"api repos/"*)
	# A pull request's conversation: the comments, the inline comments, and the reviews.
	case "$2" in
	*/git/ref/heads/*)
		git --git-dir="$E2E_ORIGIN" rev-parse "${2#*/git/ref/heads/}"
		exit 0 ;;
	*/issues/*/comments | */pulls/*/comments | */pulls/*/reviews)
		f="$s/conv-$(echo "$2" | tr '/' '-').json"
		if [ -f "$f" ]; then cat "$f"; else echo '[[]]'; fi
		exit 0 ;;
	esac
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
for arg in "$@"; do case "$arg" in sonnet|opus) echo "claude model=$arg" >>"$E2E_STATE/models.log" ;; esac; done
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
		# A merge conflict: merge main, keep both sides, push, and report the PR.
		if [[ "$2" == *"conflicts with the latest origin/main"* ]]; then
			echo "claude merge" >>"$E2E_STATE/agents.log"
			b=$(git branch --show-current); i=${b#*/}; pr=$((900 + ${i%%-*}))
			git fetch -q origin main
			git -c user.email=e2e@example.com -c user.name=e2e merge -q origin/main >/dev/null 2>&1 || true
			git show origin/main:widgets.go >widgets.go
			echo "// retries" >>widgets.go
			git add widgets.go
			git -c user.email=e2e@example.com -c user.name=e2e commit -q --no-edit
			git push -q origin "$b"
			printf '{"number":%s,"url":"https://github.com/acme/widgets/pull/%s","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main","isCrossRepository":false,"body":"Retry 5xx only.\\n\\nCloses #42"}\n' "$pr" "$pr" "$b" "$(git rev-parse HEAD)" >"$E2E_STATE/pr-$pr.json"
			printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"pr_opened","summary":"merged main","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
			exit 0
		fi
		# Repairing a merge conflict: merge the base, or fail when told to.
		if [[ "$2" == *"now has merge conflicts"* ]]; then
			echo "claude conflict" >>"$E2E_STATE/agents.log"
			if [ -f "$E2E_STATE/conflict-fail" ]; then
				echo '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"failed","summary":"the merge breaks the tests","prNumber":null,"prUrl":null,"validation":[],"questions":[]}}'
				exit 0
			fi
			git fetch -q origin main
			git -c user.email=e2e@example.com -c user.name=e2e merge -q --no-edit origin/main
			b=$(git branch --show-current); i=${b#*/}; pr=$((900 + ${i%%-*}))
			git push -q origin "$b"
			printf '{"number":%s,"url":"https://github.com/acme/widgets/pull/%s","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main","isCrossRepository":false,"mergeable":"MERGEABLE","body":"Retry 5xx only.\\n\\nCloses #42"}\n' "$pr" "$pr" "$b" "$(git rev-parse HEAD)" >"$E2E_STATE/pr-$pr.json"
			printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"pr_opened","summary":"merged main","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
			exit 0
		fi
		# Answering PR feedback: reply where the comment is and change no code.
		if [[ "$2" == *"you have not answered"* ]]; then
			echo "claude feedback" >>"$E2E_STATE/agents.log"
			printf '%s' "$2" >>"$E2E_STATE/feedback-prompt.txt"
			b=$(git branch --show-current); i=${b#*/}; pr=$((900 + ${i%%-*}))
			gh api --method POST "repos/acme/widgets/issues/$pr/comments" -f body="<!-- foreman:reply --> Capped at three." >/dev/null
			printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"pr_opened","summary":"answered the comment","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
			exit 0
		fi
	fi

elif [[ "$*" == *"## Plan first"* ]]; then
	echo "claude plan" >>"$E2E_STATE/agents.log"
	printf '%s' "$*" >"$E2E_STATE/prompt.txt"
	planning=1
else
	echo "claude implement" >>"$E2E_STATE/agents.log"
	printf '%s' "$*" >"$E2E_STATE/plan-posted-prompt.txt"
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
git push -q origin "$branch"
# main-moves is a command that changes main while this turn runs: a change to
# widgets.go conflicts with the branch, and a new file merges cleanly.
if [ -f "$E2E_STATE/main-moves" ]; then
	m=$(mktemp -d)
	git clone -q "$E2E_ORIGIN" "$m"
	(cd "$m" && eval "$(cat "$E2E_STATE/main-moves")" && git add . && git -c user.email=e2e@example.com -c user.name=e2e commit -qm "main moves" && git push -q origin main)
	rm -rf "$m" "$E2E_STATE/main-moves"
fi
issue=${branch#*/}
pr=$((900 + ${issue%%-*}))
printf '{"number":%s,"url":"https://github.com/acme/widgets/pull/%s","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main","isCrossRepository":false,"body":"Retry 5xx only.\\n\\nCloses #42"}\n' "$pr" "$pr" "$branch" "$(git rev-parse HEAD)" >"$E2E_STATE/pr-$pr.json"
echo "claude stderr progress" >&2
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"Run the tests"}}]}}'
printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.25,"structured_output":{"status":"pr_opened","summary":"added retries","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
EOF

	# Reviewer on the codex JSONL protocol.
	cat >"$sandbox/bin/codex" <<'EOF'
#!/usr/bin/env bash
for arg in "$@"; do case "$arg" in gpt-6-luna|gpt-6-sol) echo "codex model=$arg" >>"$E2E_STATE/models.log" ;; model_reasoning_effort=*) echo "codex $arg" >>"$E2E_STATE/models.log" ;; esac; done
if [[ " $* " == *" --ephemeral "* ]]; then echo "codex review"; printf '%s' "${@: -1}" >"$E2E_STATE/review-prompt.txt"; else echo "codex implement"; fi >>"$E2E_STATE/agents.log"
last=
while [ $# -gt 0 ]; do [ "$1" = -o ] && last=$2; shift; done
echo '{"type":"thread.started","thread_id":"t1"}'
sleep "${E2E_REVIEW_SLEEP:-0}"
echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5}}'
echo '{"verdict":"clean","summary":"looks right","findings":[]}' >"$last"
EOF

	chmod +x "$sandbox/bin/"*
}

# webhook starts a stand-in Discord webhook and points webhook_url at it. It
# appends each delivered body to $state/notifications.log and answers with the
# status in $state/webhook-status, 204 by default.
webhook() {
	python3 - "$state" <<'PY' &
import http.server, os, sys
state = sys.argv[1]
class Hook(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            status = int(open(os.path.join(state, "webhook-status")).read())
        except (OSError, ValueError):
            status = 204
        if status < 300:
            with open(os.path.join(state, "notifications.log"), "ab") as f:
                f.write(body + b"\n")
        self.send_response(status)
        self.end_headers()
    def log_message(self, *args):
        pass
srv = http.server.HTTPServer(("127.0.0.1", 0), Hook)
with open(os.path.join(state, "webhook-port.tmp"), "w") as f:
    f.write(str(srv.server_port))
os.rename(os.path.join(state, "webhook-port.tmp"), os.path.join(state, "webhook-port"))
srv.serve_forever()
PY
	listeners="$listeners $!"
	local i=0
	while [ ! -f "$state/webhook-port" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
	notifications_config "  webhook_url: http://127.0.0.1:$(cat "$state/webhook-port")/api/webhooks/1/e2e-secret"
}

# notifications_config <yaml line> replaces the notifications block, which setup
# writes last in the config.
notifications_config() {
	sed -i.bak '/^notifications:/,$d' "$sandbox/config.yaml"
	printf 'notifications:\n%s\n' "$1" >>"$sandbox/config.yaml"
	chmod 600 "$sandbox/config.yaml"
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
	check "a fresh session implements the posted plan" contains "$state/plan-posted-prompt.txt" "- Implementation plan: https://github.com/acme/widgets/issues/42#issuecomment-3004"
	check "planning uses its Claude model" contains "$state/models.log" "claude model=sonnet"
	check "implementation uses its Claude model" contains "$state/models.log" "claude model=opus"
	check "review uses its Codex model" contains "$state/models.log" "codex model=gpt-6-sol"
	check "review uses medium Codex effort" contains "$state/models.log" 'codex model_reasoning_effort="medium"'
	check "the review carries the PR description" contains "$state/review-prompt.txt" "> Retry 5xx only."
	check "the review leaves out the issue body" bash -c "! grep -qF 'do it' '$state/review-prompt.txt'"
	check "the review leaves out the PRD" bash -c "! grep -qF 'R1. Retry 5xx responses.' '$state/review-prompt.txt'"
	check "show lists the plan" bash -c "cd '$FOREMAN_CWD' && '$work/foreman' show 42 | grep -qF 'Plan          Analysis https://github.com/acme/widgets/issues/42#issuecomment-3001'"
	rm "$sandbox/stages/pr.md"
	check "a missing stage file stops foreman" bash -c "cd '$FOREMAN_CWD' && ! '$work/foreman' status 2>'$sandbox/err.txt' && grep -qF 'stages/pr.md' '$sandbox/err.txt'"
	check "config --stages exits nonzero" bash -c "cd '$FOREMAN_CWD' && ! '$work/foreman' config --stages >'$sandbox/stages.txt'"
	check "config --stages names it as missing" contains "$sandbox/stages.txt" "MISSING, required"
}

scenario_notify() {
	echo "notify: the developer hears about ready work and stopped runners on Discord"
	setup notify
	webhook
	echo '[{"name":"test","bucket":"pending","link":"https://ci/1"}]' >"$state/checks.json"
	foreman start 42 --agent claude >/dev/null
	check "the runner reaches a clean review" wait_status "CI pending"
	check "a clean review with pending CI stays quiet" bash -c "[ ! -s '$state/notifications.log' ]"
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	foreman status >/dev/null
	foreman status >/dev/null
	check "ready work notifies once" bash -c "[ \$(grep -c 'Ready for human review' '$state/notifications.log') = 1 ]"
	check "the post carries the state, the task, and the PR link" contains "$state/notifications.log" 'acme/widgets#42 (READY_FOR_HUMAN)\nhttps://github.com/acme/widgets/pull/'
	check "the post cannot mention anyone" contains "$state/notifications.log" '"allowed_mentions":{"parse":[]}'

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

	echo 500 >"$state/webhook-status"
	issue 45 "Fix caching"
	foreman start 45 --agent claude >/dev/null
	local i=0
	while ! grep -qF 'acme/widgets#45' "$sandbox/logs/notifications.log" 2>/dev/null && [ $i -lt 100 ]; do
		foreman status >/dev/null
		sleep 0.2
		i=$((i + 1))
	done
	foreman show 45 >"$sandbox/show-45.txt"
	check "a failing webhook leaves the task ready" contains "$sandbox/show-45.txt" "READY_FOR_HUMAN"
	check "and logs the delivery error" grep -q 'acme/widgets#45 Ready for human review: Discord returned 500' "$sandbox/logs/notifications.log"
	check "no log holds the webhook URL" bash -c "! grep -rqF e2e-secret '$sandbox/logs'"
	foreman config --show >"$sandbox/show-config.txt"
	check "config --show reports the webhook as set" contains "$sandbox/show-config.txt" "Notification webhook: set"
	check "config --show hides the webhook URL" bash -c "! grep -qF e2e-secret '$sandbox/show-config.txt'"

	chmod 644 "$sandbox/config.yaml"
	check "a config readable by others warns" bash -c "cd '$FOREMAN_CWD' && '$work/foreman' status 2>'$sandbox/perm-err.txt' >/dev/null && grep -qF 'chmod 600 $sandbox/config.yaml' '$sandbox/perm-err.txt'"
	notifications_config "  enabled: true"
	check "a missing webhook URL warns and status still works" bash -c "cd '$FOREMAN_CWD' && '$work/foreman' status 2>'$sandbox/setup-err.txt' >/dev/null && grep -qF 'notifications.webhook_url is not set' '$sandbox/setup-err.txt'"
	# wait reaps the listener, so bash prints no "Terminated" line for it.
	kill $listeners 2>/dev/null || true
	wait $listeners 2>/dev/null || true
	listeners=
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
	printf 'exclude_repositories: [acme/ignored]\n' >>"$sandbox/config.yaml"
	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	echo "[$(hit acme/widgets 950 'Review me')]" >"$state/search-prs-review-requested.json"
	FOREMAN_CWD=$sandbox

	check "non-interactive refuses a missing tasks directory" bash -c "! (cd '$sandbox' && '$work/foreman' inbox --non-interactive)"
	check "declining setup leaves no tasks directory" bash -c "echo n | (cd '$sandbox' && '$work/foreman' inbox) && [ ! -d '$sandbox/tasks' ]"

	printf 'y\n1\nclaude\n\n' | foreman inbox >"$sandbox/inbox.txt" 2>&1 || true
	check "setup creates the tasks directory" test -d "$sandbox/tasks"
	check "the picker shows repo-qualified items" contains "$sandbox/inbox.txt" "acme/widgets#42"
	check "excluded repositories are hidden" bash -c "! grep -q 'acme/ignored' '$sandbox/inbox.txt'"
	check "review requests aren't offered for delegation" bash -c "! grep -q 'acme/widgets#950' '$sandbox/inbox.txt'"
	check "the source checkout is cloned" test -d "$sandbox/repositories/acme-widgets/.git"
	check "the selection is delegated" wait_status "review ✓"

	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/widgets 43 'Fix pagination'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	foreman inbox --non-interactive </dev/null >"$sandbox/report.txt"
	check "non-interactive reports new work" contains "$sandbox/report.txt" "acme/widgets#43"
	check "non-interactive hides excluded repositories" bash -c "! grep -q 'acme/ignored' '$sandbox/report.txt'"

	cp "$sandbox/config.yaml" "$sandbox/config.exclude"
	printf 'include_repositories: [acme/ignored, acme/other]\n' >>"$sandbox/config.yaml"
	echo "[$(hit acme/widgets 43 'Fix pagination'),$(hit acme/other 5 'Included'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	foreman inbox --non-interactive </dev/null >"$sandbox/include.txt"
	check "the include list shows its repositories" contains "$sandbox/include.txt" "acme/other#5"
	check "the include list beats the exclude list" contains "$sandbox/include.txt" "acme/ignored#7"
	check "the include list hides other repositories" bash -c "! grep -q 'acme/widgets#43' '$sandbox/include.txt'"
	foreman config --show >"$sandbox/show-include.txt"
	check "config --show lists the included repositories" contains "$sandbox/show-include.txt" "Include repositories: acme/ignored, acme/other"
	printf 'v\nq\n' | foreman inbox >"$sandbox/view.txt" 2>&1
	check "the picker names the configured view" contains "$sandbox/view.txt" "INBOX (configured repositories)"
	check "v in the picker lists every repository" bash -c "sed -n '/INBOX (all repositories)/,\$p' '$sandbox/view.txt' | grep -q 'acme/widgets#43'"
	cp "$sandbox/config.exclude" "$sandbox/config.yaml"

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
	check "config --show lists the excluded repository" contains "$sandbox/show.txt" "Exclude repositories: acme/ignored"
	check "config --show lists the plan model" contains "$sandbox/show.txt" "Codex plan model: gpt-6-luna"
	check "config --show lists the plan effort" contains "$sandbox/show.txt" "Codex plan reasoning effort: xhigh"
	printf '#!/bin/sh\n[ "$1" = -w ] && echo "edited $2" >"%s/edited"\n' "$sandbox" >"$sandbox/bin/editor"
	chmod +x "$sandbox/bin/editor"
	check "config --edit runs an editor with arguments" bash -c "cd '$sandbox' && EDITOR='editor -w' VISUAL= '$work/foreman' config --edit && grep -qF '$sandbox/config.yaml' '$sandbox/edited'"
	cp "$sandbox/config.yaml" "$sandbox/config.good"
	printf 'exclude_repositories:\n  - "acme/typo\\""\n' >>"$sandbox/config.yaml"
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

scenario_conflicts() {
	echo "conflicts: a PR that conflicts with main is merged before its review"
	setup conflicts
	echo 'echo "// main" >>widgets.go' >"$state/main-moves"
	foreman start 42 --agent claude >/dev/null
	check "the merged PR reaches a clean review" wait_status "review ✓"
	check "the implementer merged once" test "$(turns 'claude merge')" = 1
	check "the merge took no fix round" test "$(turns 'codex review')" = 1
	local branch head
	branch=$(git -C "$sandbox/origin.git" for-each-ref --format='%(refname:short)' 'refs/heads/*/42-*')
	head=$(git -C "$sandbox/origin.git" rev-parse "$branch")
	check "the PR branch has a merge commit" test "$(git -C "$sandbox/origin.git" rev-list --merges --count main.."$branch")" = 1
	check "main is merged in, not rebased onto" git -C "$sandbox/origin.git" merge-base --is-ancestor main "$branch"
	check "the review covers the merge commit" grep -q "head: $head" "$sandbox/tasks/acme-widgets-42.md"

	issue 43 "Fix pagination"
	echo 'echo "package widgets" >other.go' >"$state/main-moves"
	foreman start 43 --agent claude >/dev/null
	check "a clean base update goes straight to review" bash -c "cd '$FOREMAN_CWD' && for i in \$(seq 100); do '$work/foreman' show 43 | grep -q 'review ✓\\|Latest review' && exit 0; sleep 0.1; done; exit 1"
	check "with no merge turn" test "$(turns 'claude merge')" = 1
	branch=$(git -C "$sandbox/origin.git" for-each-ref --format='%(refname:short)' 'refs/heads/*/43-*')
	check "and no merge commit" test "$(git -C "$sandbox/origin.git" rev-list --merges --count "$branch")" = 0
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

scenario_routing() {
	echo "routing: routing.yaml is reported, and a routed task keeps its profile"
	setup routing
	foreman config --show >"$sandbox/show-off.txt"
	check "config --show reports routing off without routing.yaml" contains "$sandbox/show-off.txt" "Routing: off (no "
	cp "$root/defaults/routing.yaml" "$sandbox/routing.yaml"
	foreman config --show >"$sandbox/show-on.txt"
	check "config --show reports the starter file" contains "$sandbox/show-on.txt" "Routing profiles: claude (default), codex"
	printf 'profiles: {}\n' >"$sandbox/routing.yaml"
	check "an invalid routing.yaml fails config --show" bash -c "cd '$sandbox' && ! '$work/foreman' config --show >'$sandbox/show-bad.txt'"
	check "and names the problem" contains "$sandbox/show-bad.txt" "routing.yaml: profiles: none defined"
	check "an invalid routing.yaml leaves other commands working" foreman status
	rm "$sandbox/routing.yaml"

	# A dashboard start routed #42 to a profile that plans with opus, and the run failed.
	cat >"$sandbox/tasks/acme-widgets-42.md" <<EOF2
---
repo: acme/widgets
number: 42
kind: issue
title: Add retries
url: https://github.com/acme/widgets/issues/42
state: FAILED
implementation_agent: claude
profile:
  name: deep
  agent: claude
  plan_model: opus
  model: opus
route_reason: broad or ambiguous design work
---
EOF2
	check "a retry with the same agent starts" foreman start 42 --agent claude
	check "the run reaches a clean review" wait_status "review ✓"
	local log
	log=$(ls "$sandbox"/logs/*.log)
	check "the log has the route line" contains "$log" "route:deep:claude:broad or ambiguous design work"
	check "the route line comes before the first turn" bash -c "grep -nE 'route:|claude:[a-z]+:plan' '$log' | head -1 | grep -qF route:"
	check "planning uses the profile's model" bash -c "! grep -qF 'claude model=sonnet' '$state/models.log'"
	check "the competing agent still reviews" contains "$state/agents.log" "codex review"
	foreman show 42 >"$sandbox/show.txt"
	check "show lists the profile and reason" contains "$sandbox/show.txt" "Profile       deep (plan opus, implement opus) · broad or ambiguous design work"
	check "the task file keeps the profile" contains "$sandbox/tasks/acme-widgets-42.md" "route_logged: true"
}

scenario_feedback() {
	echo "feedback: a comment on the PR comes back to the agent"
	setup feedback
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	foreman start 42 --agent claude >/dev/null
	check "the run reaches the developer" wait_status "READY_FOR_HUMAN"

	# The developer comments on the pull request.
	conv="$state/conv-repos-acme-widgets-issues-942-comments.json"
	printf '[[{"id":4401,"html_url":"https://github.com/acme/widgets/pull/942#issuecomment-4401","body":"Cap the retries.","updated_at":"2026-10-01T09:00:00Z","user":{"login":"brymartinez"}}]]\n' >"$conv"
	foreman status >/dev/null
	check "a new comment makes the task REVIEWED" wait_status "REVIEWED" 5
	foreman inbox --non-interactive >"$sandbox/inbox.txt"
	check "inbox lists it under REVIEWED" contains "$sandbox/inbox.txt" "REVIEWED"
	check "inbox counts the comment and names who left it" contains "$sandbox/inbox.txt" "PR #942 · 1 comment from brymartinez"

	foreman resume 42 >"$sandbox/resume.txt"
	check "resume reports the same PR" contains "$sandbox/resume.txt" "Resumed acme/widgets#42"
	check "the task comes back once the comment is answered" wait_status "READY_FOR_HUMAN" 15
	check "the agent saw the comment" contains "$state/feedback-prompt.txt" "Cap the retries."
	check "and was told where to reply" contains "$state/feedback-prompt.txt" "repos/acme/widgets/issues/942/comments"
	check "the agent kept its session" test "$(turns 'claude feedback')" = 1
	check "answering needed no new review" test "$(turns 'codex review')" = 1

	# The agent's own reply carries the marker, so it is not read back as feedback.
	foreman status >"$sandbox/status.txt"
	check "the reply did not start another round" contains "$sandbox/status.txt" "READY_FOR_HUMAN"
	check "and the agent was not asked again" test "$(turns 'claude feedback')" = 1
	foreman show 42 >"$sandbox/show.txt"
	check "show lists the answered comment" contains "$sandbox/show.txt" "answered"
}

# conflicts marks PR #942 as conflicting with its base.
conflicts() {
	sed -i.bak 's/,"mergeable":"[A-Z]*"//; s/"isCrossRepository":false/"isCrossRepository":false,"mergeable":"CONFLICTING"/' "$state/pr-942.json"
}

scenario_conflict() {
	echo "conflict: a handed-over PR that conflicts with main is repaired once"
	setup conflict
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	foreman start 42 --agent claude >/dev/null
	check "the run reaches the developer" wait_status "READY_FOR_HUMAN"
	(
		cd "$sandbox/checkout"
		echo "package widgets // main moved" >main.go
		git add main.go && git commit -qm "main moves on" && git push -q origin main
	)

	conflicts
	foreman status >"$sandbox/status.txt"
	check "the refresh starts a repair" contains "$sandbox/status.txt" "WORKING"
	foreman status >/dev/null
	check "the repair is reviewed and handed back" wait_status "READY_FOR_HUMAN" 15
	check "the implementer repaired it once" test "$(turns 'claude conflict')" = 1
	check "in its own session" test "$(turns 'claude implement')" = 1
	check "the reviewer reviewed the merge" test "$(turns 'codex review')" = 2
	local wt
	wt=$(ls -d "$sandbox"/worktrees/*/*)
	check "the branch now includes main" git -C "$wt" merge-base --is-ancestor "$(git --git-dir="$sandbox/origin.git" rev-parse main)" HEAD

	touch "$state/conflict-fail"
	conflicts
	foreman status >/dev/null
	check "a failed repair needs a decision" wait_status "NEEDS_DECISION" 15
	foreman status >/dev/null
	foreman status >/dev/null
	check "the same heads are not tried again" test "$(turns 'claude conflict')" = 2
	foreman show 42 >"$sandbox/show.txt"
	check "show names the blocker" contains "$sandbox/show.txt" "the merge breaks the tests"
	check "the worktree is kept" test -d "$wt"
}

scenarios=${*:-runner notify logs inbox inbox_lifecycle rereview conflicts respond cancel feedback conflict tui routing}
for s in $scenarios; do "scenario_$s"; done
if [ $failures -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"
