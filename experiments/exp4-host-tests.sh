#!/bin/bash
# Host-side tests for Experiment 4: 0.0.0.0 pre-emption
# Run this on the macOS host AFTER:
#   1. exp4-host-listener.py is running
#   2. The VM is started and shows READY_SIGNAL
#
# Usage: bash exp4-host-tests.sh [port]

PORT="${1:-8080}"
ts() { date '+%H:%M:%S'; }

echo "============================================="
echo "  Experiment 4: Host-Side Tests"
echo "  Port: $PORT"
echo "  Time: $(ts)"
echo "============================================="
echo ""

# TEST 1: Who's listening on the port?
echo "--- TEST 1: lsof -i :$PORT ---"
lsof -i :$PORT
echo ""

# Count listeners
LISTENER_COUNT=$(lsof -i :$PORT | grep LISTEN | wc -l | tr -d ' ')
echo "Listener count: $LISTENER_COUNT"
if [ "$LISTENER_COUNT" -eq 1 ]; then
    echo "RESULT: Only ONE listener — pre-emption likely worked (TSI bind failed)"
elif [ "$LISTENER_COUNT" -ge 2 ]; then
    echo "RESULT: Multiple listeners — TSI coexists (SO_REUSEPORT)"
else
    echo "RESULT: No listeners?!"
fi
echo ""

# TEST 2: Who responds to host curl?
echo "--- TEST 2: host curl localhost:$PORT/index.html ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
echo "result: '$RESULT' [$(ts)]"
if echo "$RESULT" | grep -q "from-host"; then
    echo "TEST 2: HOST LISTENER RESPONDS — host owns the port for localhost callers"
elif echo "$RESULT" | grep -q "from-guest"; then
    echo "TEST 2: GUEST RESPONDS — TSI serves localhost (coexistence, guest wins)"
elif [ -z "$RESULT" ]; then
    echo "TEST 2: EMPTY/TIMEOUT"
else
    echo "TEST 2: UNEXPECTED — '$RESULT'"
fi
echo ""

# TEST 3: Consistency check — 10 curls
echo "--- TEST 3: 10 consecutive curls ---"
HOST_COUNT=0
GUEST_COUNT=0
OTHER_COUNT=0
for i in $(seq 1 10); do
    R=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html)
    echo "  curl $i: '$R'"
    if echo "$R" | grep -q "from-host"; then
        HOST_COUNT=$((HOST_COUNT + 1))
    elif echo "$R" | grep -q "from-guest"; then
        GUEST_COUNT=$((GUEST_COUNT + 1))
    else
        OTHER_COUNT=$((OTHER_COUNT + 1))
    fi
done
echo ""
echo "Summary: host=$HOST_COUNT guest=$GUEST_COUNT other=$OTHER_COUNT"
if [ "$HOST_COUNT" -eq 10 ]; then
    echo "TEST 3: HOST WINS ALL — pre-emption is complete"
elif [ "$GUEST_COUNT" -eq 10 ]; then
    echo "TEST 3: GUEST WINS ALL — TSI takes priority"
else
    echo "TEST 3: MIXED — kernel distributing between listeners"
fi
echo ""

# TEST 4: curl from 0.0.0.0 perspective (external interface)
echo "--- TEST 4: curl to 0.0.0.0:$PORT/index.html ---"
RESULT4=$(curl -s --connect-timeout 5 http://0.0.0.0:$PORT/index.html)
echo "result: '$RESULT4' [$(ts)]"
echo ""

echo "============================================="
echo "  Tests complete [$(ts)]"
echo "============================================="
