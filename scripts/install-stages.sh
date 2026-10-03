#!/bin/sh
# install-stages.sh <defaults_dir> <stages_dir> copies each default stage file that is
# missing. Installed files are the user's, so a file that differs from its default is
# reported with a command to replace it, never overwritten.
set -eu

defaults=$1
stages=$2
for f in "$defaults"/*.md; do
	dest="$stages/$(basename "$f")"
	if [ ! -e "$dest" ]; then
		cp "$f" "$dest"
		echo "wrote $dest"
	elif cmp -s "$f" "$dest"; then
		echo "kept $dest"
	else
		echo "differs $dest"
		echo "  replace it with: cp '$f' '$dest'"
	fi
done
