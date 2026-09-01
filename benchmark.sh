#!/usr/bin/env bash
# Product-level performance acceptance benchmark.
#
# This measures user-visible operations rather than micro-optimizing internals:
# cached run-to-SSH, stopped start-to-SSH, fork-to-SSH, warm port switching, and
# idle VMM RSS. Defaults encode the latency/resource envelope required for an
# interactive local development tool; override thresholds only when comparing a
# deliberately different product profile.

set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
DEVD="$ROOT/bin/devd"
IMAGE=${IMAGE:-nicolaka/netshoot}
RUNS=${RUNS:-10}
RUN_P95_MAX_MS=${RUN_P95_MAX_MS:-1000}
START_P95_MAX_MS=${START_P95_MAX_MS:-1000}
FORK_P95_MAX_MS=${FORK_P95_MAX_MS:-1000}
SWITCH_P95_MAX_MS=${SWITCH_P95_MAX_MS:-250}
RSS_PER_VM_MAX_MIB=${RSS_PER_VM_MAX_MIB:-256}
STATE=$(mktemp -d "${TMPDIR:-/tmp}/devd-benchmark-state.XXXXXX")
PROJECT=$(mktemp -d "${TMPDIR:-/tmp}/devd-benchmark-project.XXXXXX")
STATE=$(cd "$STATE" && pwd -P)
PROJECT=$(cd "$PROJECT" && pwd -P)
DATA=$(mktemp "${TMPDIR:-/tmp}/devd-benchmark-data.XXXXXX")
LAST_OUTPUT=$(mktemp "${TMPDIR:-/tmp}/devd-benchmark-output.XXXXXX")
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
    raise SystemExit("no free benchmark port")
PY
)}

export DEVD_DIR="$STATE"
export DEVD_SSH_CONFIG="$STATE/ssh-config"

now_ns() { python3 -c 'import time; print(time.monotonic_ns())'; }
step() { printf '\n=== %s ===\n' "$*"; }
remote() { "$DEVD" ssh "$1" -- "$2"; }
cleanup() {
    if [[ ${KEEP:-0} == 1 ]]; then
        printf 'KEEP=1: benchmark state retained at %s\n' "$STATE" >&2
        return
    fi
    set +e
    for workspace in bench-beta bench-alpha bench-source; do
        "$DEVD" rm -f "$workspace" >/dev/null 2>&1 || true
    done
    for ((i = 1; i <= RUNS; i++)); do
        "$DEVD" rm -f "$(printf 'fork-%02d' "$i")" >/dev/null 2>&1 || true
        "$DEVD" rm -f "$(printf 'run-%02d' "$i")" >/dev/null 2>&1 || true
    done
    sleep 0.2
    rm -rf "$STATE" "$PROJECT"
    rm -f "$DATA" "$LAST_OUTPUT"
}
trap cleanup EXIT

measure() {
    local metric=$1
    shift
    local started finished elapsed
    started=$(now_ns)
    if ! "$@" >"$LAST_OUTPUT" 2>&1; then
        cat "$LAST_OUTPUT" >&2
        return 1
    fi
    finished=$(now_ns)
    elapsed=$(python3 - "$started" "$finished" <<'PY'
import sys
print(f"{(int(sys.argv[2]) - int(sys.argv[1])) / 1_000_000:.3f}")
PY
)
    printf '%s\t%s\n' "$metric" "$elapsed" >>"$DATA"
}

run_workspace() { (cd "$PROJECT" && "$DEVD" run "$IMAGE" -n "$1"); }
start_source() { "$DEVD" start bench-source; }
fork_source() { "$DEVD" fork bench-source -n "$1"; }
wait_guest_http() {
    local workspace=$1 want=$2 result=""
    for _ in $(seq 1 50); do
        result=$(remote "$workspace" "curl -fsS --max-time 1 http://127.0.0.1:$PORT/index.html" 2>/dev/null || true)
        [[ "$result" == "$want" ]] && return 0
        sleep 0.2
    done
    printf 'server in %s did not become ready\n' "$workspace" >&2
    return 1
}
wait_host_http() {
    local want=$1 result=""
    for _ in $(seq 1 25); do
        result=$(curl -fsS --max-time 1 "http://127.0.0.1:$PORT/index.html" 2>/dev/null || true)
        [[ "$result" == "$want" ]] && return 0
        sleep 0.05
    done
    printf 'host route did not return %s\n' "$want" >&2
    return 1
}
switch_and_request() {
    "$DEVD" switch "$1"
    wait_host_http "$2"
}

[[ "$RUNS" =~ ^[1-9][0-9]*$ ]] || { echo "ERROR: RUNS must be positive" >&2; exit 2; }
if [[ ${SKIP_BUILD:-0} != 1 ]]; then
    step "Build complete bundle"
    (cd "$ROOT" && go build -o bin/devd ./cmd/devd && scripts/build-runtime bin)
fi
[[ -x "$DEVD" ]] || { echo "ERROR: build bin/devd first" >&2; exit 1; }
if [[ -n ${DEVD_TEST_IMAGE_CACHE:-} ]]; then
    rm -rf "$STATE/images"
    ln -s "$DEVD_TEST_IMAGE_CACHE" "$STATE/images"
fi
printf 'benchmark\n' >"$PROJECT/README"

