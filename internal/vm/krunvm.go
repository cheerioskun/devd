package vm

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// CheckKrunvm verifies krunvm is installed and accessible.
func CheckKrunvm() error {
	_, err := exec.LookPath("krunvm")
	if err != nil {
		return fmt.Errorf("krunvm not found in PATH — install with: brew install krunvm")
	}
	return nil
}

// CreateOpts configures krunvm create.
type CreateOpts struct {
	Name    string
	Image   string
	CPUs    int
	Memory  int            // MB
	Volumes map[string]string // host:guest
}

// Create runs krunvm create. Returns combined output for diagnostics.
func Create(opts CreateOpts) (string, error) {
	args := []string{"create", "--name", opts.Name}
	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(opts.CPUs))
	}
	if opts.Memory > 0 {
		args = append(args, "--mem", strconv.Itoa(opts.Memory))
	}
	for host, guest := range opts.Volumes {
		args = append(args, "-v", host+":"+guest)
	}
	args = append(args, opts.Image)

	cmd := exec.Command("krunvm", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("krunvm create: %w\n%s", err, out.String())
	}
	return out.String(), nil
}

// StartOpts configures krunvm start.
type StartOpts struct {
	Name    string
	Command string
	Args    []string
	LogFile string // path to write stdout/stderr
}

// Start runs krunvm start in the background. Returns the PID of the krunvm process.
func Start(opts StartOpts) (int, error) {
	args := []string{"start", opts.Name, opts.Command}
	args = append(args, opts.Args...)

	cmd := exec.Command("krunvm", args...)

	// Set process group so the VM survives if devd exits
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if opts.LogFile != "" {
		f, err := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return 0, fmt.Errorf("open log file: %w", err)
		}
		cmd.Stdout = f
		cmd.Stderr = f
		// Don't close f here — the krunvm process needs it.
		// It will be closed when krunvm exits.
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("krunvm start: %w", err)
	}

	// Detach — don't wait. The process runs independently.
	go cmd.Wait()

	return cmd.Process.Pid, nil
}

// Delete runs krunvm delete.
func Delete(name string) error {
	cmd := exec.Command("krunvm", "delete", name)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("krunvm delete: %w\n%s", err, out.String())
	}
	return nil
}

// Stop kills the krunvm process by PID.
func Stop(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil // process not found, already dead
	}
	// Try SIGTERM first
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Already dead
		return nil
	}
	return nil
}

// IsRunning checks if a process with the given PID is still alive.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without sending a signal
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// List runs krunvm list and returns the raw output.
func List() (string, error) {
	cmd := exec.Command("krunvm", "list")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("krunvm list: %w\n%s", err, out.String())
	}
	return strings.TrimSpace(out.String()), nil
}
