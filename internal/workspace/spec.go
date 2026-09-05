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
// disk. SQLite owns provenance, resource allocation, and runtime state; this
// file owns the remaining boot behavior. No fields are authoritative in both.
type Spec struct {
	Version     int      `json:"format_version"`
	Environment []string `json:"environment,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	UserCommand string   `json:"user_command,omitempty"`
	MountHost   string   `json:"mount_host,omitempty"`
	MountGuest  string   `json:"mount_guest,omitempty"`
	KernelPath  string   `json:"kernel_path,omitempty"`
}

// Save atomically writes only the authoritative host-side spec. Guest-facing
// files are disposable inputs rendered from this spec immediately before boot.
func Save(dir string, spec Spec) error {
	spec.Version = config.WorkspaceSpecVersion
	if err := writeJSON(filepath.Join(dir, specName), spec, 0600); err != nil {
		return fmt.Errorf("write workspace spec: %w", err)
	}
	return nil
}

// PrepareControl renders the only management directory exported to this guest.
// It contains no private keys, disk paths, specs, DB files, or sibling state.
// Call only under the workspace lock with its VM confirmed stopped. Replace
// the directory rather than following files or symlinks a previous guest wrote.
func PrepareControl(dir string, spec Spec, publicKey string) (string, error) {
	control := filepath.Join(dir, "control")
	if err := os.RemoveAll(control); err != nil {
		return "", fmt.Errorf("remove old guest inputs: %w", err)
	}
	if err := os.Mkdir(control, 0700); err != nil {
		return "", fmt.Errorf("create guest inputs: %w", err)
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
		{"authorized_keys", publicKey, 0600},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(control, file.name), []byte(file.content), file.mode); err != nil {
			return "", fmt.Errorf("write guest %s: %w", file.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, regenerateIdentityName)); err == nil {
		if err := MarkRegenerateIdentity(control); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read identity regeneration intent: %w", err)
	}
	return control, nil
}

// CompleteBoot acknowledges durable first-boot work only after readiness.
// Failed boots retain the intent and may safely repeat identity regeneration.
func CompleteBoot(dir string) error {
	if err := os.Remove(filepath.Join(dir, regenerateIdentityName)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("acknowledge identity regeneration: %w", err)
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
	// Version 1 has the same boot fields, but duplicated DB provenance and used
	// the global guest export/in-disk bootstrap. Start upgrades before launch.
	if spec.Version != 1 && spec.Version != config.WorkspaceSpecVersion {
		return nil, fmt.Errorf("workspace spec version %d is unsupported (expected 1 or %d)", spec.Version, config.WorkspaceSpecVersion)
	}
	return &spec, nil
}

// MarkRegenerateIdentity makes the next boot replace machine-id and SSH host
// keys. Forked disks inherit both from their stopped parent and must diverge.
func MarkRegenerateIdentity(dir string) error {
	path := filepath.Join(dir, regenerateIdentityName)
	if err := writeFile(path, nil, 0600); err != nil {
		return fmt.Errorf("mark identity regeneration: %w", err)
	}
	return nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'), mode)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
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
