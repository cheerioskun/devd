#!/bin/bash
# Guest script for Experiment 4: 0.0.0.0 pre-emption test
# Runs inside a krunvm VM (nicolaka/netshoot image)
#
# Starts a server on 0.0.0.0:8080, runs self-tests, stays alive for host-side tests.

PORT=8080
ALIVE=300
ts() { date '+%H:%M:%S'; }

echo "=== GUEST BOOT [$(ts)] ==="

cd /tmp
echo "from-guest" > /tmp/index.html

# Start server with strace to verify bind() and listen() syscalls
echo "--- Starting server on :$PORT with strace ---"
strace -e trace=bind,listen -f python3 -u -m http.server $PORT > /tmp/server.log 2>&1 &
SERVER_PID=$!
sleep 2

echo "server PID=$SERVER_PID [$(ts)]"
echo "--- strace/server log (first 10 lines): ---"
head -10 /tmp/server.log
echo ""

# Self-test 1: curl own server
echo "--- SELF-TEST 1: curl 127.0.0.1:$PORT/index.html ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
echo "result: '$RESULT' [$(ts)]"
if [ "$RESULT" = "from-guest" ]; then
    echo "SELF-TEST 1: PASS — reached own server"
elif echo "$RESULT" | grep -q "from-host"; then
    echo "SELF-TEST 1: REACHED HOST — loopback broken, host 0.0.0.0 wins"
elif [ -z "$RESULT" ]; then
    echo "SELF-TEST 1: EMPTY/TIMEOUT — server unreachable"
else
    echo "SELF-TEST 1: UNEXPECTED — '$RESULT'"
fi
echo ""

# Self-test 2: curl localhost
echo "--- SELF-TEST 2: curl localhost:$PORT/index.html ---"
RESULT2=$(curl -s --connect-timeout 5 http://localhost:$PORT/index.html)
echo "result: '$RESULT2' [$(ts)]"
if [ "$RESULT2" = "from-guest" ]; then
    echo "SELF-TEST 2: PASS — reached own server"
elif echo "$RESULT2" | grep -q "from-host"; then
    echo "SELF-TEST 2: REACHED HOST — loopback broken"
elif [ -z "$RESULT2" ]; then
    echo "SELF-TEST 2: EMPTY/TIMEOUT — server unreachable"
else
    echo "SELF-TEST 2: UNEXPECTED — '$RESULT2'"
fi
echo ""

# Self-test 3: what does ss show? (expect empty under TSI)
echo "--- SELF-TEST 3: ss -tlnp (expect empty under TSI) ---"
ss -tlnp 2>&1 || echo "(ss not available)"
echo ""

# Self-test 4: strace bind result
echo "--- SELF-TEST 4: strace bind/listen results ---"
grep -E "bind|listen" /tmp/server.log 2>/dev/null || echo "(no bind/listen in log)"
echo ""

echo "=== GUEST READY — waiting ${ALIVE}s for host-side tests [$(ts)] ==="
echo "READY_SIGNAL"
sleep $ALIVE

kill $SERVER_PID 2>/dev/null
echo "=== GUEST DONE [$(ts)] ==="
