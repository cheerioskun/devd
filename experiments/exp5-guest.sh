#!/bin/bash
# Guest script for Experiment 5: proxy routing test
# Runs inside a krunvm VM (nicolaka/netshoot image)
#
# Starts a server on :8080 (local, will be pre-empted by host proxy)
# Starts a socat relay on :<relay_port> → localhost:8080
# The relay port is NOT pre-empted, so TSI exposes it on the host.
# The host proxy connects to this relay port to reach the VM's server.
#
# Usage: exp5-guest.sh <label> <relay_port> [alive_seconds]

LABEL="${1:-unknown}"
RELAY_PORT="${2:-9001}"
ALIVE="${3:-300}"
ts() { date '+%H:%M:%S'; }

echo "=== $LABEL BOOT [$(ts)] ==="

cd /tmp
echo "from-${LABEL}" > /tmp/index.html

# Start HTTP server on :8080
echo "--- Starting server on :8080 ---"
strace -e trace=bind,listen -f python3 -u -m http.server 8080 > /tmp/server.log 2>&1 &
SERVER_PID=$!
sleep 2

echo "server PID=$SERVER_PID [$(ts)]"
echo "--- strace log: ---"
grep -E "bind|listen|Serving" /tmp/server.log 2>/dev/null | head -5
echo ""

# Start socat relay: <relay_port> → localhost:8080
echo "--- Starting socat relay :$RELAY_PORT → localhost:8080 ---"
socat TCP-LISTEN:${RELAY_PORT},fork,reuseaddr TCP:127.0.0.1:8080 &
RELAY_PID=$!
sleep 1
echo "relay PID=$RELAY_PID [$(ts)]"
echo ""

# Self-test 1: direct loopback
echo "--- SELF-TEST 1: curl 127.0.0.1:8080/index.html (direct) ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:8080/index.html)
echo "result: '$RESULT' [$(ts)]"
if [ "$RESULT" = "from-${LABEL}" ]; then
    echo "SELF-TEST 1: PASS — reached own server directly"
else
    echo "SELF-TEST 1: FAIL — got '$RESULT' (expected 'from-${LABEL}')"
fi
echo ""

# Self-test 2: via relay
echo "--- SELF-TEST 2: curl 127.0.0.1:$RELAY_PORT/index.html (via relay) ---"
RESULT2=$(curl -s --connect-timeout 5 http://127.0.0.1:${RELAY_PORT}/index.html)
echo "result: '$RESULT2' [$(ts)]"
if [ "$RESULT2" = "from-${LABEL}" ]; then
    echo "SELF-TEST 2: PASS — relay works"
else
    echo "SELF-TEST 2: FAIL — got '$RESULT2' (expected 'from-${LABEL}')"
fi
echo ""

# Check ss -tlnp
echo "--- ss -tlnp ---"
ss -tlnp 2>&1
echo ""

echo "=== $LABEL READY [$(ts)] ==="
echo "READY_SIGNAL"
echo "Server on :8080, relay on :$RELAY_PORT, alive ${ALIVE}s"
sleep $ALIVE

kill $SERVER_PID $RELAY_PID 2>/dev/null
echo "=== $LABEL DONE [$(ts)] ==="
