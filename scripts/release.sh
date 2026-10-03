#!/bin/bash
# Build release archives for one version: scripts/release.sh v0.1.0
# Writes dist/release/foreman_<version>_<os>_<arch>.tar.gz and checksums.txt.
set -euo pipefail
version=${1:?usage: scripts/release.sh <version>}
cd "$(dirname "$0")/.."
out=dist/release
rm -rf "$out" && mkdir -p "$out"
# The runner detaches with setsid, so only Unix targets are built.
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os=${target%/*} arch=${target#*/}
  name="foreman_${version}_${os}_${arch}"
  mkdir -p "$out/$name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$out/$name/foreman" ./cmd/foreman
  cp README.md "$out/$name/"
  mkdir -p "$out/$name/stages" && cp defaults/stages/*.md "$out/$name/stages/"
  cp defaults/routing.yaml "$out/$name/"
  tar -C "$out" -czf "$out/$name.tar.gz" "$name"
  rm -rf "${out:?}/$name"
done
(cd "$out" && shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt)
ls -1 "$out"
