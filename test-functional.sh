#!/usr/bin/env bash
# End-to-end product regression suite for macOS/Linux hosts with libkrun.
# Exercises the public CLI: run/start/stop/fork/ssh/ps/rm, persistent ext4
# state, the default project mount, identity isolation, and automatic port
# switching. This is intentionally a long hardware-backed test, not a unit test.

set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
DEVD="$ROOT/bin/devd"
IMAGE=${IMAGE:-nicolaka/netshoot}
STATE=$(mktemp -d "${TMPDIR:-/tmp}/devd-functional-state.XXXXXX")
PROJECT=$(mktemp -d "${TMPDIR:-/tmp}/devd-functional-project.XXXXXX")
PORT=${PORT:-$(python3 - <<'PY'
import random
import socket
for _ in range(1000):
    port = random.randint(10000, 30000)
    with socket.socket() as sock:
        try:
            sock.bind(("0.0.0.0", port))
        except OSError:
            continue
        print(port)
        break
else:
    raise SystemExit("no free test port")
PY
)}

export DEVD_DIR="$STATE"
export DEVD_SSH_CONFIG="$STATE/ssh-config"

step() { printf '\n=== %s ===\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }
assert_eq() {
    local want=$1 got=$2 label=$3
    [[ "$got" == "$want" ]] || fail "$label: expected '$want', got '$got'"
    printf 'PASS %s\n' "$label"
}
remote() {
    "$DEVD" ssh "$1" -- "$2"
}
wait_guest_http() {
    local workspace=$1 want=$2 result=""
    for _ in $(seq 1 50); do
        result=$(remote "$workspace" "curl -fsS --max-time 1 http://127.0.0.1:$PORT/index.html" 2>/dev/null || true)
        [[ "$result" == "$want" ]] && return 0
        sleep 0.2
    done
    fail "$workspace server did not become ready (last response: '$result')"
}
wait_http() {
    local want=$1 result=""
    for _ in $(seq 1 50); do
        result=$(curl -fsS --max-time 1 "http://127.0.0.1:$PORT/index.html" 2>/dev/null || true)
        [[ "$result" == "$want" ]] && { printf '%s' "$result"; return 0; }
        sleep 0.2
    done
    printf '%s' "$result"
    return 1
}
cleanup() {
    if [[ ${KEEP:-0} == 1 ]]; then
        printf '\nKEEP=1: state retained at %s; project at %s\n' "$STATE" "$PROJECT"
        return
    fi
    set +e
    for workspace in port-beta port-alpha child source; do
        "$DEVD" rm -f "$workspace" >/dev/null 2>&1 || true
    done
    sleep 0.2
    rm -rf "$STATE" "$PROJECT"
}
trap cleanup EXIT

step "Build the complete three-binary bundle"
if [[ ${SKIP_BUILD:-0} != 1 ]]; then
    (cd "$ROOT" && go build -o bin/devd ./cmd/devd && scripts/build-runtime bin)
fi
[[ -x "$DEVD" && -x "$ROOT/bin/devd-vm" && -x "$ROOT/bin/devd-image-helper" ]] || \
    fail "complete devd bundle is not built"

if [[ -n ${DEVD_TEST_IMAGE_CACHE:-} ]]; then
    rm -rf "$STATE/images"
    ln -s "$DEVD_TEST_IMAGE_CACHE" "$STATE/images"
fi

step "Run a workspace with the default project mount"
printf 'from-host\n' >"$PROJECT/host.txt"
run_output=$(cd "$PROJECT" && "$DEVD" run "$IMAGE" -n source)
printf '%s\n' "$run_output"
[[ "$run_output" == *'Workspace "source" ready'* ]] || fail "run did not report readiness"
assert_eq "from-host" "$(remote source 'cat /workspace/host.txt')" "host file visible in guest"
remote source 'printf "from-guest\n" >/workspace/guest.txt'
assert_eq "from-guest" "$(tr -d '\n' <"$PROJECT/guest.txt")" "guest write visible on host"

step "Persist guest disk state across stop/start"
remote source 'printf "persistent\n" >/root/persistent-marker'
source_machine=$(remote source 'cat /etc/machine-id')
source_host_key=$(remote source 'cat /etc/ssh/ssh_host_ed25519_key.pub')
"$DEVD" stop source
[[ $("$DEVD" ps -a) == *"source"*"stopped"* ]] || fail "ps -a did not show stopped source"
"$DEVD" start source
assert_eq "persistent" "$(remote source 'cat /root/persistent-marker')" "disk state after restart"

step "Fork complete stopped state with fresh identity"
"$DEVD" stop source
"$DEVD" fork source -n child
assert_eq "persistent" "$(remote child 'cat /root/persistent-marker')" "fork inherited guest disk"
child_machine=$(remote child 'cat /etc/machine-id')
child_host_key=$(remote child 'cat /etc/ssh/ssh_host_ed25519_key.pub')
[[ "$child_machine" != "$source_machine" ]] || fail "fork reused machine-id"
[[ "$child_host_key" != "$source_host_key" ]] || fail "fork reused SSH host key"
printf 'PASS fork identity diverged\n'
assert_eq "from-host" "$(remote child 'cat /workspace/host.txt')" "fork reused host mount"
remote child 'printf "child-only\n" >/root/child-only'
"$DEVD" stop child
"$DEVD" start source
if remote source 'test -e /root/child-only' >/dev/null 2>&1; then
    fail "child disk write leaked into source"
fi
printf 'PASS fork disk writes are isolated\n'

step "Automatically manage and switch a shared host port"
"$DEVD" run "$IMAGE" -n port-alpha --no-mount -p "$PORT" \
    --cmd "cd /tmp && echo from-alpha >index.html && python3 -m http.server $PORT"
"$DEVD" run "$IMAGE" -n port-beta --no-mount -p "$PORT" \
    --cmd "cd /tmp && echo from-beta >index.html && python3 -m http.server $PORT"
wait_guest_http port-alpha from-alpha
wait_guest_http port-beta from-beta
assert_eq "from-beta" "$(wait_http from-beta)" "last-started workspace receives host traffic"
"$DEVD" switch port-alpha
assert_eq "from-alpha" "$(wait_http from-alpha)" "switch changes host route"
assert_eq "from-alpha" "$(remote port-alpha "curl -fsS http://127.0.0.1:$PORT/index.html")" "alpha guest loopback"
assert_eq "from-beta" "$(remote port-beta "curl -fsS http://127.0.0.1:$PORT/index.html")" "beta guest loopback"

step "Remove workspaces and stop the now-unused proxy"
"$DEVD" rm -f port-beta
"$DEVD" rm -f port-alpha
for _ in $(seq 1 30); do
    [[ ! -S "$STATE/daemon.sock" ]] && break
    sleep 0.1
done
[[ ! -S "$STATE/daemon.sock" ]] || fail "proxy daemon remained after last declared port was removed"
"$DEVD" rm child
"$DEVD" rm -f source
ps_output=$("$DEVD" ps -a)
[[ "$ps_output" != *source* && "$ps_output" != *child* && "$ps_output" != *port-alpha* ]] || \
    fail "removed workspace remains in ps -a"

step "ALL FUNCTIONAL TESTS PASSED"
