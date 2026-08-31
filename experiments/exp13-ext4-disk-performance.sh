#!/usr/bin/env bash
# Experiment 13: compare current directory-root/virtio-fs storage with the
# raw ext4 root-disk design validated by Experiment 12.
#
# Usage:
#   bash experiments/exp13-ext4-disk-performance.sh [image]
#
# Environment:
#   ROOT_ROUNDS=5       recorded rounds against the guest root filesystem
#   WORKSPACE_ROUNDS=3  recorded rounds against the shared virtio-fs workspace
#   SIZE_MIB=256        sequential test file size
#   FILES=5000          files per metadata test
#   RANDOM_OPS=25000    4 KiB random operations per test
#   FSYNC_OPS=200       4 KiB write+fsync operations per test
#   DISK_MIB=2048       logical ext4 image size
#   KEEP=1              retain images, logs, and raw results
#   WORK_DIR=...        artifact directory (must be on APFS)

set -euo pipefail

IMAGE_INPUT=${1:-nicolaka/netshoot}
IMAGE=$IMAGE_INPUT
case "$IMAGE" in
    *://* | containers-storage:* | docker-archive:* | oci-archive:* | dir:*)
        ;;
    */*)
        IMAGE_REGISTRY=${IMAGE%%/*}
        case "$IMAGE_REGISTRY" in
            *.* | *:* | localhost) ;;
            *) IMAGE="docker.io/$IMAGE" ;;
        esac
        ;;
    *)
        IMAGE="docker.io/library/$IMAGE"
        ;;
esac

ROOT_ROUNDS=${ROOT_ROUNDS:-5}
WORKSPACE_ROUNDS=${WORKSPACE_ROUNDS:-3}
SIZE_MIB=${SIZE_MIB:-256}
FILES=${FILES:-5000}
RANDOM_OPS=${RANDOM_OPS:-25000}
FSYNC_OPS=${FSYNC_OPS:-200}
DISK_MIB=${DISK_MIB:-2048}
KEEP=${KEEP:-0}
WORK_DIR=${WORK_DIR:-"${TMPDIR:-/tmp}/devd-exp13"}
DIRECTORY_PORT=${DIRECTORY_PORT:-2252}
EXT4_PORT=${EXT4_PORT:-2253}
CPUS=${CPUS:-2}
MEMORY_MIB=${MEMORY_MIB:-512}

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
VM_SOURCE="$ROOT_DIR/experiments/devd-vm-prototype/main.c"
BENCH_SOURCE="$ROOT_DIR/experiments/exp13-disk-bench"
ENTITLEMENTS="$ROOT_DIR/experiments/devd-vm-prototype/entitlements.plist"
VM_BIN="$WORK_DIR/devd-vm"
BENCH_BIN="$WORK_DIR/devd-exp13-bench"
TEMPLATE_DISK="$WORK_DIR/template.ext4"
EXT4_DISK="$WORK_DIR/ext4-root.ext4"
HOST_WORKSPACE="$WORK_DIR/host-workspace"
SSH_KEY="$WORK_DIR/id_ed25519"
RAW_RESULTS="$WORK_DIR/results.jsonl"
SUMMARY="$WORK_DIR/summary.md"

CONTAINER=""
ROOTFS=""
VM_PIDS=()

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

ms() {
    python3 -c 'import time; print(int(time.time() * 1000))'
}

wait_for_ssh() {
    local port=$1
    local pid=$2
    local deadline=$(( $(ms) + 30000 ))

    while [ "$(ms)" -lt "$deadline" ]; do
        if ! kill -0 "$pid" 2>/dev/null; then
            return 1
        fi
        if nc -z 127.0.0.1 "$port" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.1
    done
    return 1
}

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

start_directory_vm() {
    "$VM_BIN" \
        --root "$ROOTFS" \
        --cpus "$CPUS" \
        --mem "$MEMORY_MIB" \
        --virtiofs "exp13ws:$HOST_WORKSPACE" \
        --env "DEVD_MODE=directory" \
        --env "DEVD_SSH_PORT=$DIRECTORY_PORT" \
        -- /bin/sh /usr/local/sbin/devd-exp13-init \
        >"$WORK_DIR/directory-vm.log" 2>&1 &
    DIRECTORY_PID=$!
    VM_PIDS+=("$DIRECTORY_PID")
}

start_ext4_vm() {
    "$VM_BIN" \
        --disk "$EXT4_DISK" \
        --cpus "$CPUS" \
        --mem "$MEMORY_MIB" \
        --virtiofs "exp13ws:$HOST_WORKSPACE" \
        --env "DEVD_MODE=ext4" \
        --env "DEVD_SSH_PORT=$EXT4_PORT" \
        -- /bin/sh /usr/local/sbin/devd-exp13-init \
        >"$WORK_DIR/ext4-vm.log" 2>&1 &
    EXT4_PID=$!
    VM_PIDS+=("$EXT4_PID")
}

run_case() {
    local mode=$1
    local port=$2
    local location=$3
    local round=$4
    local guest_dir
    local output

    case "$location" in
        root) guest_dir="/var/lib/devd-exp13" ;;
        workspace) guest_dir="/workspace/$mode" ;;
        *) fail "unknown benchmark location: $location" ;;
    esac

    printf '  round %d: %-9s %-9s ... ' "$round" "$mode" "$location"
    output=$(ssh_run "$port" \
        /usr/local/bin/devd-exp13-bench \
        --dir "$guest_dir" \
        --mode "$mode" \
        --location "$location" \
        --round "$round" \
        --size-mib "$SIZE_MIB" \
        --files "$FILES" \
        --random-ops "$RANDOM_OPS" \
        --fsync-ops "$FSYNC_OPS" \
        --seed 130013)

    python3 -c 'import json,sys; json.loads(sys.argv[1])' "$output" || fail "invalid benchmark JSON"
    printf '%s\n' "$output" >>"$RAW_RESULTS"
    python3 - "$output" <<'PY'
import json
import sys
r = json.loads(sys.argv[1])
print(f"write {r['seq_write_mib_s']:.0f} MiB/s, "
      f"read {r['seq_read_mib_s']:.0f} MiB/s, "
      f"create {r['create_ops_s']:.0f} ops/s")
PY
}

[ "$(uname -s)" = "Darwin" ] || fail "experiment 13 currently targets macOS/APFS"
[ "$(uname -m)" = "arm64" ] || fail "experiment 13 currently targets Apple Silicon"
command -v buildah >/dev/null 2>&1 || fail "buildah not found (install krunvm)"
command -v cc >/dev/null 2>&1 || fail "C compiler not found"
command -v codesign >/dev/null 2>&1 || fail "codesign not found"
command -v go >/dev/null 2>&1 || fail "Go compiler not found"
command -v python3 >/dev/null 2>&1 || fail "python3 not found"
command -v ssh-keygen >/dev/null 2>&1 || fail "ssh-keygen not found"
command -v nc >/dev/null 2>&1 || fail "nc not found"

MKE2FS=$(find_e2fs_tool mke2fs) || fail "mke2fs not found; reload the project devenv"
E2FSCK=$(find_e2fs_tool e2fsck) || fail "e2fsck not found; reload the project devenv"
SIGNATURE_POLICY=$(find_signature_policy) || fail "containers policy.json not found; set SIGNATURE_POLICY"

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
mkdir -p "$WORK_DIR" "$HOST_WORKSPACE"
: >"$RAW_RESULTS"

printf '%s\n' "=================================================="
printf '%s\n' "  Experiment 13: ext4 guest disk performance"
printf '  %-18s %s\n' "Image:" "$IMAGE"
if [ "$IMAGE" != "$IMAGE_INPUT" ]; then
    printf '  %-18s %s\n' "Input:" "$IMAGE_INPUT (qualified as Docker Hub)"
fi
printf '  %-18s %s / %s\n' "Rounds:" "$ROOT_ROUNDS root" "$WORKSPACE_ROUNDS workspace"
printf '  %-18s %s MiB, %s files\n' "Workload:" "$SIZE_MIB" "$FILES"
printf '  %-18s %s\n' "Work dir:" "$WORK_DIR"
printf '%s\n\n' "=================================================="

echo "Phase 1: build launcher and static Linux benchmark helper"
cc -O2 -Wall -Wextra \
    -I"$LIBKRUN_PREFIX/include" \
    "$VM_SOURCE" \
    -L"$LIBKRUN_PREFIX/lib" -lkrun \
    -Wl,-rpath,"$LIBKRUN_PREFIX/lib" \
    -Wl,-rpath,"$LIBKRUNFW_PREFIX/lib" \
    -o "$VM_BIN"
codesign --force --sign - --entitlements "$ENTITLEMENTS" "$VM_BIN" >/dev/null
(
    cd "$ROOT_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
        go build -trimpath -ldflags='-s -w' -o "$BENCH_BIN" ./experiments/exp13-disk-bench
)
ssh-keygen -q -t ed25519 -N '' -f "$SSH_KEY"
echo "  benchmark helper: $(du -h "$BENCH_BIN" | awk '{print $1}') static linux/arm64"
echo

echo "Phase 2: prepare one rootfs and equivalent ext4 disk"
CONTAINER=$(buildah --signature-policy "$SIGNATURE_POLICY" from --os linux --arch arm64 "$IMAGE")
ROOTFS=$(buildah mount "$CONTAINER")
[ -d "$ROOTFS" ] || fail "buildah mount did not return a rootfs"

rm -f "$ROOTFS"/etc/ssh/ssh_host_* 2>/dev/null || true
mkdir -p "$ROOTFS/usr/local/bin" "$ROOTFS/usr/local/sbin"
cp "$BENCH_BIN" "$ROOTFS/usr/local/bin/devd-exp13-bench"
chmod 0755 "$ROOTFS/usr/local/bin/devd-exp13-bench"
PUBKEY=$(cat "$SSH_KEY.pub")

cat >"$ROOTFS/usr/local/sbin/devd-exp13-init" <<INITEOF
#!/bin/sh
set -eu

MODE=\${DEVD_MODE:-unknown}
SSH_PORT=\${DEVD_SSH_PORT:-2252}

chmod 755 /root
mkdir -p /root/.ssh /run/sshd /workspace
chmod 700 /root/.ssh
cat >/root/.ssh/authorized_keys <<'KEYEOF'
$PUBKEY
KEYEOF
chmod 600 /root/.ssh/authorized_keys

mount -t virtiofs exp13ws /workspace
ssh-keygen -A >/dev/null 2>&1
cat >/tmp/sshd_config <<SSHEOF
Port \$SSH_PORT
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication no
PidFile /run/sshd-exp13.pid
HostKey /etc/ssh/ssh_host_rsa_key
HostKey /etc/ssh/ssh_host_ecdsa_key
HostKey /etc/ssh/ssh_host_ed25519_key
SSHEOF
/usr/sbin/sshd -f /tmp/sshd_config
echo \$\$ >/run/devd-exp13-workload.pid
shutdown_workload() {
    sync
    exit 0
}
trap shutdown_workload TERM INT
echo "EXP13_READY mode=\$MODE port=\$SSH_PORT"
while true; do
    sleep 3600 &
    wait \$! || true
done
INITEOF
chmod 0755 "$ROOTFS/usr/local/sbin/devd-exp13-init"

cat >"$ROOTFS/exp13-pack-rootfs" <<'PACKEOF'
#!/bin/sh
set -eu
TARGET=/mnt/exp13-target

chown 0:0 /usr/local/bin/devd-exp13-bench /usr/local/sbin/devd-exp13-init /exp13-pack-rootfs
chmod 0755 /usr/local/bin/devd-exp13-bench /usr/local/sbin/devd-exp13-init /exp13-pack-rootfs
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
echo EXP13_PACK_COMPLETE
PACKEOF
chmod 0755 "$ROOTFS/exp13-pack-rootfs"

truncate -s "${DISK_MIB}M" "$TEMPLATE_DISK"
"$MKE2FS" -q -t ext4 -F -m 0 -L devd-exp13 "$TEMPLATE_DISK"
"$VM_BIN" \
    --root "$ROOTFS" \
    --data-disk "$TEMPLATE_DISK" \
    --cpus "$CPUS" \
    --mem "$MEMORY_MIB" \
    -- /bin/sh /exp13-pack-rootfs \
    >"$WORK_DIR/pack.log" 2>&1 || {
        tail -100 "$WORK_DIR/pack.log" >&2 || true
        fail "helper VM failed to populate ext4"
    }
grep -q EXP13_PACK_COMPLETE "$WORK_DIR/pack.log" || fail "helper did not report completion"
/bin/cp -c "$TEMPLATE_DISK" "$EXT4_DISK"
echo "  directory root: current devd/krunvm-style virtio-fs"
echo "  ext4 root:      APFS-cloned raw disk through virtio-blk"
echo

echo "Phase 3: boot both storage modes with the same runtime settings"
start_directory_vm
start_ext4_vm
wait_for_ssh "$DIRECTORY_PORT" "$DIRECTORY_PID" || {
    tail -100 "$WORK_DIR/directory-vm.log" >&2 || true
    fail "directory-root VM did not reach SSH"
}
wait_for_ssh "$EXT4_PORT" "$EXT4_PID" || {
    tail -100 "$WORK_DIR/ext4-vm.log" >&2 || true
    fail "ext4-root VM did not reach SSH"
}
ssh_run "$DIRECTORY_PORT" 'grep -q " /workspace " /proc/mounts; test -x /usr/local/bin/devd-exp13-bench'
ssh_run "$EXT4_PORT" 'grep -q " /workspace " /proc/mounts; test -x /usr/local/bin/devd-exp13-bench'
echo "  both VMs ready"
echo

echo "Phase 4: benchmark guest root filesystems"
for round in $(seq 1 "$ROOT_ROUNDS"); do
    if [ $(( round % 2 )) -eq 1 ]; then
        run_case directory "$DIRECTORY_PORT" root "$round"
        run_case ext4 "$EXT4_PORT" root "$round"
    else
        run_case ext4 "$EXT4_PORT" root "$round"
        run_case directory "$DIRECTORY_PORT" root "$round"
    fi
done
echo

echo "Phase 5: control benchmark on the unchanged host workspace mount"
for round in $(seq 1 "$WORKSPACE_ROUNDS"); do
    if [ $(( round % 2 )) -eq 1 ]; then
        run_case directory "$DIRECTORY_PORT" workspace "$round"
        run_case ext4 "$EXT4_PORT" workspace "$round"
    else
        run_case ext4 "$EXT4_PORT" workspace "$round"
        run_case directory "$DIRECTORY_PORT" workspace "$round"
    fi
done
echo

echo "Phase 6: summarize medians and apply regression threshold"
SUMMARY_RC=0
python3 - "$RAW_RESULTS" "$SUMMARY" <<'PY' || SUMMARY_RC=$?
import json
import statistics
import sys
from collections import defaultdict

raw_path, summary_path = sys.argv[1:]
with open(raw_path) as f:
    rows = [json.loads(line) for line in f if line.strip()]

metrics = [
    ("seq_write_mib_s", "Sequential write", "MiB/s", True),
    ("seq_read_mib_s", "Sequential read", "MiB/s", True),
    ("random_read_iops", "4 KiB random read", "IOPS", True),
    ("random_write_iops", "4 KiB buffered random write + sync", "IOPS", True),
    ("fsync_p50_ms", "4 KiB fsync p50", "ms", False),
    ("fsync_p95_ms", "4 KiB fsync p95", "ms", False),
    ("create_ops_s", "Small-file create", "ops/s", True),
    ("stat_ops_s", "Small-file stat", "ops/s", True),
    ("rename_ops_s", "Small-file rename", "ops/s", True),
    ("delete_ops_s", "Small-file delete", "ops/s", True),
]

groups = defaultdict(list)
for row in rows:
    groups[(row["location"], row["mode"])].append(row)

lines = []
lines.append("# Experiment 13 generated summary")
lines.append("")
lines.append("Values are medians across recorded rounds. Ratio is ext4 / directory; higher is better for throughput and lower is better for latency.")
lines.append("")

root_failures = []
for location, heading in (("root", "Guest root filesystem"), ("workspace", "Shared host workspace control")):
    lines.append(f"## {heading}")
    lines.append("")
    lines.append("| Metric | Directory root VM | ext4 root VM | ext4 / directory |")
    lines.append("|---|---:|---:|---:|")
    directory_rows = groups[(location, "directory")]
    ext4_rows = groups[(location, "ext4")]
    if not directory_rows or not ext4_rows:
        raise SystemExit(f"missing rows for {location}")
    for key, label, unit, higher_better in metrics:
        directory = statistics.median(row[key] for row in directory_rows)
        ext4 = statistics.median(row[key] for row in ext4_rows)
        ratio = ext4 / directory
        if unit == "ms":
            directory_text = f"{directory:.3f} {unit}"
            ext4_text = f"{ext4:.3f} {unit}"
        else:
            directory_text = f"{directory:,.0f} {unit}"
            ext4_text = f"{ext4:,.0f} {unit}"
        lines.append(f"| {label} | {directory_text} | {ext4_text} | {ratio:.2f}x |")
        if location == "root":
            if higher_better and ratio < 0.80:
                root_failures.append(f"{label}: throughput ratio {ratio:.2f}x < 0.80x")
            if not higher_better and ratio > 1.25:
                root_failures.append(f"{label}: latency ratio {ratio:.2f}x > 1.25x")
    lines.append("")

cache_ok = all(row.get("drop_caches") for row in rows)
lines.append(f"Guest cache dropping succeeded in every run: **{'yes' if cache_ok else 'no'}**.")
lines.append("")
if not cache_ok:
    root_failures.append("one or more runs could not drop guest page caches")

if root_failures:
    lines.append("## Verdict: REGRESSION")
    lines.append("")
    lines.append("One or more guest-root metrics crossed the 20% throughput / 25% latency regression threshold:")
    lines.extend(f"- {failure}" for failure in root_failures)
else:
    lines.append("## Verdict: PASS")
    lines.append("")
    lines.append("No guest-root metric crossed the 20% throughput / 25% latency regression threshold.")

text = "\n".join(lines) + "\n"
with open(summary_path, "w") as f:
    f.write(text)
print(text, end="")
if root_failures:
    raise SystemExit(3)
PY

echo "Phase 7: clean shutdown and filesystem check"
ssh_run "$DIRECTORY_PORT" 'sync; kill -TERM "$(cat /run/devd-exp13-workload.pid)"' >/dev/null 2>&1 || true
ssh_run "$EXT4_PORT" 'sync; kill -TERM "$(cat /run/devd-exp13-workload.pid)"' >/dev/null 2>&1 || true
wait_for_pid_exit "$DIRECTORY_PID" || fail "directory-root VM did not shut down"
wait_for_pid_exit "$EXT4_PID" || fail "ext4-root VM did not shut down"
"$E2FSCK" -fn "$EXT4_DISK" >"$WORK_DIR/fsck.log" 2>&1 || {
    cat "$WORK_DIR/fsck.log" >&2
    fail "ext4 filesystem check failed"
}
echo "  final e2fsck: PASS"
echo

if [ "$SUMMARY_RC" -ne 0 ]; then
    fail "ext4 root crossed the performance regression threshold (see $SUMMARY)"
fi

echo "Experiment 13: PASS"
echo "Raw results: $RAW_RESULTS"
echo "Summary:     $SUMMARY"
