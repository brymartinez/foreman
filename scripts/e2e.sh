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
	dash -- refreshed "pick=acme/widgets#$1" 'keys=\r1\r' confirm-branch "wait=Started acme/widgets#$1"
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
"repo clone") git clone -q "${E2E_CLONE_URL:-$E2E_ORIGIN}" "$4" ;;
"api user") cat "$s/gh-user.json" 2>/dev/null || echo '{"login":"octo-work","id":42}' ;;
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

	# Stand-in ssh: ssh -T greets as $E2E_STATE/ssh-user, as GitHub does. Any other
	# call is git's transport, so the remote command runs locally on the origin.
	cat >"$sandbox/bin/ssh" <<'EOF'
#!/usr/bin/env bash
echo "ssh $*" >>"$E2E_STATE/ssh.log"
if [ "$1" = -T ]; then
	echo "Hi $(cat "$E2E_STATE/ssh-user" 2>/dev/null || echo octo-work)! You've successfully authenticated, but GitHub does not provide shell access." >&2
	exit 1
fi
exec sh -c "${@: -1}"
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
# A knowledge ingest's merge turn answers with merge.json, or fails on merge-fail.
if [[ "$*" == *"You merge two machines' learnings"* ]]; then
	echo "claude merge-knowledge" >>"$E2E_STATE/merges.log"
	if [ -f "$E2E_STATE/merge-fail" ]; then
		echo '{"type":"result","session_id":"m1","subtype":"error_during_execution","is_error":true}'
		exit 1
	fi
	printf '{"type":"result","session_id":"m1","subtype":"success","total_cost_usd":0.01,"structured_output":%s}\n' "$(cat "$E2E_STATE/merge.json")"
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
		# review_before_pr: fix findings locally, or push the reviewed commit and open the PR.
		if [[ "$2" == *"of your local change by"* ]]; then
			echo "claude local-fix" >>"$E2E_STATE/agents.log"
			echo "// retry 429" >>widgets.go
			git commit -qam "fix: retry 429"
			echo '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"review_ready","summary":"retried 429","nextObjective":null,"prNumber":null,"prUrl":null,"validation":[{"command":"go test ./...","passed":true}],"questions":[],"documents":null,"prTitle":"feat: add retries","prBody":"Retry 5xx and 429.\n\nCloses #42"}}'
			exit 0
		fi
		if [[ "$2" == *"Push it and open the pull request now"* ]]; then
			echo "claude open-pr" >>"$E2E_STATE/agents.log"
			b=$(git branch --show-current); i=${b#*/}; pr=$((900 + ${i%%-*}))
			git push -q origin "$b"
			echo "push $b $(git rev-parse HEAD)" >>"$E2E_STATE/pushes.log"
			file=$(printf '%s' "$2" | sed -n 's/.*--body-file \([^`]*\)`.*/\1/p' | head -n 1)
			body=$(sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' "$file" | awk '{printf "%s\\n", $0}')
			printf '{"number":%s,"url":"https://github.com/acme/widgets/pull/%s","state":"OPEN","headRefName":"%s","headRefOid":"%s","baseRefName":"main","isCrossRepository":false,"title":"feat: add retries","body":"%s"}\n' "$pr" "$pr" "$b" "$(git rev-parse HEAD)" "$body" >"$E2E_STATE/pr-$pr.json"
			printf '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.01,"structured_output":{"status":"pr_opened","summary":"opened the PR","prNumber":%s,"prUrl":"https://github.com/acme/widgets/pull/%s","validation":[{"command":"go test ./...","passed":true}],"questions":[]}}\n' "$pr" "$pr"
			exit 0
		fi
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
# No -c identity: foreman sets the commit author in the worktree's own config.
git commit -qam "feat: retries"
# review_before_pr: commit only, and propose the PR for review.
if [[ "$*" == *"## Proposing the pull request"* ]]; then
	echo "claude review-ready" >>"$E2E_STATE/agents.log"
	echo '{"type":"result","session_id":"s1","subtype":"success","total_cost_usd":0.25,"structured_output":{"status":"review_ready","summary":"added retries","nextObjective":null,"prNumber":null,"prUrl":null,"validation":[{"command":"go test ./...","passed":true}],"questions":[],"documents":null,"prTitle":"feat: add retries","prBody":"Retry 5xx only.\n\nCloses #42"}}'
	exit 0
fi
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
wt=
prev=
for arg in "$@"; do [ "$prev" = -C ] && wt=$arg; prev=$arg; done
[[ " $* " == *" --ephemeral "* ]] || wt=
if [ -n "$wt" ]; then
	b=$(git -C "$wt" branch --show-current)
	if git --git-dir="$E2E_ORIGIN" show-ref --verify --quiet "refs/heads/$b"; then echo pushed; else echo local; fi >>"$E2E_STATE/review-origin.log"
fi
last=
while [ $# -gt 0 ]; do [ "$1" = -o ] && last=$2; shift; done
echo '{"type":"thread.started","thread_id":"t1"}'
echo "$$" >"$E2E_STATE/review-agent.pid"
sleep "${E2E_REVIEW_SLEEP:-0}"
echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"reasoning_output_tokens":5}}'
if [ -n "$wt" ] && [ -f "$E2E_STATE/findings-once" ]; then
	rm "$E2E_STATE/findings-once"
	echo '{"verdict":"changes_requested","summary":"429 is missing","findings":[{"severity":"blocking","file":"widgets.go","line":1,"summary":"429 is not retried","detail":"retry 429 too"}]}' >"$last"
	exit 0
fi
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
	check "the implementer self-reviews before the competing review" bash -c "grep -E '^(claude|codex) review' '$state/agents.log' | head -2 | tr '\n' ' ' | grep -qx 'claude review codex review '"
	check "the review leaves out the issue body" bash -c "! grep -qF 'do it' '$state/review-prompt.txt'"
	check "the review leaves out the PRD" bash -c "! grep -qF 'R1. Retry 5xx responses.' '$state/review-prompt.txt'"
	task 42 sd >"$sandbox/show.txt"
	check "the show pane lists the plan" contains "$sandbox/show.txt" "Analysis https://github.com/acme/widgets/issues/42#issuecomment-3001"
	rm "$sandbox/stages/pr.md"
	check "a missing stage file stops foreman" bash -c "! dash -- 2>'$sandbox/err.txt'"
	check "and names the file" contains "$sandbox/err.txt" "stages/pr.md"
}

