package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	runtimeName     = "devd-vm"
	imageHelperName = "devd-image-helper"
)

// Mount configures one host directory as a named virtio-fs device. The guest
// init mounts known tags at their configured destination.
type Mount struct {
	Tag      string
	HostPath string
}

// CheckRuntime verifies both companion executables are available.
func CheckRuntime() error {
	if _, err := RuntimePath(); err != nil {
		return err
	}
	if _, err := ImageHelperPath(); err != nil {
		return err
	}
	return nil
}

// RuntimePath locates the separately linked libkrun runtime companion.
func RuntimePath() (string, error) {
	return companionPath("DEVD_VM_RUNTIME", runtimeName)
}

// ImageHelperPath locates the static Linux OCI-to-ext4 copy helper.
func ImageHelperPath() (string, error) {
	return companionPath("DEVD_IMAGE_HELPER", imageHelperName)
}

func companionPath(envName, name string) (string, error) {
	if path := os.Getenv(envName); path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path, nil
		}
		return "", fmt.Errorf("%s points to unavailable executable %q", envName, path)
	}

	if executable, err := os.Executable(); err == nil {
		path := filepath.Join(filepath.Dir(executable), name)
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path, nil
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("%s not found; build and install devd with its runtime companions", name)
}

// StartOpts configures one ext4-root workspace VM.
type StartOpts struct {
	DiskPath    string
	CPUs        int
	Memory      int
	Env         []string
	Mounts      []Mount
	Command     string
	Args        []string
	Workdir     string
	LogFile     string
	KernelPath  string
	ProcessFile string
}

