#!/usr/bin/env bash
set -Eeuo pipefail

interface="${AWG_INTERFACE:-awg0}"
config="${AWG_CONFIG_FILE:-/etc/amnezia/amneziawg/awg0.conf}"
address="${AWG_ADDRESS:-10.77.0.1/24}"
socket="/run/amneziawg/${interface}.sock"
awg_pid=""
interface_created=false
route_added=()

if [[ ! -r "$config" ]]; then
    printf 'AmneziaWG config is missing or unreadable: %s\n' "$config" >&2
    exit 1
fi

if grep -Eq 'CHANGE_ME|REPLACE_WITH_' "$config"; then
    printf 'Replace the placeholder values in %s before starting the gateway.\n' "$config" >&2
    exit 1
fi

if [[ "$(cat /proc/sys/net/ipv4/ip_forward)" != "1" ]]; then
    printf 'IPv4 forwarding is disabled on the VDS host. Enable net.ipv4.ip_forward=1 before starting.\n' >&2
    exit 1
fi

if ip link show dev "$interface" >/dev/null 2>&1; then
    printf 'Interface %s already exists on the VDS host; refusing to replace it.\n' "$interface" >&2
    exit 1
fi

mkdir -p /run/amneziawg

cleanup() {
    local exit_code=$?
    trap - EXIT INT TERM

    for route in "${route_added[@]}"; do
        ip route del "$route" dev "$interface" 2>/dev/null || true
    done
    if [[ "$interface_created" == true ]]; then
        ip link del dev "$interface" 2>/dev/null || true
    fi

    if [[ -n "$awg_pid" ]] && kill -0 "$awg_pid" 2>/dev/null; then
        kill "$awg_pid" 2>/dev/null || true
        wait "$awg_pid" 2>/dev/null || true
    fi

    exit "$exit_code"
}
trap cleanup EXIT
trap 'exit 0' INT TERM

amneziawg-go -f "$interface" &
awg_pid=$!

for ((attempt = 0; attempt < 100; attempt++)); do
    if [[ -S "$socket" ]]; then
        break
    fi
    if ! kill -0 "$awg_pid" 2>/dev/null; then
        wait "$awg_pid"
        printf 'AmneziaWG userspace interface exited before its control socket became ready.\n' >&2
        exit 1
    fi
    sleep 0.1
done

if [[ ! -S "$socket" ]]; then
    printf 'Timed out waiting for the AmneziaWG control socket: %s\n' "$socket" >&2
    exit 1
fi

interface_created=true
awg setconf "$interface" <(awg-quick strip "$config")
ip address add "$address" dev "$interface"
ip link set mtu 1420 up dev "$interface"
while IFS= read -r route; do
    [[ -n "$route" ]] || continue
    ip route add "$route" dev "$interface"
    route_added+=("$route")
done < <(
    awk '
        /^\[Peer\]/ { in_peer = 1; next }
        /^\[/ { in_peer = 0 }
        in_peer && /^[[:space:]]*AllowedIPs[[:space:]]*=/ {
            sub(/^[^=]*=[[:space:]]*/, "")
            sub(/[[:space:]]*#.*/, "")
            count = split($0, networks, ",")
            for (i = 1; i <= count; i++) {
                gsub(/^[[:space:]]+|[[:space:]]+$/, "", networks[i])
                if (networks[i] != "") print networks[i]
            }
        }
    ' "$config" | sort -u
)

printf 'AmneziaWG gateway is ready on %s; routes installed for all peer AllowedIPs.\n' "$interface"

wait "$awg_pid"
