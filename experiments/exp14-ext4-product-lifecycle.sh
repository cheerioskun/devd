#!/usr/bin/env bash
# Experiment 14: end-to-end product ext4 create/run/fork lifecycle.
set -euo pipefail

IMAGE=${1:-nicolaka/netshoot}
KEEP=${KEEP:-0}
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
WORK_DIR=${WORK_DIR:-"${TMPDIR:-/tmp}/devd-exp14"}
export DEVD_DIR="$WORK_DIR/state"
export DEVD_SSH_CONFIG="$WORK_DIR/ssh-config"
export PATH="$ROOT_DIR/bin:$PATH"

cleanup() {
    bin/devd rm -f exp14-child >/dev/null 2>&1 || true
    bin/devd rm -f exp14-source >/dev/null 2>&1 || true
    if [ "$KEEP" = 1 ]; then
        echo "INFO Keeping $WORK_DIR"
    else
        rm -rf "$WORK_DIR"
    fi
}
trap cleanup EXIT INT TERM

[ -x "$ROOT_DIR/bin/devd" ] || { echo "FAIL: run build first" >&2; exit 1; }
[ -x "$ROOT_DIR/bin/devd-vm" ] || { echo "FAIL: run build first" >&2; exit 1; }
[ -x "$ROOT_DIR/bin/devd-image-helper" ] || { echo "FAIL: run build first" >&2; exit 1; }

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR/project"
printf host-project >"$WORK_DIR/project/marker"

bin/devd run "$IMAGE" --name exp14-source --mount "$WORK_DIR/project:/workspace"
key="$DEVD_DIR/ssh/devd_ed25519"
ssh_cmd=(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -i "$key")
"${ssh_cmd[@]}" -p 2222 root@127.0.0.1 'printf parent-state >/root/state; sync'
source_machine=$("${ssh_cmd[@]}" -p 2222 root@127.0.0.1 cat /etc/machine-id)
source_hostkey=$("${ssh_cmd[@]}" -p 2222 root@127.0.0.1 cat /etc/ssh/ssh_host_ed25519_key.pub)
bin/devd stop exp14-source

started=$(python3 -c 'import time; print(time.monotonic_ns())')
bin/devd run --name exp14-child --fork exp14-source
fork_ms=$(python3 -c "import time; print((time.monotonic_ns() - $started) // 1000000)")
child_machine=$("${ssh_cmd[@]}" -p 2223 root@127.0.0.1 cat /etc/machine-id)
child_hostkey=$("${ssh_cmd[@]}" -p 2223 root@127.0.0.1 cat /etc/ssh/ssh_host_ed25519_key.pub)

[ "$source_machine" != "$child_machine" ] || { echo "FAIL: machine IDs match" >&2; exit 1; }
[ "$source_hostkey" != "$child_hostkey" ] || { echo "FAIL: SSH host keys match" >&2; exit 1; }
[ "$("${ssh_cmd[@]}" -p 2223 root@127.0.0.1 cat /root/state)" = parent-state ]
[ "$("${ssh_cmd[@]}" -p 2223 root@127.0.0.1 cat /workspace/marker)" = host-project ]
"${ssh_cmd[@]}" -p 2223 root@127.0.0.1 'printf child-only >/root/child-only; sync'
bin/devd stop exp14-child
bin/devd start exp14-source
if "${ssh_cmd[@]}" -p 2222 root@127.0.0.1 test -e /root/child-only; then
    echo "FAIL: child write leaked into source" >&2
    exit 1
fi

echo "Experiment 14: PASS"
echo "Fork create-to-SSH: ${fork_ms}ms"
echo "State inheritance: PASS"
echo "Identity divergence: PASS"
echo "Host mount semantics: PASS"
echo "Disk write isolation: PASS"
