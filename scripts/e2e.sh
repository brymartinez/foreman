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
	mkdir -p "$sandbox/tasks"
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
"api repos/"*)
	n=${2##*/}
	cat "$s/api-$n.json" ;;
*) echo "stand-in gh: unhandled $*" >&2; exit 2 ;;
esac
EOF

	# Implementer: commits a change, records an open PR for its branch, and reports it.
	cat >"$sandbox/bin/claude" <<'EOF'
#!/usr/bin/env bash
echo "claude $*" >>"$E2E_STATE/agents.log"
if [[ " $* " == *" --no-session-persistence "* ]]; then
	echo '{"session_id":"r1","subtype":"success","total_cost_usd":0.05,"structured_output":{"verdict":"clean","summary":"looks right","findings":[]}}'
	exit 0
fi
sleep "${E2E_AGENT_SLEEP:-1}"
branch=$(git branch --show-current)
echo "// retries" >>widgets.go
git -c user.email=e2e@example.com -c user.name=e2e commit -qam "feat: retries"
printf '{"number":900,"url":"https://github.com/acme/widgets/pull/900","state":"OPEN","headRefName":"%s","baseRefName":"main","isCrossRepository":false}\n' "$branch" >"$E2E_STATE/pr-900.json"
echo "claude stderr progress" >&2
echo '{"session_id":"s1","subtype":"success","total_cost_usd":0.25,"structured_output":{"status":"pr_opened","summary":"added retries","prNumber":900,"prUrl":"https://github.com/acme/widgets/pull/900","validation":[{"command":"go test ./...","passed":true}],"question":null}}'
EOF

	# Reviewer (and implementer, when E2E_CODEX_IMPLEMENTS is set) on the codex JSONL protocol.
	cat >"$sandbox/bin/codex" <<'EOF'
#!/usr/bin/env bash
echo "codex $*" >>"$E2E_STATE/agents.log"
last=
while [ $# -gt 0 ]; do [ "$1" = -o ] && last=$2; shift; done
echo '{"type":"thread.started","thread_id":"t1"}'
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
	check "and notifies" grep -q 'Run failed.*acme/widgets#43' "$state/notifications.log"
	check "and leaves no agent behind" bash -c "! pgrep -f 'sleep 30.017' >/dev/null"

	issue 44 "Fix sorting"
	E2E_AGENT_SLEEP=30.017 foreman start 44 --agent claude >/dev/null
	sleep 0.5
	pkill -KILL -f "$work/foreman __run acme/widgets#44" || true
	foreman status >/dev/null
	check "a killed runner is reaped and notifies" grep -q 'Run failed.*acme/widgets#44' "$state/notifications.log"
	pkill -f "sleep 30.017" || true
}

scenarios=${*:-runner notify}
for s in $scenarios; do "scenario_$s"; done
if [ $failures -gt 0 ]; then
	echo "$failures check(s) failed"
	exit 1
fi
echo "all checks passed"
