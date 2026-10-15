#!/usr/bin/env bash
# End-to-end checks for the built binary. Everything runs in a temporary directory
# against a local bare git origin, a stand-in gh, and stub agents, so no real
# repository, GitHub account, or model is touched. The dashboard is driven in a
# pseudo-terminal by scripts/tui-drive.py, which needs python3.
#
#   scripts/e2e.sh            run every scenario
#   scripts/e2e.sh runner     run the named scenarios
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/foreman-e2e.XXXXXX")
listeners=
trap '[ -z "$listeners" ] || { kill $listeners 2>/dev/null; wait $listeners 2>/dev/null || true; }; if [ -n "${E2E_KEEP:-}" ]; then echo "kept $work"; else for i in 1 2 3 4 5; do rm -rf "$work" 2>/dev/null && break; sleep 0.1; done; fi' EXIT
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
eventually() { # eventually <seconds> <command...>
	local end=$((SECONDS + $1))
	shift
	until "$@"; do
		[ $SECONDS -lt $end ] || return 1
		sleep 0.1
	done
}

foreman() { (cd "${FOREMAN_CWD:-$sandbox}" && "$work/foreman" "$@"); }

# dash [flags --] [steps...] opens the dashboard in a pseudo-terminal and plays the
# steps through scripts/tui-drive.py. Auto mode is off unless a flag says otherwise,
# and HOME points into the sandbox, so the saved auto choice stays there.
dash() {
	local flags=()
	while [ $# -gt 0 ] && [ "$1" != -- ]; do flags+=("$1"); shift; done
	[ ${#flags[@]} -gt 0 ] || flags=(--no-auto)
	(cd "${FOREMAN_CWD:-$sandbox}" && HOME="$sandbox/home" python3 "$root/scripts/tui-drive.py" "$work/foreman" "${flags[@]}" "$@")
}

# check runs commands through bash -c too, so they can use dash.
export -f dash
export root work

# screen runs one GitHub refresh in the dashboard and prints what it shows.
screen() { dash -- refreshed print 2>/dev/null; }

# wait_status <text> [seconds] refreshes the dashboard until it shows text.
wait_status() {
	local want=$1 end=$((SECONDS + ${2:-20}))
	while :; do
		if screen | grep -F -- "$want" >/dev/null; then return 0; fi
		[ $SECONDS -lt $end ] || return 1
		sleep 0.1
	done
}

# assign <n>... adds issues to the assigned search, so the inbox offers them.
assign() {
	local n hits=
	printf '%s\n' "$@" >>"$state/assigned"
	for n in $(sort -un "$state/assigned"); do
		hits="$hits${hits:+,}$(hit acme/widgets "$n" "$(cat "$state/title-$n")")"
	done
	echo "[$hits]" >"$state/search-issues-assignee.json"
}

# start <n> delegates acme/widgets#n to Claude the way the developer does: select it
# in the inbox, press enter, pick Claude, and skip the context file.
start() {
	assign "$1"
	dash -- refreshed "pick=acme/widgets#$1" 'keys=\r1\r' "wait=Started acme/widgets#$1"
}

runner_pid() { sed -n 's/^runner_pid: *//p' "$sandbox/tasks/acme-widgets-$1.md"; }
pid_gone() { ! kill -0 "$1" 2>/dev/null; }
agent_gone() { ! kill -0 -- "-$(cat "$state/$1-agent.pid")" 2>/dev/null; }
processes_gone() { pid_gone "$1" && agent_gone "$2"; }
stop_agent() { kill -TERM -- "-$(cat "$state/$1-agent.pid")" 2>/dev/null || true; }

# task <n> [keys] opens task #n's lower pane, presses keys there, and prints the screen.
task() {
	dash -- refreshed keys=l "pick=acme/widgets#$1" 'keys=\r'"${2:-}" sleep=0.3 print 2>/dev/null
}

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
	export sandbox FOREMAN_CWD="$sandbox/checkout"

	cat >"$sandbox/config.yaml" <<EOF
tasks_dir: $sandbox/tasks
worktrees_dir: $sandbox/worktrees
repositories_dir: $sandbox/repositories
log_dir: $sandbox/logs
knowledge_dir: $sandbox/knowledge
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
"repo view")
	case "$PWD" in *acme-linear*) repo=acme/linear ;; *) repo=acme/widgets ;; esac
	printf '{"nameWithOwner":"%s","defaultBranchRef":{"name":"main"}}\n' "$repo" ;;
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
# The learner's evaluator turn: record it apart from the task's agents. It answers
# with evaluation-<issue title>.json when the scenario wrote one, else learns nothing.
if [[ "$*" == *"You evaluate one finished task"* ]]; then
	title=$(printf '%s\n' "$*" | sed -n 's/^Title: //p' | head -n 1)
	echo "claude evaluate $title" >>"$E2E_STATE/evaluations.log"
	out='{"assessment":"went fine","signals":[],"proposals":[],"revisions":[]}'
	[ ! -f "$E2E_STATE/evaluation-$title.json" ] || out=$(cat "$E2E_STATE/evaluation-$title.json")
	printf '{"type":"result","session_id":"e1","subtype":"success","total_cost_usd":0.01,"structured_output":%s}\n' "$out"
	exit 0
fi
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
echo "$$" >"$E2E_STATE/implement-agent.pid"
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
if [[ "$PWD" == *acme-linear* ]]; then
	pr=947
	pr_repo=acme/linear
	body='Add search for LIN-7.\n\nhttps://linear.app/acme/issue/LIN-7/add-search'
else
	pr=$((900 + ${issue%%-*}))
	pr_repo=acme/widgets
	body='Retry 5xx only.\n\nCloses #42'
