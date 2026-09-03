package cli

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/ssh"
	"devd/internal/vm"
	"devd/internal/workspace"
)

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
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("--mount must be host:guest (e.g. .:/workspace)")
	}
	host, err := filepath.Abs(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("resolve mount host path: %w", err)
	}
	host, err = filepath.EvalSymlinks(host)
	if err != nil {
		return "", "", fmt.Errorf("resolve mount host path %q: %w", parts[0], err)
	}
	info, err := os.Stat(host)
	if err != nil {
		return "", "", fmt.Errorf("inspect mount host path: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("mount host path %q is not a directory", host)
	}

	guest := filepath.Clean(parts[1])
	if !filepath.IsAbs(guest) || guest != parts[1] || strings.ContainsAny(guest, "\r\n") {
		return "", "", fmt.Errorf("mount guest path %q must be a clean absolute path", parts[1])
	}
	switch guest {
	case "/", "/dev", "/proc", "/sys", "/run", "/devd":
		return "", "", fmt.Errorf("mount guest path %q is reserved", guest)
	}
	return host, guest, nil
}

func startWorkspace(database *sql.DB, ws *db.Workspace) (time.Duration, error) {
	if ws.DiskPath == "" {
		return 0, fmt.Errorf("workspace %q has no ext4 disk path", ws.Name)
	}
	if info, err := os.Stat(ws.DiskPath); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return 0, fmt.Errorf("workspace disk %s: %w", ws.DiskPath, err)
	}
	workspaceSpec, err := workspace.Load(ws.WorkspaceDir)
	if err != nil {
		return 0, err
	}
	devdDir, err := config.DevdDir()
	if err != nil {
		return 0, fmt.Errorf("devd directory: %w", err)
	}

	mounts := []vm.Mount{{Tag: "devd", HostPath: devdDir}}
	if workspaceSpec.MountHost != "" {
		mounts = append(mounts, vm.Mount{Tag: "workspace", HostPath: workspaceSpec.MountHost})
	}
	environment := imageEnvironment(workspaceSpec.Environment)
	environment = append(environment,
		"HOME=/root",
		"DEVD_NAME="+ws.Name,
		"DEVD_SSH_PORT="+strconv.Itoa(ws.SSHPort),
	)
	logFile := filepath.Join(ws.WorkspaceDir, "vm.log")

	reservedPorts, err := db.GetReservedPorts(database, ws.Name)
	if err != nil {
		return 0, fmt.Errorf("read declared ports: %w", err)
	}
	if err := ensureProxyDaemon(reservedPorts); err != nil {
		return 0, fmt.Errorf("start workspace %q: %w", ws.Name, err)
	}

	fmt.Printf("INFO Starting workspace %q...\n", ws.Name)
	bootStart := time.Now()
	if err := ensurePortAvailable(ws.SSHPort); err != nil {
		return 0, fmt.Errorf("start workspace %q: %w", ws.Name, err)
	}
	pid, err := vm.Start(vm.StartOpts{
		DiskPath:   ws.DiskPath,
		CPUs:       ws.CPUs,
		Memory:     ws.Memory,
		Env:        environment,
		Mounts:     mounts,
		Command:    "/usr/local/sbin/devd-init",
		Workdir:    "/",
		LogFile:    logFile,
		KernelPath: workspaceSpec.KernelPath,
	})
	if err != nil {
		return 0, fmt.Errorf("start VM: %w", err)
	}

	stopAfterFailure := func() {
		keyPath, _ := config.PrivateKeyPath()
		if stopErr := vm.Stop(vm.StopOpts{PID: pid, SSHPort: ws.SSHPort, KeyPath: keyPath}); stopErr != nil {
			fmt.Printf("WARN stop VM after startup failure: %v\n", stopErr)
		}
	}
	if err := db.SetWorkspaceState(database, ws.Name, "running", pid); err != nil {
		stopAfterFailure()
		return 0, fmt.Errorf("update state: %w", err)
	}
	if err := db.SetActiveWorkspace(database, ws.Name); err != nil {
		stopAfterFailure()
		if stateErr := db.SetWorkspaceState(database, ws.Name, "stopped", 0); stateErr != nil {
			fmt.Printf("WARN update state after active-workspace failure: %v\n", stateErr)
		}
		return 0, fmt.Errorf("set active: %w", err)
	}

	fmt.Printf("INFO Waiting for SSH on port %d...\n", ws.SSHPort)
	if err := waitForSSH(ws.SSHPort, pid, 30*time.Second); err != nil {
		if workspaceSpec.KernelPath != "" && vm.IsRunning(pid) {
			return 0, fmt.Errorf("workspace %q did not reach SSH with custom kernel: %w (VM left running; check log: %s; stop with: devd stop %s)", ws.Name, err, logFile, ws.Name)
		}
		stopAfterFailure()
		if stateErr := db.SetWorkspaceState(database, ws.Name, "stopped", 0); stateErr != nil {
			fmt.Printf("WARN update state after startup failure: %v\n", stateErr)
		}
		return 0, fmt.Errorf("workspace %q failed to start: %w (check log: %s)", ws.Name, err, logFile)
	}
	ws.PID = pid
	ws.State = "running"
	return time.Since(bootStart), nil
}

func imageEnvironment(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, "HOME=") || strings.HasPrefix(value, "DEVD_NAME=") || strings.HasPrefix(value, "DEVD_SSH_PORT=") {
			continue
		}
		if strings.ContainsRune(value, '\x00') || !strings.Contains(value, "=") {
			continue
		}
		// Leave room under devd-vm's 64-entry limit for HOME and two instance values.
		if len(result) == 61 {
			break
		}
		result = append(result, value)
	}
	return result
}

func stopWorkspace(ws *db.Workspace) error {
	keyPath, err := config.PrivateKeyPath()
	if err != nil {
		return err
	}
	return vm.Stop(vm.StopOpts{
		PID:     ws.PID,
		SSHPort: ws.SSHPort,
		KeyPath: keyPath,
	})
}

func nextAvailableSSHPort(database *sql.DB) (int, error) {
	port, err := db.NextSSHPort(database)
	if err != nil {
		return 0, err
	}
	start := port
	for ; port <= 65535; port++ {
		if err := ensurePortAvailable(port); err == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no SSH ports are available at or above %d", start)
}

func ensurePortAvailable(port int) error {
	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("SSH port %d is already in use: %w", port, err)
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release SSH port %d after availability check: %w", port, err)
	}
	return nil
}

func waitForSSH(port, pid int, timeout time.Duration) error {
	keyPath, err := config.PrivateKeyPath()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !vm.IsRunning(pid) {
			return fmt.Errorf("VM process exited before SSH became ready")
		}
		sshCheck := exec.Command("ssh",
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ConnectTimeout=1",
			"-o", "ConnectionAttempts=1",
			"-i", keyPath,
			"-p", strconv.Itoa(port),
			"root@127.0.0.1",
			"true",
		)
		if err := sshCheck.Run(); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("SSH not ready on port %d after %s", port, timeout)
}

func updateSSHConfig(database *sql.DB) error {
	allWorkspaces, err := db.ListWorkspaces(database)
	if err != nil {
		return err
	}
	entries := make([]ssh.SSHConfigEntry, 0, len(allWorkspaces))
	for _, workspace := range allWorkspaces {
		entries = append(entries, ssh.SSHConfigEntry{Name: workspace.Name, Port: workspace.SSHPort})
	}
	return ssh.UpdateSSHConfig(entries)
}
