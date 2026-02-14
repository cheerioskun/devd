#!/bin/bash
# Guest script for Experiment 6: SSH tunnel relay test
# Runs inside a krunvm VM (nicolaka/netshoot image)
#
# Starts HTTP server on :8080 (local, pre-empted by host proxy)
# Starts sshd on :<ssh_port> (TSI exposes this on host)
# No socat relay needed — SSH tunnel on host handles the relay.
#
# Usage: exp6-guest.sh <label> <ssh_port> [alive_seconds]

LABEL="${1:-unknown}"
SSH_PORT="${2:-22}"
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
grep -E "bind|listen|Serving" /tmp/server.log 2>/dev/null | head -5
echo ""

# Set up sshd
echo "--- Setting up sshd on :$SSH_PORT ---"
# Generate host keys if needed
ssh-keygen -A 2>/dev/null

# Configure sshd
mkdir -p /run/sshd
cat > /tmp/sshd_config <<SSHEOF
Port $SSH_PORT
PermitRootLogin yes
PasswordAuthentication yes
HostKey /etc/ssh/ssh_host_rsa_key
HostKey /etc/ssh/ssh_host_ecdsa_key
HostKey /etc/ssh/ssh_host_ed25519_key
SSHEOF

# Set root password
echo "root:devd" | chpasswd 2>/dev/null

# Start sshd
/usr/sbin/sshd -f /tmp/sshd_config 2>/tmp/sshd.log
SSHD_EXIT=$?
echo "sshd exit: $SSHD_EXIT [$(ts)]"
if [ $SSHD_EXIT -ne 0 ]; then
    echo "sshd log:"
    cat /tmp/sshd.log
fi
echo ""

# Self-test: loopback
echo "--- SELF-TEST 1: curl 127.0.0.1:8080/index.html ---"
RESULT=$(curl -s --connect-timeout 5 http://127.0.0.1:8080/index.html)
echo "result: '$RESULT' [$(ts)]"
if [ "$RESULT" = "from-${LABEL}" ]; then
    echo "SELF-TEST 1: PASS"
else
    echo "SELF-TEST 1: FAIL — got '$RESULT'"
fi
echo ""

# Self-test: sshd listening
echo "--- SELF-TEST 2: ss -tlnp (check sshd + server) ---"
ss -tlnp 2>&1
echo ""

echo "=== $LABEL READY [$(ts)] ==="
echo "READY_SIGNAL"
echo "Server on :8080, sshd on :$SSH_PORT, alive ${ALIVE}s"
sleep $ALIVE

kill $SERVER_PID 2>/dev/null
echo "=== $LABEL DONE [$(ts)] ==="
