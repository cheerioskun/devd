package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"devd/internal/workspace"
)

// workspacePlan is resolved before any persistent workspace files are created.
// Mount is CLI syntax until resolvePlan canonicalizes it into Spec. It is not
// another persisted source of truth.
type workspacePlan struct {
	Image       string
	ImageDigest string
	ParentName  string
	Spec        workspace.Spec
	CPUs        int
	Memory      int
	Ports       []int
	Mount       string
}

type forkOverrides struct {
	CPUs               int
	CPUsChanged        bool
	Memory             int
	MemoryChanged      bool
	Ports              []int
	PortsChanged       bool
	Mount              string
	MountChanged       bool
	UserCommand        string
	UserCommandChanged bool
	KernelPath         string
	KernelPathChanged  bool
}

// applyForkOverrides is pure: omitted values inherit, explicitly empty values
// clear. Neither the source plan nor any of its slices are mutated.
func applyForkOverrides(source workspacePlan, overrides forkOverrides) workspacePlan {
	result := source
	result.Spec.Environment = slices.Clone(source.Spec.Environment)
	result.Ports = slices.Clone(source.Ports)
	if overrides.CPUsChanged {
		result.CPUs = overrides.CPUs
	}
	if overrides.MemoryChanged {
		result.Memory = overrides.Memory
	}
	if overrides.PortsChanged {
		result.Ports = slices.Clone(overrides.Ports)
	}
	if overrides.MountChanged {
		result.Mount = overrides.Mount
	}
	if overrides.UserCommandChanged {
		result.Spec.UserCommand = overrides.UserCommand
	}
	if overrides.KernelPathChanged {
		result.Spec.KernelPath = overrides.KernelPath
	}
	return result
}

func resolvePlan(plan workspacePlan) (workspacePlan, error) {
	if plan.CPUs < 1 || plan.CPUs > 255 {
		return plan, fmt.Errorf("cpus must be between 1 and 255")
	}
	if plan.Memory < 1 || uint64(plan.Memory) > uint64(^uint32(0)) {
		return plan, fmt.Errorf("memory must be between 1 and 4294967295 MiB")
	}
	if err := validatePorts(plan.Ports); err != nil {
		return plan, err
	}
	var err error
	plan.Spec.MountHost, plan.Spec.MountGuest, err = parseMount(plan.Mount)
	if err != nil {
		return plan, err
	}
	plan.Spec.KernelPath, err = resolveKernelPath(plan.Spec.KernelPath)
	return plan, err
}

func resolveKernelPath(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve kernel path: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve kernel path %q: %w", value, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect kernel path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("kernel path %q is not a regular file", value)
	}
	return path, nil
}

func validatePorts(ports []int) error {
	seen := make(map[int]bool, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("port %d must be between 1 and 65535", port)
		}
		if seen[port] {
			return fmt.Errorf("port %d was declared more than once", port)
		}
		seen[port] = true
	}
	return nil
}

func parseMount(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	hostValue, guestValue, found := strings.Cut(value, ":")
	if !found || hostValue == "" || guestValue == "" {
		return "", "", fmt.Errorf("--mount must be host:guest (e.g. .:/workspace)")
	}
	host, err := filepath.Abs(hostValue)
	if err != nil {
		return "", "", fmt.Errorf("resolve mount host path: %w", err)
	}
	host, err = filepath.EvalSymlinks(host)
	if err != nil {
		return "", "", fmt.Errorf("resolve mount host path %q: %w", hostValue, err)
	}
	info, err := os.Stat(host)
	if err != nil {
		return "", "", fmt.Errorf("inspect mount host path: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("mount host path %q is not a directory", host)
	}
	guest := filepath.Clean(guestValue)
	if !filepath.IsAbs(guest) || guest != guestValue || strings.ContainsAny(guest, "\r\n") {
		return "", "", fmt.Errorf("mount guest path %q must be a clean absolute path", guestValue)
	}
	for _, reserved := range []string{"/", "/dev", "/proc", "/sys", "/run", "/devd"} {
		if guest == reserved || (reserved != "/" && strings.HasPrefix(guest, reserved+"/")) {
			return "", "", fmt.Errorf("mount guest path %q is reserved", guest)
		}
	}
	return host, guest, nil
}

func imageEnvironment(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		key, _, valid := strings.Cut(value, "=")
		if key == "HOME" || key == "DEVD_NAME" || key == "DEVD_SSH_PORT" {
			continue
		}
		if !valid || key == "" || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("invalid image environment entry")
		}
		result = append(result, value)
	}
	if len(result) > 61 {
		return nil, fmt.Errorf("image environment exceeds 61 entries (three entries are reserved for devd)")
	}
	return result, nil
}
