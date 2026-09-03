package vm

import (
	"bytes"
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
	DiskPath   string
	CPUs       int
	Memory     int
	Env        []string
	Mounts     []Mount
	Command    string
	Args       []string
	Workdir    string
	LogFile    string
	KernelPath string
}

// Start launches an ext4-root VM in the background and returns its VMM PID.
func Start(opts StartOpts) (int, error) {
	runtimePath, err := RuntimePath()
	if err != nil {
		return 0, err
	}
	if opts.DiskPath == "" {
		return 0, fmt.Errorf("root disk path is required")
	}
	if opts.Command == "" {
		return 0, fmt.Errorf("guest command is required")
	}

	args := []string{"--disk", opts.DiskPath}
	if opts.KernelPath != "" {
		args = append(args, "--kernel", opts.KernelPath)
	}
	args = appendVMConfig(args, opts.CPUs, opts.Memory, opts.Env, opts.Mounts, opts.Workdir)
	args = append(args, "--", opts.Command)
	args = append(args, opts.Args...)

	cmd := exec.Command(runtimePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var logFile *os.File
	if opts.LogFile != "" {
		file, openErr := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if openErr != nil {
			return 0, fmt.Errorf("open VM log: %w", openErr)
		}
		logFile = file
		cmd.Stdout = file
		cmd.Stderr = file
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return 0, fmt.Errorf("start devd-vm: %w", err)
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	go cmd.Wait() // reap the detached VMM when the guest exits
	return cmd.Process.Pid, nil
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
	PID      int
	SSHPort  int
	KeyPath  string
	Graceful time.Duration
}

// Stop asks the main guest workload to exit, allowing init.krun to unmount the
// ext4 root. SIGTERM and SIGKILL are bounded crash-recovery fallbacks.
func Stop(opts StopOpts) error {
	if opts.PID <= 0 {
		return nil
	}
	graceful := opts.Graceful
	if graceful == 0 {
		graceful = 5 * time.Second
	}

	if opts.SSHPort > 0 && opts.KeyPath != "" && IsRunning(opts.PID) {
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
		cmd := exec.Command("ssh", sshArgs...)
		cmd.Stdin = nil
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		_ = cmd.Run() // disconnect during shutdown commonly returns 255
		if waitForExit(opts.PID, graceful) {
			return nil
		}
	}

	proc, err := os.FindProcess(opts.PID)
	if err != nil {
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("signal PID %d: %w", opts.PID, err)
	}
	if waitForExit(opts.PID, 5*time.Second) {
		return nil
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill PID %d after timeout: %w", opts.PID, err)
	}
	if !waitForExit(opts.PID, 5*time.Second) {
		return fmt.Errorf("PID %d did not exit after SIGKILL", opts.PID)
	}
	return nil
}

func waitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !IsRunning(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !IsRunning(pid)
}

// IsRunning checks whether a VMM process is alive.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
