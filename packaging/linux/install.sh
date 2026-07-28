#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  sudo ./packaging/linux/install.sh --host HOST --binary PATH

Options:
  --host HOST      Public IPv4 address or DNS name used in join links.
  --binary PATH    Locally built Linux amd64 ezlanctl binary.
EOF
}

host=""
binary=""
while (($# > 0)); do
    case "$1" in
        --host)
            host="${2:-}"
            shift 2
            ;;
        --binary)
            binary="${2:-}"
            shift 2
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if ((EUID != 0)); then
    echo "Run this installer as root." >&2
    exit 1
fi
if [[ -z "$host" || -z "$binary" ]]; then
    usage >&2
    exit 2
fi
if [[ ! -f "$binary" || -L "$binary" ]]; then
    echo "Binary must be a regular file: $binary" >&2
    exit 1
fi
if [[ ! -x /usr/sbin/openvpn ]]; then
    echo "OpenVPN 2.6 is required at /usr/sbin/openvpn." >&2
    exit 1
fi
if ! /usr/sbin/openvpn --version | head -n 1 | grep -Eq '^OpenVPN 2\.6\.'; then
    echo "The installed OpenVPN version is outside the tested 2.6 series." >&2
    exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
install -m 0755 "$binary" /usr/local/bin/ezlanctl

if [[ ! -e /etc/ezgamelan/config.json ]]; then
    /usr/local/bin/ezlanctl init --host "$host"
else
    echo "Keeping existing /etc/ezgamelan configuration."
fi

install -m 0644 \
    "$script_dir/ezgamelan-openvpn.service" \
    /etc/systemd/system/ezgamelan-openvpn.service
install -m 0644 \
    "$script_dir/ezgamelan-enroll.service" \
    /etc/systemd/system/ezgamelan-enroll.service

systemctl daemon-reload
systemctl enable --now ezgamelan-openvpn.service
systemctl enable --now ezgamelan-enroll.service

systemctl --quiet is-active ezgamelan-openvpn.service
systemctl --quiet is-active ezgamelan-enroll.service

cat <<EOF
EZGameLAN server is running.
Open these cloud firewall ports:
  21111/UDP  OpenVPN data
  21112/TCP  one-time enrollment
EOF
