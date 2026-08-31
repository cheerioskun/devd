package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()
	if err := WriteWorkspaceFiles(dir, WorkspaceFilesOpts{
		UserCommand:  "echo ready",
		ImageWorkdir: "/workspace",
		MountGuest:   "/workspace",
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		userCommandName:  "echo ready",
		imageWorkdirName: "/workspace\n",
		mountGuestName:   "/workspace\n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
}

func TestGuestInitHasCleanShutdownAndIdentityFork(t *testing.T) {
	for _, fragment := range []string{
		"mount -t virtiofs devd /devd",
		"regenerate-identity",
		"mount -t virtiofs workspace",
		"trap shutdown_workload TERM INT",
		"/run/devd-workload.pid",
	} {
		if !strings.Contains(guestInitScript, fragment) {
			t.Errorf("guest init missing %q", fragment)
		}
	}
}
