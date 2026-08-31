#!/usr/bin/env bash
# Experiment 12: one ext4 disk image per workspace, forked with APFS clonefile.
#
# This is a macOS/Apple Silicon proof of the storage path described in
# exp12-ext4-disk-fork.md. It deliberately uses the experimental devd-vm
# wrapper; product code must continue to shell out to a supported runtime.
#
# Usage:
#   nix shell nixpkgs#e2fsprogs
#   bash experiments/exp12-ext4-disk-fork.sh [image]
#
# Environment:
#   DISK_MIB=2048   logical size of the sparse ext4 image
#   KEEP=1          keep artifacts after the run
#   WORK_DIR=...    artifact directory (must be on APFS)
#   LIBKRUN_PREFIX=...    libkrun prefix (default: brew --prefix libkrun)
#   LIBKRUNFW_PREFIX=...  libkrunfw prefix (default: brew --prefix libkrunfw)
#   SIGNATURE_POLICY=...  containers/image policy.json path

set -euo pipefail

IMAGE_INPUT=${1:-nicolaka/netshoot}
IMAGE=$IMAGE_INPUT
case "$IMAGE" in
    *://* | containers-storage:* | docker-archive:* | oci-archive:* | dir:*)
        ;;
    */*)
        IMAGE_REGISTRY=${IMAGE%%/*}
        case "$IMAGE_REGISTRY" in
            *.* | *:* | localhost)
                ;;
            *)
                IMAGE="docker.io/$IMAGE"
                ;;
        esac
        ;;
    *)
        IMAGE="docker.io/library/$IMAGE"
        ;;
esac
DISK_MIB=${DISK_MIB:-2048}
KEEP=${KEEP:-0}
WORK_DIR=${WORK_DIR:-"${TMPDIR:-/tmp}/devd-exp12"}
SSH_PORT_A=${SSH_PORT_A:-2242}
SSH_PORT_B=${SSH_PORT_B:-2243}
CPUS=${CPUS:-2}
MEMORY_MIB=${MEMORY_MIB:-512}

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
VM_SOURCE="$ROOT_DIR/experiments/devd-vm-prototype/main.c"
ENTITLEMENTS="$ROOT_DIR/experiments/devd-vm-prototype/entitlements.plist"
VM_BIN="$WORK_DIR/devd-vm"
TEMPLATE_DISK="$WORK_DIR/template.ext4"
CHILD_A_DISK="$WORK_DIR/child-a.ext4"
CHILD_B_DISK="$WORK_DIR/child-b.ext4"
SSH_KEY="$WORK_DIR/id_ed25519"

CONTAINER=""
ROOTFS=""
VM_PIDS=()

ms() {
    python3 -c 'import time; print(int(time.time() * 1000))'
}

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

find_e2fs_tool() {
    local name=$1
    local candidate

    if command -v "$name" >/dev/null 2>&1; then
        command -v "$name"
        return
    fi

    for candidate in \
        "/opt/homebrew/opt/e2fsprogs/sbin/$name" \
        "/opt/homebrew/opt/e2fsprogs/bin/$name" \
        "/usr/local/opt/e2fsprogs/sbin/$name" \
        "/usr/local/opt/e2fsprogs/bin/$name"; do
        if [ -x "$candidate" ]; then
            echo "$candidate"
            return
        fi
    done

    return 1
}

find_signature_policy() {
    local candidate

    if [ -n "${SIGNATURE_POLICY:-}" ]; then
        [ -f "$SIGNATURE_POLICY" ] && echo "$SIGNATURE_POLICY"
        return
    fi

    for candidate in \
        "$HOME/.config/containers/policy.json" \
        "/opt/homebrew/etc/containers/policy.json" \
        "/usr/local/etc/containers/policy.json" \
        "/etc/containers/policy.json"; do
        if [ -f "$candidate" ]; then
            echo "$candidate"
            return
        fi
    done

    return 1
}

cleanup() {
    local pid
    for pid in "${VM_PIDS[@]:-}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
        fi
    done

    if [ -n "$CONTAINER" ]; then
        buildah umount "$CONTAINER" >/dev/null 2>&1 || true
        buildah rm "$CONTAINER" >/dev/null 2>&1 || true
    fi

    if [ "$KEEP" != "1" ]; then
        rm -rf "$WORK_DIR"
    else
        echo "INFO Keeping artifacts in $WORK_DIR"
    fi
}
trap cleanup EXIT INT TERM

wait_for_pid_exit() {
    local pid=$1
    local deadline=$(( $(ms) + 10000 ))
    while kill -0 "$pid" 2>/dev/null; do
        if [ "$(ms)" -ge "$deadline" ]; then
            kill -KILL "$pid" 2>/dev/null || true
            return 1
        fi
        sleep 0.1
    done
}

wait_for_ssh() {
    local port=$1
    local pid=$2
    local started_ms=$3
    local deadline=$(( started_ms + 30000 ))

    while [ "$(ms)" -lt "$deadline" ]; do
        if ! kill -0 "$pid" 2>/dev/null; then
            return 1
        fi
        if nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
            echo $(( $(ms) - started_ms ))
            return 0
        fi
        sleep 0.1
    done
    return 1
}

ssh_run() {
    local port=$1
    shift
    ssh \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        -o ConnectTimeout=5 \
        -i "$SSH_KEY" \
        -p "$port" \
        root@127.0.0.1 "$@"
}

start_child() {
    local disk=$1
    local label=$2
    local port=$3
    local log=$4

    "$VM_BIN" \
        --disk "$disk" \
        --cpus "$CPUS" \
        --mem "$MEMORY_MIB" \
        --env "DEVD_NAME=$label" \
        --env "DEVD_SSH_PORT=$port" \
        -- /bin/sh /usr/local/sbin/devd-exp12-init \
        >"$log" 2>&1 &
    LAST_PID=$!
    VM_PIDS+=("$LAST_PID")
}

[ "$(uname -s)" = "Darwin" ] || fail "experiment 12 currently targets macOS/APFS"
[ "$(uname -m)" = "arm64" ] || fail "experiment 12 currently targets Apple Silicon"
command -v buildah >/dev/null 2>&1 || fail "buildah not found (install krunvm)"
command -v cc >/dev/null 2>&1 || fail "C compiler not found"
command -v codesign >/dev/null 2>&1 || fail "codesign not found"
command -v python3 >/dev/null 2>&1 || fail "python3 not found"
command -v ssh-keygen >/dev/null 2>&1 || fail "ssh-keygen not found"
command -v nc >/dev/null 2>&1 || fail "nc not found"

MKE2FS=$(find_e2fs_tool mke2fs) || fail "mke2fs not found; run: nix shell nixpkgs#e2fsprogs"
E2FSCK=$(find_e2fs_tool e2fsck) || fail "e2fsck not found; run: nix shell nixpkgs#e2fsprogs"
SIGNATURE_POLICY=$(find_signature_policy) || fail "containers policy.json not found; set SIGNATURE_POLICY to its path"

if [ -z "${LIBKRUN_PREFIX:-}" ]; then
    command -v brew >/dev/null 2>&1 || fail "set LIBKRUN_PREFIX or install Homebrew libkrun"
    LIBKRUN_PREFIX=$(brew --prefix libkrun)
fi
[ -f "$LIBKRUN_PREFIX/include/libkrun.h" ] || fail "libkrun headers not found under $LIBKRUN_PREFIX"

if [ -z "${LIBKRUNFW_PREFIX:-}" ]; then
    command -v brew >/dev/null 2>&1 || fail "set LIBKRUNFW_PREFIX or install Homebrew libkrunfw"
    LIBKRUNFW_PREFIX=$(brew --prefix libkrunfw)
fi
[ -f "$LIBKRUNFW_PREFIX/lib/libkrunfw.5.dylib" ] || fail "libkrunfw.5.dylib not found under $LIBKRUNFW_PREFIX"

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

printf '%s\n' "============================================="
printf '%s\n' "  Experiment 12: ext4 disk fork"
printf '  %-14s %s\n' "Image:" "$IMAGE"
if [ "$IMAGE" != "$IMAGE_INPUT" ]; then
    printf '  %-14s %s\n' "Input:" "$IMAGE_INPUT (qualified as Docker Hub)"
fi
printf '  %-14s %s MiB\n' "Disk size:" "$DISK_MIB"
printf '  %-14s %s\n' "Policy:" "$SIGNATURE_POLICY"
printf '  %-14s %s\n' "Firmware:" "$LIBKRUNFW_PREFIX/lib"
printf '  %-14s %s\n' "Work dir:" "$WORK_DIR"
printf '%s\n\n' "============================================="

echo "Phase 1: build experimental libkrun launcher"
cc -O2 -Wall -Wextra \
    -I"$LIBKRUN_PREFIX/include" \
    "$VM_SOURCE" \
    -L"$LIBKRUN_PREFIX/lib" -lkrun \
    -Wl,-rpath,"$LIBKRUN_PREFIX/lib" \
    -Wl,-rpath,"$LIBKRUNFW_PREFIX/lib" \
    -o "$VM_BIN"
codesign --force --sign - --entitlements "$ENTITLEMENTS" "$VM_BIN" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$SSH_KEY"
echo "  launcher: $VM_BIN"
echo

echo "Phase 2: create one temporary Buildah rootfs (the cold path)"
PREP_STARTED=$(ms)
CONTAINER=$(buildah --signature-policy "$SIGNATURE_POLICY" from --os linux --arch arm64 "$IMAGE")
ROOTFS=$(buildah mount "$CONTAINER")
[ -d "$ROOTFS" ] || fail "buildah mount did not return a rootfs"

# The template must not contain per-workspace identity. Each disk clone creates
# these on first boot and then keeps them stable across restarts.
rm -f "$ROOTFS"/etc/ssh/ssh_host_* 2>/dev/null || true
if [ -d "$ROOTFS/etc" ]; then
    : >"$ROOTFS/etc/machine-id"
fi
mkdir -p "$ROOTFS/usr/local/sbin"

PUBKEY=$(cat "$SSH_KEY.pub")
cat >"$ROOTFS/usr/local/sbin/devd-exp12-init" <<INITEOF
#!/bin/sh
set -eu

LABEL=\${DEVD_NAME:-exp12}
SSH_PORT=\${DEVD_SSH_PORT:-2242}

chmod 755 /root
mkdir -p /root/.ssh /run/sshd
chmod 700 /root/.ssh
cat > /root/.ssh/authorized_keys <<'KEYEOF'
$PUBKEY
KEYEOF
chmod 600 /root/.ssh/authorized_keys

if [ ! -s /etc/machine-id ] && [ -r /proc/sys/kernel/random/uuid ]; then
    tr -d '-' </proc/sys/kernel/random/uuid >/etc/machine-id
fi

ssh-keygen -A >/dev/null 2>&1
cat >/tmp/sshd_config <<SSHEOF
Port \$SSH_PORT
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication no
PidFile /run/sshd-exp12.pid
HostKey /etc/ssh/ssh_host_rsa_key
HostKey /etc/ssh/ssh_host_ecdsa_key
HostKey /etc/ssh/ssh_host_ed25519_key
SSHEOF

/usr/sbin/sshd -f /tmp/sshd_config
echo \$\$ >/run/devd-exp12-workload.pid
shutdown_workload() {
    sync
    exit 0
}
trap shutdown_workload TERM INT
echo "EXP12_READY label=\$LABEL port=\$SSH_PORT"
while true; do
    sleep 3600 &
    wait \$! || true
done
INITEOF
chmod 0755 "$ROOTFS/usr/local/sbin/devd-exp12-init"

# This runs in a temporary directory-root helper VM. Copying from inside Linux
# makes libkrun/virtio-fs present OCI ownership and mode metadata to cp before
# those files are written to ext4.
cat >"$ROOTFS/exp12-pack-rootfs" <<'PACKEOF'
#!/bin/sh
set -eu

TARGET=/mnt/exp12-target

# Files injected from macOS initially have the host user's ownership. Normalize
# devd-owned template files from inside Linux before copying them to ext4.
chown 0:0 /usr/local/sbin/devd-exp12-init /exp12-pack-rootfs
chmod 0755 /usr/local/sbin/devd-exp12-init /exp12-pack-rootfs
if [ -e /etc/machine-id ]; then
    chown 0:0 /etc/machine-id
    chmod 0644 /etc/machine-id
fi

# Synthetic metadata fixture. This catches a conversion path that preserves
# bytes but loses OCI ownership, modes, links, xattrs, or file capabilities.
rm -rf /exp12-metadata
mkdir -p /exp12-metadata
printf metadata-fixture >/exp12-metadata/regular
chown 123:456 /exp12-metadata/regular
chmod 4750 /exp12-metadata/regular
ln /exp12-metadata/regular /exp12-metadata/hardlink
ln -s regular /exp12-metadata/symlink
if command -v setfattr >/dev/null 2>&1 && command -v getfattr >/dev/null 2>&1; then
    setfattr -n user.exp12 -v preserved /exp12-metadata/regular
    : >/exp12-metadata/xattr-supported
fi
if command -v setcap >/dev/null 2>&1 && command -v getcap >/dev/null 2>&1; then
    cp /bin/true /exp12-metadata/capability
    setcap cap_net_bind_service=ep /exp12-metadata/capability
    : >/exp12-metadata/capability-supported
fi

mkdir -p "$TARGET"
mount -t ext4 /dev/vda "$TARGET"

for path in /* /.[!.]* /..?*; do
    [ -e "$path" ] || [ -L "$path" ] || continue
    name=${path##*/}
    case "$name" in
        dev|proc|sys|run|mnt|lost+found|.krunvm.lock)
            mkdir -p "$TARGET/$name"
            ;;
        *)
            cp -a "$path" "$TARGET/"
            ;;
    esac
