package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devd/internal/db"
	"devd/internal/vm"
)

func TestOldTunnelCleanupCannotRemoveReplacement(t *testing.T) {
	t.Setenv("DEVD_DIR", t.TempDir())
	oldPath, err := tunnelPath("same-key")
	if err != nil {
		t.Fatal(err)
	}
	newPath, err := tunnelPath("same-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(oldPath)
		_ = os.Remove(newPath)
	})
	if oldPath == newPath {
		t.Fatal("tunnel incarnations reused a socket path")
	}
	proxy := New(nil)
	old := &Tunnel{key: "key", Path: oldPath}
	replacement := &Tunnel{key: "key", Path: newPath}
	proxy.tunnels["key"] = replacement
	proxy.removeTunnel("key", old)
	if proxy.tunnels["key"] != replacement {
		t.Fatal("old cleanup removed new map entry")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("old cleanup removed replacement path: %v", err)
	}
}

func TestStoppedProxyCannotAcquireNewResources(t *testing.T) {
	target := liveTunnelTarget(t)
	proxy := New(nil)
	proxy.Stop()
	if err := proxy.EnsurePorts([]int{8080}); err == nil || !strings.Contains(err.Error(), "proxy stopped") {
		t.Fatalf("stopped proxy attempted listener acquisition: %v", err)
	}
	if _, err := proxy.ensureTunnel(target, 8080); err == nil || err.Error() != "proxy stopped" {
		t.Fatalf("stopped proxy attempted tunnel launch: %v", err)
	}
}

func liveTunnelTarget(t *testing.T) *db.Workspace {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVD_DIR", dir)
	runtime := filepath.Join(dir, "devd-vm")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVD_VM_RUNTIME", runtime)
	process, err := vm.Start(vm.StartOpts{
		DiskPath: filepath.Join(dir, "disk"), CPUs: 1, Memory: 128,
		Command: "/bin/true", ProcessFile: filepath.Join(dir, "process.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Stop(vm.StopOpts{Process: process}) })
	return &db.Workspace{Name: "test", WorkspaceDir: dir, PID: process.PID}
}

func TestTunnelRejectsStaleDatabasePID(t *testing.T) {
	target := liveTunnelTarget(t)
	target.PID++
	proxy := New(nil)
	defer proxy.Stop()
	if _, err := proxy.ensureTunnel(target, 8080); err == nil {
		t.Fatal("stale DB PID selected a different VM incarnation")
	}
}
