package storage

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// LockDisk holds the same advisory lock as devd-vm. Lifecycle operations must
// hold this lock while cloning or removing a disk, even when SQLite says the VM
// is stopped. It also protects against a launcher whose CLI died before recording
// its PID. Workspace operation locks serialize the surrounding metadata changes.
func LockDisk(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open workspace disk: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("workspace disk %s is not a readable regular file", path)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("workspace disk %s is in use: %w", path, err)
	}
	return file, nil
}