done

sync
umount "$TARGET"
echo EXP12_PACK_COMPLETE
PACKEOF
chmod 0755 "$ROOTFS/exp12-pack-rootfs"

truncate -s "${DISK_MIB}M" "$TEMPLATE_DISK"
"$MKE2FS" -q -t ext4 -F -m 0 -L devd-root "$TEMPLATE_DISK"

echo "  Populating ext4 through a helper VM..."
"$VM_BIN" \
    --root "$ROOTFS" \
    --data-disk "$TEMPLATE_DISK" \
    --cpus 2 \
    --mem 512 \
    -- /bin/sh /exp12-pack-rootfs \
    >"$WORK_DIR/pack.log" 2>&1 || {
        tail -100 "$WORK_DIR/pack.log" >&2 || true
        fail "helper VM failed to populate ext4"
    }

grep -q EXP12_PACK_COMPLETE "$WORK_DIR/pack.log" || fail "helper did not report completion"
buildah umount "$CONTAINER" >/dev/null
ROOTFS=""
buildah rm "$CONTAINER" >/dev/null
CONTAINER=""
PREP_MS=$(( $(ms) - PREP_STARTED ))
echo "  cold template preparation: ${PREP_MS}ms"
echo "  logical size: $(du -h "$TEMPLATE_DISK" | awk '{print $1}') allocated"
echo

