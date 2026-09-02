package vm

import (
	"strings"
	"testing"
)

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
