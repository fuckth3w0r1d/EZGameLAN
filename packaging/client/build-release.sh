#!/usr/bin/env bash
set -euo pipefail

openvpn_name="OpenVPN-2.6.21-I001-amd64.msi"
openvpn_hash="845b654edb92cc02a05dc335693980c4f2d4a4f09689d0844309753de2cb5471"
openvpn_source="https://build.openvpn.net/downloads/releases/${openvpn_name}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
output="${root}/dist/EZGameLAN-Setup.exe"
openvpn_msi="${root}/third_party/openvpn/${openvpn_name}"
cd "${root}"

usage() {
    echo "usage: $0 [--openvpn-msi PATH] [--output PATH]" >&2
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --openvpn-msi)
            [[ $# -ge 2 ]] || { usage; exit 2; }
            openvpn_msi="$2"
            shift 2
            ;;
        --output)
            [[ $# -ge 2 ]] || { usage; exit 2; }
            output="$2"
            shift 2
            ;;
        *)
            usage
            exit 2
            ;;
    esac
done

[[ -f "${openvpn_msi}" ]] || {
    echo "OpenVPN MSI not found: ${openvpn_msi}" >&2
    exit 1
}
[[ "$(basename "${openvpn_msi}")" == "${openvpn_name}" ]] || {
    echo "expected ${openvpn_name}" >&2
    exit 1
}
actual_hash="$(sha256sum "${openvpn_msi}" | awk '{print $1}')"
[[ "${actual_hash}" == "${openvpn_hash}" ]] || {
    echo "OpenVPN MSI SHA-256 mismatch" >&2
    exit 1
}

temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT
stage="${temporary}/stage"
mkdir -p "${stage}/payload" "${stage}/third_party" "$(dirname "${output}")"

GOCACHE="${GOCACHE:-/tmp/ezgamelan-gocache}" \
GOMODCACHE="${GOMODCACHE:-/tmp/ezgamelan-gomodcache}" \
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath \
    -o "${stage}/payload/ezgamelan-service.exe" \
    "${root}/cmd/ezgamelan-service"
GOCACHE="${GOCACHE:-/tmp/ezgamelan-gocache}" \
GOMODCACHE="${GOMODCACHE:-/tmp/ezgamelan-gomodcache}" \
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-H=windowsgui" \
    -o "${stage}/payload/ezgamelan-client.exe" \
    "${root}/cmd/ezgamelan-client"
GOCACHE="${GOCACHE:-/tmp/ezgamelan-gocache}" \
GOMODCACHE="${GOMODCACHE:-/tmp/ezgamelan-gomodcache}" \
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-H=windowsgui" \
    -o "${stage}/payload/ezgamelan-uninstall.exe" \
    "${root}/cmd/ezgamelan-uninstall"
GOCACHE="${GOCACHE:-/tmp/ezgamelan-gocache}" \
GOMODCACHE="${GOMODCACHE:-/tmp/ezgamelan-gomodcache}" \
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "-H=windowsgui" \
    -o "${temporary}/ezgamelan-setup-base.exe" \
    "${root}/cmd/ezgamelan-setup"

cp "${openvpn_msi}" "${stage}/third_party/${openvpn_name}"
printf '%s *%s\n' "${openvpn_hash}" "${openvpn_name}" \
    >"${stage}/third_party/openvpn.sha256"
# Windows PowerShell 5.1 needs the BOM to decode non-ASCII script text as UTF-8.
printf '\357\273\277' >"${stage}/install.ps1"
cat "${script_dir}/install.ps1" >>"${stage}/install.ps1"
printf '\357\273\277' >"${stage}/uninstall.ps1"
cat "${script_dir}/uninstall.ps1" >>"${stage}/uninstall.ps1"
cat >"${stage}/THIRD_PARTY.txt" <<EOF
OpenVPN Community Windows x64
Package: ${openvpn_name}
Source: ${openvpn_source}
SHA-256: ${openvpn_hash}
License: GPL-2.0
License source: https://github.com/OpenVPN/openvpn/blob/master/COPYING

Only the OpenVPN core and TAP-Windows6 MSI features are selected. EZGameLAN
does not launch or depend on OpenVPN GUI.
EOF

(
    cd "${stage}"
    zip -q -r "${temporary}/payload.zip" \
        install.ps1 uninstall.ps1 THIRD_PARTY.txt payload third_party
)
GOCACHE="${GOCACHE:-/tmp/ezgamelan-gocache}" \
GOMODCACHE="${GOMODCACHE:-/tmp/ezgamelan-gomodcache}" \
    go run ./cmd/ezgamelan-pack \
    -exe "${temporary}/ezgamelan-setup-base.exe" \
    -payload "${temporary}/payload.zip" \
    -output "${output}"

echo "single-file installer: ${output}"
sha256sum "${output}"