echo "Phase 3: APFS-clone two workspace disks"
FREE_BEFORE=$(df -k "$WORK_DIR" | awk 'NR == 2 {print $4}')
CLONE_A_STARTED=$(ms)
/bin/cp -c "$TEMPLATE_DISK" "$CHILD_A_DISK"
CLONE_A_MS=$(( $(ms) - CLONE_A_STARTED ))
CLONE_B_STARTED=$(ms)
/bin/cp -c "$TEMPLATE_DISK" "$CHILD_B_DISK"
CLONE_B_MS=$(( $(ms) - CLONE_B_STARTED ))
FREE_AFTER=$(df -k "$WORK_DIR" | awk 'NR == 2 {print $4}')
CLONE_DELTA_KIB=$(( FREE_BEFORE - FREE_AFTER ))
echo "  child A clone: ${CLONE_A_MS}ms"
echo "  child B clone: ${CLONE_B_MS}ms"
echo "  APFS free-space delta: ${CLONE_DELTA_KIB} KiB (informational)"
[ "$CLONE_A_MS" -lt 100 ] || fail "child A clone exceeded 100ms"
[ "$CLONE_B_MS" -lt 100 ] || fail "child B clone exceeded 100ms"
echo

echo "Phase 4: boot both workspace disks concurrently"
BOOT_STARTED=$(ms)
start_child "$CHILD_A_DISK" child-a "$SSH_PORT_A" "$WORK_DIR/child-a.log"
PID_A=$LAST_PID
start_child "$CHILD_B_DISK" child-b "$SSH_PORT_B" "$WORK_DIR/child-b.log"
PID_B=$LAST_PID

