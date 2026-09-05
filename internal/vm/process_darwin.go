package vm

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, error) {
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect PID %d: %w", pid, err)
	}
	if len(entries) == 0 {
		return "", nil
	}
	info := entries[0]
	// SZOMB processes have exited but may not have been reaped yet.
	if info.Proc.P_pid == 0 || info.Proc.P_stat == 5 {
		return "", nil
	}
	start := info.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", start.Sec, start.Usec), nil
}