scenario_review_before_pr() {
	echo "review_before_pr: the PR opens only after a clean review of the local branch"
	setup review_before_pr
	echo "review_before_pr: true" >>"$sandbox/config.yaml"
	touch "$state/findings-once"
	check "the dashboard delegates #42" start 42
	check "the runner reaches a clean review" wait_status "review ✓"
	local task_file="$sandbox/tasks/acme-widgets-42.md" head
	check "the implementer proposed the PR without pushing" contains "$state/agents.log" "claude review-ready"
	check "the findings were fixed locally" contains "$state/agents.log" "claude local-fix"
	check "two reviews ran" test "$(grep -c '^codex review' "$state/agents.log")" = 2
	check "no review saw the branch on origin" bash -c "test \"\$(sort -u '$state/review-origin.log')\" = local"
	check "the re-review read the updated description" contains "$state/review-prompt.txt" "> Retry 5xx and 429."
	check "the review read a proposed pull request" contains "$state/review-prompt.txt" "proposed pull request"
	check "the branch was pushed once" test "$(wc -l <"$state/pushes.log" | tr -d ' ')" = 1
	head=$(git --git-dir="$sandbox/origin.git" rev-parse "$(sed -n 's/^branch: *//p' "$task_file")")
	check "the task records the pushed commit as the PR head" contains "$task_file" "pr_head: $head"
	check "the last review covered the pushed commit" contains "$task_file" "head: $head"
	check "the PR has the reviewed description" contains "$state/pr-942.json" "Retry 5xx and 429."
}

