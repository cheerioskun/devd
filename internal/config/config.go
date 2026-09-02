package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const (
	DefaultImage   = "nicolaka/netshoot"
	DefaultCPUs    = 2
	DefaultMemory  = 512       // MB
	DefaultDiskMiB = 32 * 1024 // sparse logical capacity

	TemplateFormatVersion = 1
	WorkspaceSpecVersion  = 1

	SSHPortBase   = 2222
	RelayPortBase = 9001

	SSHKeyName = "devd_ed25519"
)

var workspaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateWorkspaceName rejects names that cannot safely be used as directory
// and guest configuration components.
func ValidateWorkspaceName(name string) error {
	if !workspaceNamePattern.MatchString(name) {
		return fmt.Errorf("workspace name %q must match %s", name, workspaceNamePattern.String())
	}
	return nil
}

// DevdDir returns ~/.devd, creating it if needed. DEVD_DIR overrides the path
// for tests and isolated installations.
func DevdDir() (string, error) {
	dir := os.Getenv("DEVD_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".devd")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// DBPath returns ~/.devd/devd.db.
func DBPath() (string, error) {
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "devd.db"), nil
}

// DaemonSocketPath returns the local proxy daemon control socket path.
func DaemonSocketPath() (string, error) {
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.sock"), nil
}

// DaemonLockPath returns the lock held for the lifetime of the proxy daemon.
func DaemonLockPath() (string, error) {
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

// DaemonLogPath returns the background proxy daemon log path.
func DaemonLogPath() (string, error) {
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.log"), nil
}

// SSHDir returns ~/.devd/ssh/, creating it if needed.
func SSHDir() (string, error) {
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	sshDir := filepath.Join(dir, "ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return "", err
	}
	return sshDir, nil
}

// ImagesDir returns ~/.devd/images/, creating it if needed.
func ImagesDir() (string, error) {
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	imagesDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imagesDir, 0700); err != nil {
		return "", err
	}
	return imagesDir, nil
}

// WorkspaceDir returns ~/.devd/workspaces/<name>/, creating it if needed.
func WorkspaceDir(name string) (string, error) {
	if err := ValidateWorkspaceName(name); err != nil {
		return "", err
	}
	dir, err := DevdDir()
	if err != nil {
		return "", err
	}
	wsDir := filepath.Join(dir, "workspaces", name)
	if err := os.MkdirAll(wsDir, 0700); err != nil {
		return "", err
	}
	return wsDir, nil
}

// WorkspaceDiskPath returns the ext4 disk path for a workspace.
func WorkspaceDiskPath(name string) (string, error) {
	dir, err := WorkspaceDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rootfs.ext4"), nil
}

// PrivateKeyPath returns the path to the devd SSH private key.
func PrivateKeyPath() (string, error) {
	dir, err := SSHDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SSHKeyName), nil
}

// PublicKeyPath returns the path to the devd SSH public key.
func PublicKeyPath() (string, error) {
	dir, err := SSHDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SSHKeyName+".pub"), nil
}
