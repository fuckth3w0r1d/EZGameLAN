#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output="${root}/dist/EZGameLAN-Admin.exe"
cd "$root"

if [[ $# -gt 0 ]]; then
    if [[ $# -ne 2 || "$1" != "--output" ]]; then
        echo "usage: $0 [--output PATH]" >&2
        exit 2
    fi
    output="$2"
fi

mkdir -p "$(dirname "$output")"
GOCACHE="${GOCACHE:-/tmp/ezgamelan-gocache}" \
GOMODCACHE="${GOMODCACHE:-/tmp/ezgamelan-gomodcache}" \
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-H=windowsgui" \
    -o "$output" \
    ./cmd/ezgamelan-admin

echo "administrator client: $output"
sha256sum "$output"
