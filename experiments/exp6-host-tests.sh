#!/bin/bash
# Host-side tests for Experiment 6: SSH tunnel relay
#
# Usage: bash exp6-host-tests.sh [phase]
#   phase: "6a" for single VM, "6b" for two-VM switch

PHASE="${1:-6a}"
PORT=8080
ts() { date '+%H:%M:%S'; }

echo "============================================="
echo "  Experiment 6 — Phase: $PHASE"
echo "  Port: $PORT"
echo "  Time: $(ts)"
echo "============================================="
echo ""

# TEST 1: Who's listening?
echo "--- TEST 1: lsof -i :$PORT,:9001,:9002,:2222,:2223 ---"
lsof -i :$PORT -i :9001 -i :9002 -i :2222 -i :2223 2>/dev/null
echo ""

# TEST 2: Proxy target
echo "--- TEST 2: current proxy target ---"
cat /Users/hemant/repos/devd/experiments/target.txt 2>/dev/null || echo "(no target.txt)"
echo ""

# TEST 3: host curl through proxy
echo "--- TEST 3: host curl localhost:$PORT/index.html ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
echo "result: '$RESULT' [$(ts)]"
echo ""

# TEST 4: consistency
echo "--- TEST 4: 5 consecutive curls ---"
for i in $(seq 1 5); do
    R=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html)
    echo "  curl $i: '$R'"
done
echo ""

# TEST 5: direct tunnel port access
echo "--- TEST 5: direct curl to tunnel ports ---"
R1=$(curl -s --connect-timeout 3 http://127.0.0.1:9001/index.html 2>/dev/null)
echo "  :9001 → '$R1'"
R2=$(curl -s --connect-timeout 3 http://127.0.0.1:9002/index.html 2>/dev/null)
echo "  :9002 → '$R2'"
echo ""

if [ "$PHASE" = "6b" ]; then
    echo "============================================="
    echo "  Switching proxy target..."
    echo "============================================="
    echo ""

    CURRENT=$(cat /Users/hemant/repos/devd/experiments/target.txt 2>/dev/null)
    if echo "$CURRENT" | grep -q "9001"; then
        NEW_TARGET="127.0.0.1:9002"
        echo "Switching: 9001 → 9002"
    else
        NEW_TARGET="127.0.0.1:9001"
        echo "Switching: 9002 → 9001"
    fi

    echo "$NEW_TARGET" > /Users/hemant/repos/devd/experiments/target.txt

    RESULT_SW=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
    echo "  After switch: '$RESULT_SW' [$(ts)]"
    echo ""

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
