package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/ssh"
	"devd/internal/storage"
	"devd/internal/vm"
	"devd/internal/workspace"
)

func processPath(ws *db.Workspace) string {
	return filepath.Join(ws.WorkspaceDir, "process.json")
}

// loadWorkspace observes the launch receipt rather than trusting a cached DB
// state or reusable PID. The caller holds the workspace operation lock.
func loadWorkspace(database *sql.DB, name string) (*db.Workspace, error) {
	ws, err := db.GetWorkspace(database, name)
	if err != nil {
		return nil, err
	}
	process, err := vm.ReadProcess(processPath(ws))
	if err != nil {
		return nil, err
	}
	state, pid := "stopped", 0
	if process != nil {
		running, err := process.Running()
		if err != nil {
			return nil, err
		}
		if running {
			state, pid = "running", process.PID
		}
	} else if vm.IsRunning(ws.PID) {
		// Older companions have neither identity receipts nor root-disk locks.
		// Never guess that their PID is safe to signal or their disk safe to use.
		return nil, fmt.Errorf("workspace %q has an unverified legacy PID %d; stop it with the previous devd version before upgrading", name, ws.PID)
	}
	if state == "stopped" && ws.DiskPath != "" {
		lock, err := storage.LockDisk(ws.DiskPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot confirm workspace %q is stopped: %w", name, err)
		}
		if lock != nil {
			_ = lock.Close()
		}
	}
	if ws.State != state || ws.PID != pid || (state == "stopped" && ws.IsActive) {
		if err := db.SetWorkspaceState(database, name, state, pid); err != nil {
			return nil, fmt.Errorf("reconcile workspace state: %w", err)
		}
	}
	ws.State, ws.PID = state, pid
	if state == "stopped" {
		ws.IsActive = false
	}
	return ws, nil
}

// startWorkspace requires the operation lock. A started process is recorded
// before readiness; only a ready workspace becomes the active routing target.
func startWorkspace(database *sql.DB, ws *db.Workspace) (time.Duration, error) {
	if ws.State == "running" {
		return 0, fmt.Errorf("workspace %q is already running (PID %d)", ws.Name, ws.PID)
	}
	if ws.DiskPath == "" {
		return 0, fmt.Errorf("workspace %q has no ext4 disk path", ws.Name)
	}
	diskLock, err := storage.LockDisk(ws.DiskPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = diskLock.Close() }()
	boot, err := prepareBoot(ws)
	if err != nil {
		return 0, err
	}
	logFile := boot.LogFile
	reservedPorts, err := db.GetReservedPorts(database, ws.Name)
	if err != nil {
		return 0, fmt.Errorf("read declared ports: %w", err)
	}
	if err := ensureProxyDaemon(reservedPorts); err != nil {
		return 0, fmt.Errorf("prepare workspace ports: %w", err)
	}
	if err := ensurePortAvailable(ws.SSHPort); err != nil {
		return 0, err
	}
	fmt.Printf("INFO Starting workspace %q...\n", ws.Name)
	bootStart := time.Now()
	// Hand off the inode lock to the companion. The operation lock still
	// excludes all other devd commands; a competing direct launcher fails safe.
	if err := diskLock.Close(); err != nil {
		return 0, err
	}
	process, err := vm.Start(boot)
	if err != nil {
		return 0, fmt.Errorf("start VM: %w", err)
	}
	ws.PID, ws.State = process.PID, "running"
	fail := func(cause error) (time.Duration, error) {
		// A failed stop must retain the running state and process receipt.
		stopErr := stopWorkspace(database, ws)
		return 0, fmt.Errorf("workspace %q failed to start (log: %s): %w", ws.Name, logFile, errors.Join(cause, stopErr))
	}
	if err := db.SetWorkspaceState(database, ws.Name, "running", process.PID); err != nil {
		return fail(fmt.Errorf("record running state: %w", err))
	}
	fmt.Printf("INFO Waiting for SSH on port %d...\n", ws.SSHPort)
	if err := waitForSSH(ws.SSHPort, process, 30*time.Second); err != nil {
		// Preserve the current custom-kernel diagnostic contract. Explicit
		// asynchronous startup will replace this exception in a separate change.
		if running, observeErr := process.Running(); boot.KernelPath != "" && observeErr == nil && running {
			return 0, fmt.Errorf("workspace %q did not reach SSH: %w (VM left running; log: %s; stop with: devd stop %s)", ws.Name, err, logFile, ws.Name)
		}
		return fail(err)
	}
	if err := workspace.CompleteBoot(ws.WorkspaceDir); err != nil {
		return fail(err)
	}
	if err := db.SetActiveWorkspace(database, ws.Name); err != nil {
		return fail(fmt.Errorf("activate workspace: %w", err))
	}
	ws.IsActive = true
	return time.Since(bootStart), nil
}

