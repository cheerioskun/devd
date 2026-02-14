#!/bin/bash
# Host-side tests for Experiment 5: proxy routing
#
# Usage: bash exp5-host-tests.sh [phase]
#   phase: "5a" for single VM, "5b" for two-VM switch
#
# Prerequisites:
#   - exp5-proxy.py running on :8080
#   - VM(s) started and showing READY_SIGNAL
#   - target.txt pointing to the active VM's relay port

PHASE="${1:-5a}"
PORT=8080
ts() { date '+%H:%M:%S'; }

echo "============================================="
echo "  Experiment 5 — Phase: $PHASE"
echo "  Port: $PORT"
echo "  Time: $(ts)"
echo "============================================="
echo ""

# TEST 1: Who's listening on relevant ports?
echo "--- TEST 1: lsof -i :$PORT,:9001,:9002 ---"
lsof -i :$PORT -i :9001 -i :9002 2>/dev/null
echo ""

# TEST 2: What's the current proxy target?
echo "--- TEST 2: current proxy target ---"
cat /Users/hemant/repos/devd/experiments/target.txt 2>/dev/null || echo "(no target.txt)"
echo ""

# TEST 3: host curl through proxy
echo "--- TEST 3: host curl localhost:$PORT/index.html ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
echo "result: '$RESULT' [$(ts)]"
echo ""

# TEST 4: consistency — 5 curls
echo "--- TEST 4: 5 consecutive curls ---"
for i in $(seq 1 5); do
    R=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html)
    echo "  curl $i: '$R'"
done
echo ""

# TEST 5: direct relay port access (bypassing proxy)
echo "--- TEST 5: direct curl to relay ports ---"
R1=$(curl -s --connect-timeout 3 http://127.0.0.1:9001/index.html 2>/dev/null)
echo "  :9001 → '$R1'"
R2=$(curl -s --connect-timeout 3 http://127.0.0.1:9002/index.html 2>/dev/null)
echo "  :9002 → '$R2'"
echo ""

if [ "$PHASE" = "5b" ]; then
    echo "============================================="
    echo "  Switching proxy target..."
    echo "============================================="
    echo ""

    # Read current target to determine switch direction
    CURRENT=$(cat /Users/hemant/repos/devd/experiments/target.txt 2>/dev/null)
    if echo "$CURRENT" | grep -q "9001"; then
        NEW_TARGET="127.0.0.1:9002"
        echo "Switching: 9001 → 9002"
    else
        NEW_TARGET="127.0.0.1:9001"
        echo "Switching: 9002 → 9001"
    fi

    # Time the switch
    echo "$NEW_TARGET" > /Users/hemant/repos/devd/experiments/target.txt
    SWITCH_TIME=$(date +%s%N)

    # First curl after switch (measures effective switch latency)
    RESULT_SW=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
    AFTER_TIME=$(date +%s%N)

    # macOS date doesn't support %N, use python for nanoseconds
    LATENCY_MS=$(python3 -c "print(f'{($AFTER_TIME - $SWITCH_TIME) / 1_000_000:.1f}')" 2>/dev/null || echo "N/A")

    echo "  After switch: '$RESULT_SW' (latency: ${LATENCY_MS}ms) [$(ts)]"
    echo ""

    # TEST 6: consistency after switch
    echo "--- TEST 6: 5 curls after switch ---"
    for i in $(seq 1 5); do
        R=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html)
        echo "  curl $i: '$R'"
    done
    echo ""
fi

echo "============================================="
echo "  Tests complete [$(ts)]"
echo "============================================="
