#!/bin/bash
# Experiment 9: VM Density & Resource Overhead
#
# Incrementally spins up VMs, measuring per-VM:
#   - Boot time
#   - krunvm process RSS (resident memory)
#   - Host memory pressure
#   - CPU usage
#
# Stops when a VM fails to boot or a user-defined limit is hit.
#
# Usage: bash experiments/exp9-density.sh [max_vms]

set -euo pipefail

DEVD="${DEVD:-./bin/devd}"
IMAGE="${IMAGE:-nicolaka/netshoot}"
MAX_VMS="${1:-10}"
CPUS_PER_VM=2
MEM_PER_VM=512  # MB

ts() { date '+%H:%M:%S'; }

# Get total host memory in MB
HOST_RAM_MB=$(sysctl -n hw.memsize | awk '{printf "%d", $1/1024/1024}')
HOST_CPUS=$(sysctl -n hw.ncpu)

echo "============================================="
echo "  Experiment 9: VM Density & Resource Overhead"
echo "  Image:        $IMAGE"
echo "  Max VMs:      $MAX_VMS"
echo "  Per-VM:       $CPUS_PER_VM vCPUs, ${MEM_PER_VM}MB RAM"
echo "  Host:         ${HOST_RAM_MB}MB RAM, $HOST_CPUS CPUs"
echo "  Time:         $(ts)"
echo "============================================="
echo ""

cleanup() {
    echo ""
    echo "--- Cleanup ---"
    for i in $(seq 1 "$MAX_VMS"); do
        $DEVD rm -f "density-$i" 2>/dev/null || true
    done
    echo "Done."
}
trap cleanup EXIT

# Baseline: host memory before any VMs
baseline_free() {
    # macOS: page size * free pages
    local pagesize=$(vm_stat | head -1 | grep -oE '[0-9]+')
    local free_pages=$(vm_stat | grep "Pages free" | grep -oE '[0-9]+')
    local inactive_pages=$(vm_stat | grep "Pages inactive" | grep -oE '[0-9]+')
    echo $(( (free_pages + inactive_pages) * pagesize / 1024 / 1024 ))
}

krunvm_rss_total() {
    # Sum RSS of all krunvm processes (in KB → MB)
    ps -eo rss,comm 2>/dev/null | grep krunvm | awk '{sum += $1} END {printf "%d", sum/1024}'
}

krunvm_cpu_total() {
    ps -eo pcpu,comm 2>/dev/null | grep krunvm | awk '{sum += $1} END {printf "%.1f", sum}'
}

krunvm_count() {
    ps -eo comm 2>/dev/null | grep -c krunvm || echo 0
}

echo "--- Baseline (no VMs) ---"
echo "  Free+inactive memory: $(baseline_free)MB"
echo "  krunvm processes: $(krunvm_count)"
echo ""

# Table header
printf "%-4s  %8s  %8s  %10s  %8s  %8s  %s\n" \
    "VM#" "Boot" "Create" "krunvm RSS" "CPU%" "Procs" "Status"
printf "%-4s  %8s  %8s  %10s  %8s  %8s  %s\n" \
    "---" "------" "------" "----------" "-----" "-----" "------"

BOOT_TIMES=()
CREATE_TIMES=()
RSS_VALUES=()
CPU_VALUES=()

for i in $(seq 1 "$MAX_VMS"); do
    NAME="density-$i"

    # Create
    CREATE_START=$(python3 -c "import time; print(time.time())")
    CREATE_OUT=$($DEVD create "$IMAGE" --name "$NAME" --cpus $CPUS_PER_VM --memory $MEM_PER_VM 2>&1)
    CREATE_END=$(python3 -c "import time; print(time.time())")
    CREATE_T=$(python3 -c "print(f'{$CREATE_END - $CREATE_START:.2f}')")

    # Start
    BOOT_START=$(python3 -c "import time; print(time.time())")
    START_OUT=$($DEVD start "$NAME" 2>&1)
    BOOT_END=$(python3 -c "import time; print(time.time())")
    BOOT_T=$(python3 -c "print(f'{$BOOT_END - $BOOT_START:.2f}')")

    # Check if start succeeded
    if echo "$START_OUT" | grep -q "SSH ready"; then
        STATUS="OK"
    else
        STATUS="FAIL"
    fi

    # Let things settle
    sleep 1

    # Measure
    RSS=$(krunvm_rss_total)
    CPU=$(krunvm_cpu_total)
    PROCS=$(krunvm_count)

    BOOT_TIMES+=("$BOOT_T")
    CREATE_TIMES+=("$CREATE_T")
    RSS_VALUES+=("$RSS")
    CPU_VALUES+=("$CPU")

    printf "%-4d  %7ss  %7ss  %8sMB  %7s%%  %8s  %s\n" \
        "$i" "$BOOT_T" "$CREATE_T" "$RSS" "$CPU" "$PROCS" "$STATUS"

    if [ "$STATUS" = "FAIL" ]; then
        echo ""
        echo "  VM $i failed to start. Stopping here."
        MAX_VMS=$i
        break
    fi
done

echo ""
echo "============================================="
echo "  Summary: $MAX_VMS VMs running"
echo "============================================="
echo ""

# Per-VM overhead
python3 <<PYEOF
boot = [$(IFS=,; echo "${BOOT_TIMES[*]}")]
create = [$(IFS=,; echo "${CREATE_TIMES[*]}")]
rss = [$(IFS=,; echo "${RSS_VALUES[*]}")]
cpu = [$(IFS=,; echo "${CPU_VALUES[*]}")]

n = len(boot)
print(f"  VMs running:          {n}")
print(f"  Total krunvm RSS:     {rss[-1]}MB")
if n > 1:
    rss_deltas = [rss[i] - rss[i-1] for i in range(1, n)]
    print(f"  Per-VM RSS (delta):   ~{sum(rss_deltas)/len(rss_deltas):.0f}MB")
else:
    print(f"  Per-VM RSS:           {rss[0]}MB")
print(f"  Total CPU%:           {cpu[-1]}%")
print()

import statistics
print(f"  Boot time (start → SSH):")
print(f"    Mean:   {statistics.mean(boot):.2f}s")
print(f"    Median: {statistics.median(boot):.2f}s")
print(f"    Min:    {min(boot):.2f}s")
print(f"    Max:    {max(boot):.2f}s")
print()
print(f"  Create time:")
print(f"    Mean:   {statistics.mean(create):.2f}s")
print(f"    Median: {statistics.median(create):.2f}s")
PYEOF

echo ""
echo "  Host: ${HOST_RAM_MB}MB RAM, $HOST_CPUS CPUs"
echo "  Completed at $(ts)"
echo "============================================="