step "Warm image/template (excluded from hot-path thresholds)"
warm_started=$(now_ns)
run_workspace bench-source | tee "$LAST_OUTPUT"
warm_finished=$(now_ns)
warm_ms=$(python3 - "$warm_started" "$warm_finished" <<'PY'
import sys
print(f"{(int(sys.argv[2]) - int(sys.argv[1])) / 1_000_000:.1f}")
PY
)
"$DEVD" stop bench-source >/dev/null
printf 'Warm/cold preparation plus first boot: %s ms\n' "$warm_ms"

step "Cached run → authenticated SSH ready ($RUNS runs)"
for i in $(seq 1 "$RUNS"); do
    name=$(printf 'run-%02d' "$i")
    measure run_ms run_workspace "$name"
    "$DEVD" rm -f "$name" >/dev/null
    printf '.'
done
printf '\n'

step "Stopped start → authenticated SSH ready ($RUNS runs)"
for _ in $(seq 1 "$RUNS"); do
    measure start_ms start_source
    "$DEVD" stop bench-source >/dev/null
    printf '.'
done
printf '\n'

step "Stopped disk fork → authenticated SSH ready ($RUNS runs)"
for i in $(seq 1 "$RUNS"); do
    name=$(printf 'fork-%02d' "$i")
    measure fork_ms fork_source "$name"
    "$DEVD" rm -f "$name" >/dev/null
    printf '.'
done
printf '\n'

step "Warm shared-port switch → first correct response ($RUNS runs)"
"$DEVD" run "$IMAGE" -n bench-alpha --no-mount -p "$PORT" \
    --cmd "cd /tmp && echo alpha >index.html && python3 -m http.server $PORT" >/dev/null
"$DEVD" run "$IMAGE" -n bench-beta --no-mount -p "$PORT" \
    --cmd "cd /tmp && echo beta >index.html && python3 -m http.server $PORT" >/dev/null
wait_guest_http bench-alpha alpha
wait_guest_http bench-beta beta
# Warm both SSH tunnels before measuring route selection itself.
"$DEVD" switch bench-alpha >/dev/null
wait_host_http alpha
"$DEVD" switch bench-beta >/dev/null
wait_host_http beta
for i in $(seq 1 "$RUNS"); do
    if (( i % 2 )); then
        measure switch_ms switch_and_request bench-alpha alpha
    else
        measure switch_ms switch_and_request bench-beta beta
    fi
    printf '.'
done
printf '\n'

step "Idle VMM memory floor"
vm_pids=$(ps -eo pid=,command= | awk -v state="$STATE/workspaces/" '$2 ~ /\/devd-vm$/ && index($0, state) {print $1}')
[[ -n "$vm_pids" ]] || { echo "ERROR: no benchmark VMM processes found" >&2; exit 1; }
total_kib=0
vm_count=0
for pid in $vm_pids; do
    rss=$(ps -o rss= -p "$pid" | tr -d ' ')
    total_kib=$((total_kib + rss))
    vm_count=$((vm_count + 1))
done
rss_per_vm_mib=$(python3 - "$total_kib" "$vm_count" <<'PY'
import sys
print(f"{int(sys.argv[1]) / int(sys.argv[2]) / 1024:.3f}")
PY
)
printf 'rss_per_vm_mib\t%s\n' "$rss_per_vm_mib" >>"$DATA"

step "Acceptance report"
export RUNS IMAGE warm_ms RUN_P95_MAX_MS START_P95_MAX_MS FORK_P95_MAX_MS SWITCH_P95_MAX_MS RSS_PER_VM_MAX_MIB
python3 - "$DATA" "${REPORT:-}" <<'PY'
import math
import os
import platform
import statistics
import sys
from pathlib import Path

values = {}
for line in Path(sys.argv[1]).read_text().splitlines():
    metric, value = line.split("\t")
    values.setdefault(metric, []).append(float(value))

def percentile(samples, p):
    ordered = sorted(samples)
    return ordered[max(0, math.ceil(len(ordered) * p) - 1)]

specs = [
    ("run_ms", "Cached run → SSH", float(os.environ["RUN_P95_MAX_MS"]), "ms"),
    ("start_ms", "Stopped start → SSH", float(os.environ["START_P95_MAX_MS"]), "ms"),
    ("fork_ms", "Fork → SSH", float(os.environ["FORK_P95_MAX_MS"]), "ms"),
    ("switch_ms", "Warm switch → response", float(os.environ["SWITCH_P95_MAX_MS"]), "ms"),
    ("rss_per_vm_mib", "Idle RSS per VM", float(os.environ["RSS_PER_VM_MAX_MIB"]), "MiB"),
]

lines = [
    "# devd product benchmark",
    "",
    f"- Platform: {platform.platform()}",
    f"- Image: `{os.environ['IMAGE']}`",
    f"- Timed iterations: {os.environ['RUNS']}",
    f"- Image preparation + first boot (informational): {float(os.environ['warm_ms']):.1f} ms",
    "",
    "| User-visible operation | p50 | p95 | Acceptance | Result |",
    "|---|---:|---:|---:|:---:|",
]
failed = False
for metric, label, limit, unit in specs:
    samples = values[metric]
    p50 = statistics.median(samples)
    p95 = percentile(samples, 0.95)
    passed = p95 <= limit
    failed |= not passed
    lines.append(f"| {label} | {p50:.1f} {unit} | {p95:.1f} {unit} | p95 ≤ {limit:.0f} {unit} | {'PASS' if passed else 'FAIL'} |")

report = "\n".join(lines) + "\n"
print(report)
if sys.argv[2]:
    Path(sys.argv[2]).write_text(report)
if failed:
    raise SystemExit(1)
PY