// prepareBoot revalidates persisted inputs and renders the guest control export.
// It does not launch a process, reserve ports, or change routing/runtime state.
// The caller holds both the operation lock and the stopped disk's inode lock.
func prepareBoot(ws *db.Workspace) (vm.StartOpts, error) {
	spec, err := workspace.Load(ws.WorkspaceDir)
	if err != nil {
		return vm.StartOpts{}, err
	}
	mount := ""
	if spec.MountHost != "" {
		mount = spec.MountHost + ":" + spec.MountGuest
	}
	plan, err := resolvePlan(workspacePlan{Spec: *spec, CPUs: ws.CPUs, Memory: ws.Memory, Mount: mount})
	if err != nil {
		return vm.StartOpts{}, err
	}
	environment, err := imageEnvironment(plan.Spec.Environment)
	if err != nil {
		return vm.StartOpts{}, err
	}
	publicKey, err := ssh.PublicKey()
	if err != nil {
		return vm.StartOpts{}, err
	}
	// Upgrading before launch prevents an older binary from silently resuming
	// the obsolete global-export bootstrap contract on a later start.
	if err := workspace.Save(ws.WorkspaceDir, plan.Spec); err != nil {
		return vm.StartOpts{}, err
	}
	control, err := workspace.PrepareControl(ws.WorkspaceDir, plan.Spec, publicKey)
	if err != nil {
		return vm.StartOpts{}, err
	}
	if _, err := vm.WriteGuestInit(control); err != nil {
		return vm.StartOpts{}, err
	}
	mounts := []vm.Mount{{Tag: "devd", HostPath: control}}
	if plan.Spec.MountHost != "" {
		mounts = append(mounts, vm.Mount{Tag: "workspace", HostPath: plan.Spec.MountHost})
	}
	environment = append(environment,
		"HOME=/root", "DEVD_NAME="+ws.Name, "DEVD_SSH_PORT="+strconv.Itoa(ws.SSHPort))
	return vm.StartOpts{
		DiskPath: ws.DiskPath, CPUs: ws.CPUs, Memory: ws.Memory,
		Env: environment, Mounts: mounts, Command: "/bin/sh",
		Args: []string{"-c", vm.GuestBootstrap}, Workdir: "/",
		LogFile:    filepath.Join(ws.WorkspaceDir, "vm.log"),
		KernelPath: plan.Spec.KernelPath, ProcessFile: processPath(ws),
	}, nil
}

// stopWorkspace changes persistent state only after the exact launched process
// has exited. Both stop and forced removal use this same contract.
func stopWorkspace(database *sql.DB, ws *db.Workspace) error {
	process, err := vm.ReadProcess(processPath(ws))
	if err != nil {
		return err
	}
	if process == nil && vm.IsRunning(ws.PID) {
		return fmt.Errorf("cannot stop unverified VM PID %d", ws.PID)
	}
	if process != nil {
		keyPath, err := config.PrivateKeyPath()
		if err != nil {
			return err
		}
		if err := vm.Stop(vm.StopOpts{Process: *process, SSHPort: ws.SSHPort, KeyPath: keyPath}); err != nil {
			return fmt.Errorf("stop VM: %w", err)
		}
	}
	if err := db.SetWorkspaceState(database, ws.Name, "stopped", 0); err != nil {
		return fmt.Errorf("record stopped state: %w", err)
	}
	ws.State, ws.PID, ws.IsActive = "stopped", 0, false
	return nil
}

// nextAvailableSSHPort is called under the metadata lock, so allocation and
// publication cannot race another devd command. External listeners are checked
// again at launch; devd cannot reserve against unrelated host processes.
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
	listener, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("SSH port %d is already in use: %w", port, err)
	}
	return listener.Close()
}

func waitForSSH(port int, process vm.Process, timeout time.Duration) error {
	keyPath, err := config.PrivateKeyPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for ctx.Err() == nil {
		running, err := process.Running()
		if err != nil {
			return err
		}
		if !running {
			return fmt.Errorf("VM process exited before SSH became ready")
		}
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		check := exec.CommandContext(attempt, "ssh",
			"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
			"-o", "ConnectTimeout=1", "-o", "ConnectionAttempts=1",
			"-i", keyPath, "-p", strconv.Itoa(port), "root@127.0.0.1", "true")
		err = check.Run()
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("SSH not ready on port %d after %s", port, timeout)
}

// updateSSHConfig is a derived view refresh, serialized with metadata changes.
func updateSSHConfig(database *sql.DB) error {
	all, err := db.ListWorkspaces(database)
	if err != nil {
		return err
	}
	entries := make([]ssh.SSHConfigEntry, 0, len(all))
	for _, ws := range all {
		entries = append(entries, ssh.SSHConfigEntry{Name: ws.Name, Port: ws.SSHPort})
	}
	return ssh.UpdateSSHConfig(entries)
}
