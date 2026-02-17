#!/bin/bash
# Experiment 8b: Switch Validation
#
# Correct order (per experiments 4-6):
#   1. Create workspaces (registers ports in DB, no VMs started)
#   2. Start daemon (pre-empts contested ports on host)
#   3. Start VMs (TSI falls back to real kernel sockets on pre-empted ports)
#   4. Test switch routing
#
# Prerequisites:
#   - krunvm installed
#   - devd built (go build -o bin/devd ./cmd/devd)
#
# Usage: bash experiments/exp8-switch-test.sh

set -euo pipefail

DEVD="${DEVD:-./bin/devd}"
IMAGE="${IMAGE:-nicolaka/netshoot}"
PORT=8080
PASS=0
FAIL=0

ts() { date '+%H:%M:%S'; }

report() {
    local label="$1" expected="$2" actual="$3"
    if [ "$expected" = "$actual" ]; then
        echo "  $label: PASS (got '$actual')"
        PASS=$((PASS + 1))
    else
        echo "  $label: FAIL (expected '$expected', got '$actual')"
        FAIL=$((FAIL + 1))
    fi
}

cleanup() {
    echo ""
    echo "--- Cleanup ---"
    if [ -n "${DAEMON_PID:-}" ]; then
        kill "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi
    $DEVD rm -f ws-alpha 2>/dev/null || true
    $DEVD rm -f ws-beta 2>/dev/null || true
    echo "Cleanup done."
}
trap cleanup EXIT

echo "============================================="
echo "  Experiment 8b: Switch Validation"
echo "  Image: $IMAGE"
echo "  Contested port: $PORT"
echo "  Time:  $(ts)"
echo "============================================="
echo ""

if [ ! -x "$DEVD" ]; then
    echo "ERROR: devd not found at $DEVD"
    exit 1
fi

# ─── Step 1: Create workspaces (stopped, no VMs yet) ───
echo "--- Step 1: Create workspaces (stopped) ---"

$DEVD create "$IMAGE" --name ws-alpha --ports "$PORT" \
    --cmd "cd /tmp && echo from-alpha > index.html && python3 -m http.server $PORT" \
    2>&1 | sed 's/^/  [alpha] /'

echo ""

$DEVD create "$IMAGE" --name ws-beta --ports "$PORT" \
    --cmd "cd /tmp && echo from-beta > index.html && python3 -m http.server $PORT" \
    2>&1 | sed 's/^/  [beta] /'

echo ""

echo "  Workspaces created (both stopped):"
$DEVD ps -a
echo ""

# ─── Step 2: Start daemon (pre-empts port BEFORE VMs) ───
echo "--- Step 2: Start daemon (pre-empts :$PORT) ---"
$DEVD daemon &
DAEMON_PID=$!
sleep 2
echo "  Daemon PID: $DAEMON_PID"
echo ""

# ─── Step 3: Start VMs (TSI falls back on :$PORT) ───
echo "--- Step 3: Start VMs ---"
$DEVD start ws-alpha 2>&1 | sed 's/^/  [alpha] /'
echo ""
$DEVD start ws-beta 2>&1 | sed 's/^/  [beta] /'
echo ""

# Give HTTP servers a moment to start inside VMs
sleep 3

echo "  Running workspaces:"
$DEVD ps
echo ""

# Wait for daemon to discover tunnels
echo "  Waiting for daemon to set up tunnels..."
sleep 5

# ─── Step 4: Initial routing (ws-beta is active — last started) ───
echo "--- Step 4: Initial routing (ws-beta active) ---"
for i in 1 2 3; do
    RESULT=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html 2>/dev/null || echo "")
    report "curl $i" "from-beta" "$RESULT"
done
echo ""

# ─── Step 5: Switch to ws-alpha ───
echo "--- Step 5: Switch to ws-alpha ---"
$DEVD switch ws-alpha
sleep 0.5

for i in 1 2 3; do
    RESULT=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html 2>/dev/null || echo "")
    report "curl $i" "from-alpha" "$RESULT"
done
echo ""

# ─── Step 6: Switch back to ws-beta ───
echo "--- Step 6: Switch back to ws-beta ---"
$DEVD switch ws-beta
sleep 0.5

for i in 1 2 3; do
    RESULT=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html 2>/dev/null || echo "")
    report "curl $i" "from-beta" "$RESULT"
done
echo ""

# ─── Step 7: Rapid A→B→A switching ───
echo "--- Step 7: Rapid switch (A→B→A) ---"
$DEVD switch ws-alpha
sleep 0.2
R1=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html 2>/dev/null || echo "")
report "after→alpha" "from-alpha" "$R1"

$DEVD switch ws-beta
sleep 0.2
R2=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html 2>/dev/null || echo "")
report "after→beta" "from-beta" "$R2"

$DEVD switch ws-alpha
sleep 0.2
R3=$(curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html 2>/dev/null || echo "")
report "after→alpha(2)" "from-alpha" "$R3"
echo ""

# ─── Step 8: Guest loopback isolation ───
echo "--- Step 8: Guest loopback isolation ---"
ALPHA_LB=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=5 \
    -i ~/.devd/ssh/devd_ed25519 -p 2222 root@127.0.0.1 \
    "curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html" 2>/dev/null || echo "")
report "alpha loopback" "from-alpha" "$ALPHA_LB"

BETA_LB=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=5 \
    -i ~/.devd/ssh/devd_ed25519 -p 2223 root@127.0.0.1 \
    "curl -s --connect-timeout 3 http://127.0.0.1:$PORT/index.html" 2>/dev/null || echo "")
report "beta loopback" "from-beta" "$BETA_LB"
echo ""

# ─── Summary ───
echo "============================================="
echo "  Results: $PASS passed, $FAIL failed"
echo "  Completed at $(ts)"
echo "============================================="

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
