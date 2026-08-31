package proxy

import (
	"strings"
	"testing"

	"devd/internal/db"
)

func TestTunnelPathStaysBelowUnixSocketLimit(t *testing.T) {
	longStateDir := t.TempDir() + "/" + strings.Repeat("nested-state-directory-", 8)
	t.Setenv("DEVD_DIR", longStateDir)
	path, err := tunnelPath("workspace:8080")
	if err != nil {
		t.Fatal(err)
	}
	if len(path) >= 104 {
		t.Fatalf("tunnel path is too long for macOS Unix sockets: %d bytes: %s", len(path), path)
	}
}

func TestTargetForPort(t *testing.T) {
	t.Setenv("DEVD_DIR", t.TempDir())
	database, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	createWorkspace := func(name string, sshPort int) {
		t.Helper()
		workspace := &db.Workspace{
			Name:         name,
			Image:        "image",
			ImageDigest:  "sha256:" + name,
			WorkspaceDir: "/tmp/" + name,
			DiskPath:     "/tmp/" + name + "/rootfs.ext4",
			SSHPort:      sshPort,
			RelayPort:    9000 + sshPort,
			CPUs:         2,
			Memory:       512,
			State:        "running",
		}
		if err := db.CreateWorkspace(database, workspace); err != nil {
			t.Fatal(err)
		}
	}

	createWorkspace("alpha", 2222)
	if err := db.AddReservedPort(database, "alpha", 8080); err != nil {
		t.Fatal(err)
	}
	portProxy := New(database)
	target, err := portProxy.targetForPort(8080)
	if err != nil || target.Name != "alpha" {
		t.Fatalf("single target = %#v, %v", target, err)
	}

	createWorkspace("beta", 2223)
	if err := db.AddReservedPort(database, "beta", 8080); err != nil {
		t.Fatal(err)
	}
	if err := db.SetActiveWorkspace(database, "beta"); err != nil {
		t.Fatal(err)
	}
	target, err = portProxy.targetForPort(8080)
	if err != nil || target.Name != "beta" {
		t.Fatalf("active target = %#v, %v", target, err)
	}

	createWorkspace("other", 2224)
	if err := db.SetActiveWorkspace(database, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := portProxy.targetForPort(8080); err == nil {
		t.Fatal("shared port without an active claimant unexpectedly had a target")
	}
}
