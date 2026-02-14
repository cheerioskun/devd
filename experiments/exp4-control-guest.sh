#!/bin/bash
# Control test: VM with NO host pre-emption
# Check if ss -tlnp shows listeners (was empty in earlier experiments)
PORT=8080
ts() { date '+%H:%M:%S'; }

echo "=== CONTROL BOOT [$(ts)] ==="
cd /tmp
echo "from-control" > /tmp/index.html

strace -e trace=bind,listen -f python3 -u -m http.server $PORT > /tmp/server.log 2>&1 &
SERVER_PID=$!
sleep 2

echo "server PID=$SERVER_PID [$(ts)]"
echo "--- strace log ---"
head -10 /tmp/server.log
echo ""

echo "--- ss -tlnp ---"
ss -tlnp 2>&1
echo ""

echo "--- curl self-test ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:$PORT/index.html)
echo "self-test: '$RESULT' [$(ts)]"
echo ""

echo "=== CONTROL READY [$(ts)] ==="
echo "READY_SIGNAL"
sleep 60

kill $SERVER_PID 2>/dev/null
echo "=== CONTROL DONE ==="