scenario_plan_reuse() {
	echo "plan_reuse: a task whose record lost its plan adopts the plan on the issue"
	setup plan_reuse
	local comments='' i=0 doc title
	for doc in analysis:Analysis prd:PRD design:Design "plan:Implementation plan"; do
		i=$((i + 1)); title=${doc#*:}
		comments="$comments${comments:+,}$(printf '{"id":%s,"html_url":"https://github.com/acme/widgets/issues/42#issuecomment-%s","body":"<!-- foreman:%s -->\\n## %s\\n\\nPosted earlier.","updated_at":"2026-09-20T10:00:00Z","user":{"login":"brymartinez"}}' "$((2000 + i))" "$((2000 + i))" "${doc%%:*}" "$title")"
	done
	printf '[[{"id":1999,"html_url":"https://github.com/acme/widgets/issues/42#issuecomment-1999","body":"Thanks, looking.","updated_at":"2026-09-20T09:00:00Z","user":{"login":"brymartinez"}},%s]]\n' "$comments" >"$state/conv-repos-acme-widgets-issues-42-comments.json"
	check "the dashboard delegates #42" start 42
	check "the runner reaches a clean review" wait_status "review ✓"
	check "no planning turn runs" bash -c "! grep -qx 'claude plan' '$state/agents.log'"
	check "nothing is posted on the issue" bash -c "! grep -qF 'api --method POST repos/acme/widgets/issues/42/comments' '$state/gh.log'"
	check "the implementation follows the plan on the issue" contains "$state/plan-posted-prompt.txt" "- Implementation plan: https://github.com/acme/widgets/issues/42#issuecomment-2004"
	check "the task records the adopted comments" grep -q 'comment_id: 2002' "$sandbox/tasks/acme-widgets-42.md"
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
	check "the show pane reports usage" contains "$sandbox/show.txt" 'Claude $0.32 (3 turns) · Codex 125 tokens (1 turn)'

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
	check "selecting an issue delegates it" dash -- refreshed "pick=acme/widgets#42" 'keys=\r1\r' confirm-branch "wait=Started acme/widgets#42"
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
	dash --no-auto -s all -- refreshed print >"$sandbox/include-all.txt" 2>/dev/null
	check "the inbox names the configured view" contains "$sandbox/include-all.txt" "INBOX · configured repos"
	dash -- refreshed keys=v print >"$sandbox/view.txt" 2>/dev/null
	check "v keeps the configured repository filter" bash -c "! grep -q 'acme/widgets#43' '$sandbox/view.txt'"
	cp "$sandbox/config.exclude" "$sandbox/config.yaml"
	check "searches ask for gh's maximum" contains "$state/gh.log" "--limit=1000"

	printf '#!/bin/sh\n[ "$1" = -w ] && echo "edited $2" >"%s/edited"\n' "$sandbox" >"$sandbox/bin/editor"
	chmod +x "$sandbox/bin/editor"
	check "-c runs an editor with arguments" bash -c "cd '$sandbox' && EDITOR='editor -w' VISUAL= '$work/foreman' -c </dev/null && grep -qF '$sandbox/config.yaml' '$sandbox/edited'"
	check "-c prefers VISUAL" bash -c "cd '$sandbox' && rm '$sandbox/edited' && VISUAL='editor -w' EDITOR=false '$work/foreman' -c </dev/null && test -f '$sandbox/edited'"
	cp "$sandbox/config.yaml" "$sandbox/config.good"
	echo "mentoins: false" >>"$sandbox/config.yaml"
	check "a misspelled config key stops the dashboard and names the key" bash -c "! dash -- 2>'$sandbox/unknown-key.txt' && grep -qF mentoins '$sandbox/unknown-key.txt'"
	cp "$sandbox/config.good" "$sandbox/config.yaml"
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
	dash --no-auto -s all -- refreshed print >"$sandbox/mixed.txt" 2>/dev/null
	check "the mixed inbox shows the GitHub section" contains "$sandbox/mixed.txt" "GITHUB"
	check "the mixed inbox shows the Linear section" contains "$sandbox/mixed.txt" "LINEAR"
	check "the mixed inbox shows both issues" bash -c "grep -qF 'acme/widgets#43' '$sandbox/mixed.txt' && grep -qF 'acme/linear · LIN-7' '$sandbox/mixed.txt'"
	check "the mapped repository hides GitHub issues" bash -c "! grep -qF 'acme/linear#44' '$sandbox/mixed.txt'"
	dash --no-auto -s github -- refreshed print >"$sandbox/github.txt" 2>/dev/null
	check "the GitHub filter hides Linear issues" bash -c "grep -qF 'acme/widgets#43' '$sandbox/github.txt' && ! grep -qF 'LIN-7' '$sandbox/github.txt'"
	dash --no-auto -s linear -- refreshed print >"$sandbox/linear.txt" 2>/dev/null
	check "the Linear filter hides GitHub inbox issues" bash -c "grep -qF 'LIN-7' '$sandbox/linear.txt' && ! grep -qF 'GITHUB' '$sandbox/linear.txt'"
	touch "$state/linear-fail"
	dash --auto -s all -- refreshed print >"$sandbox/failed.txt" 2>/dev/null
	check "a failed Linear refresh keeps GitHub results with a warning" bash -c "grep -qF 'acme/widgets#43' '$sandbox/failed.txt' && grep -qF 'warning' '$sandbox/failed.txt'"
	check "a failed mixed refresh blocks all auto starts" grep -q '^state: DISCOVERED' "$sandbox/tasks/acme-widgets-43.md"
	rm -f "$state/linear-fail"
	check "a GitHub issue can be delegated" dash --no-auto -s github -- refreshed "pick=acme/widgets#43" 'keys=\r1\r' confirm-branch "wait=Started acme/widgets#43"
	check "the GitHub task keeps its issue number" grep -q '^number: 43' "$sandbox/tasks/acme-widgets-43.md"
	if dash --no-auto -s linear -- refreshed "pick=acme/linear · LIN-7" 'keys=\r1\r' confirm-branch "wait=Started acme/linear · LIN-7" >"$sandbox/linear-start.txt" 2>&1; then
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
	check "the GitHub-only view hides delegated Linear work" bash -c "! grep -qF 'LIN-7' '$sandbox/github-delegated.txt'"
	dash --no-auto -s linear -- refreshed print >"$sandbox/linear-delegated.txt" 2>/dev/null
	check "the Linear-only view shows delegated Linear work" contains "$sandbox/linear-delegated.txt" "LIN-7"
	dash --no-auto -- refreshed print >"$sandbox/default-delegated.txt" 2>/dev/null
	check "the built-in default source hides delegated Linear work" bash -c "! grep -qF 'LIN-7' '$sandbox/default-delegated.txt'"
	echo "default_source: all" >>"$sandbox/config.yaml"
	dash --no-auto -- refreshed print >"$sandbox/both-delegated.txt" 2>/dev/null
	check "default_source all shows delegated Linear work" contains "$sandbox/both-delegated.txt" "LIN-7"
	unset LINEAR_API_KEY
	check "-s github overrides default_source and needs no Linear key" dash --no-auto -s github -- refreshed
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
	check "the runner is working" wait_status "IMPLEMENTING" 5
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

scenario_reset() {
	echo "reset: a FAILED run is discarded locally and started again on its open PR"
	setup reset
	issue 42 "Add retries" '[{"name":"triaged"}]'
	start 42 >/dev/null
	check "the first run reaches a clean review" wait_status "review ✓"
	local file="$sandbox/tasks/acme-widgets-42.md" wt branch log
	wt=$(sed -n 's/^worktree: *//p' "$file")
	branch=$(sed -n 's/^branch: *//p' "$file")
	log=$(ls "$sandbox"/logs/acme-widgets-42.log)
	perl -pi -e 's/^state: .*/state: FAILED/' "$file"
	echo wip >"$wt/notes.txt"
	echo '[[{"id":71,"html_url":"https://github.com/acme/widgets/pull/942#issuecomment-71","body":"Only retry idempotent calls.","updated_at":"2026-10-01T10:00:00Z","user":{"login":"brymartinez"}}]]' \
		>"$state/conv-repos-acme-widgets-issues-942-comments.json"
	local writes
	writes=$(grep -c 'api --method' "$state/gh.log")

	mv "$state/pr-942.json" "$state/pr-942.hold"
	check "with GitHub failing, x then y keeps the run" dash -- refreshed keys=l pick=acme/widgets#42 keys=xy "wait=nothing was deleted"
	check "and the task file" grep -q '^state: FAILED' "$file"
	check "and the dirty worktree" test -f "$wt/notes.txt"
	mv "$state/pr-942.hold" "$state/pr-942.json"

	check "x then y resets the task" dash -- refreshed keys=l pick=acme/widgets#42 keys=xy "wait=Reset acme/widgets#42"
	check "the task file keeps only the PR link" bash -c "grep -q '^reset:' '$file' && ! grep -q '^implementation_agent:' '$file'"
	check "the dirty worktree is gone" test ! -e "$wt"
	check "the local branch is gone" test -z "$(git -C "$sandbox/repositories/acme-widgets" branch --list "$branch")"
	check "the runner log is gone" test ! -e "$log"
	check "the remote branch is kept" git --git-dir="$sandbox/origin.git" rev-parse --verify -q "refs/heads/$branch"
	check "reset wrote nothing to GitHub" test "$(grep -c 'api --method' "$state/gh.log")" = "$writes"
	check "the task leaves delegated work" bash -c "! (dash -- refreshed print 2>/dev/null | grep -qF FAILED)"

	local turns
	turns=$(grep -c . "$state/agents.log")
	dash --auto -- refreshed sleep=2 >/dev/null 2>&1
	check "auto mode leaves a reset issue alone" test "$(grep -c . "$state/agents.log")" = "$turns"
	check "and the reset mark stays" grep -q '^reset:' "$file"

	start 42 >/dev/null
	check "a manual start reaches a clean review" wait_status "review ✓"
	check "the new run continues PR #942" bash -c "grep -q '^pr: 942$' '$file' && grep -q '^prior_pr: 942$' '$file' && ! grep -q '^reset:' '$file'"
	check "on the PR's branch" grep -q "^branch: $branch\$" "$file"
	check "a fresh session reads the prior PR" contains "$state/plan-posted-prompt.txt" "Continue updating pull request #942"
	check "with its comments" contains "$state/plan-posted-prompt.txt" "> Only retry idempotent calls."
	check "the branch kept the first run's commit" test "$(git --git-dir="$sandbox/origin.git" log --oneline "$branch" | grep -c 'feat: retries')" = 2
}

scenario_tui() {
	echo "tui: launch flags, the removed commands, and the dashboard's help"
	setup tui
	foreman </dev/null >"$sandbox/usage.txt" 2>&1
	check "foreman exits 0 on a pipe" test $? -eq 0
	check "and prints usage" contains "$sandbox/usage.txt" "-a, --auto"
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
	check "and prints usage, not the error" contains "$sandbox/broken.txt" "-a, --auto"
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
	echo "feedback: PR comments stay with the developer"
	setup feedback
	echo '[{"name":"test","bucket":"pass","link":"https://ci/1"}]' >"$state/checks.json"
	start 42 >/dev/null
	check "the run reaches the developer" wait_status "READY_FOR_HUMAN"

	# The developer comments on the pull request.
	conv="$state/conv-repos-acme-widgets-issues-942-comments.json"
	printf '[[{"id":4401,"html_url":"https://github.com/acme/widgets/pull/942#issuecomment-4401","body":"Cap the retries.","updated_at":"2026-10-01T09:00:00Z","user":{"login":"brymartinez"}}]]\n' >"$conv"
	check "a new comment leaves the task ready" wait_status "READY_FOR_HUMAN" 5
	screen >"$sandbox/inbox.txt"
	check "the inbox does not offer a feedback resume" test "$(grep -cE '^    REVIEWED' "$sandbox/inbox.txt" || true)" = 0
	check "the agent was not asked to answer" test "$(turns 'claude feedback')" = 0
	check "the comment was not answered by foreman" jq -e 'length == 1 and .[0][0].id == 4401' "$conv"
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
	wt=$(sed -n 's/^worktree: *//p' "$sandbox/tasks/acme-widgets-42.md")
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
	printf '{"assessment":"went fine","signals":[],"proposals":[{"kind":"practice","text":"Run go vet before each commit.","scope":"reusable","why":"vet caught a mistake","source":"observed","supports":[],"cites":[]}],"revisions":[]}' >"$state/evaluation-$1.json"
}

scenario_learning() {
	echo "learning: finished tasks teach later tasks in the same repository only"
	setup learning
	local k=$sandbox/knowledge/acme/widgets
	issue 43 "Fix pagination"
	issue 44 "Drop the old flag"
	propose "Add retries"
	propose "Fix pagination"
	printf '{"assessment":"the developer corrected the plan","signals":[],"proposals":[],"revisions":[{"id":"L1","action":"retire","text":null,"reason":"the developer said vet runs in CI only","source":"maintainer","cites":["C1"]}]}' >"$state/evaluation-Drop the old flag.json"

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
	dash -- refreshed "pick=acme/linear#45" 'keys=\r1\r' confirm-branch "wait=Started acme/linear#45" >/dev/null 2>&1 || true
	check "the other repository's task reaches a clean review" eventually 20 grep -q 'verdict: clean' "$sandbox/tasks/acme-linear-45.md"
	check "its prompt carries no knowledge from acme/widgets" bash -c "! grep -qF 'go vet' '$state/prompt.txt'"
	check "and has no knowledge section" bash -c "! grep -qF '## Repository knowledge' '$state/prompt.txt'"
	check "it is evaluated in its own store" evaluated acme/linear 45
	check "the other repository learns nothing" bash -c "! grep -qF 'go vet' '$sandbox/knowledge/acme/linear/knowledge.md'"

	# The developer's PR comment is the maintainer evidence the retirement needs.
	printf '[[{"id":4402,"html_url":"https://github.com/acme/widgets/pull/944#issuecomment-4402","body":"vet runs in CI only; drop it.","updated_at":"2026-10-01T09:00:00Z","user":{"login":"brymartinez"}}]]\n' >"$state/conv-repos-acme-widgets-issues-944-comments.json"
	start 44 >/dev/null
	check "the third task reaches a clean review" reviewed 44 20
	check "its planning prompt carries the learning" contains "$state/prompt.txt" "Run go vet before each commit."
	check "under the repository knowledge heading" contains "$state/prompt.txt" "## Repository knowledge"
	check "and is evaluated" evaluated acme/widgets 44
	check "the developer's comment is citable maintainer evidence" eventually 10 grep -q -- '- C1' "$k/evaluations/44.yaml"
	check "a maintainer contradiction retires the learning" eventually 5 grep -q 'status: retired' "$k/learnings.yaml"
	check "the retired learning leaves knowledge.md" bash -c "! grep -qF 'go vet' '$k/knowledge.md'"
	check "the learner evaluated every finished task" bash -c "[ \"\$(grep -c '^claude evaluate' '$state/evaluations.log')\" -ge 4 ]"
}

# seed <store dir> <task> <task start> <source> <text> gives the store's acme/widgets
# one learning backed by one task, as the learner would have written it.
seed() {
	local k="$1/acme/widgets" status=candidate
	[ "$4" = observed ] || status=active
	mkdir -p "$k/evaluations"
	cat >"$k/learnings.yaml" <<EOF
next_id: 1
learnings:
  - id: L1
    kind: convention
    text: $5
    status: $status
    evidence:
      - task: "$2"
        at: $3
        source: $4
    history:
      - at: $3
        task: "$2"
        action: propose
EOF
	cat >"$k/evaluations/$2.yaml" <<EOF
task: "$2"
task_at: $3
at: $3
fingerprint: fp-$2
assessment: fine
EOF
}

# store_digest <store dir> hashes every file but the lock, to show nothing changed.
store_digest() { (cd "$1" && find . -type f ! -name learning.lock | LC_ALL=C sort | xargs shasum); }

# merges counts the merge agent's turns.
merges() { if [ -f "$state/merges.log" ]; then wc -l <"$state/merges.log" | tr -d ' '; else echo 0; fi; }

# machine <name> writes config-<name>.yaml with its own knowledge_dir.
machine() { sed "s|knowledge_dir: .*|knowledge_dir: $sandbox/knowledge-$1|" "$sandbox/config.yaml" >"$sandbox/config-$1.yaml"; }
on() { local m=$1; shift; FOREMAN_CONFIG="$sandbox/config-$m.yaml" foreman "$@"; }

scenario_knowledge_transfer() {
	echo "knowledge_transfer: export, restore, merge, repeat, identity, conflict, failure"
	setup knowledge_transfer
	local a=$sandbox/knowledge b=$sandbox/knowledge-b c=$sandbox/knowledge-c out=$sandbox/out.txt before turns
	machine b
	machine c
	seed "$a" 1 2026-09-01T12:00:00Z observed "Wrap errors with the operation name."
	seed "$b" 7 2026-09-05T12:00:00Z observed "Errors carry the operation name."

	check "export writes the file" foreman knowledge export acme/widgets -o "$sandbox/a.md"
	check "the export names its repository" contains "$sandbox/a.md" "Repository: acme/widgets"
	check "and carries the versioned payload" contains "$sandbox/a.md" "format: foreman-knowledge/1"
	on b knowledge export acme/widgets -o "$sandbox/b.md" >/dev/null

	on c knowledge ingest "$sandbox/a.md" >"$out" 2>&1 || true
	check "an empty store restores the export" contains "$out" "Restored acme/widgets"
	check "without a merge turn" test "$(merges)" = 0
	check "the restore keeps the evaluation" test -f "$c/acme/widgets/evaluations/1.yaml"
	check "and regenerates knowledge.md" test -f "$c/acme/widgets/knowledge.md"

	if on c knowledge ingest "$sandbox/a.md" --repo acme/gadgets >"$out" 2>&1; then fail "another repository is rejected"; else pass "another repository is rejected"; fi
	check "and stores nothing there" test ! -e "$c/acme/gadgets/learnings.yaml"
	on c knowledge ingest "$sandbox/a.md" --repo acme/gadgets --rebind >"$out" 2>&1 || true
	check "rebinding stores it under the destination" test -f "$c/acme/gadgets/evaluations/1.yaml"

	before=$(store_digest "$a")
	printf '# notes\n' >"$sandbox/bad.md"
	if foreman knowledge ingest "$sandbox/bad.md" >"$out" 2>&1; then fail "an invalid file is rejected"; else pass "an invalid file is rejected"; fi
	touch "$state/merge-fail"
	if foreman knowledge ingest "$sandbox/b.md" >"$out" 2>&1; then fail "a failed merge turn is reported"; else pass "a failed merge turn is reported"; fi
	check "after one merge turn" test "$(merges)" = 1
	mv "$state/merge-fail" "$state/merge-fail.done"
	echo '{"groups":[{"members":["local:L1"],"kind":"convention","text":"Wrap errors.","conflict":null}]}' >"$state/merge.json"
	if foreman knowledge ingest "$sandbox/b.md" >"$out" 2>&1; then fail "a merge that leaves out a learning is rejected"; else pass "a merge that leaves out a learning is rejected"; fi
	check "after one more merge turn" test "$(merges)" = 2
	check "and none of the failures changed the store" test "$(store_digest "$a")" = "$before"

	echo '{"groups":[{"members":["local:L1","import:L1"],"kind":"convention","text":"Wrap returned errors with the operation name.","conflict":null}]}' >"$state/merge.json"
	on b knowledge ingest "$sandbox/a.md" --dry-run >"$out" 2>&1 || true
	check "a dry run shows the merged knowledge.md" contains "$out" "- Wrap returned errors with the operation name."
	check "and saves nothing" bash -c "! grep -qF 'Wrap returned' '$b/acme/widgets/learnings.yaml'"

	turns=$(merges)
	foreman knowledge ingest "$sandbox/b.md" >"$out" 2>&1 || true
	check "both sides' knowledge merges in one turn" test "$(merges)" = $((turns + 1))
	check "into one active learning" contains "$a/acme/widgets/knowledge.md" "Wrap returned errors with the operation name."
	check "with both tasks' evaluations" test -f "$a/acme/widgets/evaluations/7.yaml"

	before=$(store_digest "$a")
	foreman knowledge ingest "$sandbox/b.md" >"$out" 2>&1 || true
	check "a repeat ingest changes nothing" contains "$out" "nothing changed"
	check "without a merge turn" test "$(merges)" = $((turns + 1))
	check "and leaves the store byte for byte" test "$(store_digest "$a")" = "$before"

	machine d
	machine e
	seed "$sandbox/knowledge-d" 1 2026-09-01T12:00:00Z maintainer "Indent YAML with two spaces."
	seed "$sandbox/knowledge-e" 2 2026-09-01T12:00:10Z maintainer "Indent YAML with four spaces."
	on e knowledge export acme/widgets -o "$sandbox/e.md" >/dev/null
	echo '{"groups":[{"members":["local:L1","import:L1"],"kind":"convention","text":"Indent YAML consistently.","conflict":"they ask for different indentation"}]}' >"$state/merge.json"
	before=$(store_digest "$sandbox/knowledge-d")
	if on d knowledge ingest "$sandbox/e.md" >"$out" 2>&1; then fail "an unresolved conflict fails"; else pass "an unresolved conflict fails"; fi
	check "and reports both corrections" bash -c "grep -qF 'two spaces' '$out' && grep -qF 'four spaces' '$out'"
	check "and saves nothing" test "$(store_digest "$sandbox/knowledge-d")" = "$before"
}

scenario_account() {
	echo "account: a task keeps the gh account it started under for commits and pushes"
	setup account
	export E2E_CLONE_URL="e2e-host:$sandbox/origin.git"
	local file="$sandbox/tasks/acme-widgets-42.md" email=42+octo-work@users.noreply.github.com
	check "the dashboard delegates #42" start 42
	check "the runner reaches a clean review" wait_status "review ✓"
	check "the task pins the active gh account" grep -q '^account: octo-work$' "$file"
	check "and its noreply email" grep -q "^account_email: $email\$" "$file"
	check "the managed checkout pushes over SSH" contains "$sandbox/repositories/acme-widgets/.git/config" "e2e-host:"
	check "the SSH host was asked who it pushes as" contains "$state/ssh.log" "ssh -T -o BatchMode=yes e2e-host"
	check "the pushed commit is authored and committed by the pinned account" \
		test "$(git --git-dir="$sandbox/origin.git" log -1 --format='%an <%ae>|%cn <%ce>' --branches='*42-*')" = "octo-work <$email>|octo-work <$email>"
	check "the main checkout keeps its own identity" test "$(git -C "$sandbox/checkout" config user.name)" = e2e

	issue 43 "Fix pagination"
	file="$sandbox/tasks/acme-widgets-43.md"
	E2E_AGENT_SLEEP=30.017 start 43 >/dev/null
	check "a second task is working" wait_status "IMPLEMENTING" 5
	sleep 0.5
	kill -TERM "$(runner_pid 43)"
	check "stopping its runner leaves it FAILED" wait_status "FAILED" 5
	stop_agent implement
	local turns
	turns=$(grep -c . "$state/agents.log")
	echo '{"login":"octo-home","id":7}' >"$state/gh-user.json"
	start 43 >/dev/null 2>&1 || true
	check "a restart after gh switches accounts fails" grep -q '^state: FAILED' "$file"
	check "and says how to switch back" contains "$file" 'gh auth switch --user octo-work'
	check "and keeps the pin" grep -q '^account: octo-work$' "$file"
	rm "$state/gh-user.json"
	echo octo-home >"$state/ssh-user"
	start 43 >/dev/null 2>&1 || true
	check "a restart whose SSH key pushes as another account fails" grep -q '^state: FAILED' "$file"
	check "and names the SSH host" contains "$file" 'SSH host e2e-host pushes as octo-home'
	check "no agent ran for either restart" test "$(grep -c . "$state/agents.log")" = "$turns"
	rm "$state/ssh-user"
	start 43 >/dev/null
	check "a restart with matching accounts resumes the task" wait_status "review ✓"
	unset E2E_CLONE_URL
}

scenarios=${*:-runner review_before_pr plan_reuse notify logs inbox inbox_lifecycle rereview conflicts respond cancel reset feedback conflict tui routing history linear account learning knowledge_transfer}
for s in $scenarios; do "scenario_$s"; done
if [ $failures -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"
