#!/bin/bash
# Experiment 11: devd-vm standalone test
#
# Tests the devd-vm binary directly against an APFS-cloned rootfs.
# Compares clone + boot time vs the krunvm baseline from exp8.
#
# Usage: bash experiments/exp11-devd-vm-test.sh

set -euo pipefail

DEVD_VM="${DEVD_VM:-./build/devd-vm}"
TEMPLATE_ROOTFS=$(du -sh ~/.local/share/containers/storage/vfs/dir/*/ 2>/dev/null | sort -rh | head -1 | awk '{print $2}')
CLONE_DIR="/tmp/devd-vm-test-rootfs"
DEVD_DIR="$HOME/.devd"
SSH_PORT=2233
WS_NAME="devd-vm-test"

ts() { date '+%H:%M:%S'; }
ms() { python3 -c "import time; print(int(time.time()*1000))"; }

echo "============================================="
echo "  Experiment 11: devd-vm standalone test"
echo "  Template: $TEMPLATE_ROOTFS"
echo "  Clone to: $CLONE_DIR"
echo "  SSH port: $SSH_PORT"
echo "  Time:     $(ts)"
echo "============================================="
echo ""

if [ ! -x "$DEVD_VM" ]; then
    echo "ERROR: devd-vm not found at $DEVD_VM — run 'make devd-vm'"
    exit 1
fi

if [ -z "$TEMPLATE_ROOTFS" ]; then
    echo "ERROR: no buildah rootfs found. Run 'krunvm create netshoot' first."
    exit 1
fi

# Clean up from previous runs
rm -rf "$CLONE_DIR"
pkill -f "devd-vm.*devd-vm-test" 2>/dev/null || true
sleep 0.5

# ─── Phase 1: APFS clone the rootfs ───

echo "Phase 1: APFS clone rootfs (cp -Rc)"
T1=$(ms)
cp -Rc "$TEMPLATE_ROOTFS" "$CLONE_DIR"
T2=$(ms)
CLONE_MS=$((T2 - T1))
echo "  Clone time: ${CLONE_MS}ms"
echo "  Clone size: $(du -sh "$CLONE_DIR" | awk '{print $1}')"
echo ""

# ─── Phase 2: Write init script ───

echo "Phase 2: Write init script"
WS_DIR="$DEVD_DIR/workspaces/$WS_NAME"
mkdir -p "$WS_DIR"

# Read the devd SSH public key
PUBKEY=""
if [ -f "$DEVD_DIR/ssh/devd_ed25519.pub" ]; then
    PUBKEY=$(cat "$DEVD_DIR/ssh/devd_ed25519.pub")
fi

cat > "$WS_DIR/init.sh" <<'INITEOF'
#!/bin/sh
LABEL="devd-vm-test"
SSH_PORT=2233
ts() { date '+%H:%M:%S'; }

echo "=== $LABEL boot [$(ts)] ==="

# SSH setup
chmod 755 /root
mkdir -p /root/.ssh
chmod 700 /root/.ssh
INITEOF

if [ -n "$PUBKEY" ]; then
    echo "echo '$PUBKEY' >> /root/.ssh/authorized_keys" >> "$WS_DIR/init.sh"
fi

cat >> "$WS_DIR/init.sh" <<'INITEOF'
chmod 600 /root/.ssh/authorized_keys 2>/dev/null
echo 'root:devd' | chpasswd 2>/dev/null
ssh-keygen -A 2>/dev/null

mkdir -p /run/sshd
cat > /tmp/sshd_config <<SSHEOF
Port 2233
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication yes
HostKey /etc/ssh/ssh_host_rsa_key
HostKey /etc/ssh/ssh_host_ecdsa_key
HostKey /etc/ssh/ssh_host_ed25519_key
SSHEOF

/usr/sbin/sshd -f /tmp/sshd_config
echo "=== $LABEL ready [$(ts)] ==="
echo "DEVD_READY"
while true; do sleep 3600; done
INITEOF
chmod +x "$WS_DIR/init.sh"
echo "  Written to $WS_DIR/init.sh"
echo ""

# ─── Phase 3: Boot VM with devd-vm ───

echo "Phase 3: Boot VM with devd-vm"
LOGFILE="$WS_DIR/vm.log"
T3=$(ms)

$DEVD_VM \
    --root "$CLONE_DIR" \
    --cpus 2 \
    --mem 512 \
    --virtiofs "devd:$DEVD_DIR" \
    -- /bin/sh "/devd/workspaces/$WS_NAME/init.sh" \
    > "$LOGFILE" 2>&1 &

VM_PID=$!
echo "  VM PID: $VM_PID"

# ─── Phase 4: Wait for SSH ───

echo "Phase 4: Waiting for SSH on port $SSH_PORT..."
SSH_READY=false
DEADLINE=$(($(ms) + 30000))

while [ "$(ms)" -lt "$DEADLINE" ]; do
    if nc -z 127.0.0.1 "$SSH_PORT" 2>/dev/null; then
        SSH_READY=true
        break
    fi
    sleep 0.2
done

T4=$(ms)
BOOT_MS=$((T4 - T3))

if $SSH_READY; then
    echo "  SSH ready in ${BOOT_MS}ms"
else
    echo "  SSH NOT ready after 30s"
    echo "  VM log:"
    cat "$LOGFILE" 2>/dev/null | head -30
fi
echo ""

# ─── Phase 5: Verify SSH ───

SSH_OK="FAIL"
if $SSH_READY; then
    echo "Phase 5: Verify SSH"
    RESULT=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR -o ConnectTimeout=5 \
        -i "$DEVD_DIR/ssh/devd_ed25519" \
        -p "$SSH_PORT" root@127.0.0.1 "echo ssh-ok" 2>/dev/null || true)
    if [ "$RESULT" = "ssh-ok" ]; then
        SSH_OK="PASS (key)"
    else
        RESULT=$(sshpass -p devd ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            -o LogLevel=ERROR -o ConnectTimeout=5 \
            -p "$SSH_PORT" root@127.0.0.1 "echo ssh-ok" 2>/dev/null || true)
        if [ "$RESULT" = "ssh-ok" ]; then
            SSH_OK="PASS (password)"
        fi
    fi
    echo "  SSH verify: $SSH_OK"
    echo ""
fi

# ─── Results ───

TOTAL_MS=$((CLONE_MS + BOOT_MS))

echo "============================================="
echo "  Results: devd-vm"
echo "============================================="
echo ""
printf "  %-25s %8s\n" "APFS clone (cp -Rc):" "${CLONE_MS}ms"
printf "  %-25s %8s\n" "Boot (→ SSH ready):" "${BOOT_MS}ms"
printf "  %-25s %8s\n" "Total:" "${TOTAL_MS}ms"
printf "  %-25s %8s\n" "SSH:" "$SSH_OK"
echo ""
echo "  Baseline (krunvm, exp8): Create 8310ms + Boot 600ms = 8910ms"
echo "  Speedup: $(python3 -c "print(f'{8910/$TOTAL_MS:.1f}x')")"
echo ""
echo "============================================="

# ─── Cleanup ───

echo "Cleaning up..."
kill "$VM_PID" 2>/dev/null || true
sleep 1
rm -rf "$CLONE_DIR"
rm -rf "$WS_DIR"
echo "Done."
