package cli

import (
	"os"
	"path/filepath"
	"testing"

	"devd/internal/db"
	"devd/internal/storage"
	"devd/internal/vm"
	"devd/internal/workspace"
)

func TestPrepareBootHasNoRuntimeEffects(t *testing.T) {
	database, ws := operationFixture(t)
	lock, err := storage.LockDisk(ws.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	boot, err := prepareBoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(boot.Mounts) != 1 || boot.Mounts[0].Tag != "devd" || boot.Mounts[0].HostPath != filepath.Join(ws.WorkspaceDir, "control") {
		t.Fatalf("unsafe management export: %#v", boot.Mounts)
	}
	if boot.Command != "/bin/sh" || len(boot.Args) != 2 || boot.Args[1] != vm.GuestBootstrap {
		t.Fatalf("boot depends on obsolete in-disk policy: %#v", boot)
	}
	for _, path := range []string{boot.ProcessFile, boot.LogFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preparation launched a process: %s, %v", path, err)
		}
	}
	stored, err := db.GetWorkspace(database, ws.Name)
	if err != nil || stored.State != "stopped" || stored.IsActive {
		t.Fatalf("preparation changed runtime state: %#v, %v", stored, err)
	}
}

func TestMissingPersistedKernelFailsBeforeGuestRendering(t *testing.T) {
	database, ws := operationFixture(t)
	if err := workspace.Save(ws.WorkspaceDir, workspace.Spec{KernelPath: filepath.Join(t.TempDir(), "missing-Image")}); err != nil {
		t.Fatal(err)
	}
	if _, err := startWorkspace(database, ws); err == nil {
		t.Fatal("started with missing persisted kernel")
	}
	if _, err := os.Stat(filepath.Join(ws.WorkspaceDir, "control")); !os.IsNotExist(err) {
		t.Fatalf("invalid boot inputs reached rendering: %v", err)
	}
}
