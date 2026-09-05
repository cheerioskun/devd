package vm

import (
	"fmt"
	"os"
	"path/filepath"
)

const guestInitName = "devd-init"

// GuestBootstrap mounts the narrowly scoped control export and runs the current
// host-supplied init. It deliberately bypasses bootstrap scripts in old disks.
const GuestBootstrap = "mkdir -p /devd && mount -t virtiofs devd /devd && exec /bin/sh /devd/devd-init"

// WriteGuestInit installs this version's guest policy in the control directory.
func WriteGuestInit(dir string) (string, error) {
	path := filepath.Join(dir, guestInitName)
	if err := os.WriteFile(path, []byte(guestInitScript), 0755); err != nil {
		return "", fmt.Errorf("write guest init: %w", err)
	}
	return path, nil
}

const guestInitScript = `#!/bin/sh
set -eu

LABEL=${DEVD_NAME:?DEVD_NAME is required}
SSH_PORT=${DEVD_SSH_PORT:?DEVD_SSH_PORT is required}

ts() { date '+%H:%M:%S'; }
echo "=== $LABEL boot [$(ts)] ==="

WS_DIR=/devd
test -d "$WS_DIR"

if [ -f "$WS_DIR/regenerate-identity" ]; then
    rm -f /etc/ssh/ssh_host_*
    rm -f /var/lib/dbus/machine-id
    : >/etc/machine-id
fi

if [ -s "$WS_DIR/mount-guest" ]; then
    MOUNT_GUEST=$(cat "$WS_DIR/mount-guest")
    if [ -n "$MOUNT_GUEST" ]; then
        mkdir -p "$MOUNT_GUEST"
        mount -t virtiofs workspace "$MOUNT_GUEST"
    fi
fi

hostname "$LABEL" 2>/dev/null || true
chmod 755 /root
mkdir -p /root/.ssh /run/sshd
chmod 700 /root/.ssh
cat "$WS_DIR/authorized_keys" >/root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
# Unlock the account for key login without installing a shared password.
# Password and keyboard-interactive authentication are disabled below.
passwd -d root >/dev/null 2>&1 || true

if [ ! -s /etc/machine-id ] && [ -r /proc/sys/kernel/random/uuid ]; then
    tr -d '-' </proc/sys/kernel/random/uuid >/etc/machine-id
fi
ssh-keygen -A >/dev/null 2>&1

cat >/tmp/sshd_config <<SSHEOF
Port $SSH_PORT
PermitRootLogin prohibit-password
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
PidFile /run/sshd-devd.pid
HostKey /etc/ssh/ssh_host_rsa_key
HostKey /etc/ssh/ssh_host_ecdsa_key
HostKey /etc/ssh/ssh_host_ed25519_key
SSHEOF
/usr/sbin/sshd -f /tmp/sshd_config

if [ -f "$WS_DIR/regenerate-identity" ]; then
    rm -f "$WS_DIR/regenerate-identity"
fi

if [ -s "$WS_DIR/user-command" ]; then
    WORKDIR=$(cat "$WS_DIR/image-workdir")
    (
        cd "$WORKDIR" 2>/dev/null || cd /
        /bin/sh "$WS_DIR/user-command"
    ) &
    echo "user cmd PID=$!"
fi

echo $$ >/run/devd-workload.pid
shutdown_workload() {
    sync
    exit 0
}
trap shutdown_workload TERM INT

echo "=== $LABEL ready [$(ts)] ==="
echo DEVD_READY
while true; do
    sleep 3600 &
    wait $! || true
done
`
