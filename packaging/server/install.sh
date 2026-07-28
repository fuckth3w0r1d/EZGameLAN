#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  sudo ./packaging/server/install.sh --host HOST --binary PATH [options]

Options:
  --host HOST          Public DNS name or IPv4 address used by OpenVPN.
  --binary PATH        Locally built Linux amd64 ezlanctl binary.
  --enroll-url URL     Public Nginx HTTPS endpoint. Defaults to
                       https://HOST/ezgamelan/v1/enroll.
  --admin-user USER    SSH user allowed to run the fixed admin-stdio command.
EOF
}

host=""
binary=""
enroll_url=""
admin_user=""
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
        --enroll-url)
            enroll_url="${2:-}"
            shift 2
            ;;
        --admin-user)
            admin_user="${2:-}"
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
if [[ -z "$enroll_url" ]]; then
    enroll_url="https://${host}/ezgamelan/v1/enroll"
fi
if [[ ! -f "$binary" || -L "$binary" ]]; then
    echo "Binary must be a regular file: $binary" >&2
    exit 1
fi
if [[ ! -x /usr/sbin/openvpn ]]; then
    echo "OpenVPN 2.6 is required at /usr/sbin/openvpn." >&2
    exit 1
fi
if [[ -n "$admin_user" ]]; then
    if [[ ! "$admin_user" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] ||
        ! getent passwd "$admin_user" >/dev/null; then
        echo "Admin user is invalid or does not exist: $admin_user" >&2
        exit 1
    fi
fi
if ! /usr/sbin/openvpn --version | head -n 1 | grep -Eq '^OpenVPN 2\.6\.'; then
    echo "The installed OpenVPN version is outside the tested 2.6 series." >&2
    exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
install -m 0755 "$binary" /usr/local/bin/ezlanctl

/usr/local/bin/ezlanctl init \
    --host "$host" \
    --enroll-url "$enroll_url" \
    --enroll-listen "127.0.0.1:21112"

install -m 0644 \
    "$script_dir/ezgamelan-openvpn.service" \
    /etc/systemd/system/ezgamelan-openvpn.service
install -m 0644 \
    "$script_dir/ezgamelan-enroll.service" \
    /etc/systemd/system/ezgamelan-enroll.service

if [[ -n "$admin_user" ]]; then
    sudoers_temp="$(mktemp)"
    trap 'rm -f "$sudoers_temp"' EXIT
    printf '%s ALL=(root) NOPASSWD: /usr/local/bin/ezlanctl admin-stdio\n' \
        "$admin_user" >"$sudoers_temp"
    chmod 0440 "$sudoers_temp"
    /usr/sbin/visudo -cf "$sudoers_temp"
    install -m 0440 "$sudoers_temp" /etc/sudoers.d/ezgamelan-admin
fi

systemctl daemon-reload
systemctl enable ezgamelan-openvpn.service
systemctl enable ezgamelan-enroll.service
systemctl restart ezgamelan-openvpn.service
systemctl restart ezgamelan-enroll.service

systemctl --quiet is-active ezgamelan-openvpn.service
systemctl --quiet is-active ezgamelan-enroll.service

cat <<EOF
EZGameLAN server is running.
Cloud firewall:
  21111/UDP  OpenVPN data
  443/TCP    existing Nginx HTTPS
The enrollment backend only listens on 127.0.0.1:21112.
EOF
