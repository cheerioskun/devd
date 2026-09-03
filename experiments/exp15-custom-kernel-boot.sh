#!/usr/bin/env bash
# Experiment 15: boot devd workspace clones with independently built kernels.
set -euo pipefail

IMAGE=${1:-nicolaka/netshoot}
KERNEL_A=${2:-${KERNEL_A:-}}
KERNEL_B=${3:-${KERNEL_B:-}}
KEEP=${KEEP:-0}
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
WORK_DIR=${WORK_DIR:-"${TMPDIR:-/tmp}/devd-exp15"}
export DEVD_DIR="$WORK_DIR/state"
export DEVD_SSH_CONFIG="$WORK_DIR/ssh-config"
export PATH="$ROOT_DIR/bin:$PATH"

PIDS=()
cleanup() {
    for pid in "${PIDS[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    "$ROOT_DIR/bin/devd" rm -f exp15-child >/dev/null 2>&1 || true
    "$ROOT_DIR/bin/devd" rm -f exp15-source >/dev/null 2>&1 || true
    if [ "$KEEP" = 1 ]; then
        echo "INFO Keeping $WORK_DIR"
    else
        rm -rf "$WORK_DIR"
    fi
}
trap cleanup EXIT INT TERM

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

for binary in devd devd-vm devd-image-helper; do
    [ -x "$ROOT_DIR/bin/$binary" ] || fail "run build first (missing bin/$binary)"
done
for command in cc cpio gzip file ssh; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done
[ -n "$KERNEL_A" ] || fail "pass kernel A as the second argument or set KERNEL_A"
[ -n "$KERNEL_B" ] || fail "pass kernel B as the third argument or set KERNEL_B"
[ -f "$KERNEL_A" ] || fail "kernel A is not a file: $KERNEL_A"
[ -f "$KERNEL_B" ] || fail "kernel B is not a file: $KERNEL_B"
cmp -s "$KERNEL_A" "$KERNEL_B" && fail "kernel A and kernel B have identical bytes"

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR/empty-initramfs"

case $(uname -s) in
    Darwin)
        libkrun_prefix=${LIBKRUN_PREFIX:-$(brew --prefix libkrun)}
        libkrunfw_prefix=${LIBKRUNFW_PREFIX:-$(brew --prefix libkrunfw)}
        cc -O2 -Wall -Wextra \
            -I"$libkrun_prefix/include" \
            "$ROOT_DIR/experiments/exp15-custom-kernel-boot.c" \
            -L"$libkrun_prefix/lib" -lkrun \
            -L"$libkrunfw_prefix/lib" -lkrunfw \
            -Wl,-rpath,"$libkrun_prefix/lib" \
            -Wl,-rpath,"$libkrunfw_prefix/lib" \
            -o "$WORK_DIR/launcher"
        codesign --force --sign - \
            --entitlements "$ROOT_DIR/cmd/devd-vm/entitlements.plist" \
            "$WORK_DIR/launcher" >/dev/null
        ;;
    Linux)
        # shellcheck disable=SC2046
        cc -O2 -Wall -Wextra \
            "$ROOT_DIR/experiments/exp15-custom-kernel-boot.c" \
            $(pkg-config --cflags --libs libkrun) -lkrunfw \
            -o "$WORK_DIR/launcher"
        ;;
    *) fail "unsupported host OS: $(uname -s)" ;;
esac

"$WORK_DIR/launcher" --extract "$WORK_DIR/embedded-kernel"
file "$WORK_DIR/embedded-kernel" "$KERNEL_A" "$KERNEL_B"
(
    cd "$WORK_DIR/empty-initramfs"
    find . -print | cpio -o -H newc 2>/dev/null | gzip -9 >"$WORK_DIR/empty-initramfs.cpio.gz"
)

ssh_guest() {
    local port=$1
    shift
    ssh -o BatchMode=yes -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        -o ConnectTimeout=1 -i "$DEVD_DIR/ssh/devd_ed25519" \
        -p "$port" root@127.0.0.1 "$@"
}

workspace_port() {
    local name=$1
    "$ROOT_DIR/bin/devd" ps -a | awk -v name="$name" '$1 == name { print $4 }'
}

wait_for_ssh() {
    local pid=$1
    local port=$2
    for _ in $(seq 1 150); do
        if ssh_guest "$port" true >/dev/null 2>&1; then
            return 0
        fi
        if ! kill -0 "$pid" 2>/dev/null; then
            return 1
        fi
        sleep 0.1
    done
    return 1
}

launch_vm() {
    local label=$1
    local disk=$2
    local name=$3
    local port=$4
    local kernel=$5
    local initramfs=$6
    local cmdline=$7

    "$WORK_DIR/launcher" "$disk" "$DEVD_DIR" "$name" "$port" \
        "$kernel" "$initramfs" "$cmdline" >"$WORK_DIR/$label.log" 2>&1 &
    LAUNCH_PID=$!
    PIDS+=("$LAUNCH_PID")
    if ! wait_for_ssh "$LAUNCH_PID" "$port"; then
        echo "--- $label log ---" >&2
        cat "$WORK_DIR/$label.log" >&2
        fail "$label did not reach SSH"
    fi
}

shutdown_vm() {
    local label=$1
    local pid=$2
    local port=$3

    ssh_guest "$port" 'kill -TERM "$(cat /run/devd-workload.pid)"' >/dev/null 2>&1 || true
    for _ in $(seq 1 100); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.1
    done
    if kill -0 "$pid" 2>/dev/null; then
        fail "$label launcher did not exit after guest shutdown"
    fi
    wait "$pid"
}