BOOT_A_MS=$(wait_for_ssh "$SSH_PORT_A" "$PID_A" "$BOOT_STARTED") || {
    tail -100 "$WORK_DIR/child-a.log" >&2 || true
    fail "child A SSH did not become ready"
}
BOOT_B_MS=$(wait_for_ssh "$SSH_PORT_B" "$PID_B" "$BOOT_STARTED") || {
    tail -100 "$WORK_DIR/child-b.log" >&2 || true
    fail "child B SSH did not become ready"
}
echo "  child A SSH ready: ${BOOT_A_MS}ms"
echo "  child B SSH ready: ${BOOT_B_MS}ms"
[ "$BOOT_A_MS" -lt 2000 ] || fail "child A boot exceeded 2s"
[ "$BOOT_B_MS" -lt 2000 ] || fail "child B boot exceeded 2s"
echo

echo "Phase 5: verify metadata, identity, and write isolation"
ssh_run "$SSH_PORT_A" 'test "$(stat -c %u:%g /bin/sh)" = 0:0; test -x /bin/sh'
ssh_run "$SSH_PORT_B" 'test "$(stat -c %u:%g /bin/sh)" = 0:0; test -x /bin/sh'
ssh_run "$SSH_PORT_A" '
    test "$(stat -c %u:%g:%a /exp12-metadata/regular)" = 123:456:4750
    test "$(stat -c %i /exp12-metadata/regular)" = "$(stat -c %i /exp12-metadata/hardlink)"
    test "$(readlink /exp12-metadata/symlink)" = regular
    if test -e /exp12-metadata/xattr-supported; then
        test "$(getfattr --only-values -n user.exp12 /exp12-metadata/regular 2>/dev/null)" = preserved
    fi
    if test -e /exp12-metadata/capability-supported; then
        getcap /exp12-metadata/capability | grep -q cap_net_bind_service=ep
    fi
