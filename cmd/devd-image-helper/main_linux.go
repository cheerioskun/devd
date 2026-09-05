//go:build linux

// devd-image-helper runs in a disposable helper microVM. It copies an OCI
// rootfs, exposed through virtio-fs, into an ext4 data disk while preserving
// Linux metadata. It is shipped as a static binary and does not depend on tools
// in the user's image.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	sourceDir = "/source"
	targetDir = "/target"
)

type inodeKey struct {
	dev uint64
	ino uint64
}

type copier struct {
	hardlinks map[inodeKey]string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "devd-image-helper: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("DEVD_IMAGE_COMPLETE")
}

func run() error {
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		return fmt.Errorf("create source mountpoint: %w", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target mountpoint: %w", err)
	}

	if err := unix.Mount("source", sourceDir, "virtiofs", unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount OCI rootfs: %w", err)
	}
	defer unix.Unmount(sourceDir, 0) //nolint:errcheck

	if err := unix.Mount("/dev/vda", targetDir, "ext4", 0, ""); err != nil {
		return fmt.Errorf("mount ext4 target: %w", err)
	}
	targetMounted := true
	defer func() {
		if targetMounted {
			_ = unix.Unmount(targetDir, 0)
		}
	}()

	c := &copier{hardlinks: make(map[inodeKey]string)}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read OCI rootfs: %w", err)
	}
	for _, entry := range entries {
		source := filepath.Join(sourceDir, entry.Name())
		target := filepath.Join(targetDir, entry.Name())
		switch entry.Name() {
		case "dev", "proc", "sys", "run", "mnt", "lost+found", ".krunvm.lock":
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create empty system directory %s: %w", target, err)
			}
		default:
			if err := c.copyEntry(source, target); err != nil {
				return err
			}
		}
	}

	if err := sanitizeIdentity(); err != nil {
		return err
	}

	unix.Sync()
	if err := unix.Unmount(targetDir, 0); err != nil {
		return fmt.Errorf("unmount ext4 target: %w", err)
	}
	targetMounted = false
	if err := unix.Unmount(sourceDir, 0); err != nil {
		return fmt.Errorf("unmount OCI rootfs: %w", err)
	}
	return nil
}

func (c *copier) copyEntry(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("stat %s: %w", source, err)
	}
	stat := &unix.Stat_t{}
	if err := unix.Lstat(source, stat); err != nil {
		return fmt.Errorf("read Linux metadata %s: %w", source, err)
	}

	switch {
	case info.IsDir():
		if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create directory %s: %w", target, err)
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return fmt.Errorf("read directory %s: %w", source, err)
		}
		for _, entry := range entries {
			if err := c.copyEntry(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return applyMetadata(source, target, info, stat, false)

	case info.Mode().IsRegular():
		key := inodeKey{dev: uint64(stat.Dev), ino: stat.Ino}
		if first, exists := c.hardlinks[key]; exists {
			if err := os.Link(first, target); err != nil {
				return fmt.Errorf("create hardlink %s: %w", target, err)
			}
			return nil
		}
		if err := copyRegular(source, target); err != nil {
			return err
		}
		if err := applyMetadata(source, target, info, stat, false); err != nil {
			return err
		}
		c.hardlinks[key] = target
		return nil

	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(source)
		if err != nil {
			return fmt.Errorf("read symlink %s: %w", source, err)
		}
		if err := os.Symlink(link, target); err != nil {
			return fmt.Errorf("create symlink %s: %w", target, err)
		}
		return applyMetadata(source, target, info, stat, true)

	case info.Mode()&os.ModeNamedPipe != 0:
		if err := unix.Mkfifo(target, uint32(info.Mode().Perm())); err != nil {
			return fmt.Errorf("create fifo %s: %w", target, err)
		}
		return applyMetadata(source, target, info, stat, false)

	case info.Mode()&(os.ModeDevice|os.ModeCharDevice) != 0:
		mode := uint32(info.Mode().Perm())
		if info.Mode()&os.ModeCharDevice != 0 {
			mode |= unix.S_IFCHR
		} else {
			mode |= unix.S_IFBLK
		}
		if err := unix.Mknod(target, mode, int(stat.Rdev)); err != nil {
			return fmt.Errorf("create device %s: %w", target, err)
		}
		return applyMetadata(source, target, info, stat, false)

	case info.Mode()&os.ModeSocket != 0:
		return nil
	default:
		return fmt.Errorf("unsupported file type %s (%s)", source, info.Mode())
	}
}

func copyRegular(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", source, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	return nil
}

func applyMetadata(source, target string, info os.FileInfo, stat *unix.Stat_t, symlink bool) error {
	if err := os.Lchown(target, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("chown %s: %w", target, err)
	}
	if !symlink {
		if err := os.Chmod(target, info.Mode().Perm()|info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
			return fmt.Errorf("chmod %s: %w", target, err)
		}
	}
	times := []unix.Timespec{stat.Atim, stat.Mtim}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, target, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("set timestamps %s: %w", target, err)
	}
	if err := copyXattrs(source, target); err != nil {
		return err
	}
	return nil
}

func copyXattrs(source, target string) error {
	size, err := unix.Llistxattr(source, nil)
	if err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("list xattrs %s: %w", source, err)
	}
	if size == 0 {
		return nil
	}

	names := make([]byte, size)
	size, err = unix.Llistxattr(source, names)
	if err != nil {
		return fmt.Errorf("list xattrs %s: %w", source, err)
	}
	for _, name := range strings.Split(string(names[:size]), "\x00") {
		if name == "" || strings.HasPrefix(name, "user.containers.") {
			continue
		}
		if !strings.HasPrefix(name, "user.") &&
			!strings.HasPrefix(name, "security.") &&
			!strings.HasPrefix(name, "trusted.") &&
			!strings.HasPrefix(name, "system.") {
			continue
		}
		valueSize, err := unix.Lgetxattr(source, name, nil)
		if err != nil {
			return fmt.Errorf("read xattr %s on %s: %w", name, source, err)
		}
		value := make([]byte, valueSize)
		if valueSize > 0 {
			valueSize, err = unix.Lgetxattr(source, name, value)
			if err != nil {
				return fmt.Errorf("read xattr %s on %s: %w", name, source, err)
			}
			value = value[:valueSize]
		}
		if err := unix.Lsetxattr(target, name, value, 0); err != nil {
			return fmt.Errorf("write xattr %s on %s: %w", name, target, err)
		}
	}
	return nil
}

func sanitizeIdentity() error {
	matches, err := filepath.Glob(filepath.Join(targetDir, "etc/ssh/ssh_host_*"))
	if err != nil {
		return fmt.Errorf("find SSH host keys: %w", err)
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove SSH host key %s: %w", match, err)
		}
	}

	machineID := filepath.Join(targetDir, "etc/machine-id")
	if err := os.MkdirAll(filepath.Dir(machineID), 0o755); err != nil {
		return fmt.Errorf("create machine-id directory: %w", err)
	}
	if err := os.RemoveAll(machineID); err != nil {
		return fmt.Errorf("replace machine-id: %w", err)
	}
	if err := os.WriteFile(machineID, nil, 0o644); err != nil {
		return fmt.Errorf("sanitize machine-id: %w", err)
	}
	if err := os.Chown(machineID, 0, 0); err != nil {
		return fmt.Errorf("chown machine-id: %w", err)
	}
	dbusMachineID := filepath.Join(targetDir, "var/lib/dbus/machine-id")
	if err := os.Remove(dbusMachineID); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sanitize dbus machine-id: %w", err)
	}
	return nil
}
