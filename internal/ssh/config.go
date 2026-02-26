package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"devd/internal/config"
)

const (
	beginMarker = "# BEGIN devd-managed"
	endMarker   = "# END devd-managed"
)

// SSHConfigEntry represents an entry for ~/.ssh/config.
type SSHConfigEntry struct {
	Name string
	Port int
}

// UpdateSSHConfig rewrites the devd-managed block in ~/.ssh/config.
func UpdateSSHConfig(entries []SSHConfigEntry) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	sshConfigPath := filepath.Join(home, ".ssh", "config")

	privKeyPath, err := config.PrivateKeyPath()
	if err != nil {
		return err
	}

	// Build the devd block
	var block strings.Builder
	block.WriteString(beginMarker + "\n")
	for _, e := range entries {
		block.WriteString(fmt.Sprintf("Host devd-%s\n", e.Name))
		block.WriteString("    HostName 127.0.0.1\n")
		block.WriteString(fmt.Sprintf("    Port %d\n", e.Port))
		block.WriteString("    User root\n")
		block.WriteString(fmt.Sprintf("    IdentityFile %s\n", privKeyPath))
		block.WriteString("    StrictHostKeyChecking no\n")
		block.WriteString("    UserKnownHostsFile /dev/null\n")
		block.WriteString("    LogLevel ERROR\n")
		block.WriteString("\n")
	}
	block.WriteString(endMarker + "\n")

	// Read existing config
	existing, err := os.ReadFile(sshConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ssh config: %w", err)
	}

	// Ensure ~/.ssh/ exists
	if err := os.MkdirAll(filepath.Dir(sshConfigPath), 0700); err != nil {
		return err
	}

	content := string(existing)
	newContent := replaceBlock(content, block.String())

	return os.WriteFile(sshConfigPath, []byte(newContent), 0600)
}

// RemoveSSHConfigEntry removes all devd entries from ~/.ssh/config.
func RemoveSSHConfigEntry() error {
	return UpdateSSHConfig(nil)
}

func replaceBlock(content, newBlock string) string {
	startIdx := strings.Index(content, beginMarker)
	endIdx := strings.Index(content, endMarker)

	if startIdx >= 0 && endIdx >= 0 {
		// Replace existing block
		endIdx += len(endMarker)
		// Skip trailing newline if present
		if endIdx < len(content) && content[endIdx] == '\n' {
			endIdx++
		}
		return content[:startIdx] + newBlock + content[endIdx:]
	}

	// No existing block — append
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + "\n" + newBlock
}
