#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if ! command -v powershell.exe >/dev/null; then
    echo "Build the portable package on Windows with packaging/client/build-release.ps1." >&2
    exit 1
fi
if command -v wslpath >/dev/null; then
    script="$(wslpath -w "${script_dir}/build-release.ps1")"
else
    script="$(cygpath -w "${script_dir}/build-release.ps1")"
fi
exec powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$script" "$@"
