#!/usr/bin/env bash
set -Eeuo pipefail

readonly interface="awg0"
readonly config="${AWG_CONFIG_FILE:-}"
readonly socket="/run/amneziawg/${interface}.sock"
awg_pid=""
interface_created=false
route_added=()

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

[[ -n "$config" && -r "$config" ]] || fail "AWG_CONFIG_FILE is missing or unreadable: $config"
grep -Eq 'CHANGE_ME|REPLACE_WITH_' "$config" && fail "Replace all placeholders in $config."
[[ "$(cat /proc/sys/net/ipv4/ip_forward)" == "1" ]] || fail "Enable net.ipv4.ip_forward=1."
[[ -c /dev/net/tun ]] || fail "/dev/net/tun is unavailable; load the Linux tun module."
if ip link show dev "$interface" >/dev/null 2>&1; then
    fail "Interface $interface already exists; refusing to replace it."
fi

address="$(awk '
    /^\[Interface\]/ { in_interface = 1; next }
    /^\[/ { in_interface = 0 }
    in_interface && /^[[:space:]]*Address[[:space:]]*=/ {
        sub(/^[^=]*=[[:space:]]*/, "")
        sub(/[[:space:]]*#.*/, "")
        gsub(/^[[:space:]]+|[[:space:]]+$/, "")
        print
        exit
    }
' "$config")"
[[ -n "$address" ]] || fail "No Address found in the [Interface] section of $config."

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
        fail "AmneziaWG userspace process exited before its control socket became ready."
    fi
    sleep 0.1
done
[[ -S "$socket" ]] || fail "Timed out waiting for AmneziaWG control socket $socket."

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

printf 'AmneziaWG is ready on %s; routes installed for all peer AllowedIPs.\n' "$interface"
wait "$awg_pid"