fi
printf '{"number":%s,"url":"https://github.com/%s/pull/%s","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main","isCrossRepository":false,"body":"%s"}\n' "$pr" "$pr_repo" "$pr" "$branch" "$(git rev-parse HEAD)" "$body" >"$E2E_STATE/pr-$pr.json"
echo "claude stderr progress" >&2
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"Run the tests"}}]}}'
printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.25,"structured_output":{"status":"pr_opened","summary":"added retries","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
EOF

	# Reviewer on the codex JSONL protocol.
	cat >"$sandbox/bin/codex" <<'EOF'
#!/usr/bin/env bash
for arg in "$@"; do case "$arg" in gpt-6-luna|gpt-6-sol) echo "codex model=$arg" >>"$E2E_STATE/models.log" ;; model_reasoning_effort=*) echo "codex $arg" >>"$E2E_STATE/models.log" ;; esac; done
prompt=${@: -1}
if [[ "$prompt" == *"You evaluate one finished task"* ]]; then
	echo "codex evaluate" >>"$E2E_STATE/evaluations.log"
	last=
	while [ $# -gt 0 ]; do [ "$1" = -o ] && last=$2; shift; done
	echo '{"type":"thread.started","thread_id":"e1"}'
	echo '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0}}'
	echo '{"assessment":"went fine","signals":[],"proposals":[],"revisions":[]}' >"$last"
	exit 0
fi
if [[ " $* " == *" --ephemeral "* ]]; then echo "codex review"; printf '%s' "${@: -1}" >"$E2E_STATE/review-prompt.txt"; else echo "codex implement"; fi >>"$E2E_STATE/agents.log"
last=
while [ $# -gt 0 ]; do [ "$1" = -o ] && last=$2; shift; done
echo '{"type":"thread.started","thread_id":"t1"}'
echo "$$" >"$E2E_STATE/review-agent.pid"
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
	echo "$2" >"$state/title-$1"
	printf '%s\n' "${3:-[]}" >"$state/labels-$1.json"
	printf '{"number":%s,"title":"%s","body":"do it","labels":%s,"url":"https://github.com/acme/widgets/issues/%s","state":"OPEN","updatedAt":"2026-09-25T10:00:00Z"}\n' \
		"$1" "$2" "${3:-[]}" "$1" >"$state/issue-$1.json"
	printf '{"number":%s,"title":"%s","html_url":"https://github.com/acme/widgets/issues/%s","state":"open","updated_at":"2026-09-25T10:00:00Z"}\n' \
		"$1" "$2" "$1" >"$state/api-$1.json"
}

scenario_runner() {
	echo "runner: a detached runner outlives the dashboard that started it"
	setup runner
	check "the dashboard delegates #42" start 42
	check "the runner reaches a clean review" wait_status "review ✓"
	local log
	log=$(ls "$sandbox"/logs/acme-widgets-42.log)
	check "log has the runner's finish line" contains "$log" "finished"
	check "log lines are timestamped" grep -qE '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' "$log"
	check "agent stderr reached the log" contains "$log" "claude stderr progress"
	check "no line is stamped twice" bash -c "! grep -qE '^[^ ]+ [0-9]{4}-[0-9]{2}-[0-9]{2}T' '$log'"
	check "the clean review starts the learner" eventually 10 test -f "$sandbox/knowledge/acme/widgets/evaluations/42.yaml"
	check "the learner evaluates with the implementer's agent" contains "$state/evaluations.log" "claude evaluate"
	check "the learner logs to learning.log" contains "$sandbox/logs/learning.log" "learn: evaluated acme/widgets task 42"
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
	task 42 sd >"$sandbox/show.txt"
	check "the show pane lists the plan" contains "$sandbox/show.txt" "Analysis https://github.com/acme/widgets/issues/42#issuecomment-3001"
	rm "$sandbox/stages/pr.md"
	check "a missing stage file stops foreman" bash -c "! dash -- 2>'$sandbox/err.txt'"
	check "and names the file" contains "$sandbox/err.txt" "stages/pr.md"
}

scenario_notify() {
	echo "notify: the developer hears about ready work and stopped runners on Discord"
	setup notify
	webhook
	echo '[{"name":"test","bucket":"pending","link":"https://ci/1"}]' >"$state/checks.json"
	start 42 >/dev/null
	check "the runner reaches a clean review" wait_status "CI pending"
	check "a clean review with pending CI stays quiet" bash -c "[ ! -s '$state/notifications.log' ]"
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	screen >/dev/null
	screen >/dev/null
	check "ready work notifies once" bash -c "[ \$(grep -c 'Ready for human review' '$state/notifications.log') = 1 ]"
	check "the post carries the state, the task, and the PR link" contains "$state/notifications.log" 'acme/widgets#42 (READY_FOR_HUMAN)\nhttps://github.com/acme/widgets/pull/'
	check "the post cannot mention anyone" contains "$state/notifications.log" '"allowed_mentions":{"parse":[]}'

	issue 43 "Fix pagination"
	E2E_AGENT_SLEEP=30.017 start 43 >/dev/null
	sleep 0.5
	kill -TERM "$(runner_pid 43)"
	check "a stopped runner records FAILED promptly" wait_status "FAILED" 5
	check "and notifies" grep -q 'Run cancelled.*acme/widgets#43' "$state/notifications.log"
	check "and leaves no agent behind" agent_gone implement

	issue 44 "Fix sorting"
	E2E_AGENT_SLEEP=30.017 start 44 >/dev/null
	sleep 0.5
	kill -KILL "$(runner_pid 44)"
	screen >/dev/null
	check "a killed runner is reaped and notifies" grep -q 'Run failed.*acme/widgets#44' "$state/notifications.log"
	stop_agent implement

	echo 500 >"$state/webhook-status"
	issue 45 "Fix caching"
	start 45 >/dev/null
	local i=0
	while ! grep -qF 'acme/widgets#45' "$sandbox/logs/notifications.log" 2>/dev/null && [ $i -lt 50 ]; do
		screen >/dev/null
		sleep 0.2
		i=$((i + 1))
	done
	check "a failing webhook leaves the task ready" grep -q '^state: READY_FOR_HUMAN' "$sandbox/tasks/acme-widgets-45.md"
	check "and logs the delivery error" grep -q 'acme/widgets#45 Ready for human review: Discord returned 500' "$sandbox/logs/notifications.log"
	check "no log holds the webhook URL" bash -c "! grep -rqF e2e-secret '$sandbox/logs'"

	chmod 644 "$sandbox/config.yaml"
	check "a config readable by others warns" bash -c "dash -- refreshed 2>'$sandbox/perm-err.txt' && grep -qF 'chmod 600 $sandbox/config.yaml' '$sandbox/perm-err.txt'"
	notifications_config "  enabled: true"
	check "a missing webhook URL warns and the dashboard still opens" bash -c "dash -- refreshed 2>'$sandbox/setup-err.txt' && grep -qF 'notifications.webhook_url is not set' '$sandbox/setup-err.txt'"
	# wait reaps the listener, so bash prints no "Terminated" line for it.
	kill $listeners 2>/dev/null || true
	wait $listeners 2>/dev/null || true
	listeners=
}

scenario_logs() {
	echo "logs: the dashboard's log pane shows cleaned and raw lines and pages back to the start"
	setup logs
	start 42 >/dev/null
	wait_status "review ✓" || true
	sleep 1
	task 42 >"$sandbox/clean.txt"
	check "clean shows Claude's result" contains "$sandbox/clean.txt" 'claude: subtype=success cost=$0.25'
	check "clean shows Claude's tool calls" contains "$sandbox/clean.txt" "claude Bash Run the tests"
	check "clean shows Codex usage without a price" contains "$sandbox/clean.txt" "(USD unavailable)"
	check "clean hides Codex JSON" bash -c "! grep -q '\"type\":\"turn' '$sandbox/clean.txt'"
	task 42 R >"$sandbox/raw.txt"
	check "R marks the pane raw" contains "$sandbox/raw.txt" "LOG · raw · acme/widgets#42"
	check "raw keeps Codex JSON" contains "$sandbox/raw.txt" '"type":"turn.completed"'
	task 42 sd >"$sandbox/show.txt"
	check "the show pane reports usage" contains "$sandbox/show.txt" 'Claude $0.27 (2 turns) · Codex 125 tokens (1 turn)'

	# Far more than one bounded read of the log, so g must load older chunks.
	local log first
	log=$(ls "$sandbox"/logs/acme-widgets-42.log)
	first=$(head -1 "$log" | cut -d' ' -f2-)
	awk 'BEGIN { for (i = 0; i < 6000; i++) printf "2026-10-04T10:00:00+00:00 filler line %d of a long run\n", i }' >>"$log"
	task 42 >"$sandbox/tail.txt"
	check "a long log opens at its tail" contains "$sandbox/tail.txt" "filler line 5999 of a long run"
	check "without its first line" bash -c "! grep -qF '$first' '$sandbox/tail.txt'"
	task 42 g >"$sandbox/start.txt"
	check "g loads older lines back to the first" contains "$sandbox/start.txt" "$first"
	task 42 Rg >"$sandbox/raw-start.txt"
	check "and in the raw view too" contains "$sandbox/raw-start.txt" "$first"
}

hit() { # hit <repo> <number> <title>
	local labels='[]'
	[ ! -f "$state/labels-$2.json" ] || labels=$(cat "$state/labels-$2.json")
	printf '{"number":%s,"title":"%s","url":"https://github.com/%s/issues/%s","state":"OPEN","updatedAt":"2026-09-25T10:00:00Z","repository":{"nameWithOwner":"%s"},"labels":%s}' "$2" "$3" "$1" "$2" "$1" "$labels"
}

scenario_inbox() {
	echo "inbox: first run, filters, and delegation from outside any checkout"
	setup inbox
	rm -rf "$sandbox/tasks"
	printf 'exclude_repositories: [acme/ignored]\n' >>"$sandbox/config.yaml"
	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	echo "[$(hit acme/widgets 950 'Review me')]" >"$state/search-prs-review-requested.json"
	FOREMAN_CWD=$sandbox

	check "declining setup leaves no tasks directory" bash -c "dash -- 'keys=n\\r' && [ ! -d '$sandbox/tasks' ]"
	dash -- 'keys=y\r' refreshed print >"$sandbox/inbox.txt" 2>&1 || true
	check "setup creates the tasks directory" test -d "$sandbox/tasks"
	check "the inbox shows repo-qualified items" contains "$sandbox/inbox.txt" "acme/widgets#42"
	check "excluded repositories are hidden" bash -c "! grep -q 'acme/ignored' '$sandbox/inbox.txt'"
	check "review requests aren't offered for delegation" bash -c "! grep -q 'acme/widgets#950' '$sandbox/inbox.txt'"
	check "selecting an issue delegates it" dash -- refreshed "pick=acme/widgets#42" 'keys=\r1\r' "wait=Started acme/widgets#42"
	check "the source checkout is cloned" test -d "$sandbox/repositories/acme-widgets/.git"
	check "the selection reaches a clean review" wait_status "review ✓"

	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/widgets 43 'Fix pagination'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	screen >"$sandbox/report.txt"
	check "a refresh shows new work" contains "$sandbox/report.txt" "acme/widgets#43"
	check "and hides excluded repositories" bash -c "! grep -q 'acme/ignored' '$sandbox/report.txt'"

	cp "$sandbox/config.yaml" "$sandbox/config.exclude"
	printf 'include_repositories: [acme/ignored, acme/other]\n' >>"$sandbox/config.yaml"
	echo "[$(hit acme/widgets 43 'Fix pagination'),$(hit acme/other 5 'Included'),$(hit acme/ignored 7 'Ignore me')]" >"$state/search-issues-assignee.json"
	screen >"$sandbox/include.txt"
	check "the include list shows its repositories" contains "$sandbox/include.txt" "acme/other#5"
	check "the include list beats the exclude list" contains "$sandbox/include.txt" "acme/ignored#7"
	check "the include list hides other repositories" bash -c "! grep -q 'acme/widgets#43' '$sandbox/include.txt'"
	check "the inbox names the configured view" contains "$sandbox/include.txt" "INBOX · configured repos"
	dash -- refreshed keys=v print >"$sandbox/view.txt" 2>/dev/null
	check "v keeps the configured repository filter" bash -c "! grep -q 'acme/widgets#43' '$sandbox/view.txt'"
	cp "$sandbox/config.exclude" "$sandbox/config.yaml"
	check "searches ask for gh's maximum" contains "$state/gh.log" "--limit=1000"

	printf '#!/bin/sh\n[ "$1" = -w ] && echo "edited $2" >"%s/edited"\n' "$sandbox" >"$sandbox/bin/editor"
	chmod +x "$sandbox/bin/editor"
	check "-c runs an editor with arguments" bash -c "cd '$sandbox' && EDITOR='editor -w' VISUAL= '$work/foreman' -c </dev/null && grep -qF '$sandbox/config.yaml' '$sandbox/edited'"
	check "-c prefers VISUAL" bash -c "cd '$sandbox' && rm '$sandbox/edited' && VISUAL='editor -w' EDITOR=false '$work/foreman' -c </dev/null && test -f '$sandbox/edited'"
	cp "$sandbox/config.yaml" "$sandbox/config.good"
	printf 'exclude_repositories:\n  - "acme/typo\\""\n' >>"$sandbox/config.yaml"
	check "an invalid config stops the dashboard" bash -c "! dash -- 2>/dev/null"
	check "and -c does not fix it by itself" bash -c "cd '$sandbox' && ! EDITOR=true VISUAL= '$work/foreman' -c 2>'$sandbox/edit-err.txt' </dev/null"
	check "and says to run -c again" contains "$sandbox/edit-err.txt" "run foreman -c again to fix it"
	printf '#!/bin/sh\ncp "%s/config.good" "$1"\n' "$sandbox" >"$sandbox/bin/fixer"
	chmod +x "$sandbox/bin/fixer"
	check "-c still opens an invalid config" bash -c "cd '$sandbox' && EDITOR=fixer VISUAL= '$work/foreman' -c 2>'$sandbox/edit-err.txt' </dev/null && grep -qF 'so you can fix it' '$sandbox/edit-err.txt'"
	check "and the fixed config opens the dashboard" dash -- refreshed
}

scenario_linear() {
	echo "linear: mixed issue sources, filters, delegation, and failed refresh"
	setup linear
	python3 "$root/scripts/linear-stub.py" "$state" &
	listeners="$listeners $!"
	eventually 5 test -s "$state/linear-port"
	export LINEAR_API_KEY=e2e-personal-key
	export FOREMAN_LINEAR_ENDPOINT="http://127.0.0.1:$(cat "$state/linear-port")/graphql"
	cat >>"$sandbox/config.yaml" <<'EOF'
linear_projects:
  acme/linear: https://linear.app/acme/project/linear-work-123
EOF
	issue 43 "GitHub work" '[{"name":"triaged"}]'
	echo "[$(hit acme/widgets 43 'GitHub work'),$(hit acme/linear 44 'Mapped GitHub work')]" >"$state/search-issues-assignee.json"
	dash -- refreshed print >"$sandbox/mixed.txt" 2>/dev/null
	check "the mixed inbox shows the GitHub section" contains "$sandbox/mixed.txt" "GITHUB"
	check "the mixed inbox shows the Linear section" contains "$sandbox/mixed.txt" "LINEAR"
	check "the mixed inbox shows both issues" bash -c "grep -qF 'acme/widgets#43' '$sandbox/mixed.txt' && grep -qF 'acme/linear · LIN-7' '$sandbox/mixed.txt'"
	check "the mapped repository hides GitHub issues" bash -c "! grep -qF 'acme/linear#44' '$sandbox/mixed.txt'"
	dash --no-auto -s github -- refreshed print >"$sandbox/github.txt" 2>/dev/null
	check "the GitHub filter hides Linear issues" bash -c "grep -qF 'acme/widgets#43' '$sandbox/github.txt' && ! grep -qF 'LIN-7' '$sandbox/github.txt'"
	dash --no-auto -s linear -- refreshed print >"$sandbox/linear.txt" 2>/dev/null
	check "the Linear filter hides GitHub inbox issues" bash -c "grep -qF 'LIN-7' '$sandbox/linear.txt' && ! grep -qF 'GITHUB' '$sandbox/linear.txt'"
	touch "$state/linear-fail"
	dash --auto -- refreshed print >"$sandbox/failed.txt" 2>/dev/null
	check "a failed Linear refresh keeps GitHub results with a warning" bash -c "grep -qF 'acme/widgets#43' '$sandbox/failed.txt' && grep -qF 'warning' '$sandbox/failed.txt'"
	check "a failed mixed refresh blocks all auto starts" grep -q '^state: DISCOVERED' "$sandbox/tasks/acme-widgets-43.md"
	rm -f "$state/linear-fail"
	check "a GitHub issue can be delegated" dash --no-auto -s github -- refreshed "pick=acme/widgets#43" 'keys=\r1\r' "wait=Started acme/widgets#43"
	check "the GitHub task keeps its issue number" grep -q '^number: 43' "$sandbox/tasks/acme-widgets-43.md"
	if dash --no-auto -s linear -- refreshed "pick=acme/linear · LIN-7" 'keys=\r1\r' "wait=Started acme/linear · LIN-7" >"$sandbox/linear-start.txt" 2>&1; then
		pass "a Linear issue can be delegated"
	else
		fail "a Linear issue can be delegated"
	fi
	check "the Linear task keeps its issue ID" grep -q '11111111-1111-4111-8111-111111111111' "$sandbox/tasks/acme-linear-linear-11111111-1111-4111-8111-111111111111.md"
	check "the Linear task uses the name supplied by Linear" grep -q '^branch: bryan/lin-7-add-search' "$sandbox/tasks/acme-linear-linear-11111111-1111-4111-8111-111111111111.md"
	check "the runner posts planning comments to Linear" eventually 10 test -f "$state/linear-comment-4"
	check "the Linear task reaches a clean GitHub PR review" eventually 15 grep -q 'verdict: clean' "$sandbox/tasks/acme-linear-linear-11111111-1111-4111-8111-111111111111.md"
	check "the Linear PR stays in its GitHub repository and links its issue" bash -c "grep -qF 'github.com/acme/linear/pull/947' '$state/pr-947.json' && grep -qF 'LIN-7/add-search' '$state/pr-947.json' && ! grep -qF 'Closes #' '$state/pr-947.json'"
	check "the Linear PR prompt names its issue link rule" contains "$state/plan-posted-prompt.txt" "Put LIN-7 in the pull request title"
	dash --no-auto -s github -- refreshed print >"$sandbox/github-delegated.txt" 2>/dev/null
	check "the GitHub-only view keeps delegated Linear work" contains "$sandbox/github-delegated.txt" "LIN-7"
	unset LINEAR_API_KEY
	check "GitHub-only use needs no Linear key" dash --no-auto -s github -- refreshed
	check "mixed use without a Linear key gives a setup error" bash -c "! dash -- 2>'$sandbox/missing-key.txt' && grep -qF LINEAR_API_KEY '$sandbox/missing-key.txt'"
	unset FOREMAN_LINEAR_ENDPOINT
}

scenario_inbox_lifecycle() {
	echo "inbox lifecycle: departed items, inaccessible issues, and recovery"
	setup inbox_lifecycle
	echo "[$(hit acme/widgets 42 'Add retries'),$(hit acme/widgets 43 'Unavailable')]" >"$state/search-issues-assignee.json"
	check "initial discovery succeeds" dash -- refreshed
	echo "[$(hit acme/widgets 44 'Current work')]" >"$state/search-issues-assignee.json"
	screen >"$sandbox/report.txt"
	check "an inaccessible historic item does not abort the refresh" contains "$sandbox/report.txt" "acme/widgets#44"
	check "its failure is counted as a warning" contains "$sandbox/report.txt" "! 1 warning"
	check "the departed issue is not offered" bash -c "! grep -q 'acme/widgets#42' '$sandbox/report.txt'"
	screen >/dev/null
	check "the departed issue is fetched only once" bash -c "[ \"\$(grep -c 'api repos/acme/widgets/issues/42' '$state/gh.log')\" = 1 ]"
	issue 43 'Available again'
	screen >"$sandbox/recovered.txt"
	check "a recovered item is not offered" bash -c "! grep -q 'acme/widgets#43' '$sandbox/recovered.txt'"
	check "and leaves no warning" bash -c "! grep -q 'warning' '$sandbox/recovered.txt'"
	screen >/dev/null
	check "a recovered departed issue stops being fetched" bash -c "[ \"\$(grep -c 'api repos/acme/widgets/issues/43' '$state/gh.log')\" = 3 ]"

	issue 45 'Deleted later'
	echo "[$(hit acme/widgets 44 'Current work'),$(hit acme/widgets 45 'Deleted later')]" >"$state/search-issues-assignee.json"
	screen >/dev/null
	echo "[$(hit acme/widgets 44 'Current work')]" >"$state/search-issues-assignee.json"
	touch "$state/gone-45"
	screen >"$sandbox/gone.txt"
	check "a deleted issue leaves no warning" bash -c "! grep -q 'warning' '$sandbox/gone.txt'"
	screen >/dev/null
	check "and is not looked up again" bash -c "[ \"\$(grep -c 'api repos/acme/widgets/issues/45' '$state/gh.log')\" = 1 ]"
}

turns() { grep -cx "$1" "$state/agents.log" || true; }

# reviewed <n> [seconds] waits for task #n's file to record a clean review.
reviewed() {
	local end=$((SECONDS + ${2:-10}))
	until grep -q 'verdict: clean' "$sandbox/tasks/acme-widgets-$1.md" 2>/dev/null; do
		[ $SECONDS -lt $end ] || return 1
		screen >/dev/null
		sleep 0.1
	done
}

scenario_rereview() {
	echo "rereview: new commits and interrupted reviews are reviewed without reimplementing"
	setup rereview
	echo '[{"name":"test","bucket":"pending","link":"https://ci/1"}]' >"$state/checks.json"
	start 42 >/dev/null
	wait_status "CI pending" || true
	sed -i.bak 's/"headRefOid":"[0-9a-f]*"/"headRefOid":"feedfacefeedfacefeedfacefeedfacefeedface"/' "$state/pr-942.json"
	screen >/dev/null
	check "a push after the clean review is reviewed again" wait_status "CI pending"
	check "by the reviewer alone" test "$(turns 'claude implement') $(turns 'codex review')" = "1 2"
	check "the review covers the new head" grep -q 'head: feedface' "$sandbox/tasks/acme-widgets-42.md"

	issue 43 "Fix pagination"
	E2E_REVIEW_SLEEP=30.017 start 43 >/dev/null
	local i=0
	until [ "$(turns 'codex review')" = 3 ] || [ $i -gt 100 ]; do sleep 0.1; i=$((i + 1)); done
	kill -KILL "$(runner_pid 43)"
	stop_agent review
	check "a runner killed mid-review is reaped" wait_status "FAILED" 5
	start 43 >/dev/null
	check "the restart finishes the review" reviewed 43
	check "without implementing again" test "$(turns 'claude implement')" = 2
}

scenario_conflicts() {
	echo "conflicts: a PR that conflicts with main is merged before its review"
	setup conflicts
	echo 'echo "// main" >>widgets.go' >"$state/main-moves"
	start 42 >/dev/null
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
	start 43 >/dev/null
	check "a clean base update goes straight to review" reviewed 43
	check "with no merge turn" test "$(turns 'claude merge')" = 1
	branch=$(git -C "$sandbox/origin.git" for-each-ref --format='%(refname:short)' 'refs/heads/*/43-*')
	check "and no merge commit" test "$(git -C "$sandbox/origin.git" rev-list --merges --count "$branch")" = 0
}

scenario_respond() {
	echo "respond: several questions from one turn, answered in one sitting"
	setup respond
	touch "$state/block-once"
	start 42 >/dev/null
	check "the task waits on both questions" wait_status "NEEDS_DECISION"
	task 42 >"$sandbox/question.txt"
	check "enter opens the first question" contains "$sandbox/question.txt" "Retry 4xx?"
	check "the first answer leaves one question" dash -- refreshed keys=l pick=acme/widgets#42 'keys=\rB\r' "wait=1 question still waiting"
	check "the task still waits" wait_status "NEEDS_DECISION" 1
	check "the second answer relaunches the run" dash -- refreshed keys=l pick=acme/widgets#42 'keys=\r\r' "wait=Resumed acme/widgets#42 with the answers"
	check "the run reaches a clean review" wait_status "review ✓"
	check "the agent resumed once, with both answers" test "$(turns 'claude resume')" = 1
	check "resume carries the first answer" contains "$state/resume-prompt.txt" "Decision: B. 4xx and 5xx"
	check "resume carries the second answer" contains "$state/resume-prompt.txt" "Decision: A. 3 tries"
}

scenario_cancel() {
	echo "cancel: stopping a run keeps its work and can be resumed"
	setup cancel
	E2E_AGENT_SLEEP=30.017 start 42 >/dev/null
	check "the runner is working" wait_status "WORKING" 5
	sleep 0.5
	local pid
	pid=$(runner_pid 42)
	check "c then y cancels the run" dash -- refreshed keys=l pick=acme/widgets#42 keys=cy "wait=Cancelled acme/widgets#42; the branch and worktree are kept"
	check "the task is FAILED" wait_status "FAILED" 1
	check "no runner or agent is left" processes_gone "$pid" implement
	local log
	log=$(ls "$sandbox"/logs/acme-widgets-42.log)
	check "the log shows who stopped it" contains "$log" "foreman: cancel requested; stopping runner"
	check "a second cancel has nothing to stop" dash -- refreshed keys=l pick=acme/widgets#42 keys=c "wait=acme/widgets#42 is FAILED with no runner; nothing to cancel"
	start 42 >/dev/null
	check "starting it again resumes the cancelled task" wait_status "review ✓"
}

scenario_tui() {
	echo "tui: launch flags, the removed commands, and the dashboard's help"
	setup tui
	foreman </dev/null >"$sandbox/usage.txt" 2>&1
	check "foreman exits 0 on a pipe" test $? -eq 0
	check "and prints usage" contains "$sandbox/usage.txt" "foreman -a, --auto"
	check "the usage names -c" contains "$sandbox/usage.txt" "foreman -c"
	check "the usage lists no removed command" bash -c "! grep -qE 'foreman (inbox|status|show|start|respond|resume|cancel|clean|config|logs)' '$sandbox/usage.txt'"
	local cmd
	for cmd in inbox status show start respond resume cancel clean config logs tui "config --edit" "--auto --no-auto" "-c -a"; do
		check "foreman $cmd exits 64" bash -c "cd '$sandbox' && '$work/foreman' $cmd </dev/null >/dev/null 2>&1; [ \$? -eq 64 ]"
	done
	check "foreman --version prints the version" bash -c "'$work/foreman' --version | grep -q '^foreman '"

	# The terminal check runs before the config is read, so nothing a piped `foreman`
	# never needed can make it fail where it used to print usage.
	mv "$sandbox/stages/implement.md" "$sandbox/implement.md"
	foreman </dev/null >"$sandbox/broken.txt" 2>&1
	check "a missing stage file still gets usage on a pipe" test $? -eq 0
	check "and prints usage, not the error" contains "$sandbox/broken.txt" "foreman -a, --auto"
	mv "$sandbox/implement.md" "$sandbox/stages/implement.md"

	local saved="$sandbox/home/.local/share/foreman/state.json" version
	version=$("$work/foreman" --version)
	dash -- refreshed keys=? sleep=0.3 print >"$sandbox/help.txt" 2>/dev/null
	check "? shows the keys" contains "$sandbox/help.txt" "switch the log between cleaned and raw lines"
	check "and the version" contains "$sandbox/help.txt" "KEYS · $version"
	check "any key closes the help without quitting" dash -- refreshed keys=? keys=q "wait=DELEGATED WORK"
	check "--no-auto starts with auto mode off" dash --no-auto -- refreshed "wait=auto off"
	check "and saves nothing" test ! -e "$saved"
	issue 42 "Add retries" '[{"name":"triaged"}]'
	assign 42
	check "--auto starts with auto mode on" dash --auto -- refreshed "wait=auto on"
	check "and auto mode starts new work" wait_status "review ✓"
	check "-a saves nothing either" test ! -e "$saved"
	check "a turns auto mode on and saves it" dash --no-auto -- refreshed keys=a "wait=auto on"
	check "the saved choice is on" grep -q '"auto": *true' "$saved"
	check "a launch flag beats the saved choice" dash --no-auto -- refreshed "wait=auto off"
	check "and leaves it saved" grep -q '"auto": *true' "$saved"
}

scenario_routing() {
	echo "routing: a routed task keeps its profile"
	setup routing
	printf 'profiles: {}\n' >"$sandbox/routing.yaml"
	check "an invalid routing.yaml leaves the dashboard working" dash -- refreshed
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
	check "a retry with the same agent starts" start 42
	check "the run reaches a clean review" wait_status "review ✓"
	local log
	log=$(ls "$sandbox"/logs/acme-widgets-42.log)
	check "the log has the route line" contains "$log" "route:deep:claude:broad or ambiguous design work"
	check "the route line comes before the first turn" bash -c "grep -nE 'route:|claude:[a-z]+:plan' '$log' | head -1 | grep -qF route:"
	check "planning uses the profile's model" bash -c "! grep -qF 'claude model=sonnet' '$state/models.log'"
	check "the competing agent still reviews" contains "$state/agents.log" "codex review"
	task 42 s >"$sandbox/show.txt"
	check "the show pane lists the profile and reason" contains "$sandbox/show.txt" "Profile       deep (plan opus, implement opus) · broad or ambiguous design work"
	check "the task file keeps the profile" contains "$sandbox/tasks/acme-widgets-42.md" "route_logged: true"
}

scenario_feedback() {
	echo "feedback: a comment on the PR comes back to the agent"
	setup feedback
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	start 42 >/dev/null
	check "the run reaches the developer" wait_status "READY_FOR_HUMAN"

	# The developer comments on the pull request.
	conv="$state/conv-repos-acme-widgets-issues-942-comments.json"
	printf '[[{"id":4401,"html_url":"https://github.com/acme/widgets/pull/942#issuecomment-4401","body":"Cap the retries.","updated_at":"2026-10-01T09:00:00Z","user":{"login":"brymartinez"}}]]\n' >"$conv"
	check "a new comment makes the task REVIEWED" wait_status "REVIEWED" 5
	screen >"$sandbox/inbox.txt"
	check "the inbox offers it to resume" grep -qE '^[> ] acme/widgets#42 ' "$sandbox/inbox.txt"
	check "marked REVIEWED" grep -qE '^    REVIEWED' "$sandbox/inbox.txt"

	check "selecting it resumes the agent" dash -- refreshed pick=acme/widgets#42 'keys=\r' "wait=Resumed acme/widgets#42 on PR #942"
	check "the task comes back once the comment is answered" wait_status "READY_FOR_HUMAN" 15
	check "the agent saw the comment" contains "$state/feedback-prompt.txt" "Cap the retries."
	check "and was told where to reply" contains "$state/feedback-prompt.txt" "repos/acme/widgets/issues/942/comments"
	check "the agent kept its session" test "$(turns 'claude feedback')" = 1
	check "answering needed no new review" test "$(turns 'codex review')" = 1

	# The agent's own reply carries the marker, so it is not read back as feedback.
	screen >"$sandbox/status.txt"
	check "the reply did not start another round" contains "$sandbox/status.txt" "READY_FOR_HUMAN"
	check "and the agent was not asked again" test "$(turns 'claude feedback')" = 1
	task 42 sd >"$sandbox/show.txt"
	check "the show pane lists the answered comment" contains "$sandbox/show.txt" "answered"
}

# conflicts marks PR #942 as conflicting with its base.
conflicts() {
	sed -i.bak 's/,"mergeable":"[A-Z]*"//; s/"isCrossRepository":false/"isCrossRepository":false,"mergeable":"CONFLICTING"/' "$state/pr-942.json"
}

scenario_conflict() {
	echo "conflict: a handed-over PR that conflicts with main is repaired once"
	setup conflict
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	start 42 >/dev/null
	check "the run reaches the developer" wait_status "READY_FOR_HUMAN"
	(
		cd "$sandbox/checkout"
		echo "package widgets // main moved" >main.go
		git add main.go && git commit -qm "main moves on" && git push -q origin main
	)

	conflicts
	screen >/dev/null
	check "the refresh starts a repair" eventually 10 grep -q 'claude conflict' "$state/agents.log"
	screen >/dev/null
	check "the repair is reviewed and handed back" wait_status "READY_FOR_HUMAN" 15
	check "the implementer repaired it once" test "$(turns 'claude conflict')" = 1
	check "in its own session" test "$(turns 'claude implement')" = 1
	check "the reviewer reviewed the merge" test "$(turns 'codex review')" = 2
	local wt
	wt=$(ls -d "$sandbox"/worktrees/*/*)
	check "the branch now includes main" git -C "$wt" merge-base --is-ancestor "$(git --git-dir="$sandbox/origin.git" rev-parse main)" HEAD

	touch "$state/conflict-fail"
	conflicts
	screen >/dev/null
	check "a failed repair needs a decision" wait_status "NEEDS_DECISION" 15
	screen >/dev/null
	screen >/dev/null
	check "the same heads are not tried again" test "$(turns 'claude conflict')" = 2
	check "the task file names the blocker" contains "$sandbox/tasks/acme-widgets-42.md" "the merge breaks the tests"
	check "the worktree is kept" test -d "$wt"
}

scenario_history() {
	echo "history: merged and closed PRs with their phase times and logs"
	setup history
	check "the dashboard delegates #42" start 42
	check "the run is handed to the developer" wait_status "READY_FOR_HUMAN" 15
	check "the task file records the attempt's handoff" grep -q 'handoff_at:' "$sandbox/tasks/acme-widgets-42.md"
	sed -i.bak 's/"state":"OPEN"/"state":"MERGED","title":"Add retries","closedAt":"2026-10-04T10:00:00Z","mergedAt":"2026-10-04T10:00:00Z"/' "$state/pr-942.json"
	screen >/dev/null
	check "the merged PR ends the task" grep -q '^state: DONE' "$sandbox/tasks/acme-widgets-42.md"
	check "the merge ends the attempt" grep -q 'ended_at:' "$sandbox/tasks/acme-widgets-42.md"
	# An older task's log with no task file: its PR still has a row.
	printf '2026-09-01T09:00:00Z foreman: runner 1 started\n2026-09-01T09:20:00Z foreman: verified PR #907 https://github.com/acme/widgets/pull/907 at abc1234\n' >"$sandbox/logs/acme-widgets-7.log"
	echo '{"number":907,"url":"https://github.com/acme/widgets/pull/907","state":"CLOSED","title":"Old attempt","closedAt":"2026-09-02T10:00:00Z"}' >"$state/pr-907.json"
	dash -- refreshed keys=p "wait=#907" sleep=0.3 print >"$sandbox/history.txt" 2>/dev/null
	check "p opens the PR history" contains "$sandbox/history.txt" "PR HISTORY"
	check "the merged PR has a row with its title" grep -qE '#942 +Add retries +merged' "$sandbox/history.txt"
	check "the older PR from a log has a row" grep -qE '#907 +Old attempt +closed +unavailable' "$sandbox/history.txt"
	check "the newest close comes first" awk '/#942 +Add retries +merged/{a=NR} /#907 +Old attempt +closed/{b=NR} END{exit !(a && b && a<b)}' "$sandbox/history.txt"
	dash -- refreshed keys=p "wait=#942" 'keys=\r' "wait=total" sleep=0.3 print >"$sandbox/detail.txt" 2>/dev/null
	check "enter shows the phase times" contains "$sandbox/detail.txt" "planning"
	check "and the attempt's log" contains "$sandbox/detail.txt" "foreman: runner"
	check "j, ctrl-f, ctrl-b, ctrl-d, and ctrl-u page the list" dash -- refreshed keys=p "wait=#907" 'keys=j\x06\x02\x04\x15k' "wait=PR HISTORY"
	check "esc returns to the dashboard" dash -- refreshed keys=p "wait=#907" 'keys=\x1b' "wait=DELEGATED WORK"
}

# evaluated <repo dir> <n> [seconds] waits for task #n's evaluation, refreshing so a
# learner that lost the repository lock is started again.
evaluated() {
	local end=$((SECONDS + ${3:-15}))
	until [ -f "$sandbox/knowledge/$1/evaluations/$2.yaml" ]; do
		[ $SECONDS -lt $end ] || return 1
		screen >/dev/null
		sleep 0.1
	done
}

# propose <title> writes the evaluator's answer for that issue: one reusable,
# observed practice.
propose() {
	printf '{"assessment":"went fine","signals":[],"proposals":[{"kind":"practice","text":"Run go vet before each commit.","scope":"reusable","why":"vet caught a mistake","source":"observed","supports":[]}],"revisions":[]}' >"$state/evaluation-$1.json"
}

scenario_learning() {
	echo "learning: finished tasks teach later tasks in the same repository only"
	setup learning
	local k=$sandbox/knowledge/acme/widgets
	issue 43 "Fix pagination"
	issue 44 "Drop the old flag"
	propose "Add retries"
	propose "Fix pagination"
	printf '{"assessment":"the developer corrected the plan","signals":[],"proposals":[],"revisions":[{"id":"L1","action":"retire","text":null,"reason":"the developer said vet runs in CI only","source":"maintainer"}]}' >"$state/evaluation-Drop the old flag.json"

	start 42 >/dev/null
	check "the first task reaches a clean review" reviewed 42 20
	check "and is evaluated" evaluated acme/widgets 42
	check "one task's learning stays a candidate" eventually 5 grep -q 'status: candidate' "$k/learnings.yaml"
	check "a candidate stays out of knowledge.md" bash -c "! grep -qF 'go vet' '$k/knowledge.md'"

	start 43 >/dev/null
	check "the second task reaches a clean review" reviewed 43 20
	check "and is evaluated" evaluated acme/widgets 43
	check "a second task promotes the learning" eventually 5 grep -qF 'Run go vet before each commit.' "$k/knowledge.md"
	check "knowledge.md names no issue" bash -c "! grep -qE '#[0-9]|acme/' '$k/knowledge.md'"

	# A task in another repository: same origin, but foreman keys it as acme/linear.
	echo "[$(hit acme/linear 45 'Other repo work')]" >"$state/search-issues-assignee.json"
	issue 45 "Other repo work"
	dash -- refreshed "pick=acme/linear#45" 'keys=\r1\r' "wait=Started acme/linear#45" >/dev/null 2>&1 || true
	check "the other repository's task reaches a clean review" eventually 20 grep -q 'verdict: clean' "$sandbox/tasks/acme-linear-45.md"
	check "its prompt carries no knowledge from acme/widgets" bash -c "! grep -qF 'go vet' '$state/prompt.txt'"
	check "and has no knowledge section" bash -c "! grep -qF '## Repository knowledge' '$state/prompt.txt'"
	check "it is evaluated in its own store" evaluated acme/linear 45
	check "the other repository learns nothing" bash -c "! grep -qF 'go vet' '$sandbox/knowledge/acme/linear/knowledge.md'"

	start 44 >/dev/null
	check "the third task reaches a clean review" reviewed 44 20
	check "its planning prompt carries the learning" contains "$state/prompt.txt" "Run go vet before each commit."
	check "under the repository knowledge heading" contains "$state/prompt.txt" "## Repository knowledge"
	check "and is evaluated" evaluated acme/widgets 44
	check "a maintainer contradiction retires the learning" eventually 5 grep -q 'status: retired' "$k/learnings.yaml"
	check "the retired learning leaves knowledge.md" bash -c "! grep -qF 'go vet' '$k/knowledge.md'"
	check "the learner evaluated every finished task" bash -c "[ \"\$(grep -c '^claude evaluate' '$state/evaluations.log')\" -ge 4 ]"
}

scenarios=${*:-runner notify logs inbox inbox_lifecycle rereview conflicts respond cancel feedback conflict tui routing history linear learning}
for s in $scenarios; do "scenario_$s"; done
if [ $failures -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"
