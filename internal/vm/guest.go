package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GuestInitOpts configures the guest init script.
type GuestInitOpts struct {
	Label     string // workspace name
	SSHPort   int
	PublicKey string // contents of devd SSH public key
	UserCmd   string // optional command to run after sshd starts
}

// WriteInitScript generates the guest init script for a workspace.
// Returns the path to the script inside the guest mount.
func WriteInitScript(wsDir string, opts GuestInitOpts) (string, error) {
	script := generateInitScript(opts)
	scriptPath := filepath.Join(wsDir, "init.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("write init script: %w", err)
	}
	return scriptPath, nil
}

func generateInitScript(opts GuestInitOpts) string {
	var b strings.Builder

	b.WriteString("#!/bin/sh\n")
	b.WriteString("# devd guest init — auto-generated\n")
	b.WriteString(fmt.Sprintf("LABEL=%q\n", opts.Label))
	b.WriteString(fmt.Sprintf("SSH_PORT=%d\n", opts.SSHPort))
	b.WriteString("ts() { date '+%H:%M:%S'; }\n\n")

	b.WriteString("echo \"=== $LABEL boot [$(ts)] ===\"\n\n")

	// Set up SSH authorized keys
	b.WriteString("# SSH authorized keys\n")
	b.WriteString("chmod 755 /root\n") // sshd rejects authorized_keys if home is group-writable
	b.WriteString("mkdir -p /root/.ssh\n")
	b.WriteString("chmod 700 /root/.ssh\n")
	b.WriteString(fmt.Sprintf("echo %q >> /root/.ssh/authorized_keys\n", opts.PublicKey))
	b.WriteString("chmod 600 /root/.ssh/authorized_keys\n\n")

	// Password fallback for convenience
	b.WriteString("# Password fallback\n")
	b.WriteString("echo 'root:devd' | chpasswd 2>/dev/null\n\n")

	// Generate SSH host keys
	b.WriteString("# SSH host keys\n")
	b.WriteString("ssh-keygen -A 2>/dev/null\n\n")

	// Configure sshd
	b.WriteString("# Configure sshd\n")
	b.WriteString("mkdir -p /run/sshd\n")
	b.WriteString("cat > /tmp/sshd_config <<SSHEOF\n")
	b.WriteString(fmt.Sprintf("Port %d\n", opts.SSHPort))
	b.WriteString("PermitRootLogin yes\n")
	b.WriteString("PubkeyAuthentication yes\n")
	b.WriteString("PasswordAuthentication yes\n")
	b.WriteString("HostKey /etc/ssh/ssh_host_rsa_key\n")
	b.WriteString("HostKey /etc/ssh/ssh_host_ecdsa_key\n")
	b.WriteString("HostKey /etc/ssh/ssh_host_ed25519_key\n")
	b.WriteString("SSHEOF\n\n")

	// Start sshd
	b.WriteString("# Start sshd\n")
	b.WriteString("/usr/sbin/sshd -f /tmp/sshd_config\n")
	b.WriteString("SSHD_EXIT=$?\n")
	b.WriteString("if [ $SSHD_EXIT -ne 0 ]; then\n")
	b.WriteString("    echo \"sshd failed (exit $SSHD_EXIT)\"\n")
	b.WriteString("    cat /tmp/sshd.log 2>/dev/null\n")
	b.WriteString("fi\n\n")

	// Optional user command
	if opts.UserCmd != "" {
		b.WriteString("# User command\n")
		b.WriteString(fmt.Sprintf("(%s) &\n", opts.UserCmd))
		b.WriteString("USER_CMD_PID=$!\n")
		b.WriteString("echo \"user cmd PID=$USER_CMD_PID\"\n\n")
	}

	// Signal ready
	b.WriteString("echo \"=== $LABEL ready [$(ts)] ===\"\n")
	b.WriteString("echo \"DEVD_READY\"\n\n")

	// Stay alive
	b.WriteString("# Stay alive\n")
	b.WriteString("while true; do sleep 3600; done\n")

	return b.String()
}
