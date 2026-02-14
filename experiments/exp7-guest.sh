#!/bin/bash
# Experiment 7: Multi-VM loopback isolation test (NO pre-emption)
#
# Usage: exp7-guest.sh <label> [alive_seconds]

LABEL="${1:-unknown}"
ALIVE="${2:-120}"
ts() { date '+%H:%M:%S'; }

echo "=== $LABEL BOOT [$(ts)] ==="

cd /tmp
echo "from-${LABEL}" > /tmp/index.html

# Start server
python3 -u -m http.server 8080 > /tmp/server.log 2>&1 &
SERVER_PID=$!
sleep 2

echo "server PID=$SERVER_PID [$(ts)]"
echo ""

# Self-test: who do I reach?
echo "--- LOOPBACK TEST (5 curls) ---"
for i in $(seq 1 5); do
    RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:8080/index.html)
    if [ "$RESULT" = "from-${LABEL}" ]; then
        echo "  curl $i: '$RESULT' ← OWN SERVER"
    else
        echo "  curl $i: '$RESULT' ← SOMEONE ELSE (expected from-${LABEL})"
    fi
done
echo ""

echo "--- ss -tlnp ---"
ss -tlnp 2>&1
echo ""

echo "=== $LABEL READY [$(ts)] ==="
echo "READY_SIGNAL"
sleep $ALIVE

kill $SERVER_PID 2>/dev/null
echo "=== $LABEL DONE [$(ts)] ==="
