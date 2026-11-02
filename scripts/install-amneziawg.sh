#!/usr/bin/env bash
set -Eeuo pipefail

readonly awg_go_commit="b5928efb6ca19f0153958460c3d141f04abc5c2e"
readonly awg_tools_commit="ee0f0a9aa34ff0a0da4b3433b9512781cfe02843"
readonly service_name="crsf2-amneziawg"
readonly project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

[[ "$EUID" -eq 0 ]] || fail "Run this installer as root (for example: sudo go run ./cmd/deploy-server)."
[[ "$(uname -s)" == "Linux" ]] || fail "Native installation is supported on Linux VDS only."
[[ -r /etc/os-release ]] || fail "Cannot identify the Linux distribution."
source /etc/os-release
case "${ID:-}" in
    debian|ubuntu) ;;
    *) fail "Supported distributions are Debian and Ubuntu; detected ${ID:-unknown}." ;;
esac
command -v systemctl >/dev/null || fail "systemd is required."
command -v go >/dev/null || fail "Install Go 1.25 or newer before running this installer."

go_version="$(go env GOVERSION | sed 's/^go//')"
minimum_go_version="$(printf '%s\n' 1.25.0 "$go_version" | sort -V | head -n1)"
[[ "$minimum_go_version" == "1.25.0" ]] || fail "Go 1.25 or newer is required; found go${go_version}."

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    build-essential ca-certificates git iproute2 make

build_dir="$(mktemp -d)"
cleanup() {
    rm -rf "$build_dir"
}
trap cleanup EXIT

fetch_commit() {
    local directory="$1"
    local repository="$2"
    local commit="$3"
    git init "$directory"
    git -C "$directory" remote add origin "$repository"
    git -C "$directory" fetch --depth 1 origin "$commit"
    git -C "$directory" checkout --detach FETCH_HEAD
}

fetch_commit "$build_dir/amneziawg-go" \
    https://github.com/amnezia-vpn/amneziawg-go.git "$awg_go_commit"
make -C "$build_dir/amneziawg-go"

fetch_commit "$build_dir/amneziawg-tools" \
    https://github.com/amnezia-vpn/amneziawg-tools.git "$awg_tools_commit"
make -C "$build_dir/amneziawg-tools/src" \
    WITH_BASHCOMPLETION=no \
    WITH_WGQUICK=yes \
    WITH_SYSTEMDUNITS=no
make -C "$build_dir/amneziawg-tools/src" install \
    DESTDIR="$build_dir/tools" \
    PREFIX=/usr/local \
    WITH_BASHCOMPLETION=no \
    WITH_WGQUICK=yes \
    WITH_SYSTEMDUNITS=no

install -d -m 0755 /usr/local/bin /usr/local/libexec /etc/amnezia/amneziawg
install -m 0755 "$build_dir/amneziawg-go/amneziawg-go" /usr/local/bin/amneziawg-go
install -m 0755 "$build_dir/tools/usr/local/bin/awg" /usr/local/bin/awg
install -m 0755 "$build_dir/tools/usr/local/bin/awg-quick" /usr/local/bin/awg-quick
install -m 0755 "$project_root/scripts/crsf2-awg-start.sh" /usr/local/libexec/crsf2-awg-start
install -m 0644 "$project_root/scripts/crsf2-amneziawg.service" \
    "/etc/systemd/system/${service_name}.service"

escaped_config_path="${project_root}/config/server.conf"
escaped_config_path="${escaped_config_path//\\/\\\\}"
escaped_config_path="${escaped_config_path//\"/\\\"}"
install -d -m 0755 /etc/default
printf 'AWG_CONFIG_FILE="%s"\n' "$escaped_config_path" > /etc/default/"$service_name"
chmod 0644 /etc/default/"$service_name"

install -d -m 0755 /etc/sysctl.d
printf 'net.ipv4.ip_forward=1\n' > /etc/sysctl.d/99-crsf2-routing.conf
sysctl -w net.ipv4.ip_forward=1
systemctl daemon-reload

printf 'Installed AmneziaWG tools and %s.service.\n' "$service_name"