'

MACHINE_A=$(ssh_run "$SSH_PORT_A" 'cat /etc/machine-id')
MACHINE_B=$(ssh_run "$SSH_PORT_B" 'cat /etc/machine-id')
HOSTKEY_A=$(ssh_run "$SSH_PORT_A" 'cat /etc/ssh/ssh_host_ed25519_key.pub')
HOSTKEY_B=$(ssh_run "$SSH_PORT_B" 'cat /etc/ssh/ssh_host_ed25519_key.pub')
[ -n "$MACHINE_A" ] && [ -n "$MACHINE_B" ] || fail "machine-id was not generated"
[ "$MACHINE_A" != "$MACHINE_B" ] || fail "children inherited the same machine-id"
[ "$HOSTKEY_A" != "$HOSTKEY_B" ] || fail "children inherited the same SSH host key"

ssh_run "$SSH_PORT_A" 'printf from-a >/fork-marker; dd if=/dev/zero of=/fork-growth bs=1M count=16 status=none; sync'
ssh_run "$SSH_PORT_B" 'test ! -e /fork-marker; printf from-b >/fork-marker; sync'
[ "$(ssh_run "$SSH_PORT_A" 'cat /fork-marker')" = "from-a" ] || fail "child A marker changed"
[ "$(ssh_run "$SSH_PORT_B" 'cat /fork-marker')" = "from-b" ] || fail "child B marker changed"
echo "  OCI metadata fixture: PASS"
echo "  root ownership/mode: PASS"
echo "  machine identity divergence: PASS"
echo "  SSH host-key divergence: PASS"
echo "  filesystem write isolation: PASS"
echo