// Start launches a detached VM and records its process identity before returning.
// The companion also locks its root disk for its entire lifetime.
func Start(opts StartOpts) (Process, error) {
	runtimePath, err := RuntimePath()
	if err != nil {
		return Process{}, err
	}
	args, err := startArgs(opts)
	if err != nil {
		return Process{}, err
	}
	cmd := exec.Command(runtimePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var logFile *os.File
	if opts.LogFile != "" {
		file, openErr := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if openErr != nil {
			return Process{}, fmt.Errorf("open VM log: %w", openErr)
		}
		logFile = file
		cmd.Stdout = file
		cmd.Stderr = file
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return Process{}, fmt.Errorf("start devd-vm: %w", err)
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	go cmd.Wait() // reap the detached VMM when the guest exits
	identity, err := processIdentity(cmd.Process.Pid)
	process := Process{PID: cmd.Process.Pid, StartTime: identity}
	if err == nil && identity == "" {
		err = fmt.Errorf("VM exited before its process identity could be recorded")
	}
	if err == nil {
		err = writeProcess(opts.ProcessFile, process)
	}
	if err != nil {
		// We still own the launched process. Never return a launch success without
		// its recovery receipt. The companion's disk lock protects the failure gap.
		_ = cmd.Process.Kill()
		return Process{}, fmt.Errorf("record VM process (PID %d): %w", process.PID, err)
	}
	return process, nil
}

func startArgs(opts StartOpts) ([]string, error) {
	if opts.DiskPath == "" || opts.Command == "" || opts.ProcessFile == "" {
		return nil, fmt.Errorf("root disk, guest command, and process receipt path are required")
	}
	if opts.CPUs < 1 || opts.CPUs > 255 || opts.Memory < 1 || uint64(opts.Memory) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("invalid VM CPU or memory allocation")
	}
	if len(opts.Env) > 64 || len(opts.Mounts) > 8 {
		return nil, fmt.Errorf("VM configuration exceeds companion limits (64 environment entries, 8 mounts)")
	}
	args := []string{"--disk", opts.DiskPath}
	if opts.KernelPath != "" {
		args = append(args, "--kernel", opts.KernelPath)
	}
	args = appendVMConfig(args, opts.CPUs, opts.Memory, opts.Env, opts.Mounts, opts.Workdir)
	args = append(args, "--", opts.Command)
	return append(args, opts.Args...), nil
}

// PackOpts configures the one-time OCI directory to ext4 conversion helper.
type PackOpts struct {
	HelperRoot string
	SourceRoot string
	TargetDisk string
	CPUs       int
	Memory     int
	LogFile    string
}

// Pack runs the internal conversion VM synchronously. Directory-root mode is
// deliberately confined to this cold image-cache path.
func Pack(opts PackOpts) error {
	runtimePath, err := RuntimePath()
	if err != nil {
		return err
	}
	args := []string{
		"--helper-root", opts.HelperRoot,
		"--data-disk", opts.TargetDisk,
		"--virtiofs", "source:" + opts.SourceRoot,
	}
	args = appendVMConfig(args, opts.CPUs, opts.Memory, nil, nil, "/")
	args = append(args, "--", "/devd-image-helper")

	cmd := exec.Command(runtimePath, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if opts.LogFile != "" {
		file, openErr := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if openErr != nil {
			return fmt.Errorf("open image conversion log: %w", openErr)
		}
		defer func() { _ = file.Close() }()
		cmd.Stdout = io.MultiWriter(file, &output)
		cmd.Stderr = io.MultiWriter(file, &output)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run image conversion VM: %w\n%s", err, output.String())
	}
	if !bytes.Contains(output.Bytes(), []byte("DEVD_IMAGE_COMPLETE")) {
		return fmt.Errorf("image conversion VM exited without completion marker\n%s", output.String())
	}
	return nil
}

func appendVMConfig(args []string, cpus, memory int, env []string, mounts []Mount, workdir string) []string {
	if cpus > 0 {
		args = append(args, "--cpus", strconv.Itoa(cpus))
	}
	if memory > 0 {
		args = append(args, "--mem", strconv.Itoa(memory))
	}
	for _, mount := range mounts {
		args = append(args, "--virtiofs", mount.Tag+":"+mount.HostPath)
	}
	for _, value := range env {
		args = append(args, "--env", value)
	}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	return args
}

// StopOpts configures graceful guest shutdown with a bounded VMM signal
// fallback for guests that never reached SSH.
type StopOpts struct {
	Process  Process
	SSHPort  int
	KeyPath  string
	Graceful time.Duration
}

// Stop asks the main guest workload to exit, allowing init.krun to unmount the
// ext4 root. SIGTERM and SIGKILL are bounded crash-recovery fallbacks.
func Stop(opts StopOpts) error {
	running, err := opts.Process.Running()
	if err != nil || !running {
		return err
	}
	graceful := opts.Graceful
	if graceful == 0 {
		graceful = 5 * time.Second
	}

	if opts.SSHPort > 0 && opts.KeyPath != "" {
		sshArgs := []string{
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ConnectTimeout=2",
			"-i", opts.KeyPath,
			"-p", strconv.Itoa(opts.SSHPort),
			"root@127.0.0.1",
			`sync; kill -TERM "$(cat /run/devd-workload.pid)"`,
		}
		ctx, cancel := context.WithTimeout(context.Background(), graceful)
		cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
		cmd.Stdin = nil
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		_ = cmd.Run() // disconnect during shutdown commonly returns 255
		cancel()
		if exited, err := waitForExit(opts.Process, graceful); err != nil || exited {
			return err
		}
	}

	for _, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		// Recheck the incarnation before each destructive action, including the
		// escalation after a timeout. A recycled PID is not our VM.
		running, err := opts.Process.Running()
		if err != nil || !running {
			return err
		}
		proc, err := os.FindProcess(opts.Process.PID)
		if err != nil {
			return err
		}
		if err := proc.Signal(signal); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("signal PID %d: %w", opts.Process.PID, err)
		}
		if exited, err := waitForExit(opts.Process, 5*time.Second); err != nil || exited {
			return err
		}
	}
	return fmt.Errorf("PID %d did not exit after SIGKILL", opts.Process.PID)
}

func waitForExit(process Process, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		running, err := process.Running()
		if err != nil || !running {
			return !running, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// IsRunning is a conservative legacy-PID probe, not proof of VM identity.
// Observation errors count as alive so migration cannot authorize disk reuse
// on missing permissions. New launches must use Process.Running instead.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	identity, err := processIdentity(pid)
	return err != nil || identity != ""
}
