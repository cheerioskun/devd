package vm

import (
	"strings"
	"testing"
)

func TestGuestInitHasCleanShutdownAndIdentityFork(t *testing.T) {
	if !strings.Contains(GuestBootstrap, "mount -t virtiofs devd /devd") {
		t.Fatal("bootstrap does not mount guest inputs")
	}
	for _, forbidden := range []string{"root:devd", "/devd/workspaces", "/devd/ssh", "PasswordAuthentication yes"} {
		if strings.Contains(guestInitScript, forbidden) {
			t.Errorf("guest init contains legacy unsafe policy %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"WS_DIR=/devd",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PermitRootLogin prohibit-password",
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