echo "Phase 6: verify persistence after graceful and abrupt VMM stop"
ssh_run "$SSH_PORT_A" 'sync; kill -TERM "$(cat /run/devd-exp12-workload.pid)"' >/dev/null 2>&1 || true
wait_for_pid_exit "$PID_A" || fail "child A did not shut down after workload exit"
kill -TERM "$PID_B" 2>/dev/null || true
wait_for_pid_exit "$PID_B" || fail "child B did not stop after SIGTERM"
sleep 0.5

RESTART_STARTED=$(ms)
start_child "$CHILD_A_DISK" child-a "$SSH_PORT_A" "$WORK_DIR/child-a-restart.log"
PID_A2=$LAST_PID
start_child "$CHILD_B_DISK" child-b "$SSH_PORT_B" "$WORK_DIR/child-b-restart.log"
PID_B2=$LAST_PID
wait_for_ssh "$SSH_PORT_A" "$PID_A2" "$RESTART_STARTED" >/dev/null || fail "child A restart failed"
wait_for_ssh "$SSH_PORT_B" "$PID_B2" "$RESTART_STARTED" >/dev/null || fail "child B restart/journal recovery failed"

[ "$(ssh_run "$SSH_PORT_A" 'cat /fork-marker')" = "from-a" ] || fail "child A lost persistent state"
[ "$(ssh_run "$SSH_PORT_B" 'cat /fork-marker')" = "from-b" ] || fail "child B lost persistent state"
[ "$(ssh_run "$SSH_PORT_A" 'cat /etc/machine-id')" = "$MACHINE_A" ] || fail "child A machine-id changed"
[ "$(ssh_run "$SSH_PORT_B" 'cat /etc/machine-id')" = "$MACHINE_B" ] || fail "child B machine-id changed"
[ "$(ssh_run "$SSH_PORT_A" 'cat /etc/ssh/ssh_host_ed25519_key.pub')" = "$HOSTKEY_A" ] || fail "child A host key changed"
[ "$(ssh_run "$SSH_PORT_B" 'cat /etc/ssh/ssh_host_ed25519_key.pub')" = "$HOSTKEY_B" ] || fail "child B host key changed"

ssh_run "$SSH_PORT_A" 'sync; kill -TERM "$(cat /run/devd-exp12-workload.pid)"' >/dev/null 2>&1 || true
ssh_run "$SSH_PORT_B" 'sync; kill -TERM "$(cat /run/devd-exp12-workload.pid)"' >/dev/null 2>&1 || true
wait_for_pid_exit "$PID_A2" || fail "child A final shutdown failed"
wait_for_pid_exit "$PID_B2" || fail "child B final shutdown failed"

"$E2FSCK" -fn "$CHILD_A_DISK" >"$WORK_DIR/fsck-a.log" 2>&1 || {
    cat "$WORK_DIR/fsck-a.log" >&2
    fail "child A ext4 check failed"
}
"$E2FSCK" -fn "$CHILD_B_DISK" >"$WORK_DIR/fsck-b.log" 2>&1 || {
    cat "$WORK_DIR/fsck-b.log" >&2
    fail "child B ext4 check failed"
}
echo "  graceful persistence: PASS"
echo "  abrupt-stop journal recovery: PASS"
echo "  final e2fsck: PASS"
echo

printf '%s\n' "============================================="
printf '%s\n' "  Experiment 12: PASS"
printf '%-30s %8s\n' "Cold template preparation:" "${PREP_MS}ms"
printf '%-30s %8s\n' "APFS clone A:" "${CLONE_A_MS}ms"
printf '%-30s %8s\n' "APFS clone B:" "${CLONE_B_MS}ms"
printf '%-30s %8s\n' "Concurrent boot A → SSH:" "${BOOT_A_MS}ms"
printf '%-30s %8s\n' "Concurrent boot B → SSH:" "${BOOT_B_MS}ms"
printf '%s\n' "============================================="