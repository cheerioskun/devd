#!/bin/bash
# Experiment 8a: VM Boot Benchmark — Time to First SSH
#
# Measures wall-clock time from 'devd run' to first successful SSH connection.
# Reports create time, boot time, and total time per iteration.
#
# Prerequisites:
#   - krunvm installed (brew install krunvm)
#   - devd built (go build -o bin/devd ./cmd/devd)
#   - sshpass installed (brew install hudochenkov/sshpass/sshpass)
#
# Usage: bash experiments/exp8-benchmark.sh [iterations]

set -euo pipefail

DEVD="${DEVD:-./bin/devd}"
IMAGE="${IMAGE:-nicolaka/netshoot}"
ITERATIONS="${1:-5}"

ts() { date '+%H:%M:%S'; }

echo "============================================="
echo "  Experiment 8a: VM Boot Benchmark"
echo "  Image:      $IMAGE"
echo "  Iterations: $ITERATIONS"
echo "  Time:       $(ts)"
echo "  devd:       $DEVD"
echo "============================================="
echo ""

# Preflight
if [ ! -x "$DEVD" ]; then
    echo "ERROR: devd not found at $DEVD — run 'go build -o bin/devd ./cmd/devd'"
    exit 1
fi
if ! command -v krunvm &>/dev/null; then
    echo "ERROR: krunvm not found — install with: brew install krunvm"
    exit 1
fi

# Arrays to collect results
CREATE_TIMES=()
BOOT_TIMES=()
TOTAL_TIMES=()
SSH_RESULTS=()

for i in $(seq 1 "$ITERATIONS"); do
    NAME="bench-$i"
    echo "--- Run $i/$ITERATIONS: $NAME ---"

    # Clean up any leftover
    $DEVD rm -f "$NAME" 2>/dev/null || true

    # Run devd and capture output
    OUTPUT=$($DEVD run "$IMAGE" --name "$NAME" 2>&1)
    echo "$OUTPUT" | grep -E "^(INFO|     )"

    # Parse times from devd output
    CREATE_T=$(echo "$OUTPUT" | grep -oE 'Create:  [0-9]+\.[0-9]+s' | grep -oE '[0-9]+\.[0-9]+' || echo "0")
    BOOT_T=$(echo "$OUTPUT" | grep -oE 'Boot:    [0-9]+\.[0-9]+s' | grep -oE '[0-9]+\.[0-9]+' || echo "0")
    TOTAL_T=$(echo "$OUTPUT" | grep -oE 'Total:   [0-9]+\.[0-9]+s' | grep -oE '[0-9]+\.[0-9]+' || echo "0")

    CREATE_TIMES+=("$CREATE_T")
    BOOT_TIMES+=("$BOOT_T")
    TOTAL_TIMES+=("$TOTAL_T")

    # Verify SSH actually works (key auth)
    SSH_PORT=$(echo "$OUTPUT" | grep -oE 'Port:    [0-9]+' | grep -oE '[0-9]+' || echo "0")
    SSH_OK="FAIL"
    if [ "$SSH_PORT" != "0" ]; then
        RESULT=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            -o LogLevel=ERROR -o ConnectTimeout=5 \
            -i ~/.devd/ssh/devd_ed25519 \
            -p "$SSH_PORT" root@127.0.0.1 "echo ssh-ok" 2>/dev/null || true)
        if [ "$RESULT" = "ssh-ok" ]; then
            SSH_OK="PASS (key)"
        else
            # Fallback: try password auth
            RESULT=$(sshpass -p devd ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
                -o LogLevel=ERROR -o ConnectTimeout=5 \
                -p "$SSH_PORT" root@127.0.0.1 "echo ssh-ok" 2>/dev/null || true)
            if [ "$RESULT" = "ssh-ok" ]; then
                SSH_OK="PASS (password)"
            fi
        fi
    fi
    SSH_RESULTS+=("$SSH_OK")
    echo "  SSH verify: $SSH_OK"
    echo ""

    # Cleanup
    $DEVD rm -f "$NAME" 2>/dev/null || true
    sleep 1
done

echo "============================================="
echo "  Results"
echo "============================================="
echo ""

# Table header
printf "  %-5s  %8s  %8s  %8s  %s\n" "Run" "Create" "Boot" "Total" "SSH"
printf "  %-5s  %8s  %8s  %8s  %s\n" "---" "------" "----" "-----" "---"

for i in "${!CREATE_TIMES[@]}"; do
    printf "  %-5d  %7ss  %7ss  %7ss  %s\n" \
        "$((i+1))" "${CREATE_TIMES[$i]}" "${BOOT_TIMES[$i]}" "${TOTAL_TIMES[$i]}" "${SSH_RESULTS[$i]}"
done
echo ""

# Stats via python
python3 <<PYEOF
import statistics

create = [$(IFS=,; echo "${CREATE_TIMES[*]}")]
boot = [$(IFS=,; echo "${BOOT_TIMES[*]}")]
total = [$(IFS=,; echo "${TOTAL_TIMES[*]}")]

def stats(label, data):
    print(f"  {label}:")
    print(f"    Min:    {min(data):.2f}s")
    print(f"    Max:    {max(data):.2f}s")
    print(f"    Mean:   {statistics.mean(data):.2f}s")
    print(f"    Median: {statistics.median(data):.2f}s")
    if len(data) > 1:
        print(f"    Stdev:  {statistics.stdev(data):.2f}s")
    print()

stats("Create (krunvm create — OCI extraction)", create)
stats("Boot (krunvm start → SSH ready)", boot)
stats("Total (end-to-end)", total)
PYEOF

echo "  Completed at $(ts)"
echo "============================================="
