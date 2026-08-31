package vm

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	guestInitName           = "devd-init"
	userCommandName         = "user-command"
	imageWorkdirName        = "image-workdir"
	mountGuestName          = "mount-guest"
	regenerateIdentityName  = "regenerate-identity"
	defaultGuestUserWorkdir = "/root"
)

// WorkspaceFilesOpts configures host-side files consumed by the generic guest
// init after it mounts ~/.devd through virtio-fs.
type WorkspaceFilesOpts struct {
	UserCommand  string
	ImageWorkdir string
	MountGuest   string
}

// WriteWorkspaceFiles writes per-workspace configuration without modifying the
// ext4 image. This keeps template cloning on the create hot path.
func WriteWorkspaceFiles(wsDir string, opts WorkspaceFilesOpts) error {
	workdir := opts.ImageWorkdir
	if workdir == "" {
		workdir = defaultGuestUserWorkdir
	}

	files := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{userCommandName, opts.UserCommand, 0600},
		{imageWorkdirName, workdir + "\n", 0600},
		{mountGuestName, opts.MountGuest + "\n", 0600},
	}
	for _, file := range files {
		path := filepath.Join(wsDir, file.name)
		if err := os.WriteFile(path, []byte(file.content), file.mode); err != nil {
			return fmt.Errorf("write %s: %w", file.name, err)
		}
	}
	return nil
}

// MarkRegenerateIdentity makes the next boot replace machine-id and SSH host
// keys. Forked disks inherit both from their stopped parent and must diverge.
func MarkRegenerateIdentity(wsDir string) error {
	path := filepath.Join(wsDir, regenerateIdentityName)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		return fmt.Errorf("mark identity regeneration: %w", err)
	}
	return nil
}

// WriteGuestInit writes the generic init used in every immutable ext4 image.
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

mkdir -p /devd
mount -t virtiofs devd /devd
WS_DIR="/devd/workspaces/$LABEL"
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
cat /devd/ssh/devd_ed25519.pub >/root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
echo 'root:devd' | chpasswd 2>/dev/null || true

if [ ! -s /etc/machine-id ] && [ -r /proc/sys/kernel/random/uuid ]; then
    tr -d '-' </proc/sys/kernel/random/uuid >/etc/machine-id
fi
ssh-keygen -A >/dev/null 2>&1

cat >/tmp/sshd_config <<SSHEOF
Port $SSH_PORT
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication yes
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
