#!/usr/bin/env bash
# Experiment 15: boot the same devd ext4 workspace with libkrunfw's implicit
# kernel and with an explicit kernel passed through krun_set_kernel().
set -euo pipefail

IMAGE=${1:-nicolaka/netshoot}
KEEP=${KEEP:-0}
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
WORK_DIR=${WORK_DIR:-"${TMPDIR:-/tmp}/devd-exp15"}
export DEVD_DIR="$WORK_DIR/state"
export DEVD_SSH_CONFIG="$WORK_DIR/ssh-config"
export PATH="$ROOT_DIR/bin:$PATH"

LAUNCH_PID=
cleanup() {
    if [ -n "$LAUNCH_PID" ] && kill -0 "$LAUNCH_PID" 2>/dev/null; then
        kill "$LAUNCH_PID" 2>/dev/null || true
        wait "$LAUNCH_PID" 2>/dev/null || true
    fi
    "$ROOT_DIR/bin/devd" rm -f exp15 >/dev/null 2>&1 || true
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
command -v cc >/dev/null 2>&1 || fail "cc is required"
command -v cpio >/dev/null 2>&1 || fail "cpio is required"
command -v gzip >/dev/null 2>&1 || fail "gzip is required"

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

"$WORK_DIR/launcher" --extract "$WORK_DIR/kernel"
file "$WORK_DIR/kernel"
(
    cd "$WORK_DIR/empty-initramfs"
    find . -print | cpio -o -H newc 2>/dev/null | gzip -9 >"$WORK_DIR/empty-initramfs.cpio.gz"
)

# Use the product path to produce a real ext4 workspace with devd-init and SSH
# configuration, then stop it before direct-launching the same disk.
"$ROOT_DIR/bin/devd" run "$IMAGE" --name exp15 --no-mount
"$ROOT_DIR/bin/devd" stop exp15

disk="$DEVD_DIR/workspaces/exp15/rootfs.ext4"
key="$DEVD_DIR/ssh/devd_ed25519"
port=2222
ssh_cmd=(ssh -o BatchMode=yes -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -o ConnectTimeout=1 -i "$key" -p "$port" root@127.0.0.1)

wait_for_ssh() {
    for _ in $(seq 1 150); do
        if "${ssh_cmd[@]}" true >/dev/null 2>&1; then
            return 0
        fi
        if ! kill -0 "$LAUNCH_PID" 2>/dev/null; then
            return 1
        fi
        sleep 0.1
    done
    return 1
}

run_case() {
    label=$1
    kernel=$2
    initramfs=$3
    cmdline=$4
    log="$WORK_DIR/$label.log"

    "$WORK_DIR/launcher" "$disk" "$DEVD_DIR" exp15 "$port" \
        "$kernel" "$initramfs" "$cmdline" >"$log" 2>&1 &
    LAUNCH_PID=$!
    if ! wait_for_ssh; then
        echo "--- $label log ---" >&2
        cat "$log" >&2
        fail "$label did not reach SSH"
    fi

    CASE_UNAME=$("${ssh_cmd[@]}" uname -r)
    CASE_CMDLINE=$("${ssh_cmd[@]}" cat /proc/cmdline)
    printf '%-24s kernel=%s\n' "$label" "$CASE_UNAME"

    "${ssh_cmd[@]}" 'kill -TERM "$(cat /run/devd-workload.pid)"' >/dev/null 2>&1 || true
    for _ in $(seq 1 100); do
        kill -0 "$LAUNCH_PID" 2>/dev/null || break
        sleep 0.1
    done
    if kill -0 "$LAUNCH_PID" 2>/dev/null; then
        fail "$label launcher did not exit after guest shutdown"
    fi
    wait "$LAUNCH_PID"
    LAUNCH_PID=
}

run_case implicit - - -
implicit_uname=$CASE_UNAME
implicit_cmdline=$CASE_CMDLINE

run_case explicit "$WORK_DIR/kernel" - -
explicit_uname=$CASE_UNAME
explicit_cmdline=$CASE_CMDLINE

custom_cmdline='reboot=k panic=-1 panic_print=0 nomodule console=hvc0 rootfstype=virtiofs rw quiet no-kvmapf init=/init.krun exp15.marker=present'
run_case explicit-initramfs "$WORK_DIR/kernel" "$WORK_DIR/empty-initramfs.cpio.gz" "$custom_cmdline"
initramfs_uname=$CASE_UNAME
initramfs_cmdline=$CASE_CMDLINE

[ "$implicit_uname" = "$explicit_uname" ] || fail "implicit and explicit kernel versions differ"
[ "$implicit_cmdline" = "$explicit_cmdline" ] || fail "NULL explicit cmdline did not preserve libkrun defaults"
[ "$explicit_uname" = "$initramfs_uname" ] || fail "initramfs case booted a different kernel"
[[ "$implicit_cmdline" == *"init=/init.krun"* ]] || fail "implicit cmdline lacks init=/init.krun"
[[ "$explicit_cmdline" == *"init=/init.krun"* ]] || fail "explicit default cmdline lacks init=/init.krun"
[[ "$initramfs_cmdline" == *"exp15.marker=present"* ]] || fail "custom cmdline marker is absent"

cat <<EOF
Experiment 15: PASS
Implicit kernel boot: PASS
Explicit kernel boot: PASS
Explicit initramfs: PASS
Explicit cmdline: PASS
Kernel release: $explicit_uname
EOF
