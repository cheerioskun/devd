package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"

	"devd/internal/config"
)

// Lock excludes other lifecycle operations on these workspaces. Locks live
// outside workspace directories and are never unlinked: removing a workspace
// must not create a second lock inode for the same name. Contention fails fast
// rather than making an interactive command wait behind an entire VM boot.
func Lock(names ...string) (func(), error) {
	for _, name := range names {
		if err := config.ValidateWorkspaceName(name); err != nil {
			return nil, err
		}
	}
	dir, err := config.DevdDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create operation lock directory: %w", err)
	}
	names = slices.Clone(names)
	slices.Sort(names)
	var files []*os.File
	unlock := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, name := range slices.Compact(names) {
		file, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			unlock()
			return nil, fmt.Errorf("open workspace lock: %w", err)
		}
		files = append(files, file)
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			unlock()
			return nil, fmt.Errorf("workspace %q is busy with another operation: %w", name, err)
		}
	}
	return unlock, nil
}
