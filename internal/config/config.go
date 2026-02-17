package config

import (
	"os"
	"path/filepath"
)

const (
	DefaultImage  = "nicolaka/netshoot"
	DefaultCPUs   = 2
	DefaultMemory = 512 // MB

	SSHPortBase   = 2222
	RelayPortBase = 9001

	SSHKeyName = "devd_ed25519"
)

// DevdDir returns ~/.devd, creating it if needed.
func DevdDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".devd")
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

// WorkspaceDir returns ~/.devd/workspaces/<name>/, creating it if needed.
func WorkspaceDir(name string) (string, error) {
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
