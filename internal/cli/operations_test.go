package cli

import (
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devd/internal/config"
	"devd/internal/db"
	"devd/internal/storage"
	"devd/internal/vm"
	"devd/internal/workspace"
)

func operationFixture(t *testing.T) (*sql.DB, *db.Workspace) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("DEVD_DIR", state)
	t.Setenv("DEVD_SSH_CONFIG", filepath.Join(state, "ssh-config"))
	database, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	dir, err := config.WorkspaceDir("test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sshPort := port.Addr().(*net.TCPAddr).Port
	_ = port.Close()
	ws := &db.Workspace{
		Name: "test", Image: "image", ImageDigest: "sha256:test", WorkspaceDir: dir,
		DiskPath: filepath.Join(dir, "rootfs.ext4"), SSHPort: sshPort, RelayPort: 9001,
		CPUs: 2, Memory: 512, State: "stopped",
	}
	if err := os.WriteFile(ws.DiskPath, []byte("persistent disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Save(dir, workspace.Spec{}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWorkspace(database, ws); err != nil {
		t.Fatal(err)
	}
	pub, err := config.PublicKeyPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pub, []byte("public-key"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(state, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(state, "fake.pid")
	t.Setenv("DEVD_TEST_PID_FILE", pidFile)
	for name, script := range map[string]string{
		"devd-vm": "#!/bin/sh\necho $$ >\"$DEVD_TEST_PID_FILE\"\nexec sleep 300\n",
		"ssh":     "#!/bin/sh\ncase \"$*\" in *devd-workload.pid*) kill \"$(cat \"$DEVD_TEST_PID_FILE\")\";; esac\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEVD_VM_RUNTIME", filepath.Join(bin, "devd-vm"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		process, err := vm.ReadProcess(processPath(ws))
		if err == nil && process != nil {
			_ = vm.Stop(vm.StopOpts{Process: *process})
		}
	})
	unlock, err := workspace.Lock(ws.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	return database, ws
}

func TestStartStopAndLaunchRecovery(t *testing.T) {
	database, ws := operationFixture(t)
	if err := os.WriteFile(filepath.Join(ws.WorkspaceDir, "config.json"), []byte(`{"format_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := startWorkspace(database, ws); err != nil {
		t.Fatal(err)
	}
	spec, err := workspace.Load(ws.WorkspaceDir)
	if err != nil || spec.Version != config.WorkspaceSpecVersion {
		t.Fatalf("legacy spec was not upgraded: %#v, %v", spec, err)
	}
	if _, err := startWorkspace(database, ws); err == nil {
		t.Fatal("second start succeeded")
	}
	// Simulate a CLI that launched successfully but died before updating the DB.
	if err := db.SetWorkspaceState(database, ws.Name, "stopped", 0); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadWorkspace(database, ws.Name)
	if err != nil || recovered.State != "running" || recovered.PID != ws.PID {
		t.Fatalf("launch recovery = %#v, %v", recovered, err)
	}
	if err := stopWorkspace(database, recovered); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetWorkspace(database, ws.Name)
	if err != nil || stored.State != "stopped" || stored.PID != 0 || stored.IsActive {
		t.Fatalf("stopped state = %#v, %v", stored, err)
	}
}

func TestStartupFailureDoesNotStealActivation(t *testing.T) {
	database, ws := operationFixture(t)
	other := *ws
	other.Name, other.SSHPort, other.RelayPort, other.State = "healthy", ws.SSHPort+1, 9002, "running"
	if err := db.CreateWorkspace(database, &other); err != nil {
		t.Fatal(err)
	}
	if err := db.SetActiveWorkspace(database, other.Name); err != nil {
		t.Fatal(err)
	}
	// Companion exits while readiness probes fail. No VM hardware is involved.
	if err := os.WriteFile(os.Getenv("DEVD_VM_RUNTIME"), []byte("#!/bin/sh\nexec sleep 0.2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(filepath.Dir(os.Getenv("DEVD_VM_RUNTIME")), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := startWorkspace(database, ws); err == nil {
		t.Fatal("failed companion reported readiness")
	}
	active, err := db.GetActiveWorkspace(database)
	if err != nil || active == nil || active.Name != other.Name {
		t.Fatalf("failed boot changed active workspace: %#v, %v", active, err)
	}
	stored, err := db.GetWorkspace(database, ws.Name)
	if err != nil || stored.State != "stopped" {
		t.Fatalf("failed boot not reconciled: %#v, %v", stored, err)
	}
}

func TestActivationFailureStopsVM(t *testing.T) {
	database, ws := operationFixture(t)
	if _, err := database.Exec(`CREATE TRIGGER fail_activation BEFORE UPDATE OF is_active ON workspaces
		WHEN NEW.is_active = 1 BEGIN SELECT RAISE(FAIL, 'injected activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := startWorkspace(database, ws); err == nil || !strings.Contains(err.Error(), "injected activation failure") {
		t.Fatalf("activation failure = %v", err)
	}
	if ws.State != "stopped" {
		t.Fatal("activation failure left VM running")
	}
}

func TestRemovePreservesDiskOnStopFailure(t *testing.T) {
	database, ws := operationFixture(t)
	ws.State = "running"
	if err := db.SetWorkspaceState(database, ws.Name, "running", 123); err != nil {
		t.Fatal(err)
	}
	// A corrupt receipt prevents a safe stop. Forced removal must not continue.
	if err := os.WriteFile(processPath(ws), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkspace(database, ws, true); err == nil {
		t.Fatal("forced removal ignored stop failure")
	}
	if _, err := os.Stat(ws.DiskPath); err != nil {
		t.Fatalf("stop failure destroyed disk: %v", err)
	}
	stored, err := db.GetWorkspace(database, ws.Name)
	if err != nil || stored.State != "running" {
		t.Fatalf("stop failure lost running state: %#v, %v", stored, err)
	}
}

func TestUnrecordedDiskOwnerBlocksDestructiveOperations(t *testing.T) {
	database, ws := operationFixture(t)
	unlock, err := workspace.Lock("child")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	lock, err := storage.LockDisk(ws.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := loadWorkspace(database, ws.Name); err == nil {
		t.Fatal("reconciliation declared a locked disk stopped")
	}
	if _, err := startWorkspace(database, ws); err == nil {
		t.Fatal("started a locked disk")
	}
	if err := removeWorkspace(database, ws, true); err == nil {
		t.Fatal("removed a locked disk")
	}
	if _, err := doFork(database, ws.Name, "child", forkOverrides{}); err == nil {
		t.Fatal("forked a locked disk")
	}
}

func TestStaleIdentityReconcilesWithoutSignalling(t *testing.T) {
	database, ws := operationFixture(t)
	data, err := json.Marshal(vm.Process{PID: os.Getpid(), StartTime: "not this process"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(processPath(ws), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.SetWorkspaceState(database, ws.Name, "running", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	current, err := loadWorkspace(database, ws.Name)
	if err != nil || current.State != "stopped" || current.PID != 0 {
		t.Fatalf("stale identity = %#v, %v", current, err)
	}
}

func TestCreationDoesNotClaimExistingFiles(t *testing.T) {
	database, ws := operationFixture(t)
	if err := db.DeleteWorkspace(database, ws.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := publishWorkspace(database, ws.Name, "/missing/source", workspacePlan{}); err == nil {
		t.Fatal("creation accepted orphan directory")
	}
	if data, err := os.ReadFile(ws.DiskPath); err != nil || string(data) != "persistent disk" {
		t.Fatalf("creation damaged unowned files: %q, %v", data, err)
	}
}

func TestReadinessBoundsHungSSH(t *testing.T) {
	_, ws := operationFixture(t)
	process, err := vm.Start(vm.StartOpts{
		DiskPath: ws.DiskPath, CPUs: 1, Memory: 128, Command: "/bin/true", ProcessFile: processPath(ws),
	})
	if err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(filepath.Dir(os.Getenv("DEVD_VM_RUNTIME")), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := waitForSSH(ws.SSHPort, process, 100*time.Millisecond); err == nil {
		t.Fatal("hung SSH reported ready")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("readiness exceeded its deadline: %s", elapsed)
	}
}