run_case() {
    local label=$1
    local kernel=$2
    local initramfs=$3
    local cmdline=$4

    launch_vm "$label" "$source_disk" exp15-source "$source_port" \
        "$kernel" "$initramfs" "$cmdline"
    local pid=$LAUNCH_PID
    CASE_UNAME=$(ssh_guest "$source_port" uname -r)
    CASE_CMDLINE=$(ssh_guest "$source_port" cat /proc/cmdline)
    printf '%-24s kernel=%s\n' "$label" "$CASE_UNAME"
    shutdown_vm "$label" "$pid" "$source_port"
}

# Use the product path to create a real ext4 workspace, including devd-init and
# SSH setup. The marker becomes userspace state that the later fork must clone.
"$ROOT_DIR/bin/devd" run "$IMAGE" --name exp15-source --no-mount
source_port=$(workspace_port exp15-source)
[ -n "$source_port" ] || fail "could not determine source SSH port"
ssh_guest "$source_port" 'printf inherited-before-fork > /root/exp15-inherited'
"$ROOT_DIR/bin/devd" stop exp15-source
source_disk="$DEVD_DIR/workspaces/exp15-source/rootfs.ext4"

# Preserve the original mechanism checks: implicit kernel, extracted embedded
# kernel, explicit initramfs, and exact custom command line.
run_case implicit - - -
implicit_uname=$CASE_UNAME
implicit_cmdline=$CASE_CMDLINE

run_case explicit-embedded "$WORK_DIR/embedded-kernel" - -
embedded_uname=$CASE_UNAME
embedded_cmdline=$CASE_CMDLINE

custom_cmdline='reboot=k panic=-1 panic_print=0 nomodule console=hvc0 rootfstype=virtiofs rw quiet no-kvmapf init=/init.krun exp15.marker=present'
run_case explicit-initramfs "$WORK_DIR/embedded-kernel" "$WORK_DIR/empty-initramfs.cpio.gz" "$custom_cmdline"
initramfs_uname=$CASE_UNAME
initramfs_cmdline=$CASE_CMDLINE

[ "$implicit_uname" = "$embedded_uname" ] || fail "implicit and extracted embedded kernel releases differ"
[ "$implicit_cmdline" = "$embedded_cmdline" ] || fail "NULL explicit cmdline did not preserve libkrun defaults"
[ "$embedded_uname" = "$initramfs_uname" ] || fail "initramfs case booted a different kernel"
[[ "$implicit_cmdline" == *"init=/init.krun"* ]] || fail "implicit cmdline lacks init=/init.krun"
[[ "$initramfs_cmdline" == *"exp15.marker=present"* ]] || fail "custom cmdline marker is absent"

# Clone the stopped disk through the actual product fork path. Verify inherited
# state before making a child-only write, then stop it for direct kernel boots.
"$ROOT_DIR/bin/devd" fork exp15-source --name exp15-child --no-mount
child_port=$(workspace_port exp15-child)
[ -n "$child_port" ] || fail "could not determine child SSH port"
[ "$(ssh_guest "$child_port" cat /root/exp15-inherited)" = inherited-before-fork ] || \
    fail "fork did not inherit source userspace state"
ssh_guest "$child_port" 'printf child-only > /root/exp15-child-only'
"$ROOT_DIR/bin/devd" stop exp15-child
child_disk="$DEVD_DIR/workspaces/exp15-child/rootfs.ext4"

# Boot the clone and its parent concurrently from independently compiled kernel
# images. Kernel A uses CONFIG_HZ=100; kernel B uses CONFIG_HZ=1000.
launch_vm kernel-a-source "$source_disk" exp15-source "$source_port" "$KERNEL_A" - -
source_pid=$LAUNCH_PID
launch_vm kernel-b-child "$child_disk" exp15-child "$child_port" "$KERNEL_B" - -
child_pid=$LAUNCH_PID

source_uname=$(ssh_guest "$source_port" uname -r)
child_uname=$(ssh_guest "$child_port" uname -r)
printf '%-24s kernel=%s\n' kernel-a-source "$source_uname"
printf '%-24s kernel=%s\n' kernel-b-child "$child_uname"
[ "$source_uname" != "$child_uname" ] || fail "independent kernels reported the same release"
[ "$(ssh_guest "$source_port" cat /root/exp15-inherited)" = inherited-before-fork ] || fail "source lost inherited marker"
[ "$(ssh_guest "$child_port" cat /root/exp15-inherited)" = inherited-before-fork ] || fail "child lost inherited marker"
ssh_guest "$source_port" 'test ! -e /root/exp15-child-only; printf source-only > /root/exp15-source-only' || \
    fail "child-only write leaked into source"
ssh_guest "$child_port" 'test ! -e /root/exp15-source-only; test "$(cat /root/exp15-child-only)" = child-only' || \
    fail "source write leaked into child or child write was lost"
# Both commands succeeding while both launcher PIDs are alive proves concurrent
# guest operation rather than sequential reuse of one root disk.
kill -0 "$source_pid" && kill -0 "$child_pid" || fail "a concurrent VM exited early"

shutdown_vm kernel-a-source "$source_pid" "$source_port"
shutdown_vm kernel-b-child "$child_pid" "$child_port"

cat <<EOF
Experiment 15: PASS
Implicit kernel boot: PASS
Extracted embedded kernel: PASS
Explicit initramfs and cmdline: PASS
Independent kernel A: $source_uname
Independent kernel B: $child_uname
Fork inherited userspace state: PASS
Fork write isolation: PASS
Concurrent operation: PASS
Clean shutdown: PASS
EOF
