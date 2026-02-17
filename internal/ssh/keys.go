package ssh

import (
	"fmt"
	"os"
	"os/exec"

	"devd/internal/config"
)

// EnsureKeypair generates the devd SSH keypair if it doesn't exist.
// Returns the public key contents.
func EnsureKeypair() (string, error) {
	privPath, err := config.PrivateKeyPath()
	if err != nil {
		return "", err
	}
	pubPath, err := config.PublicKeyPath()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(privPath); err == nil {
		// Key already exists, read public key
		pub, err := os.ReadFile(pubPath)
		if err != nil {
			return "", fmt.Errorf("read public key: %w", err)
		}
		return string(pub), nil
	}

	// Generate new keypair
	cmd := exec.Command("ssh-keygen",
		"-t", "ed25519",
		"-f", privPath,
		"-N", "", // no passphrase
		"-C", "devd",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %w", err)
	}

	pub, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("read public key: %w", err)
	}
	return string(pub), nil
}

// PublicKey reads the existing devd public key.
func PublicKey() (string, error) {
	pubPath, err := config.PublicKeyPath()
	if err != nil {
		return "", err
	}
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		return "", fmt.Errorf("read public key: %w", err)
	}
	return string(pub), nil
}
