package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"devd/internal/config"
)

const (
	specName               = "config.json"
	userCommandName        = "user-command"
	imageWorkdirName       = "image-workdir"
	mountGuestName         = "mount-guest"
	regenerateIdentityName = "regenerate-identity"
	defaultWorkdir         = "/root"
)

// Spec describes persistent workspace behavior that is not part of its root
// disk. The SQLite record owns identity and runtime state; this file owns the
// inputs needed to boot and fork the workspace.
type Spec struct {
	Version     int      `json:"format_version"`
	Image       string   `json:"image"`
	ImageDigest string   `json:"image_digest"`
	Environment []string `json:"environment,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	UserCommand string   `json:"user_command,omitempty"`
	MountHost   string   `json:"mount_host,omitempty"`
	MountGuest  string   `json:"mount_guest,omitempty"`
	ParentName  string   `json:"parent_name,omitempty"`
}

// Save atomically writes the host-side spec, then renders the small plain-text
// files consumed by devd-init through the devd virtio-fs mount.
func Save(dir string, spec Spec) error {
	spec.Version = config.WorkspaceSpecVersion
	if err := writeJSON(filepath.Join(dir, specName), spec, 0600); err != nil {
		return fmt.Errorf("write workspace spec: %w", err)
	}

	workdir := spec.WorkingDir
	if workdir == "" {
		workdir = defaultWorkdir
	}
	files := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{userCommandName, spec.UserCommand, 0600},
		{imageWorkdirName, workdir + "\n", 0600},
		{mountGuestName, spec.MountGuest + "\n", 0600},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte(file.content), file.mode); err != nil {
			return fmt.Errorf("write workspace %s: %w", file.name, err)
		}
	}
	return nil
}

// Load reads a workspace's persistent boot specification.
func Load(dir string) (*Spec, error) {
	data, err := os.ReadFile(filepath.Join(dir, specName))
	if err != nil {
		return nil, fmt.Errorf("read workspace spec: %w", err)
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("decode workspace spec: %w", err)
	}
	if spec.Version != config.WorkspaceSpecVersion {
		return nil, fmt.Errorf("workspace spec version %d is unsupported (expected %d)", spec.Version, config.WorkspaceSpecVersion)
	}
	return &spec, nil
}

// MarkRegenerateIdentity makes the next boot replace machine-id and SSH host
// keys. Forked disks inherit both from their stopped parent and must diverge.
func MarkRegenerateIdentity(dir string) error {
	path := filepath.Join(dir, regenerateIdentityName)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		return fmt.Errorf("mark identity regeneration: %w", err)
	}
	return nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, mode); err != nil {
		return err
	}
	file, err := os.Open(temp)
	if err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
