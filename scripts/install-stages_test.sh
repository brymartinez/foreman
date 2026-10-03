#!/bin/sh
# Tests scripts/install-stages.sh against temporary directories.
set -eu

script="$(cd "$(dirname "$0")" && pwd)/install-stages.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The space checks that paths with spaces stay one argument.
defaults="$tmp/foreman clone/defaults"
stages="$tmp/stages"
mkdir -p "$defaults" "$stages"
failed=0

fail() {
	echo "FAIL: $1" >&2
	failed=1
}

printf 'new default\n' >"$defaults/missing.md"
printf 'same\n' >"$defaults/same.md"
printf 'same\n' >"$stages/same.md"
printf 'new default\n' >"$defaults/old.md"
printf 'my old copy\n' >"$stages/old.md"

out=$(sh "$script" "$defaults" "$stages")

cmp -s "$defaults/missing.md" "$stages/missing.md" || fail "a missing stage file is not written"
echo "$out" | grep -qxF "wrote $stages/missing.md" || fail "a missing stage file is not reported as wrote"

echo "$out" | grep -qxF "kept $stages/same.md" || fail "an identical stage file is not reported as kept"

echo "$out" | grep -qxF "differs $stages/old.md" || fail "a differing stage file is not reported as differs"
echo "$out" | grep -qxF "  replace it with: cp '$defaults/old.md' '$stages/old.md'" || fail "a differing stage file has no cp command"
[ "$(cat "$stages/old.md")" = "my old copy" ] || fail "a differing stage file was overwritten"

if [ "$failed" -ne 0 ]; then
	printf 'output was:\n%s\n' "$out" >&2
	exit 1
fi
echo "ok  install-stages"
